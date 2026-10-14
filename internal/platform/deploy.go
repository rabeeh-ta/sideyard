package platform

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"sideyard/internal/runner"
	"sideyard/internal/store"
)

const keepReleases = 5 // binaries kept on disk for rollback

// Deploy takes an uploaded binary (a temp file, which it moves), records a
// release and makes it live if it passes its health check. On failure the
// previous release keeps serving traffic.
func (p *Platform) Deploy(ctx context.Context, name, upload, commit, message string) (store.Release, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return store.Release{}, err
	}
	unlock := p.lock(proj.ID)
	defer unlock()

	size, sum, err := fileDigest(upload)
	if err != nil {
		return store.Release{}, err
	}
	rel, err := p.store.CreateRelease(proj.ID, commit, message, size, sum)
	if err != nil {
		return rel, err
	}
	if err := p.install(proj, upload, p.binaryPath(name, rel.Number)); err != nil {
		return p.failRelease(rel, fmt.Sprintf("%s rejected: %v\n", stamp(), err), err)
	}
	return p.activate(ctx, proj, rel)
}

// install moves an upload into place, readable by the project's user, and
// checks it can run here.
func (p *Platform) install(proj store.Project, upload, bin string) error {
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		return err
	}
	if err := os.Rename(upload, bin); err != nil {
		return err
	}
	if err := os.Chmod(bin, 0o750); err != nil {
		return err
	}
	if err := p.runner.Own(p.ref(proj), filepath.Dir(bin), runner.AppReadable); err != nil {
		return err
	}
	return checkBinary(bin)
}

// Rollback makes an earlier release live again.
func (p *Platform) Rollback(ctx context.Context, name string, number int) (store.Release, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return store.Release{}, err
	}
	unlock := p.lock(proj.ID)
	defer unlock()
	releases, err := p.store.Releases(proj.ID, 1000)
	if err != nil {
		return store.Release{}, err
	}
	for _, rel := range releases {
		if rel.Number == number {
			if rel.Pruned {
				return rel, invalid("release #%d's binary has been pruned", number)
			}
			return p.activate(ctx, proj, rel)
		}
	}
	return store.Release{}, ErrNotFound
}

// Restart starts the active release in a new process and switches traffic
// once it's healthy, so there's no downtime. It also starts stopped projects.
func (p *Platform) Restart(ctx context.Context, name string) error {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return err
	}
	unlock := p.lock(proj.ID)
	defer unlock()
	if proj.ActiveReleaseID == 0 {
		return ErrNoRelease
	}
	rel, err := p.store.Release(proj.ActiveReleaseID)
	if err != nil {
		return err
	}
	_, err = p.activate(ctx, proj, rel)
	return err
}

func (p *Platform) Stop(name string) error {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return err
	}
	unlock := p.lock(proj.ID)
	defer unlock()
	if err := p.store.SetDesiredState(proj.ID, store.StateStopped); err != nil {
		return err
	}
	return p.stopLocked(proj)
}

// stopLocked takes a project out of service. The caller holds the project
// lock.
func (p *Platform) stopLocked(proj store.Project) error {
	l := p.setLive(proj.ID, nil)
	if err := p.store.SetLive(proj.ID, "", 0); err != nil {
		return err
	}
	err := p.applyRoutes()
	if l != nil {
		l.inst.Stop(10 * time.Second)
		p.logf(proj, "stopped")
	}
	return err
}

// activate starts rel in a new process, health-checks it, then points the
// proxy at it and stops the old process. The caller holds the project lock.
func (p *Platform) activate(ctx context.Context, proj store.Project, rel store.Release) (store.Release, error) {
	// A disconnecting client must not abandon a deploy half way.
	ctx = context.WithoutCancel(ctx)
	var steps strings.Builder
	step := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		steps.WriteString(stamp() + " " + msg + "\n")
		p.logf(proj, "release #%d: %s", rel.Number, msg)
	}

	step("starting")
	inst, port, err := p.start(proj, rel)
	if err != nil {
		step("failed to start: %v", err)
		return p.failRelease(rel, steps.String(), err)
	}
	step("started %s on port %d, checking %s", inst.ID(), port, proj.HealthPath)
	if err := runner.WaitHealthy(ctx, inst, healthURL(port, proj.HealthPath), p.healthTimeout); err != nil {
		inst.Stop(5 * time.Second)
		step("failed: %v", err)
		return p.failRelease(rel, steps.String(), err)
	}
	step("health check passed")

	old := p.setLive(proj.ID, &live{inst: inst, port: port, releaseID: rel.ID})
	if err := p.applyRoutes(); err != nil {
		step("warning: proxy update failed: %v", err)
	} else {
		step("live")
	}
	if old != nil {
		p.retire(old.inst)
	}
	if err := p.store.ActivateRelease(proj.ID, rel.ID, steps.String(), inst.ID(), port); err != nil {
		return rel, err
	}
	p.prune(proj)
	return p.store.Release(rel.ID)
}

// failRelease records a failed attempt. Only a new release is marked failed;
// a failed restart or rollback leaves the current process serving, so an
// existing release keeps its status.
func (p *Platform) failRelease(rel store.Release, logText string, cause error) (store.Release, error) {
	status := rel.Status
	if status == store.ReleaseStarting {
		status = store.ReleaseFailed
	}
	if err := p.store.UpdateRelease(rel.ID, status, logText); err != nil {
		return rel, errors.Join(cause, err)
	}
	updated, err := p.store.Release(rel.ID)
	if err != nil {
		return rel, errors.Join(cause, err)
	}
	return updated, cause
}

// start launches a release on a free port. It doesn't wait for health.
func (p *Platform) start(proj store.Project, rel store.Release) (runner.Instance, int, error) {
	bin := p.binaryPath(proj.Name, rel.Number)
	if _, err := os.Stat(bin); err != nil {
		return nil, 0, fmt.Errorf("release binary missing: %w", err)
	}
	port, err := freePort()
	if err != nil {
		return nil, 0, err
	}
	env, err := p.environment(proj, rel, port)
	if err != nil {
		return nil, 0, err
	}
	inst, err := p.runner.Start(runner.Spec{
		Project:    p.ref(proj),
		Release:    rel.Number,
		Binary:     bin,
		Args:       expandArgs(proj.Args, env),
		Env:        env,
		MemoryMB:   proj.MemoryMB,
		CPUPercent: proj.CPUPercent,
	})
	return inst, port, err
}

func (p *Platform) binaryPath(name string, number int) string {
	return p.dir(name, "releases", strconv.Itoa(number), "app")
}

// prune deletes binaries beyond the newest keepReleases, never the live one.
func (p *Platform) prune(proj store.Project) {
	releases, err := p.store.Releases(proj.ID, 1000)
	if err != nil {
		return
	}
	kept := 0
	for _, rel := range releases {
		if rel.Pruned {
			continue
		}
		if kept < keepReleases || rel.Status == store.ReleaseActive {
			kept++
			continue
		}
		if os.RemoveAll(filepath.Dir(p.binaryPath(proj.Name, rel.Number))) == nil {
			p.store.MarkPruned(rel.ID)
		}
	}
}

// checkBinary rejects uploads that can't run on this server, with a hint.
func checkBinary(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	want := runtime.GOOS + "/" + runtime.GOARCH
	hint := fmt.Sprintf("build with GOOS=%s GOARCH=%s CGO_ENABLED=0", runtime.GOOS, runtime.GOARCH)
	var got string
	if ef, err := elf.NewFile(f); err == nil {
		got = "linux/" + map[elf.Machine]string{elf.EM_X86_64: "amd64", elf.EM_AARCH64: "arm64"}[ef.Machine]
	} else if mf, err := macho.NewFile(f); err == nil {
		got = "darwin/" + map[macho.Cpu]string{macho.CpuAmd64: "amd64", macho.CpuArm64: "arm64"}[mf.Cpu]
	} else {
		return fmt.Errorf("not an executable; %s", hint)
	}
	if got != want {
		return fmt.Errorf("binary is built for %s but this server is %s; %s", got, want, hint)
	}
	return nil
}

func fileDigest(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

func stamp() string {
	return time.Now().UTC().Format("15:04:05")
}
