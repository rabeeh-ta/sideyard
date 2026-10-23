// Package store is Sideyard's own SQLite database: projects, domains,
// variables, releases, settings and sessions. Project data never lives here.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

const (
	StateRunning = "running"
	StateStopped = "stopped"

	ReleaseStarting   = "starting"
	ReleaseActive     = "active"
	ReleaseFailed     = "failed"
	ReleaseSuperseded = "superseded"
)

type Project struct {
	ID              int64
	Name            string
	HealthPath      string
	Args            string
	DeployTokenHash string
	DesiredState    string
	ActiveReleaseID int64
	// LiveInstance and LivePort record the instance serving traffic, so a
	// restarted Sideyard can adopt it instead of starting a new one.
	LiveInstance string
	LivePort     int
	MemoryMB     int // 0 means no limit
	CPUPercent   int // 0 means no limit
	// AppToken (encrypted) is the app's SIDEYARD_TOKEN: Sideyard sends it
	// with job calls, and the app sends it to Sideyard's email API.
	AppToken  []byte
	EmailFrom string // the project's sender; empty means the platform default
	CreatedAt time.Time
}

type Release struct {
	ID        int64
	ProjectID int64
	Number    int
	Commit    string
	Message   string
	Size      int64
	SHA256    string
	Status    string
	Log       string
	Pruned    bool
	CreatedAt time.Time
}

type Variable struct {
	Key       string
	Value     []byte // encrypted
	UpdatedAt time.Time
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection serialises writes and keeps the pragmas on every query.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

var migrations = []string{
	`CREATE TABLE settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		expires_at INTEGER NOT NULL
	);
	CREATE TABLE projects (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		health_path TEXT NOT NULL DEFAULT '/healthz',
		args TEXT NOT NULL DEFAULT '',
		deploy_token_hash TEXT NOT NULL,
		desired_state TEXT NOT NULL DEFAULT 'running',
		active_release_id INTEGER,
		created_at INTEGER NOT NULL
	);
	CREATE TABLE domains (
		host TEXT PRIMARY KEY,
		project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL
	);
	CREATE TABLE variables (
		project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		key TEXT NOT NULL,
		value BLOB NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (project_id, key)
	);
	CREATE TABLE releases (
		id INTEGER PRIMARY KEY,
		project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		number INTEGER NOT NULL,
		commit_sha TEXT NOT NULL DEFAULT '',
		message TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL,
		sha256 TEXT NOT NULL,
		status TEXT NOT NULL,
		log TEXT NOT NULL DEFAULT '',
		pruned INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		UNIQUE (project_id, number)
	);`,
	`ALTER TABLE projects ADD COLUMN live_instance TEXT NOT NULL DEFAULT '';
	ALTER TABLE projects ADD COLUMN live_port INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE projects ADD COLUMN memory_mb INTEGER NOT NULL DEFAULT 512;
	ALTER TABLE projects ADD COLUMN cpu_percent INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE projects ADD COLUMN app_token BLOB NOT NULL DEFAULT x'';
	ALTER TABLE projects ADD COLUMN email_from TEXT NOT NULL DEFAULT '';
	CREATE TABLE storage (
		project_id INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
		endpoint TEXT NOT NULL,
		region TEXT NOT NULL,
		bucket TEXT NOT NULL,
		access_key TEXT NOT NULL,
		secret_key BLOB NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE jobs (
		id INTEGER PRIMARY KEY,
		project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		schedule TEXT NOT NULL,
		timeout_sec INTEGER NOT NULL,
		retries INTEGER NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL,
		UNIQUE (project_id, name)
	);
	CREATE TABLE job_runs (
		id INTEGER PRIMARY KEY,
		job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
		trigger TEXT NOT NULL,
		scheduled_at INTEGER NOT NULL,
		started_at INTEGER NOT NULL,
		finished_at INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0,
		http_status INTEGER NOT NULL DEFAULT 0,
		output TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX job_runs_by_job ON job_runs (job_id, id);
	CREATE TABLE backup_runs (
		id INTEGER PRIMARY KEY,
		trigger TEXT NOT NULL,
		started_at INTEGER NOT NULL,
		finished_at INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL,
		files INTEGER NOT NULL DEFAULT 0,
		bytes INTEGER NOT NULL DEFAULT 0,
		log TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE emails (
		id INTEGER PRIMARY KEY,
		project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		sender TEXT NOT NULL,
		recipients TEXT NOT NULL,
		subject TEXT NOT NULL,
		status TEXT NOT NULL,
		detail TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	);
	CREATE INDEX emails_by_project ON emails (project_id, id);`,
	`CREATE TABLE queries (
		project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		id TEXT NOT NULL,
		name TEXT NOT NULL,
		sql TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (project_id, id)
	);`,
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// --- settings and sessions ---

func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) CreateSession(tokenHash string, expires time.Time) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, expires_at) VALUES (?, ?)`, tokenHash, expires.Unix())
	return err
}

func (s *Store) SessionValid(tokenHash string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token_hash = ? AND expires_at > ?`,
		tokenHash, time.Now().Unix()).Scan(&n)
	return n > 0, err
}

func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ? OR expires_at <= ?`, tokenHash, time.Now().Unix())
	return err
}

func (s *Store) DeleteAllSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions`)
	return err
}

// --- projects ---

const projectCols = `id, name, health_path, args, deploy_token_hash, desired_state, COALESCE(active_release_id, 0),
	live_instance, live_port, memory_mb, cpu_percent, app_token, email_from, created_at`

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var created int64
	err := row.Scan(&p.ID, &p.Name, &p.HealthPath, &p.Args, &p.DeployTokenHash, &p.DesiredState, &p.ActiveReleaseID,
		&p.LiveInstance, &p.LivePort, &p.MemoryMB, &p.CPUPercent, &p.AppToken, &p.EmailFrom, &created)
	p.CreatedAt = time.Unix(created, 0).UTC()
	return p, err
}

func (s *Store) CreateProject(name, tokenHash string) (Project, error) {
	res, err := s.db.Exec(`INSERT INTO projects (name, deploy_token_hash, created_at) VALUES (?, ?, ?)`,
		name, tokenHash, time.Now().Unix())
	if err != nil {
		if isUnique(err) {
			return Project{}, ErrConflict
		}
		return Project{}, err
	}
	id, _ := res.LastInsertId()
	return s.ProjectByID(id)
}

func (s *Store) ProjectByID(id int64) (Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) ProjectByName(name string) (Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) Projects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdateProjectSettings(id int64, healthPath, args string, memoryMB, cpuPercent int) error {
	_, err := s.db.Exec(`UPDATE projects SET health_path = ?, args = ?, memory_mb = ?, cpu_percent = ? WHERE id = ?`,
		healthPath, args, memoryMB, cpuPercent, id)
	return err
}

// SetLive records the instance serving a project, or clears it.
func (s *Store) SetLive(id int64, instance string, port int) error {
	_, err := s.db.Exec(`UPDATE projects SET live_instance = ?, live_port = ? WHERE id = ?`, instance, port, id)
	return err
}

func (s *Store) SetDeployTokenHash(id int64, hash string) error {
	_, err := s.db.Exec(`UPDATE projects SET deploy_token_hash = ? WHERE id = ?`, hash, id)
	return err
}

func (s *Store) SetDesiredState(id int64, state string) error {
	_, err := s.db.Exec(`UPDATE projects SET desired_state = ? WHERE id = ?`, state, id)
	return err
}

func (s *Store) DeleteProject(id int64) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

// --- domains ---

func (s *Store) AddDomain(host string, projectID int64) error {
	_, err := s.db.Exec(`INSERT INTO domains (host, project_id, created_at) VALUES (?, ?, ?)`,
		host, projectID, time.Now().Unix())
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) RemoveDomain(host string, projectID int64) error {
	res, err := s.db.Exec(`DELETE FROM domains WHERE host = ? AND project_id = ?`, host, projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Domains returns every project's hosts, keyed by project ID.
func (s *Store) Domains() (map[int64][]string, error) {
	rows, err := s.db.Query(`SELECT project_id, host FROM domains ORDER BY created_at, host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var host string
		if err := rows.Scan(&id, &host); err != nil {
			return nil, err
		}
		out[id] = append(out[id], host)
	}
	return out, rows.Err()
}

// --- variables ---

func (s *Store) Variables(projectID int64) ([]Variable, error) {
	rows, err := s.db.Query(`SELECT key, value, updated_at FROM variables WHERE project_id = ? ORDER BY key`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Variable
	for rows.Next() {
		var v Variable
		var updated int64
		if err := rows.Scan(&v.Key, &v.Value, &updated); err != nil {
			return nil, err
		}
		v.UpdatedAt = time.Unix(updated, 0).UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Variable(projectID int64, key string) (Variable, error) {
	var v Variable
	var updated int64
	err := s.db.QueryRow(`SELECT key, value, updated_at FROM variables WHERE project_id = ? AND key = ?`,
		projectID, key).Scan(&v.Key, &v.Value, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	v.UpdatedAt = time.Unix(updated, 0).UTC()
	return v, err
}

// UpdateVariables sets and deletes variables in one transaction.
func (s *Store) UpdateVariables(projectID int64, set map[string][]byte, del []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for k, v := range set {
		if _, err := tx.Exec(`INSERT INTO variables (project_id, key, value, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (project_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			projectID, k, v, now); err != nil {
			return err
		}
	}
	for _, k := range del {
		if _, err := tx.Exec(`DELETE FROM variables WHERE project_id = ? AND key = ?`, projectID, k); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// --- releases ---

const releaseCols = `id, project_id, number, commit_sha, message, size, sha256, status, log, pruned, created_at`

func scanRelease(row interface{ Scan(...any) error }) (Release, error) {
	var r Release
	var created int64
	err := row.Scan(&r.ID, &r.ProjectID, &r.Number, &r.Commit, &r.Message, &r.Size, &r.SHA256, &r.Status, &r.Log, &r.Pruned, &created)
	r.CreatedAt = time.Unix(created, 0).UTC()
	return r, err
}

// CreateRelease records a new upload and assigns the next release number.
func (s *Store) CreateRelease(projectID int64, commit, message string, size int64, sha string) (Release, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Release{}, err
	}
	defer tx.Rollback()
	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(number), 0) + 1 FROM releases WHERE project_id = ?`, projectID).Scan(&next); err != nil {
		return Release{}, err
	}
	res, err := tx.Exec(`INSERT INTO releases (project_id, number, commit_sha, message, size, sha256, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, projectID, next, commit, message, size, sha, ReleaseStarting, time.Now().Unix())
	if err != nil {
		return Release{}, err
	}
	if err := tx.Commit(); err != nil {
		return Release{}, err
	}
	id, _ := res.LastInsertId()
	return s.Release(id)
}

func (s *Store) Release(id int64) (Release, error) {
	r, err := scanRelease(s.db.QueryRow(`SELECT `+releaseCols+` FROM releases WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

func (s *Store) Releases(projectID int64, limit int) ([]Release, error) {
	rows, err := s.db.Query(`SELECT `+releaseCols+` FROM releases WHERE project_id = ? ORDER BY number DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateRelease sets the status and appends to the release's deploy log.
func (s *Store) UpdateRelease(id int64, status, appendLog string) error {
	_, err := s.db.Exec(`UPDATE releases SET status = ?, log = log || ? WHERE id = ?`, status, appendLog, id)
	return err
}

// ActivateRelease marks a release as the live one for its project, served
// by the given instance and port.
func (s *Store) ActivateRelease(projectID, releaseID int64, appendLog, instance string, port int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []struct {
		q    string
		args []any
	}{
		{`UPDATE releases SET status = ? WHERE project_id = ? AND status = ? AND id <> ?`,
			[]any{ReleaseSuperseded, projectID, ReleaseActive, releaseID}},
		{`UPDATE releases SET status = ?, log = log || ? WHERE id = ?`, []any{ReleaseActive, appendLog, releaseID}},
		{`UPDATE projects SET active_release_id = ?, desired_state = ?, live_instance = ?, live_port = ? WHERE id = ?`,
			[]any{releaseID, StateRunning, instance, port, projectID}},
	}
	for _, st := range stmts {
		if _, err := tx.Exec(st.q, st.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MarkPruned(id int64) error {
	_, err := s.db.Exec(`UPDATE releases SET pruned = 1 WHERE id = ?`, id)
	return err
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
