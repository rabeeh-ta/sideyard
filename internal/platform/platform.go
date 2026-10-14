// Package platform ties the store, the runner and the proxy together. Every
// operation on a project (deploy, restart, stop, settings, database import)
// goes through here and is serialised per project.
package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"sideyard/internal/config"
	"sideyard/internal/proxy"
	"sideyard/internal/runner"
	"sideyard/internal/secrets"
	"sideyard/internal/store"
	"sideyard/internal/sysinfo"
	"sideyard/internal/version"
)

var (
	ErrNotFound   = store.ErrNotFound
	ErrConflict   = store.ErrConflict
	ErrNoRelease  = errors.New("this project has no release yet; deploy one first")
	ErrValidation = errors.New("invalid input")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}

type Platform struct {
	cfg    config.Config
	store  *store.Store
	box    *secrets.Box
	proxy  *proxy.Proxy
	runner runner.Runner

	// healthTimeout is how long a new release has to pass its health check.
	healthTimeout time.Duration
	// jobRetryDelay is the wait before a failed job's first retry; later
	// retries wait proportionally longer.
	jobRetryDelay time.Duration

	// bg is cancelled by Shutdown; the scheduler, backups and health checks
	// run under it and are tracked by bgWG.
	bg     context.Context
	stopBg context.CancelFunc
	bgWG   sync.WaitGroup

	mu       sync.Mutex
	live     map[int64]*live          // the instance serving each project
	retiring map[runner.Instance]bool // replaced instances finishing in-flight requests
	locks    map[int64]*sync.Mutex
	// jobRunning holds jobs with a run in progress.
	jobRunning    map[int64]bool
	backupRunning bool
	cpuSamples    map[string]cpuSample // by instance ID
	healthStates  map[int64]healthState
	sizes         map[string]sizeEntry // by project name
	// Recent samples for the dashboard's charts, from metricsLoop.
	projectMetrics map[int64][]MetricPoint
	serverMetrics  []ServerPoint
	sampler        sysinfo.Sampler
	lastSample     sysinfo.Sample
	self           MetricPoint // Sideyard's own process
	host           sysinfo.Host
	version        string
	started        time.Time

	upd     updateState
	restart chan struct{}
	// exe is the binary updates replace; tests point it at a stand-in.
	exe string
}

type live struct {
	inst      runner.Instance
	port      int
	releaseID int64
}

func New(cfg config.Config, st *store.Store, box *secrets.Box, px *proxy.Proxy, rn runner.Runner) *Platform {
	bg, stopBg := context.WithCancel(context.Background())
	return &Platform{
		bg:             bg,
		stopBg:         stopBg,
		jobRetryDelay:  15 * time.Second,
		jobRunning:     map[int64]bool{},
		cpuSamples:     map[string]cpuSample{},
		healthStates:   map[int64]healthState{},
		sizes:          map[string]sizeEntry{},
		projectMetrics: map[int64][]MetricPoint{},
		host:           sysinfo.ReadHost(),
		version:        version.Version,
		started:        time.Now().UTC(),
		restart:        make(chan struct{}, 1),
		cfg:            cfg,
		store:          st,
		box:            box,
		proxy:          px,
		runner:         rn,
		healthTimeout:  60 * time.Second,
		live:           map[int64]*live{},
		retiring:       map[runner.Instance]bool{},
		locks:          map[int64]*sync.Mutex{},
	}
}

func (p *Platform) Config() config.Config { return p.cfg }
func (p *Platform) RunnerName() string    { return p.runner.Name() }

func (p *Platform) lock(projectID int64) func() {
	p.mu.Lock()
	l, ok := p.locks[projectID]
	if !ok {
		l = &sync.Mutex{}
		p.locks[projectID] = l
	}
	p.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (p *Platform) dir(name string, parts ...string) string {
	return filepath.Join(append([]string{p.cfg.DataDir, "projects", name}, parts...)...)
}

func (p *Platform) ref(proj store.Project) runner.ProjectRef {
	return runner.ProjectRef{ID: proj.ID, Name: proj.Name, Dir: p.dir(proj.Name)}
}

func (p *Platform) logf(proj store.Project, format string, args ...any) {
	p.runner.Logf(p.ref(proj), format, args...)
}

func (p *Platform) setLive(projectID int64, l *live) (old *live) {
	p.mu.Lock()
	defer p.mu.Unlock()
	old = p.live[projectID]
	if l == nil {
		delete(p.live, projectID)
	} else {
		p.live[projectID] = l
	}
	return old
}

func (p *Platform) getLive(projectID int64) *live {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live[projectID]
}

// Boot applies routes, then brings every project that should be running
// back: adopting instances that outlived a Sideyard restart, starting the
// rest, and stopping anything left over from a crash.
func (p *Platform) Boot(ctx context.Context) error {
	instance, err := p.InstanceID()
	if err != nil {
		return err
	}
	if err := p.runner.Init(p.cfg.DataDir, instance); err != nil {
		return fmt.Errorf("runner: %w", err)
	}
	if err := p.applyRoutes(); err != nil {
		return fmt.Errorf("proxy: %w", err)
	}
	projects, err := p.store.Projects()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	for _, proj := range projects {
		if err := p.runner.Prepare(p.ref(proj)); err != nil {
			log.Printf("prepare %s: %v", proj.Name, err)
			continue
		}
		if proj.DesiredState != store.StateRunning || proj.ActiveReleaseID == 0 {
			continue
		}
		wg.Go(func() {
			if err := p.bootProject(ctx, proj); err != nil {
				log.Printf("boot %s: %v", proj.Name, err)
			}
		})
	}
	wg.Wait()

	keep := map[string]bool{}
	p.mu.Lock()
	for _, l := range p.live {
		keep[l.inst.ID()] = true
	}
	p.mu.Unlock()
	p.runner.Sweep(keep)
	if err := p.applyRoutes(); err != nil {
		return err
	}
	p.bgWG.Go(p.schedulerLoop)
	p.bgWG.Go(p.backupLoop)
	p.bgWG.Go(p.healthLoop)
	p.bgWG.Go(p.metricsLoop)
	p.bgWG.Go(p.updateLoop)
	return nil
}

// InstanceID is this installation's short random ID, created on first
// boot. The systemd runner puts it in unit and user names.
func (p *Platform) InstanceID() (string, error) {
	id, err := p.store.Setting("instance_id")
	if errors.Is(err, store.ErrNotFound) {
		b := make([]byte, 2)
		rand.Read(b)
		id = hex.EncodeToString(b)
		err = p.store.SetSetting("instance_id", id)
	}
	return id, err
}

// bootProject brings one project back. There's no older instance to fall
// back to, so a fresh start is routed even if it's slow to become healthy.
func (p *Platform) bootProject(ctx context.Context, proj store.Project) error {
	unlock := p.lock(proj.ID)
	defer unlock()
	if proj.LiveInstance != "" && proj.LivePort > 0 {
		if inst, ok := p.runner.Adopt(p.ref(proj), proj.LiveInstance); ok {
			p.setLive(proj.ID, &live{inst: inst, port: proj.LivePort, releaseID: proj.ActiveReleaseID})
			p.logf(proj, "reattached to %s on port %d", inst.ID(), proj.LivePort)
			return nil
		}
	}
	rel, err := p.store.Release(proj.ActiveReleaseID)
	if err != nil {
		return err
	}
	inst, port, err := p.start(proj, rel)
	if err != nil {
		return err
	}
	p.setLive(proj.ID, &live{inst: inst, port: port, releaseID: rel.ID})
	if err := p.store.SetLive(proj.ID, inst.ID(), port); err != nil {
		return err
	}
	p.logf(proj, "booted release #%d as %s on port %d", rel.Number, inst.ID(), port)
	if err := runner.WaitHealthy(ctx, inst, healthURL(port, proj.HealthPath), 30*time.Second); err != nil {
		p.logf(proj, "warning: %v", err)
	}
	return nil
}

// Shutdown stops app instances that would otherwise be orphaned: all of
// them for the process runner, only replaced ones still draining for a
// detached runner, whose live apps keep serving while Sideyard is down.
func (p *Platform) Shutdown() {
	// Stop background work first: running jobs are cancelled and recorded.
	p.stopBg()
	p.bgWG.Wait()

	p.mu.Lock()
	var stop []runner.Instance
	if !p.runner.Detached() {
		for _, l := range p.live {
			stop = append(stop, l.inst)
		}
	}
	for inst := range p.retiring {
		stop = append(stop, inst)
	}
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, inst := range stop {
		wg.Go(func() { inst.Stop(10 * time.Second) })
	}
	wg.Wait()
}

// retire stops a replaced instance after giving in-flight requests a moment
// to finish. Shutdown stops it sooner if it comes first; Stop is idempotent.
func (p *Platform) retire(inst runner.Instance) {
	p.mu.Lock()
	p.retiring[inst] = true
	p.mu.Unlock()
	go func() {
		time.Sleep(2 * time.Second)
		inst.Stop(10 * time.Second)
		p.mu.Lock()
		delete(p.retiring, inst)
		p.mu.Unlock()
	}()
}

// applyRoutes rebuilds the proxy config from the store and live instances.
func (p *Platform) applyRoutes() error {
	projects, err := p.store.Projects()
	if err != nil {
		return err
	}
	domains, err := p.store.Domains()
	if err != nil {
		return err
	}
	routes := []proxy.Route{{Hosts: []string{p.cfg.DashboardHost}, Upstream: p.cfg.APIAddr}}
	p.mu.Lock()
	for _, proj := range projects {
		hosts := domains[proj.ID]
		if len(hosts) == 0 {
			continue
		}
		r := proxy.Route{Hosts: hosts, Internal: []string{JobPathPrefix + "*"}}
		switch l, ok := p.live[proj.ID]; {
		case ok:
			r.Upstream = fmt.Sprintf("127.0.0.1:%d", l.port)
		case proj.ActiveReleaseID == 0:
			r.Message = proj.Name + " hasn't been deployed yet."
		default:
			r.Message = proj.Name + " is stopped."
		}
		routes = append(routes, r)
	}
	p.mu.Unlock()
	return p.proxy.Apply(routes)
}

// --- projects ---

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

func (p *Platform) CreateProject(name string) (store.Project, string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !nameRe.MatchString(name) {
		return store.Project{}, "", invalid("name must be 1–40 lowercase letters, digits or dashes, e.g. my-app")
	}
	host := name + "." + p.cfg.Domain
	if host == p.cfg.DashboardHost || name == "www" {
		return store.Project{}, "", invalid("%q is reserved", name)
	}
	token := secrets.NewToken("sy_")
	proj, err := p.store.CreateProject(name, secrets.Hash(token))
	if err != nil {
		return proj, "", err
	}
	for _, d := range []string{"data", "releases"} {
		if err := os.MkdirAll(p.dir(name, d), 0o750); err != nil {
			return proj, "", err
		}
	}
	if err := p.runner.Prepare(p.ref(proj)); err != nil {
		return proj, "", err
	}
	if err := p.store.AddDomain(host, proj.ID); err != nil && !errors.Is(err, ErrConflict) {
		return proj, "", err
	}
	return proj, token, p.applyRoutes()
}

func (p *Platform) Project(name string) (store.Project, error) {
	return p.store.ProjectByName(name)
}

func (p *Platform) Projects() ([]store.Project, error) {
	return p.store.Projects()
}

func (p *Platform) Domains() (map[int64][]string, error) {
	return p.store.Domains()
}

func (p *Platform) Releases(proj store.Project, limit int) ([]store.Release, error) {
	return p.store.Releases(proj.ID, limit)
}

func (p *Platform) DeleteProject(name string) error {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return err
	}
	unlock := p.lock(proj.ID)
	defer unlock()
	if l := p.setLive(proj.ID, nil); l != nil {
		l.inst.Stop(10 * time.Second)
	}
	if err := p.store.DeleteProject(proj.ID); err != nil {
		return err
	}
	if err := p.applyRoutes(); err != nil {
		return err
	}
	if err := p.runner.Remove(p.ref(proj)); err != nil {
		log.Printf("delete %s: %v", name, err)
	}
	return os.RemoveAll(p.dir(name))
}

func (p *Platform) RegenerateToken(name string) (string, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return "", err
	}
	token := secrets.NewToken("sy_")
	return token, p.store.SetDeployTokenHash(proj.ID, secrets.Hash(token))
}

type Settings struct {
	HealthPath string `json:"healthPath"`
	Args       string `json:"args"`
	MemoryMB   int    `json:"memoryMB"`
	CPUPercent int    `json:"cpuPercent"`
}

// UpdateSettings saves runtime settings and restarts the app (without
// downtime) if a change only takes effect on a new process.
func (p *Platform) UpdateSettings(ctx context.Context, name string, s Settings) error {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return err
	}
	s.Args = strings.TrimSpace(s.Args)
	switch {
	case !strings.HasPrefix(s.HealthPath, "/"):
		return invalid("health check path must start with /")
	case s.MemoryMB != 0 && (s.MemoryMB < 32 || s.MemoryMB > 1<<20):
		return invalid("memory limit must be at least 32 MB, or 0 for no limit")
	case s.CPUPercent < 0 || s.CPUPercent > 6400:
		return invalid("CPU limit must be between 0 (no limit) and 6400%%")
	}
	if err := p.store.UpdateProjectSettings(proj.ID, s.HealthPath, s.Args, s.MemoryMB, s.CPUPercent); err != nil {
		return err
	}
	changed := s.Args != proj.Args || s.MemoryMB != proj.MemoryMB || s.CPUPercent != proj.CPUPercent
	if changed && p.getLive(proj.ID) != nil {
		return p.Restart(ctx, name)
	}
	return nil
}

// --- domains ---

var hostRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

func (p *Platform) AddDomain(name, host string) error {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return err
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if !hostRe.MatchString(host) {
		return invalid("%q is not a valid domain name", host)
	}
	if host == p.cfg.DashboardHost {
		return invalid("%s is the dashboard's domain", host)
	}
	if err := p.store.AddDomain(host, proj.ID); err != nil {
		return err
	}
	return p.applyRoutes()
}

func (p *Platform) RemoveDomain(name, host string) error {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return err
	}
	if err := p.store.RemoveDomain(host, proj.ID); err != nil {
		return err
	}
	return p.applyRoutes()
}

// --- runtime status ---

type RuntimeStatus struct {
	State       string    `json:"state"` // running, crashed, stopped, not-deployed
	Instance    string    `json:"instance,omitempty"`
	PID         int       `json:"pid,omitempty"`
	Port        int       `json:"port,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitzero"`
	Restarts    int       `json:"restarts"`
	MemoryBytes uint64    `json:"memoryBytes"`
	CPUPercent  float64   `json:"cpuPercent"`
	Health      Health    `json:"health"`
}

func (p *Platform) Status(proj store.Project) RuntimeStatus {
	l := p.getLive(proj.ID)
	if l == nil {
		if proj.ActiveReleaseID == 0 {
			return RuntimeStatus{State: "not-deployed"}
		}
		return RuntimeStatus{State: "stopped"}
	}
	st := l.inst.Status()
	rs := RuntimeStatus{
		State: string(st.State), Instance: l.inst.ID(), PID: st.PID, Port: l.port,
		StartedAt: st.StartedAt, Restarts: st.Restarts, MemoryBytes: st.MemoryBytes,
		Health: p.health(proj.ID),
	}
	if st.CPUNanos > 0 {
		rs.CPUPercent = p.cpuPercent(l.inst.ID(), st.CPUNanos)
	}
	return rs
}

func (p *Platform) Logs(name string, lines int) ([]string, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return nil, err
	}
	return p.runner.Tail(p.ref(proj), lines)
}

// --- helpers ---

func healthURL(port int, path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
