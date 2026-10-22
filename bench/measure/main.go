// Command measure puts one app stack under rising load and samples the memory
// and CPU of every process in it (the app, its database server, workers).
//
// Load is open-loop: each stage sends requests at a fixed rate, whether or not
// earlier ones have finished, the way independent users do. 80% list the
// newest notes (GET /api/notes), 20% create one (POST /api/notes).
//
// Memory is the sum of PSS over the stack's process trees, so pages shared
// between processes (Postgres's shared buffers, forked workers) count once.
// CPU is CPU-seconds used per second: 1.0 is one core kept busy.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type stage struct {
	Rate       int     `json:"rate"` // requests per second sent
	Sent       int     `json:"sent"`
	OK         int     `json:"ok"`
	Failed     int     `json:"failed"` // non-2xx, connection errors and timeouts
	Throughput float64 `json:"throughput"`
	P50ms      float64 `json:"p50ms"`
	P95ms      float64 `json:"p95ms"`
	P99ms      float64 `json:"p99ms"`
	MemMB      float64 `json:"memMB"`    // mean PSS while sampling, whole stack
	MemMaxMB   float64 `json:"memMaxMB"` // highest sample
	CPU        float64 `json:"cpu"`      // mean cores busy, whole stack
	Procs      int     `json:"procs"`
	HostLoad   float64 `json:"hostLoad"` // 1-minute load average at the end, for noise
	// The same figures per part of the stack, like "app" and "db".
	Parts map[string]part `json:"parts"`
}

type part struct {
	MemMB float64 `json:"memMB"`
	CPU   float64 `json:"cpu"`
}

// group is one part of the stack: the process trees under its root PIDs.
type group struct {
	name  string
	roots []int
}

type result struct {
	Name      string  `json:"name"`
	StartupMS int     `json:"startupMs"`
	DiskMB    float64 `json:"diskMB"`
	Stages    []stage `json:"stages"`
}

func main() {
	var (
		name      = flag.String("name", "", "stack name for the report")
		base      = flag.String("url", "", "app base URL, like http://127.0.0.1:47101")
		procList  = flag.String("procs", "", "parts of the stack and their root PIDs, like app=123,db=456,db=457")
		rates     = flag.String("rates", "0,25,50,100,200,400,800,1600", "requests per second for each stage; 0 is idle")
		stageDur  = flag.Duration("stage", 30*time.Second, "length of each stage")
		settle    = flag.Duration("settle", 10*time.Second, "time at the start of each stage that isn't sampled")
		seed      = flag.Int("seed", 10000, "notes to create before the first stage")
		startupMS = flag.Int("startup-ms", 0, "measured time from start to healthy, copied into the report")
		diskMB    = flag.Float64("disk-mb", 0, "measured install size, copied into the report")
		out       = flag.String("out", "", "file for the JSON report")
	)
	flag.Parse()
	var groups []group
	for _, s := range strings.Split(*procList, ",") {
		name, p, _ := strings.Cut(strings.TrimSpace(s), "=")
		pid, err := strconv.Atoi(p)
		if err != nil || name == "" {
			log.Fatalf("bad process %q, want name=pid", s)
		}
		i := slices.IndexFunc(groups, func(g group) bool { return g.name == name })
		if i < 0 {
			groups = append(groups, group{name: name})
			i = len(groups) - 1
		}
		groups[i].roots = append(groups[i].roots, pid)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10000,
			MaxIdleConnsPerHost: 10000,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
		},
	}

	log.Printf("%s: seeding %d notes", *name, *seed)
	seedNotes(client, *base, *seed)

	res := result{Name: *name, StartupMS: *startupMS, DiskMB: *diskMB}
	for _, r := range strings.Split(*rates, ",") {
		rate, err := strconv.Atoi(strings.TrimSpace(r))
		if err != nil {
			log.Fatalf("bad rate %q", r)
		}
		st := runStage(client, *base, groups, rate, *stageDur, *settle)
		log.Printf("%s: %4d/s  ok %6d  failed %5d  p95 %7.1fms  mem %6.1f MB  cpu %.2f  procs %d  load %.1f",
			*name, rate, st.OK, st.Failed, st.P95ms, st.MemMB, st.CPU, st.Procs, st.HostLoad)
		res.Stages = append(res.Stages, st)
	}

	buf, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(*out, append(buf, '\n'), 0o644); err != nil {
		log.Fatal(err)
	}
}

func seedNotes(client *http.Client, base string, n int) {
	var next atomic.Int64
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			for i := next.Add(1); i <= int64(n); i = next.Add(1) {
				if _, err := request(client, base, true, i); err != nil {
					log.Fatalf("seeding: %v", err)
				}
			}
		})
	}
	wg.Wait()
}

// request sends one GET or POST and reports whether it got a 2xx.
func request(client *http.Client, base string, write bool, n int64) (bool, error) {
	var resp *http.Response
	var err error
	if write {
		body := fmt.Sprintf(`{"body":"note %d: remember to water the plants and reply to the landlord"}`, n)
		resp, err = client.Post(base+"/api/notes", "application/json", bytes.NewBufferString(body))
	} else {
		resp, err = client.Get(base + "/api/notes")
	}
	if err != nil {
		return false, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode/100 == 2, nil
}

func runStage(client *http.Client, base string, groups []group, rate int, dur, settle time.Duration) stage {
	st := stage{Rate: rate, Parts: map[string]part{}}
	var (
		mu        sync.Mutex
		latencies []float64
		ok, fail  int
		wg        sync.WaitGroup
		inFlight  atomic.Int64
		counter   atomic.Int64
	)
	done := make(chan struct{})
	if rate > 0 {
		go func() {
			interval := time.Second / time.Duration(rate)
			start := time.Now()
			for i := 0; ; i++ {
				// Schedule against the start time so slow ticks don't lower the rate.
				time.Sleep(time.Until(start.Add(time.Duration(i) * interval)))
				select {
				case <-done:
					return
				default:
				}
				st.Sent++
				if inFlight.Load() > 20000 { // the client itself is overwhelmed: count as failed
					mu.Lock()
					fail++
					mu.Unlock()
					continue
				}
				inFlight.Add(1)
				wg.Go(func() {
					defer inFlight.Add(-1)
					t := time.Now()
					good, _ := request(client, base, rand.Float64() < 0.2, counter.Add(1))
					ms := float64(time.Since(t).Microseconds()) / 1000
					mu.Lock()
					if good {
						ok++
						latencies = append(latencies, ms)
					} else {
						fail++
					}
					mu.Unlock()
				})
			}
		}()
	}

	time.Sleep(settle)
	var mems, cpus []float64
	gMem := make([][]float64, len(groups))
	gCPU := make([][]float64, len(groups))
	prev := make([]ticks, len(groups))
	for i, g := range groups {
		prev[i] = sampleTicks(g.roots)
	}
	t0 := time.Now()
	for time.Since(t0) < dur-settle {
		time.Sleep(time.Second)
		var mem, cpu float64
		var procs int
		for i, g := range groups {
			m, n := samplePSS(g.roots)
			cur := sampleTicks(g.roots)
			c := cpuBetween(prev[i], cur)
			prev[i] = cur
			gMem[i] = append(gMem[i], m)
			gCPU[i] = append(gCPU[i], c)
			mem, cpu, procs = mem+m, cpu+c, procs+n
		}
		mems = append(mems, mem)
		cpus = append(cpus, cpu)
		st.Procs = max(st.Procs, procs)
	}
	for i, g := range groups {
		st.Parts[g.name] = part{MemMB: mean(gMem[i]), CPU: mean(gCPU[i])}
	}
	close(done)
	wg.Wait()

	st.OK, st.Failed = ok, fail
	st.Throughput = float64(ok) / dur.Seconds()
	slices.Sort(latencies)
	st.P50ms, st.P95ms, st.P99ms = pct(latencies, 0.50), pct(latencies, 0.95), pct(latencies, 0.99)
	st.MemMB, st.MemMaxMB = mean(mems), slices.Max(mems)
	st.CPU = mean(cpus)
	st.HostLoad = loadAvg()
	return st
}

// tree returns the PIDs of roots and all their descendants.
func tree(roots []int) []int {
	children := map[int][]int{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		f := statFields(stat)
		if len(f) < 2 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		children[ppid] = append(children[ppid], pid)
	}
	var out []int
	queue := slices.Clone(roots)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		out = append(out, pid)
		queue = append(queue, children[pid]...)
	}
	return out
}

// statFields returns /proc/<pid>/stat after the command name, which can hold
// spaces: field 0 is the state, 1 the parent PID, 11 and 12 utime and stime.
func statFields(stat []byte) []string {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return nil
	}
	return strings.Fields(string(stat[i+1:]))
}

func samplePSS(roots []int) (mb float64, procs int) {
	for _, pid := range tree(roots) {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid))
		if err != nil {
			continue
		}
		procs++
		for _, line := range strings.Split(string(data), "\n") {
			if rest, ok := strings.CutPrefix(line, "Pss:"); ok {
				kb, _ := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")))
				mb += float64(kb) / 1024
			}
		}
	}
	return mb, procs
}

type ticks struct {
	at  time.Time
	pid map[int]int64
}

func sampleTicks(roots []int) ticks {
	t := ticks{at: time.Now(), pid: map[int]int64{}}
	for _, pid := range tree(roots) {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		f := statFields(stat)
		if len(f) < 13 {
			continue
		}
		u, _ := strconv.ParseInt(f[11], 10, 64)
		s, _ := strconv.ParseInt(f[12], 10, 64)
		t.pid[pid] = u + s
	}
	return t
}

// cpuBetween is the cores busy between two samples. Linux reports CPU time
// in ticks of 1/100 s on every platform Go supports.
func cpuBetween(a, b ticks) float64 {
	var used int64
	for pid, n := range b.pid {
		used += n - a.pid[pid] // a process that started in between counts from zero
	}
	return float64(used) / 100 / b.at.Sub(a.at).Seconds()
}

func loadAvg() float64 {
	data, _ := os.ReadFile("/proc/loadavg")
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[min(len(sorted)-1, int(p*float64(len(sorted))))]
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}
