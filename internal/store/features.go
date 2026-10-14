package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// --- per-project values ---

func (s *Store) SetAppToken(id int64, sealed []byte) error {
	_, err := s.db.Exec(`UPDATE projects SET app_token = ? WHERE id = ?`, sealed, id)
	return err
}

func (s *Store) SetEmailFrom(id int64, from string) error {
	_, err := s.db.Exec(`UPDATE projects SET email_from = ? WHERE id = ?`, from, id)
	return err
}

// --- JSON settings ---

// JSONSetting decodes a setting stored as JSON. A missing one leaves v as is.
func (s *Store) JSONSetting(key string, v any) error {
	raw, err := s.Setting(key)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), v)
}

func (s *Store) SetJSONSetting(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetSetting(key, string(b))
}

// --- storage ---

type Storage struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey []byte // encrypted
	UpdatedAt time.Time
}

func (s *Store) Storage(projectID int64) (Storage, error) {
	var st Storage
	var updated int64
	err := s.db.QueryRow(`SELECT endpoint, region, bucket, access_key, secret_key, updated_at FROM storage WHERE project_id = ?`,
		projectID).Scan(&st.Endpoint, &st.Region, &st.Bucket, &st.AccessKey, &st.SecretKey, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	st.UpdatedAt = time.Unix(updated, 0).UTC()
	return st, err
}

func (s *Store) SetStorage(projectID int64, st Storage) error {
	_, err := s.db.Exec(`INSERT INTO storage (project_id, endpoint, region, bucket, access_key, secret_key, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (project_id) DO UPDATE SET endpoint = excluded.endpoint, region = excluded.region,
			bucket = excluded.bucket, access_key = excluded.access_key, secret_key = excluded.secret_key,
			updated_at = excluded.updated_at`,
		projectID, st.Endpoint, st.Region, st.Bucket, st.AccessKey, st.SecretKey, time.Now().Unix())
	return err
}

func (s *Store) DeleteStorage(projectID int64) error {
	_, err := s.db.Exec(`DELETE FROM storage WHERE project_id = ?`, projectID)
	return err
}

// --- jobs ---

type Job struct {
	ID         int64
	ProjectID  int64
	Name       string
	Schedule   string
	TimeoutSec int
	Retries    int
	Enabled    bool
	CreatedAt  time.Time
}

const jobCols = `id, project_id, name, schedule, timeout_sec, retries, enabled, created_at`

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var created int64
	err := row.Scan(&j.ID, &j.ProjectID, &j.Name, &j.Schedule, &j.TimeoutSec, &j.Retries, &j.Enabled, &created)
	j.CreatedAt = time.Unix(created, 0).UTC()
	return j, err
}

func (s *Store) CreateJob(j Job) (Job, error) {
	res, err := s.db.Exec(`INSERT INTO jobs (project_id, name, schedule, timeout_sec, retries, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, j.ProjectID, j.Name, j.Schedule, j.TimeoutSec, j.Retries, j.Enabled, time.Now().Unix())
	if isUnique(err) {
		return j, ErrConflict
	}
	if err != nil {
		return j, err
	}
	id, _ := res.LastInsertId()
	return s.Job(id)
}

// UpdateJob saves a job. rebase restarts its schedule from now: created_at
// doubles as the base the scheduler counts slots from.
func (s *Store) UpdateJob(j Job, rebase bool) error {
	q := `UPDATE jobs SET schedule = ?, timeout_sec = ?, retries = ?, enabled = ? WHERE id = ?`
	if rebase {
		q = `UPDATE jobs SET schedule = ?, timeout_sec = ?, retries = ?, enabled = ?, created_at = unixepoch() WHERE id = ?`
	}
	_, err := s.db.Exec(q, j.Schedule, j.TimeoutSec, j.Retries, j.Enabled, j.ID)
	return err
}

func (s *Store) DeleteJob(id int64) error {
	_, err := s.db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	return err
}

func (s *Store) Job(id int64) (Job, error) {
	j, err := scanJob(s.db.QueryRow(`SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

func (s *Store) JobByName(projectID int64, name string) (Job, error) {
	j, err := scanJob(s.db.QueryRow(`SELECT `+jobCols+` FROM jobs WHERE project_id = ? AND name = ?`, projectID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

// Jobs lists a project's jobs, or every job when projectID is 0.
func (s *Store) Jobs(projectID int64) ([]Job, error) {
	q, args := `SELECT `+jobCols+` FROM jobs ORDER BY project_id, name`, []any{}
	if projectID != 0 {
		q, args = `SELECT `+jobCols+` FROM jobs WHERE project_id = ? ORDER BY name`, []any{projectID}
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

type JobRun struct {
	ID          int64
	JobID       int64
	Trigger     string // schedule, catch-up or manual
	ScheduledAt time.Time
	StartedAt   time.Time
	FinishedAt  time.Time // zero while running
	Status      string    // running, succeeded, failed or skipped
	Attempts    int
	HTTPStatus  int
	Output      string
}

const runCols = `id, job_id, trigger, scheduled_at, started_at, finished_at, status, attempts, http_status, output`

func scanRun(row interface{ Scan(...any) error }) (JobRun, error) {
	var r JobRun
	var sched, started, finished int64
	err := row.Scan(&r.ID, &r.JobID, &r.Trigger, &sched, &started, &finished, &r.Status, &r.Attempts, &r.HTTPStatus, &r.Output)
	r.ScheduledAt = time.Unix(sched, 0).UTC()
	r.StartedAt = time.Unix(started, 0).UTC()
	if finished != 0 {
		r.FinishedAt = time.Unix(finished, 0).UTC()
	}
	return r, err
}

func (s *Store) StartRun(r JobRun) (JobRun, error) {
	res, err := s.db.Exec(`INSERT INTO job_runs (job_id, trigger, scheduled_at, started_at, status) VALUES (?, ?, ?, ?, ?)`,
		r.JobID, r.Trigger, r.ScheduledAt.Unix(), r.StartedAt.Unix(), r.Status)
	if err != nil {
		return r, err
	}
	r.ID, _ = res.LastInsertId()
	return r, nil
}

// FinishRun records a run's result and trims the job's history to keep runs.
func (s *Store) FinishRun(r JobRun, keep int) error {
	finished := int64(0)
	if !r.FinishedAt.IsZero() {
		finished = r.FinishedAt.Unix()
	}
	if _, err := s.db.Exec(`UPDATE job_runs SET finished_at = ?, status = ?, attempts = ?, http_status = ?, output = ? WHERE id = ?`,
		finished, r.Status, r.Attempts, r.HTTPStatus, r.Output, r.ID); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM job_runs WHERE job_id = ? AND id NOT IN
		(SELECT id FROM job_runs WHERE job_id = ? ORDER BY id DESC LIMIT ?)`, r.JobID, r.JobID, keep)
	return err
}

func (s *Store) Runs(jobID int64, limit int) ([]JobRun, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM job_runs WHERE job_id = ? ORDER BY id DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobRun
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastScheduledRun is the newest slot a job ran for on its schedule, for
// catching up on runs missed while Sideyard was down.
func (s *Store) LastScheduledRun(jobID int64) (time.Time, error) {
	var t sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(scheduled_at) FROM job_runs WHERE job_id = ? AND trigger != 'manual'`, jobID).Scan(&t)
	if err != nil || !t.Valid {
		return time.Time{}, err
	}
	return time.Unix(t.Int64, 0).UTC(), nil
}

// AbandonRunningRuns marks runs left "running" by a Sideyard restart.
func (s *Store) AbandonRunningRuns() error {
	_, err := s.db.Exec(`UPDATE job_runs SET status = 'failed', finished_at = ?, output = output || ?
		WHERE status = 'running'`, time.Now().Unix(), "\n[interrupted: Sideyard restarted during this run]")
	return err
}

// --- backups ---

type BackupRun struct {
	ID         int64
	Trigger    string
	StartedAt  time.Time
	FinishedAt time.Time
	Status     string // running, succeeded, partial or failed
	Files      int
	Bytes      int64
	Log        string
}

func (s *Store) StartBackupRun(trigger string) (BackupRun, error) {
	r := BackupRun{Trigger: trigger, StartedAt: time.Now().UTC(), Status: "running"}
	res, err := s.db.Exec(`INSERT INTO backup_runs (trigger, started_at, status) VALUES (?, ?, ?)`,
		r.Trigger, r.StartedAt.Unix(), r.Status)
	if err != nil {
		return r, err
	}
	r.ID, _ = res.LastInsertId()
	return r, nil
}

func (s *Store) FinishBackupRun(r BackupRun) error {
	if _, err := s.db.Exec(`UPDATE backup_runs SET finished_at = ?, status = ?, files = ?, bytes = ?, log = ? WHERE id = ?`,
		r.FinishedAt.Unix(), r.Status, r.Files, r.Bytes, r.Log, r.ID); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM backup_runs WHERE id NOT IN (SELECT id FROM backup_runs ORDER BY id DESC LIMIT 100)`)
	return err
}

func (s *Store) BackupRuns(limit int) ([]BackupRun, error) {
	rows, err := s.db.Query(`SELECT id, trigger, started_at, finished_at, status, files, bytes, log
		FROM backup_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupRun
	for rows.Next() {
		var r BackupRun
		var started, finished int64
		if err := rows.Scan(&r.ID, &r.Trigger, &started, &finished, &r.Status, &r.Files, &r.Bytes, &r.Log); err != nil {
			return nil, err
		}
		r.StartedAt = time.Unix(started, 0).UTC()
		if finished != 0 {
			r.FinishedAt = time.Unix(finished, 0).UTC()
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) AbandonRunningBackups() error {
	_, err := s.db.Exec(`UPDATE backup_runs SET status = 'failed', finished_at = ?, log = log || ? WHERE status = 'running'`,
		time.Now().Unix(), "interrupted: Sideyard restarted during this backup\n")
	return err
}

// --- emails ---

type Email struct {
	ID         int64
	ProjectID  int64
	Sender     string
	Recipients string
	Subject    string
	Status     string // sent or failed
	Detail     string
	CreatedAt  time.Time
}

// LogEmail records a send attempt and keeps the project's newest 500.
func (s *Store) LogEmail(e Email) error {
	if _, err := s.db.Exec(`INSERT INTO emails (project_id, sender, recipients, subject, status, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, e.ProjectID, e.Sender, e.Recipients, e.Subject, e.Status, e.Detail, time.Now().Unix()); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM emails WHERE project_id = ? AND id NOT IN
		(SELECT id FROM emails WHERE project_id = ? ORDER BY id DESC LIMIT 500)`, e.ProjectID, e.ProjectID)
	return err
}

func (s *Store) Emails(projectID int64, limit int) ([]Email, error) {
	rows, err := s.db.Query(`SELECT id, project_id, sender, recipients, subject, status, detail, created_at
		FROM emails WHERE project_id = ? ORDER BY id DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Email
	for rows.Next() {
		var e Email
		var created int64
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.Sender, &e.Recipients, &e.Subject, &e.Status, &e.Detail, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// EmailsSince counts a project's send attempts since t, for rate limiting.
func (s *Store) EmailsSince(projectID int64, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM emails WHERE project_id = ? AND created_at >= ?`, projectID, t.Unix()).Scan(&n)
	return n, err
}

// Snapshot writes a consistent copy of Sideyard's own database to dst.
func (s *Store) Snapshot(dst string) error {
	_, err := s.db.Exec(`VACUUM INTO ?`, dst)
	return err
}
