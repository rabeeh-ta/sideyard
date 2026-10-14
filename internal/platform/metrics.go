package platform

import (
	"os"
	"path/filepath"
	"time"

	"sideyard/internal/sysinfo"
)

// Metrics keeps a day of samples per app and for the server, in memory, for
// the dashboard's charts. A restart starts afresh.
const (
	metricsInterval = 15 * time.Second
	metricsKeep     = int(24 * time.Hour / metricsInterval)
	// metricsMaxPoints is the most a chart gets; longer ranges are averaged
	// down to it.
	metricsMaxPoints = 240
)

// MetricPoint is an app's usage at one moment.
type MetricPoint struct {
	T   int64   `json:"t"`   // Unix seconds
	CPU float64 `json:"cpu"` // percent of one core
	Mem uint64  `json:"mem"` // bytes in use
}

// ServerPoint is the server's usage at one moment.
type ServerPoint struct {
	T         int64   `json:"t"`   // Unix seconds
	CPU       float64 `json:"cpu"` // busy, percent of all cores
	IOWait    float64 `json:"iowait"`
	Steal     float64 `json:"steal"`
	Load1     float64 `json:"load1"`
	Mem       uint64  `json:"mem"` // bytes in use: total minus available
	Swap      uint64  `json:"swap"`
	DiskRead  float64 `json:"diskRead"` // bytes per second
	DiskWrite float64 `json:"diskWrite"`
	NetRx     float64 `json:"netRx"`
	NetTx     float64 `json:"netTx"`
}

// selfCPUKey keys Sideyard's own process among cpuSamples.
const selfCPUKey = "\x00sideyard"

func (p *Platform) metricsLoop() {
	// Rates need two readings: take one now so the first sample has them.
	p.primeMetrics()
	select {
	case <-p.bg.Done():
		return
	case <-time.After(2 * time.Second):
	}
	t := time.NewTicker(metricsInterval)
	defer t.Stop()
	for {
		p.sampleMetrics()
		select {
		case <-p.bg.Done():
			return
		case <-t.C:
		}
	}
}

// primeMetrics takes the first reading of every counter that becomes a
// rate, and records nothing.
func (p *Platform) primeMetrics() {
	s := p.sampler.Sample()
	p.cpuPercent(selfCPUKey, sysinfo.ProcessCPU(os.Getpid()))
	if projects, err := p.store.Projects(); err == nil {
		for _, proj := range projects {
			if p.getLive(proj.ID) != nil {
				p.Status(proj)
			}
		}
	}
	p.mu.Lock()
	p.lastSample = s
	p.mu.Unlock()
}

func (p *Platform) sampleMetrics() {
	s := p.sampler.Sample()
	now := s.At.Unix()
	server := ServerPoint{
		T: now, CPU: s.CPUPercent, IOWait: s.IOWait, Steal: s.Steal, Load1: s.Load1,
		Mem: s.MemUsedBytes(), Swap: s.SwapUsedBytes(),
		DiskRead: s.DiskReadBps, DiskWrite: s.DiskWriteBps, NetRx: s.NetRxBps, NetTx: s.NetTxBps,
	}
	self := MetricPoint{T: now, Mem: sysinfo.ProcessRSS(os.Getpid())}
	self.CPU = p.cpuPercent(selfCPUKey, sysinfo.ProcessCPU(os.Getpid()))
	projects, err := p.store.Projects()
	if err != nil {
		return
	}
	points := map[int64]MetricPoint{}
	for _, proj := range projects {
		if p.getLive(proj.ID) == nil {
			continue
		}
		st := p.Status(proj)
		points[proj.ID] = MetricPoint{T: now, CPU: st.CPUPercent, Mem: st.MemoryBytes}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSample = s
	p.self = self
	p.serverMetrics = appendPoint(p.serverMetrics, server)
	for id, pt := range points {
		p.projectMetrics[id] = appendPoint(p.projectMetrics[id], pt)
	}
}

func appendPoint[T any](s []T, pt T) []T {
	s = append(s, pt)
	if len(s) > metricsKeep {
		s = s[len(s)-metricsKeep:]
	}
	return s
}

// window returns the points of the last span, averaged down to at most
// metricsMaxPoints. Each average is stamped with its bucket's last time.
func window[T any](pts []T, span time.Duration, at func(T) int64, mean func([]T) T) []T {
	if len(pts) == 0 {
		return []T{}
	}
	since := at(pts[len(pts)-1]) - int64(span.Seconds())
	start := len(pts)
	for start > 0 && at(pts[start-1]) > since {
		start--
	}
	pts = pts[start:]
	if len(pts) <= metricsMaxPoints {
		return append([]T{}, pts...)
	}
	step := float64(span.Seconds()) / metricsMaxPoints
	out := make([]T, 0, metricsMaxPoints)
	for i := 0; i < len(pts); {
		bucket := int64(float64(at(pts[i])-since-1) / step)
		j := i + 1
		for j < len(pts) && int64(float64(at(pts[j])-since-1)/step) == bucket {
			j++
		}
		out = append(out, mean(pts[i:j]))
		i = j
	}
	return out
}

func meanMetric(b []MetricPoint) MetricPoint {
	out := MetricPoint{T: b[len(b)-1].T}
	var mem float64
	for _, pt := range b {
		out.CPU += pt.CPU
		mem += float64(pt.Mem)
	}
	n := float64(len(b))
	out.CPU /= n
	out.Mem = uint64(mem / n)
	return out
}

func meanServer(b []ServerPoint) ServerPoint {
	out := ServerPoint{T: b[len(b)-1].T}
	var mem, swap float64
	for _, pt := range b {
		out.CPU += pt.CPU
		out.IOWait += pt.IOWait
		out.Steal += pt.Steal
		out.Load1 += pt.Load1
		mem += float64(pt.Mem)
		swap += float64(pt.Swap)
		out.DiskRead += pt.DiskRead
		out.DiskWrite += pt.DiskWrite
		out.NetRx += pt.NetRx
		out.NetTx += pt.NetTx
	}
	n := float64(len(b))
	out.CPU /= n
	out.IOWait /= n
	out.Steal /= n
	out.Load1 /= n
	out.Mem = uint64(mem / n)
	out.Swap = uint64(swap / n)
	out.DiskRead /= n
	out.DiskWrite /= n
	out.NetRx /= n
	out.NetTx /= n
	return out
}

// MetricsRange parses a chart range like "1h" or "24h": at least ten
// minutes, at most the day that's kept.
func MetricsRange(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return time.Hour
	}
	return min(max(d, 10*time.Minute), 24*time.Hour)
}

// Metrics returns a project's samples over the last span, oldest first.
func (p *Platform) Metrics(name string, span time.Duration) ([]MetricPoint, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return window(p.projectMetrics[proj.ID], span, func(m MetricPoint) int64 { return m.T }, meanMetric), nil
}

// ServerMetrics returns the server's samples over the last span, oldest
// first.
func (p *Platform) ServerMetrics(span time.Duration) []ServerPoint {
	p.mu.Lock()
	defer p.mu.Unlock()
	return window(p.serverMetrics, span, func(m ServerPoint) int64 { return m.T }, meanServer)
}

// Resources is the server's current usage, for the dashboard's header.
type Resources struct {
	CPUs           int     `json:"cpus"`
	CPUPercent     float64 `json:"cpuPercent"`
	Load1          float64 `json:"load1"`
	MemTotalBytes  uint64  `json:"memTotalBytes"`
	MemAvailBytes  uint64  `json:"memAvailableBytes"`
	DiskTotalBytes uint64  `json:"diskTotalBytes"`
	DiskUsedBytes  uint64  `json:"diskUsedBytes"`
}

// Server reports the latest sample, and the disk holding the data dir.
func (p *Platform) Server() Resources {
	p.mu.Lock()
	s := p.lastSample
	p.mu.Unlock()
	d := sysinfo.Disk(p.cfg.DataDir)
	return Resources{
		CPUs: p.host.CPUs, CPUPercent: s.CPUPercent, Load1: s.Load1,
		MemTotalBytes: s.MemTotalBytes, MemAvailBytes: s.MemAvailBytes,
		DiskTotalBytes: d.TotalBytes, DiskUsedBytes: d.UsedBytes,
	}
}

// SystemApp is one app's share of the server.
type SystemApp struct {
	Name           string    `json:"name"`
	State          string    `json:"state"`
	CPUPercent     float64   `json:"cpuPercent"`
	MemoryBytes    uint64    `json:"memoryBytes"`
	MemoryMaxBytes uint64    `json:"memoryMaxBytes"` // its limit
	DataBytes      int64     `json:"dataBytes"`
	StartedAt      time.Time `json:"startedAt,omitzero"`
}

// System is everything the dashboard's System page shows.
type System struct {
	Host     sysinfo.Host   `json:"host"`
	Now      sysinfo.Sample `json:"now"`
	Mounts   []SystemMount  `json:"mounts"`
	Apps     []SystemApp    `json:"apps"`
	Sideyard SystemSideyard `json:"sideyard"`
}

type SystemMount struct {
	sysinfo.Mount
	// Data is set on the mount holding Sideyard's data directory.
	Data bool `json:"data"`
}

type SystemSideyard struct {
	Version     string    `json:"version"`
	Runner      string    `json:"runner"`
	Instance    string    `json:"instance"`
	DataDir     string    `json:"dataDir"`
	StartedAt   time.Time `json:"startedAt"`
	CPUPercent  float64   `json:"cpuPercent"`
	MemoryBytes uint64    `json:"memoryBytes"`
}

func (p *Platform) System() (System, error) {
	projects, err := p.store.Projects()
	if err != nil {
		return System{}, err
	}
	instance, _ := p.InstanceID()
	p.mu.Lock()
	out := System{Host: p.host, Now: p.lastSample, Sideyard: SystemSideyard{
		Version: p.version, Runner: p.runner.Name(), Instance: instance, DataDir: p.cfg.DataDir,
		StartedAt: p.started, CPUPercent: p.self.CPU, MemoryBytes: p.self.Mem,
	}}
	p.mu.Unlock()

	dataDir := p.cfg.DataDir
	if real, err := filepath.EvalSymlinks(dataDir); err == nil {
		dataDir = real
	}
	mounts := sysinfo.Mounts()
	data := sysinfo.MountFor(mounts, dataDir)
	for i, m := range mounts {
		out.Mounts = append(out.Mounts, SystemMount{Mount: m, Data: i == data})
	}
	if data < 0 { // not on a filesystem we list (macOS, or a FUSE mount)
		out.Mounts = append(out.Mounts, SystemMount{Mount: sysinfo.Disk(p.cfg.DataDir), Data: true})
	}

	out.Apps = []SystemApp{}
	for _, proj := range projects {
		st := p.Status(proj)
		out.Apps = append(out.Apps, SystemApp{
			Name: proj.Name, State: st.State, CPUPercent: st.CPUPercent, MemoryBytes: st.MemoryBytes,
			MemoryMaxBytes: uint64(proj.MemoryMB) << 20, DataBytes: p.DataSize(proj.Name), StartedAt: st.StartedAt,
		})
	}
	return out, nil
}
