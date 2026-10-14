package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sideyard/internal/config"
	"sideyard/internal/update"
)

func TestMetricsWindow(t *testing.T) {
	var pts []MetricPoint
	end := int64(1_800_000_000)
	for i := range metricsKeep {
		t := end - int64(metricsKeep-1-i)*15
		pts = append(pts, MetricPoint{T: t, CPU: float64(i % 6), Mem: uint64(i)})
	}
	at := func(m MetricPoint) int64 { return m.T }

	hour := window(pts, time.Hour, at, meanMetric)
	if len(hour) != 240 || hour[len(hour)-1] != pts[len(pts)-1] {
		t.Fatalf("an hour is %d points ending %+v", len(hour), hour[len(hour)-1])
	}
	six := window(pts, 6*time.Hour, at, meanMetric)
	if len(six) != metricsMaxPoints {
		t.Fatalf("six hours is %d points", len(six))
	}
	// Six samples per bucket, CPU cycling 0..5: every average is 2.5.
	for _, pt := range six {
		if pt.CPU != 2.5 {
			t.Fatalf("bucket average %+v", pt)
		}
	}
	if six[len(six)-1].T != end {
		t.Fatalf("last bucket stamped %d, want %d", six[len(six)-1].T, end)
	}
	if day := window(pts, 24*time.Hour, at, meanMetric); len(day) != metricsMaxPoints {
		t.Fatalf("a day is %d points", len(day))
	}
	if empty := window(nil, time.Hour, at, meanMetric); empty == nil || len(empty) != 0 {
		t.Fatalf("no samples should be an empty list, got %#v", empty)
	}
	if MetricsRange("bogus") != time.Hour || MetricsRange("72h") != 24*time.Hour || MetricsRange("1m") != 10*time.Minute {
		t.Fatal("MetricsRange bounds")
	}
}

func TestSystem(t *testing.T) {
	e := newTestEnv(t)
	if _, _, err := e.p.CreateProject("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.deploy("demo"); err != nil {
		t.Fatal(err)
	}
	e.p.sampleMetrics()
	sys, err := e.p.System()
	if err != nil {
		t.Fatal(err)
	}
	if len(sys.Apps) != 1 || sys.Apps[0].Name != "demo" || sys.Apps[0].State != "running" || sys.Apps[0].MemoryMaxBytes != 512<<20 {
		t.Fatalf("apps: %+v", sys.Apps)
	}
	data := 0
	for _, m := range sys.Mounts {
		if m.Data {
			data++
		}
	}
	if data != 1 {
		t.Fatalf("want one mount holding the data dir: %+v", sys.Mounts)
	}
	if sys.Sideyard.Version != "dev" || sys.Sideyard.Runner != "process" || sys.Host.CPUs == 0 {
		t.Fatalf("sideyard %+v host %+v", sys.Sideyard, sys.Host)
	}
	if len(e.p.ServerMetrics(time.Hour)) == 0 {
		t.Fatal("no server sample")
	}
	if pts, err := e.p.Metrics("demo", time.Hour); err != nil || len(pts) == 0 {
		t.Fatalf("project samples: %v %v", pts, err)
	}
}

// releaseSource serves one release, laid out like GitHub Releases, whose
// binary is a script that reports its version.
func releaseSource(t *testing.T, version string) string {
	t.Helper()
	bin := []byte("#!/bin/sh\necho " + version + "\n")
	sum := sha256.Sum256(bin)
	name := "sideyard-" + update.Platform()
	manifest, _ := json.Marshal(update.Manifest{Version: version, Assets: map[string]update.Asset{
		update.Platform(): {Name: name, Size: int64(len(bin)), SHA256: hex.EncodeToString(sum[:])},
	}})
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest/download/release.json", func(w http.ResponseWriter, r *http.Request) { w.Write(manifest) })
	mux.HandleFunc("/releases/download/"+version+"/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(bin) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestUpdate(t *testing.T) {
	plain := newTestEnv(t).p
	if st := plain.UpdateStatus(); st.Source != "" || st.Available {
		t.Fatalf("a build without a source: %+v", st)
	}
	if err := plain.InstallUpdate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("install without a source: %v", err)
	}

	source := releaseSource(t, "v1.1.0")
	p := newTestEnv(t, func(c *config.Config) { c.UpdateSource = source }).p
	p.version = "v1.1.0"
	if st := p.CheckForUpdate(context.Background()); st.Latest != "v1.1.0" || st.Available || st.Error != "" {
		t.Fatalf("up to date: %+v", st)
	}
	if err := p.InstallUpdate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("install when up to date: %v", err)
	}

	p.version = "v1.0.0"
	p.exe = filepath.Join(t.TempDir(), "sideyard")
	os.WriteFile(p.exe, []byte("#!/bin/sh\necho v1.0.0\n"), 0o755)
	if err := p.SetAutoUpdate(true); err != nil {
		t.Fatal(err)
	}
	st := p.UpdateStatus()
	if !st.Available || !st.AutoInstall || st.State != "idle" {
		t.Fatalf("before install: %+v", st)
	}
	if err := p.InstallUpdate(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.RestartRequested():
	case <-time.After(10 * time.Second):
		t.Fatalf("no restart requested: %+v", p.UpdateStatus())
	}
	if b, _ := os.ReadFile(p.exe); !strings.Contains(string(b), "v1.1.0") {
		t.Fatalf("binary not replaced: %q", b)
	}
	if st := p.UpdateStatus(); st.State != "restarting" {
		t.Fatalf("after install: %+v", st)
	}
	// The new binary is on probation until it boots.
	if _, err := os.Stat(p.exe + ".pending"); err != nil {
		t.Fatalf("update not marked pending: %v", err)
	}
	// The restarted Sideyard reports where it came from.
	p.version = "v1.1.0"
	if st := p.UpdateStatus(); st.Last == nil || st.Last.From != "v1.0.0" || st.Last.To != "v1.1.0" {
		t.Fatalf("last update: %+v", st.Last)
	}
}
