// Package update finds new Sideyard releases and installs them over the
// running binary.
//
// A release source is laid out like GitHub Releases, so a GitHub repository
// works as one directly:
//
//	<source>/releases/latest/download/release.json
//	<source>/releases/download/<version>/release.json
//	<source>/releases/download/<version>/<asset>
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Manifest describes a release. release.json is published with every one.
type Manifest struct {
	Version   string    `json:"version"`
	Published time.Time `json:"published"`
	// Assets are the binaries, by platform ("linux-amd64").
	Assets map[string]Asset `json:"assets"`
}

type Asset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Platform is this build's OS and architecture, as assets are keyed.
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// Latest fetches the newest release's manifest.
func Latest(ctx context.Context, client *http.Client, source string) (Manifest, error) {
	return fetchManifest(ctx, client, source+"/releases/latest/download/release.json")
}

// Release fetches a given version's manifest.
func Release(ctx context.Context, client *http.Client, source, version string) (Manifest, error) {
	return fetchManifest(ctx, client, source+"/releases/download/"+version+"/release.json")
}

func fetchManifest(ctx context.Context, client *http.Client, url string) (Manifest, error) {
	var m Manifest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return m, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("%s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return m, fmt.Errorf("%s: %w", url, err)
	}
	if _, ok := parse(m.Version); !ok {
		return m, fmt.Errorf("%s: %q isn't a release version", url, m.Version)
	}
	return m, nil
}

// Newer reports whether release version a is newer than b. A version that
// isn't vMAJOR.MINOR.PATCH (a local "dev" build) is never newer, and
// nothing is newer than it.
func Newer(a, b string) bool {
	va, ok := parse(a)
	vb, okb := parse(b)
	if !ok || !okb {
		return false
	}
	for i := range 3 {
		if va.n[i] != vb.n[i] {
			return va.n[i] > vb.n[i]
		}
	}
	// A pre-release comes before its release: v1.2.0-rc.1 < v1.2.0.
	switch {
	case va.pre == vb.pre:
		return false
	case va.pre == "":
		return true
	case vb.pre == "":
		return false
	}
	return va.pre > vb.pre
}

type semver struct {
	n   [3]int
	pre string
}

func parse(v string) (semver, bool) {
	var s semver
	rest, ok := strings.CutPrefix(v, "v")
	if !ok {
		return s, false
	}
	rest, s.pre, _ = strings.Cut(rest, "-")
	parts := strings.Split(rest, ".")
	if len(parts) != 3 {
		return s, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return s, false
		}
		s.n[i] = n
	}
	return s, true
}

// Install downloads the release's binary for this platform next to exe,
// checks its size and checksum, checks that it runs and reports the
// release's version, then puts it in exe's place. The binary it replaces is
// kept as exe.prev. The running process is unaffected until it restarts.
func Install(ctx context.Context, client *http.Client, source string, m Manifest, exe string) error {
	asset, ok := m.Assets[Platform()]
	if !ok {
		return fmt.Errorf("%s has no build for %s", m.Version, Platform())
	}
	if !validName(asset.Name) {
		return fmt.Errorf("release asset name %q", asset.Name)
	}
	exe, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	tmp := exe + ".new"
	if err := download(ctx, client, source+"/releases/download/"+m.Version+"/"+asset.Name, tmp, asset); err != nil {
		os.Remove(tmp)
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EROFS) {
			return fmt.Errorf("can't write next to %s (%w); install with the install script, which puts Sideyard where it can update itself", exe, err)
		}
		return err
	}
	// Explicitly, since Sideyard runs with umask 077: apps' users run this
	// binary too (sqlite-query, sqlite-snapshot), so it must stay 0755.
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := checkRuns(ctx, tmp, m.Version); err != nil {
		os.Remove(tmp)
		return err
	}
	prev := exe + ".prev"
	os.Remove(prev)
	if err := os.Link(exe, prev); err != nil {
		if err := copyFile(exe, prev); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("keep the current binary: %w", err)
		}
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func validName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\`) && name != "." && name != ".."
}

func download(ctx context.Context, client *http.Client, url, dst string, want Asset) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, want.Size+1))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n != want.Size {
		return fmt.Errorf("download is %d bytes, the release says %d", n, want.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want.SHA256) {
		return fmt.Errorf("checksum mismatch: got %s, the release says %s", got, want.SHA256)
	}
	return nil
}

// checkRuns runs the new binary's version command: it must start on this
// machine and say it's the version the manifest promised.
func checkRuns(ctx context.Context, bin, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.Env = []string{} // it needs nothing from Sideyard's environment
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("the new binary doesn't run here: %w", err)
	}
	if got, _, _ := strings.Cut(strings.TrimSpace(string(out)), " "); got != version {
		return fmt.Errorf("the new binary says it's %q, the release says %s", got, version)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Chmod(0o755); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
