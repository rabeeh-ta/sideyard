package runner

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"sideyard/internal/sysinfo"
)

// Process runs releases as child processes of Sideyard, as Sideyard's user,
// logging to <project>/logs/app.log. Apps stop when Sideyard stops, and
// resource limits aren't applied. It's the runner for dev mode and for
// hosts without systemd.
type Process struct {
	mu   sync.Mutex
	logs map[int64]*Log
}

func NewProcess() *Process {
	return &Process{logs: map[int64]*Log{}}
}

func (r *Process) Name() string                        { return "process" }
func (r *Process) Detached() bool                      { return false }
func (r *Process) Init(dataDir, instance string) error { return nil }

func (r *Process) Prepare(p ProjectRef) error {
	_, err := r.log(p)
	return err
}

func (r *Process) Own(ProjectRef, string, Ownership) error { return nil }

func (r *Process) Credential(ProjectRef) (*syscall.Credential, error) { return nil, nil }

func (r *Process) Adopt(ProjectRef, string) (Instance, bool) { return nil, false }

func (r *Process) Sweep(map[string]bool) {}

func (r *Process) Start(s Spec) (Instance, error) {
	l, err := r.log(s.Project)
	if err != nil {
		return nil, err
	}
	return startProc(procSpec{
		Binary: s.Binary,
		Args:   s.Args,
		Dir:    s.Project.DataDir(),
		Env:    s.Env,
		Log:    l,
		Tag:    "#" + strconv.Itoa(s.Release),
	})
}

func (r *Process) Logf(p ProjectRef, format string, args ...any) {
	if l, err := r.log(p); err == nil {
		l.Printf(format, args...)
	}
}

func (r *Process) Tail(p ProjectRef, n int) ([]string, error) {
	l, err := r.log(p)
	if err != nil {
		return nil, err
	}
	return l.Tail(n)
}

func (r *Process) Remove(p ProjectRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.logs[p.ID]; ok {
		delete(r.logs, p.ID)
		return l.Close()
	}
	return nil
}

func (r *Process) log(p ProjectRef) (*Log, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.logs[p.ID]; ok {
		return l, nil
	}
	dir := filepath.Join(p.Dir, "logs")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	l, err := OpenLog(filepath.Join(dir, "app.log"))
	if err != nil {
		return nil, err
	}
	r.logs[p.ID] = l
	return l, nil
}

type procSpec struct {
	Binary string
	Args   []string
	Dir    string
	Env    []string
	Log    *Log
	Tag    string // prefix on this process's log lines, e.g. "#12"
}

// proc is one supervised process. If it exits without Stop being called, it
// is restarted with exponential backoff.
type proc struct {
	spec procSpec

	mu        sync.Mutex
	cmd       *exec.Cmd
	out, err  *lineWriter
	state     State
	startedAt time.Time
	restarts  int
	stopping  bool

	stop chan struct{}
	done chan struct{} // closed when supervision ends
}

func startProc(spec procSpec) (*proc, error) {
	p := &proc{spec: spec, stop: make(chan struct{}), done: make(chan struct{})}
	if err := p.launch(); err != nil {
		return nil, err
	}
	go p.supervise()
	return p, nil
}

func (p *proc) ID() string {
	return fmt.Sprintf("pid-%d", p.Status().PID)
}

func (p *proc) launch() error {
	cmd := exec.Command(p.spec.Binary, p.spec.Args...)
	cmd.Dir = p.spec.Dir
	cmd.Env = p.spec.Env
	out := &lineWriter{log: p.spec.Log, tag: p.spec.Tag}
	errw := &lineWriter{log: p.spec.Log, tag: p.spec.Tag}
	cmd.Stdout, cmd.Stderr = out, errw
	// Don't hang on output pipes held open by a leftover child process.
	cmd.WaitDelay = 5 * time.Second
	// Own process group, so stopping also stops anything the app spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	p.mu.Lock()
	p.cmd, p.out, p.err = cmd, out, errw
	p.state = Running
	p.startedAt = time.Now()
	p.mu.Unlock()
	return nil
}

func (p *proc) supervise() {
	defer close(p.done)
	backoff := time.Second
	for {
		p.mu.Lock()
		cmd, out, errw := p.cmd, p.out, p.err
		p.mu.Unlock()

		waitErr := cmd.Wait()
		out.flush()
		errw.flush()

		p.mu.Lock()
		if p.stopping {
			p.state = Stopped
			p.mu.Unlock()
			return
		}
		p.state = Crashed
		if time.Since(p.startedAt) > time.Minute {
			backoff = time.Second
		}
		p.mu.Unlock()

		for {
			p.spec.Log.Printf("%s exited (%v), restarting in %s", p.spec.Tag, exitReason(waitErr), backoff)
			select {
			case <-time.After(backoff):
			case <-p.stop:
				p.mu.Lock()
				p.state = Stopped
				p.mu.Unlock()
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			if err := p.launch(); err != nil {
				waitErr = err
				continue
			}
			p.mu.Lock()
			p.restarts++
			p.mu.Unlock()
			break
		}
	}
}

// Stop sends SIGTERM to the process group, then SIGKILL after timeout.
func (p *proc) Stop(timeout time.Duration) {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		<-p.done
		return
	}
	p.stopping = true
	close(p.stop)
	pid := p.cmd.Process.Pid
	alive := p.state == Running
	p.mu.Unlock()

	if alive {
		syscall.Kill(-pid, syscall.SIGTERM)
	}
	select {
	case <-p.done:
	case <-time.After(timeout):
		syscall.Kill(-pid, syscall.SIGKILL)
		<-p.done
	}
}

func (p *proc) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := Status{State: p.state, PID: p.cmd.Process.Pid, StartedAt: p.startedAt, Restarts: p.restarts}
	if st.State == Running {
		st.MemoryBytes = sysinfo.ProcessRSS(st.PID)
		st.CPUNanos = sysinfo.ProcessCPU(st.PID)
	}
	return st
}

func exitReason(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}
