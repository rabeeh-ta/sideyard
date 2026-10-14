package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v1.2.4", "v1.2.3", true},
		{"v1.10.0", "v1.9.9", true},
		{"v2.0.0", "v1.99.99", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.3", "v1.2.4", false},
		{"v1.2.0", "v1.2.0-rc.1", true},
		{"v1.2.0-rc.1", "v1.2.0", false},
		{"v1.2.0-rc.2", "v1.2.0-rc.1", true},
		{"v1.2.3", "dev", false},
		{"dev", "v1.2.3", false},
		{"1.2.3", "v1.0.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

// release serves a source with one release whose binary is a script that
// reports version said.
func release(t *testing.T, version, said string, tamper bool) (*httptest.Server, Manifest) {
	t.Helper()
	bin := []byte("#!/bin/sh\necho " + said + "\n")
	sum := sha256.Sum256(bin)
	m := Manifest{Version: version, Assets: map[string]Asset{
		Platform(): {Name: "sideyard-" + Platform(), Size: int64(len(bin)), SHA256: hex.EncodeToString(sum[:])},
	}}
	if tamper {
		bin[len(bin)-2] = 'X'
	}
	manifest, _ := json.Marshal(m)
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest/download/release.json", func(w http.ResponseWriter, r *http.Request) { w.Write(manifest) })
	mux.HandleFunc("/releases/download/"+version+"/release.json", func(w http.ResponseWriter, r *http.Request) { w.Write(manifest) })
	mux.HandleFunc("/releases/download/"+version+"/sideyard-"+Platform(), func(w http.ResponseWriter, r *http.Request) { w.Write(bin) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, m
}

func current(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "sideyard")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho v1.0.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestInstall(t *testing.T) {
	srv, _ := release(t, "v1.1.0", "v1.1.0", false)
	ctx := context.Background()
	m, err := Latest(ctx, srv.Client(), srv.URL)
	if err != nil || m.Version != "v1.1.0" {
		t.Fatalf("latest: %+v %v", m, err)
	}
	if pinned, err := Release(ctx, srv.Client(), srv.URL, "v1.1.0"); err != nil || pinned.Version != "v1.1.0" {
		t.Fatalf("release: %+v %v", pinned, err)
	}
	exe := current(t)
	// Sideyard runs with umask 077; the binary must still be runnable by
	// apps' users.
	old := syscall.Umask(0o077)
	err = Install(ctx, srv.Client(), srv.URL, m, exe)
	syscall.Umask(old)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(exe); info.Mode().Perm() != 0o755 {
		t.Fatalf("installed binary is %v, want 0755", info.Mode().Perm())
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "v1.1.0") {
		t.Fatalf("binary not replaced: %q", b)
	}
	if b, _ := os.ReadFile(exe + ".prev"); !strings.Contains(string(b), "v1.0.0") {
		t.Fatalf("previous binary not kept: %q", b)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatal("download left behind")
	}
}

func TestInstallRefusesBadBinaries(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		said   string
		tamper bool
		want   string
	}{
		"tampered":      {"v1.1.0", true, "checksum mismatch"},
		"wrong version": {"v1.0.9", false, `says it's "v1.0.9"`},
	} {
		t.Run(name, func(t *testing.T) {
			srv, m := release(t, "v1.1.0", c.said, c.tamper)
			exe := current(t)
			err := Install(ctx, srv.Client(), srv.URL, m, exe)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "v1.0.0") {
				t.Fatal("current binary changed")
			}
			entries, _ := os.ReadDir(filepath.Dir(exe))
			if len(entries) != 1 {
				t.Fatalf("left files behind: %v", entries)
			}
		})
	}
}

func TestManifestMustHaveAVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"latest"}`))
	}))
	defer srv.Close()
	if _, err := Latest(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("accepted a manifest without a release version")
	}
}

func TestRollback(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sideyard")
	os.WriteFile(exe, []byte("new"), 0o755)
	os.WriteFile(exe+".prev", []byte("old"), 0o755)

	// No mark: nothing to do.
	if rolled, err := BootAttempt(exe, "v1.1.0"); rolled || err != nil {
		t.Fatalf("without a mark: %v %v", rolled, err)
	}
	if err := MarkPending(exe, "v1.0.0", "v1.1.0"); err != nil {
		t.Fatal(err)
	}
	// Three starts that never boot, then the fourth puts the old binary back.
	for i := range maxBootAttempts {
		if rolled, err := BootAttempt(exe, "v1.1.0"); rolled || err != nil {
			t.Fatalf("attempt %d: %v %v", i+1, rolled, err)
		}
	}
	rolled, err := BootAttempt(exe, "v1.1.0")
	if !rolled || err != nil {
		t.Fatalf("expected a rollback: %v %v", rolled, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatalf("binary is %q after the rollback", b)
	}
	if b, _ := os.ReadFile(exe + ".failed"); string(b) != "new" {
		t.Fatalf("failed binary not kept: %q", b)
	}
	if r, ok := RolledBack(exe); !ok || r.To != "v1.1.0" || r.From != "v1.0.0" {
		t.Fatalf("rollback record: %+v %v", r, ok)
	}
	// The old binary boots normally.
	if rolled, err := BootAttempt(exe, "v1.0.0"); rolled || err != nil {
		t.Fatalf("old binary: %v %v", rolled, err)
	}
}

func TestBootClearsTheMark(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "sideyard")
	os.WriteFile(exe, []byte("new"), 0o755)
	MarkPending(exe, "v1.0.0", "v1.1.0")
	BootAttempt(exe, "v1.1.0")
	Booted(exe)
	for range maxBootAttempts + 1 {
		if rolled, _ := BootAttempt(exe, "v1.1.0"); rolled {
			t.Fatal("rolled back a binary that booted")
		}
	}
	// A mark for another version (the binary was replaced by hand) is dropped.
	MarkPending(exe, "v1.0.0", "v1.2.0")
	BootAttempt(exe, "v1.1.0")
	if _, err := os.Stat(exe + ".pending"); !os.IsNotExist(err) {
		t.Fatal("stale mark kept")
	}
}
