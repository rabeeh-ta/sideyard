package platform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"sideyard/internal/store"
)

// Jobs are cron schedules that Sideyard runs by calling the app:
// POST /_jobs/<name> on its loopback port, with the app's SIDEYARD_TOKEN
// as a bearer token. The proxy answers 404 for /_jobs/ from outside, so
// only Sideyard can trigger them.
const (
	JobPathPrefix = "/_jobs/"
	keepJobRuns   = 100
	maxJobOutput  = 64 << 10
	// catchUpWindow bounds how far back a missed run is made up.
	catchUpWindow = 7 * 24 * time.Hour
)

var (
	jobNameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	errJobBusy = errors.New("this job is already running")
)

type JobSpec struct {
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	TimeoutSec int    `json:"timeoutSec"`
	Retries    int    `json:"retries"`
	Enabled    bool   `json:"enabled"`
}

type JobView struct {
	JobSpec
	Path    string    `json:"path"`
	NextRun time.Time `json:"nextRun,omitzero"`
	Running bool      `json:"running"`
	LastRun *RunView  `json:"lastRun"`
}

type RunView struct {
	ID          int64     `json:"id"`
	Trigger     string    `json:"trigger"`
	ScheduledAt time.Time `json:"scheduledAt"`
	StartedAt   time.Time `json:"startedAt"`
	FinishedAt  time.Time `json:"finishedAt,omitzero"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	HTTPStatus  int       `json:"httpStatus"`
	Output      string    `json:"output"`
}

func viewRun(r store.JobRun) RunView {
	return RunView{r.ID, r.Trigger, r.ScheduledAt, r.StartedAt, r.FinishedAt, r.Status, r.Attempts, r.HTTPStatus, r.Output}
}

// parseSchedule accepts standard five-field cron, descriptors like @daily
// and @every 15m, and an optional CRON_TZ=Area/City prefix (UTC otherwise).
func parseSchedule(spec string) (cron.Schedule, error) {
	sched, err := cron.ParseStandard(strings.TrimSpace(spec))
	if err != nil {
		return nil, invalid("schedule: %v", err)
	}
	a := sched.Next(time.Now().UTC())
	if a.IsZero() {
		return nil, invalid("that schedule never runs")
	}
	if sched.Next(a).Sub(a) < time.Minute {
		return nil, invalid("a job can run at most once a minute")
	}
	return sched, nil
}

func (spec *JobSpec) normalize() error {
	spec.Name = strings.ToLower(strings.TrimSpace(spec.Name))
	spec.Schedule = strings.TrimSpace(spec.Schedule)
	if spec.TimeoutSec == 0 {
		spec.TimeoutSec = 300
	}
	switch {
	case !jobNameRe.MatchString(spec.Name):
		return invalid("job name must be lowercase letters, digits, - or _, e.g. daily-report")
	case spec.TimeoutSec < 1 || spec.TimeoutSec > 3600:
		return invalid("timeout must be between 1 and 3600 seconds")
	case spec.Retries < 0 || spec.Retries > 5:
		return invalid("retries must be between 0 and 5")
	}
	_, err := parseSchedule(spec.Schedule)
	return err
}

func (p *Platform) Jobs(name string) ([]JobView, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return nil, err
	}
	jobs, err := p.store.Jobs(proj.ID)
	if err != nil {
		return nil, err
	}
	out := []JobView{}
	for _, j := range jobs {
		v := JobView{
			JobSpec: JobSpec{j.Name, j.Schedule, j.TimeoutSec, j.Retries, j.Enabled},
			Path:    JobPathPrefix + j.Name,
			Running: p.jobBusy(j.ID),
		}
		if sched, err := parseSchedule(j.Schedule); err == nil && j.Enabled {
			v.NextRun = sched.Next(time.Now().UTC())
		}
		if runs, err := p.store.Runs(j.ID, 1); err == nil && len(runs) > 0 {
			r := viewRun(runs[0])
			v.LastRun = &r
		}
		out = append(out, v)
	}
	return out, nil
}

func (p *Platform) CreateJob(name string, spec JobSpec) error {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return err
	}
	if err := spec.normalize(); err != nil {
		return err
	}
	_, err = p.store.CreateJob(store.Job{ProjectID: proj.ID, Name: spec.Name, Schedule: spec.Schedule,
		TimeoutSec: spec.TimeoutSec, Retries: spec.Retries, Enabled: spec.Enabled})
	return err
}

// UpdateJob changes a job. Changing its schedule or re-enabling it starts
// the schedule afresh, so it doesn't make up slots from before.
func (p *Platform) UpdateJob(name, jobName string, spec JobSpec) error {
	j, err := p.job(name, jobName)
	if err != nil {
		return err
	}
	spec.Name = j.Name
	if err := spec.normalize(); err != nil {
		return err
	}
	rebase := spec.Schedule != j.Schedule || (spec.Enabled && !j.Enabled)
	j.Schedule, j.TimeoutSec, j.Retries, j.Enabled = spec.Schedule, spec.TimeoutSec, spec.Retries, spec.Enabled
	return p.store.UpdateJob(j, rebase)
}

func (p *Platform) DeleteJob(name, jobName string) error {
	j, err := p.job(name, jobName)
	if err != nil {
		return err
	}
	return p.store.DeleteJob(j.ID)
}

// RunJob starts a job now. It returns the run, which continues in the
// background.
func (p *Platform) RunJob(name, jobName string) (RunView, error) {
	j, err := p.job(name, jobName)
	if err != nil {
		return RunView{}, err
	}
	run, err := p.startRun(j, "manual", time.Now().UTC())
	if errors.Is(err, errJobBusy) {
		return RunView{}, fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return viewRun(run), err
}

func (p *Platform) JobRuns(name, jobName string, limit int) ([]RunView, error) {
	j, err := p.job(name, jobName)
	if err != nil {
		return nil, err
	}
	runs, err := p.store.Runs(j.ID, limit)
	if err != nil {
		return nil, err
	}
	out := []RunView{}
	for _, r := range runs {
		out = append(out, viewRun(r))
	}
	return out, nil
}

func (p *Platform) job(name, jobName string) (store.Job, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return store.Job{}, err
	}
	return p.store.JobByName(proj.ID, jobName)
}

// --- scheduler ---

func (p *Platform) schedulerLoop() {
	if err := p.store.AbandonRunningRuns(); err != nil {
		log.Printf("jobs: %v", err)
	}
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		p.scheduleTick(time.Now().UTC())
		select {
		case <-p.bg.Done():
			return
		case <-t.C:
		}
	}
}

// scheduleTick starts every job whose latest slot hasn't run yet.
func (p *Platform) scheduleTick(now time.Time) {
	jobs, err := p.store.Jobs(0)
	if err != nil {
		log.Printf("jobs: %v", err)
		return
	}
	for _, j := range jobs {
		if !j.Enabled {
			continue
		}
		sched, err := parseSchedule(j.Schedule)
		if err != nil {
			continue
		}
		base := j.CreatedAt // also reset when the schedule changes
		if last, err := p.store.LastScheduledRun(j.ID); err == nil && last.After(base) {
			base = last
		}
		due, ok := dueSlot(sched, base, now)
		if !ok {
			continue
		}
		trigger := "schedule"
		if now.Sub(due) > 2*time.Minute {
			trigger = "catch-up"
		}
		if _, err := p.startRun(j, trigger, due); errors.Is(err, errJobBusy) {
			p.recordSkipped(j, trigger, due, "the previous run was still in progress")
		}
	}
}

// dueSlot is the latest scheduled time after base and no later than now.
// Several missed slots collapse into one run.
func dueSlot(sched cron.Schedule, base, now time.Time) (time.Time, bool) {
	if now.Sub(base) > catchUpWindow {
		base = now.Add(-catchUpWindow)
	}
	var due time.Time
	for t := sched.Next(base); !t.IsZero() && !t.After(now); t = sched.Next(t) {
		due = t
	}
	return due, !due.IsZero()
}

func (p *Platform) jobBusy(jobID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jobRunning[jobID]
}

func (p *Platform) startRun(j store.Job, trigger string, scheduledAt time.Time) (store.JobRun, error) {
	p.mu.Lock()
	if p.jobRunning[j.ID] {
		p.mu.Unlock()
		return store.JobRun{}, errJobBusy
	}
	p.jobRunning[j.ID] = true
	p.mu.Unlock()

	run, err := p.store.StartRun(store.JobRun{JobID: j.ID, Trigger: trigger, ScheduledAt: scheduledAt,
		StartedAt: time.Now().UTC(), Status: "running"})
	if err != nil {
		p.mu.Lock()
		delete(p.jobRunning, j.ID)
		p.mu.Unlock()
		return run, err
	}
	p.bgWG.Go(func() {
		defer func() {
			p.mu.Lock()
			delete(p.jobRunning, j.ID)
			p.mu.Unlock()
		}()
		p.executeRun(j, run)
	})
	return run, nil
}

func (p *Platform) recordSkipped(j store.Job, trigger string, scheduledAt time.Time, why string) {
	now := time.Now().UTC()
	run, err := p.store.StartRun(store.JobRun{JobID: j.ID, Trigger: trigger, ScheduledAt: scheduledAt, StartedAt: now, Status: "skipped"})
	if err != nil {
		return
	}
	run.FinishedAt, run.Status, run.Output = now, "skipped", why
	p.store.FinishRun(run, keepJobRuns)
}

func (p *Platform) executeRun(j store.Job, run store.JobRun) {
	proj, err := p.store.ProjectByID(j.ProjectID)
	if err != nil {
		return
	}
	for attempt := 1; attempt <= j.Retries+1; attempt++ {
		run.Attempts = attempt
		l := p.getLive(j.ProjectID)
		if l == nil {
			run.Status, run.Output = "skipped", "the app isn't running"
			break
		}
		status, output, err := p.callJob(proj, j, run, l.port)
		run.HTTPStatus = status
		run.Output = output
		if err != nil {
			run.Output = strings.TrimSpace(output + "\n" + err.Error())
		}
		if attempt > 1 {
			run.Output = fmt.Sprintf("[attempt %d of %d]\n%s", attempt, j.Retries+1, run.Output)
		}
		if err == nil && status >= 200 && status < 300 {
			run.Status = "succeeded"
			break
		}
		run.Status = "failed"
		if attempt <= j.Retries {
			select {
			case <-time.After(p.jobRetryDelay * time.Duration(attempt)):
			case <-p.bg.Done():
				attempt = j.Retries + 1 // shutting down: no more attempts
			}
		}
	}
	run.FinishedAt = time.Now().UTC()
	if err := p.store.FinishRun(run, keepJobRuns); err != nil {
		log.Printf("jobs: %v", err)
	}
	p.logf(proj, "job %s %s after %s (%s)", j.Name, run.Status, run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond), run.Trigger)
}

// callJob makes one request to the app's job endpoint.
func (p *Platform) callJob(proj store.Project, j store.Job, run store.JobRun, port int) (int, string, error) {
	token, err := p.appToken(proj)
	if err != nil {
		return 0, "", err
	}
	timeout := time.Duration(j.TimeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(p.bg, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s%s", port, JobPathPrefix, j.Name), nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Sideyard-Job", j.Name)
	req.Header.Set("X-Sideyard-Run", strconv.FormatInt(run.ID, 10))
	req.Header.Set("X-Sideyard-Scheduled-At", run.ScheduledAt.Format(time.RFC3339))
	req.Header.Set("X-Sideyard-Attempt", strconv.Itoa(run.Attempts))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, "", fmt.Errorf("timed out after %s", timeout)
		}
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJobOutput))
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return resp.StatusCode, string(body), fmt.Errorf("timed out after %s", timeout)
	}
	return resp.StatusCode, strings.ToValidUTF8(string(body), "�"), nil
}
