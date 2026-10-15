// Package runner runs project releases. Two implementations share one
// interface: Systemd (production on Linux: one transient unit per release,
// a Unix user per project, resource limits, journald logs) and Process (dev
// mode and anywhere else: supervised child processes logging to a file).
package runner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

type State string

const (
	Running State = "running"
	Crashed State = "crashed"
	Stopped State = "stopped"
)

// ProjectRef identifies a project to the runner.
type ProjectRef struct {
	ID   int64
	Name string
	Dir  string // <data dir>/projects/<name>
}

func (p ProjectRef) DataDir() string     { return filepath.Join(p.Dir, "data") }
func (p ProjectRef) ReleasesDir() string { return filepath.Join(p.Dir, "releases") }

// Spec is one release to start.
type Spec struct {
	Project    ProjectRef
	Release    int
	Binary     string
	Args       []string
	Env        []string // the complete environment
	MemoryMB   int      // 0 means no limit
	CPUPercent int      // 0 means no limit; 100 is one full core
}

type Status struct {
	State       State
	PID         int
	StartedAt   time.Time
	Restarts    int
	MemoryBytes uint64
	// CPUNanos is CPU time used so far; the platform turns samples of it
	// into a percentage.
	CPUNanos uint64
}

// Instance is one running release.
type Instance interface {
	// ID is stable across Sideyard restarts for runners that are Detached.
	ID() string
	Status() Status
	Stop(timeout time.Duration)
}

// Ownership says who may write a path inside a project.
type Ownership int

const (
	AppWritable Ownership = iota // the app's data: owned by the project's user
	AppReadable                  // release binaries: owned by root, readable by the project's group
)

type Runner interface {
	Name() string
	// Detached runners' instances outlive Sideyard, so a Sideyard restart
	// leaves apps running and Sideyard adopts them again on boot.
	Detached() bool
	// Init prepares the data dir once at startup. instance is a short ID
	// unique to this Sideyard installation: units and users carry it, so
	// two installations on one machine never touch each other's apps.
	Init(dataDir, instance string) error
	// Prepare makes sure a project can run: its OS user and directories.
	Prepare(p ProjectRef) error
	// Own sets ownership of a path inside the project, recursively.
	Own(p ProjectRef, path string, o Ownership) error
	Start(s Spec) (Instance, error)
	// Adopt reattaches to an instance from before a restart.
	Adopt(p ProjectRef, id string) (Instance, bool)
	// Sweep stops instances that aren't in keep: leftovers from a crash.
	Sweep(keep map[string]bool)
	// Logf writes a Sideyard event into the project's log.
	Logf(p ProjectRef, format string, args ...any)
	// Tail returns the project's newest n log lines, oldest first; with a
	// match, only lines containing it (ignoring case), searched further back.
	Tail(p ProjectRef, n int, match string) ([]string, error)
	// Credential is how to run a helper command as the project's user, or
	// nil to run it as Sideyard.
	Credential(p ProjectRef) (*syscall.Credential, error)
	// Remove deletes the project's OS user and log. Its instances must
	// already be stopped.
	Remove(p ProjectRef) error
}

// New picks a runner: kind is "systemd", "process" or "" for automatic,
// which means systemd on Linux when running as root under systemd.
func New(kind string) (Runner, error) {
	switch kind {
	case "process":
		return NewProcess(), nil
	case "systemd":
		return NewSystemd()
	case "":
		if SystemdAvailable() {
			return NewSystemd()
		}
		return NewProcess(), nil
	}
	return nil, fmt.Errorf("unknown runner %q (want systemd or process)", kind)
}

func SystemdAvailable() bool {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return false
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	_, err := exec.LookPath("systemd-run")
	return err == nil
}

// WaitHealthy polls url until it answers below 400. It fails early if the
// instance exits or restarts before then.
func WaitHealthy(ctx context.Context, inst Instance, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	lastErr := errors.New("no response yet")
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 400 {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		if st := inst.Status(); st.State != Running || st.Restarts > 0 {
			return errors.New("process exited before its health check passed (see Logs)")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("health check %s did not pass within %s: %v", url, timeout, lastErr)
		case <-time.After(250 * time.Millisecond):
		}
	}
}
