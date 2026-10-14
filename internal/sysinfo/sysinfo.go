// Package sysinfo reads the server's and processes' resource usage from
// /proc and /sys. Figures are only available on Linux; elsewhere they are
// zero, apart from disk space and what Go itself knows.
package sysinfo

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Host describes the machine. It changes rarely, so read it once.
type Host struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"` // PRETTY_NAME from /etc/os-release
	Kernel   string `json:"kernel"`
	Arch     string `json:"arch"`
	CPUModel string `json:"cpuModel"`
	CPUs     int    `json:"cpus"`
	// Machine is the hardware or hypervisor's description of itself, such
	// as "Microsoft Corporation Virtual Machine" or "Amazon EC2 t3.small".
	Machine  string    `json:"machine"`
	BootedAt time.Time `json:"bootedAt,omitzero"`
}

// Sample is the server's usage at one moment. Percentages and rates cover
// the time since the previous sample from the same Sampler.
type Sample struct {
	At         time.Time `json:"at"`
	CPUPercent float64   `json:"cpuPercent"` // busy, of all cores, 0–100
	IOWait     float64   `json:"iowaitPercent"`
	// Steal is time the hypervisor gave this VM's CPUs to someone else.
	Steal          float64 `json:"stealPercent"`
	Load1          float64 `json:"load1"`
	Load5          float64 `json:"load5"`
	Load15         float64 `json:"load15"`
	Processes      int     `json:"processes"`
	MemTotalBytes  uint64  `json:"memTotalBytes"`
	MemAvailBytes  uint64  `json:"memAvailableBytes"`
	MemCachedBytes uint64  `json:"memCachedBytes"` // page cache and buffers, given back when needed
	SwapTotalBytes uint64  `json:"swapTotalBytes"`
	SwapFreeBytes  uint64  `json:"swapFreeBytes"`
	DiskReadBps    float64 `json:"diskReadBps"`
	DiskWriteBps   float64 `json:"diskWriteBps"`
	NetRxBps       float64 `json:"netRxBps"`
	NetTxBps       float64 `json:"netTxBps"`
}

// MemUsedBytes is memory that can't be reclaimed: total minus available.
func (s Sample) MemUsedBytes() uint64 {
	if s.MemTotalBytes > s.MemAvailBytes {
		return s.MemTotalBytes - s.MemAvailBytes
	}
	return 0
}

func (s Sample) SwapUsedBytes() uint64 {
	if s.SwapTotalBytes > s.SwapFreeBytes {
		return s.SwapTotalBytes - s.SwapFreeBytes
	}
	return 0
}

// Mount is a filesystem's space.
type Mount struct {
	Path       string `json:"path"`
	Device     string `json:"device"`
	FSType     string `json:"fsType"`
	TotalBytes uint64 `json:"totalBytes"`
	UsedBytes  uint64 `json:"usedBytes"`
	// AvailBytes is what's free for anyone but root.
	AvailBytes uint64 `json:"availBytes"`
}

// Disk reports the filesystem holding path.
func Disk(path string) Mount {
	m := Mount{Path: path}
	statMount(&m)
	return m
}

func statMount(m *Mount) bool {
	var st syscall.Statfs_t
	if syscall.Statfs(m.Path, &st) != nil {
		return false
	}
	bsize := uint64(st.Bsize)
	m.TotalBytes = uint64(st.Blocks) * bsize
	m.UsedBytes = (uint64(st.Blocks) - uint64(st.Bfree)) * bsize
	m.AvailBytes = uint64(st.Bavail) * bsize
	return true
}

// Sampler turns the kernel's cumulative counters into rates. Keep one per
// series of samples; it's safe for concurrent use.
type Sampler struct {
	mu   sync.Mutex
	prev counters
}

// counters are cumulative since boot.
type counters struct {
	at        time.Time
	cpu       cpuTimes
	diskRead  uint64 // bytes
	diskWrite uint64
	netRx     uint64
	netTx     uint64
}

func (s *Sampler) Sample() Sample {
	now := time.Now()
	cur := readCounters()
	cur.at = now
	out := Sample{At: now}
	readLoadAndMemory(&out)

	s.mu.Lock()
	prev := s.prev
	s.prev = cur
	s.mu.Unlock()
	if prev.at.IsZero() {
		return out
	}
	if cur.cpu.total() > prev.cpu.total() {
		total := cur.cpu.total() - prev.cpu.total()
		pct := func(c, p uint64) float64 {
			if c < p {
				return 0
			}
			return 100 * float64(c-p) / float64(total)
		}
		out.CPUPercent = pct(cur.cpu.busy(), prev.cpu.busy())
		out.IOWait = pct(cur.cpu.iowait, prev.cpu.iowait)
		out.Steal = pct(cur.cpu.steal, prev.cpu.steal)
	}
	secs := now.Sub(prev.at).Seconds()
	rate := func(c, p uint64) float64 {
		if c < p || secs <= 0 { // counter reset, or a device went away
			return 0
		}
		return float64(c-p) / secs
	}
	out.DiskReadBps = rate(cur.diskRead, prev.diskRead)
	out.DiskWriteBps = rate(cur.diskWrite, prev.diskWrite)
	out.NetRxBps = rate(cur.netRx, prev.netRx)
	out.NetTxBps = rate(cur.netTx, prev.netTx)
	return out
}

// cpuTimes are jiffies from the "cpu" line of /proc/stat.
type cpuTimes struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

// Guest time is already counted in user and nice.
func (c cpuTimes) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}

// busy is everything but idle, iowait and steal: time this VM's CPUs ran.
func (c cpuTimes) busy() uint64 { return c.user + c.nice + c.system + c.irq + c.softirq }

// ReadHost describes this machine.
func ReadHost() Host {
	h := Host{Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), OS: runtime.GOOS}
	h.Hostname, _ = os.Hostname()
	readHost(&h)
	return h
}

// unescapeMount decodes the octal escapes /proc/self/mounts uses for
// spaces, tabs, newlines and backslashes in paths.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if v, ok := octal(s[i+1 : i+4]); ok {
				b.WriteByte(v)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func octal(s string) (byte, bool) {
	if len(s) != 3 {
		return 0, false
	}
	var v int
	for _, c := range s {
		if c < '0' || c > '7' {
			return 0, false
		}
		v = v*8 + int(c-'0')
	}
	return byte(v), v < 256
}
