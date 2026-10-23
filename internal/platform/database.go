package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"sideyard/internal/runner"
	"sideyard/internal/store"
)

// backupsDir, inside DATA_DIR, holds the versions that imports replaced.
const (
	backupsDir  = ".sideyard/backups"
	keepBackups = 10
)

type Database struct {
	Path       string    `json:"path"` // relative to DATA_DIR
	Size       int64     `json:"size"` // including the -wal file
	ModifiedAt time.Time `json:"modifiedAt"`
}

// Databases lists the SQLite files in a project's data dir, and the earlier
// versions that imports kept, newest first.
func (p *Platform) Databases(name string) (dbs, backups []Database, err error) {
	if _, err := p.store.ProjectByName(name); err != nil {
		return nil, nil, err
	}
	data := p.dir(name, "data")
	err = filepath.WalkDir(data, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(data, path)
		if d.IsDir() {
			if rel == ".sideyard" || strings.Count(rel, string(filepath.Separator)) >= 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if db, ok := describeDB(data, path); ok && len(dbs) < 50 {
			dbs = append(dbs, db)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	entries, _ := os.ReadDir(filepath.Join(data, backupsDir))
	for _, e := range entries {
		if db, ok := describeDB(data, filepath.Join(data, backupsDir, e.Name())); ok {
			backups = append(backups, db)
		}
	}
	slices.SortFunc(backups, func(a, b Database) int { return strings.Compare(backupStamp(b.Path), backupStamp(a.Path)) })
	return dbs, backups, nil
}

func describeDB(data, path string) (Database, bool) {
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		if strings.HasSuffix(path, side) {
			return Database{}, false
		}
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() || !store.IsSQLite(path) {
		return Database{}, false
	}
	rel, _ := filepath.Rel(data, path)
	db := Database{Path: filepath.ToSlash(rel), Size: st.Size(), ModifiedAt: st.ModTime().UTC()}
	if wal, err := os.Stat(path + "-wal"); err == nil {
		db.Size += wal.Size()
		if wal.ModTime().After(st.ModTime()) {
			db.ModifiedAt = wal.ModTime().UTC()
		}
	}
	return db, true
}

// dbPath resolves a path relative to the project's DATA_DIR, refusing
// anything that would escape it.
func (p *Platform) dbPath(name, rel string) (string, string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(rel)))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", invalid("database path must be relative to DATA_DIR, e.g. app.db")
	}
	return filepath.Join(p.dir(name, "data"), clean), filepath.ToSlash(clean), nil
}

// SnapshotDatabase makes a consistent copy of a database, even while the
// app is writing to it, and returns its path. cleanup deletes the copy.
// Under the systemd runner the copy is made as the project's user, so any
// SQLite side files it touches stay owned by the app.
func (p *Platform) SnapshotDatabase(ctx context.Context, name, rel string) (path string, cleanup func(), err error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return "", nil, err
	}
	src, _, err := p.dbPath(name, rel)
	if err != nil {
		return "", nil, err
	}
	if !store.IsSQLite(src) {
		return "", nil, ErrNotFound
	}
	dir, err := os.MkdirTemp("", "sideyard-snapshot-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	dst := filepath.Join(dir, filepath.Base(src))

	cred, err := p.runner.Credential(p.ref(proj))
	if err == nil && cred == nil {
		err = store.SnapshotFile(src, dst)
	} else if err == nil {
		err = snapshotAs(ctx, cred, dir, src, dst)
	}
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("snapshot %s: %w", rel, err)
	}
	return dst, cleanup, nil
}

// snapshotAs runs `sideyard sqlite-snapshot` as another user.
func snapshotAs(ctx context.Context, cred *syscall.Credential, dir, src, dst string) error {
	if err := os.Chown(dir, int(cred.Uid), int(cred.Gid)); err != nil {
		return err
	}
	return runHelperAs(ctx, cred, nil, io.Discard, "sqlite-snapshot", src, dst)
}

// ImportUploadDir is where the API stages an uploaded database: on the same
// filesystem as DATA_DIR, so the import is a rename.
func (p *Platform) ImportUploadDir(name string) string {
	return p.dir(name)
}

// ImportDatabase replaces (or creates) a database in the project's DATA_DIR
// with an uploaded file, which it moves. A running app is stopped for the
// swap and started again; if it then fails its health check, the previous
// database is put back. The replaced file is kept under .sideyard/backups,
// whose path is returned.
func (p *Platform) ImportDatabase(ctx context.Context, name, rel, upload string) (backup string, err error) {
	target, rel, err := p.dbPath(name, rel)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, ".sideyard/") {
		return "", invalid("choose a path outside .sideyard")
	}
	if !store.IsSQLite(upload) {
		return "", invalid("that file isn't a SQLite database")
	}
	if err := store.CheckFile(upload); err != nil {
		return "", invalid("the database failed SQLite's check: %v", err)
	}

	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return "", err
	}
	unlock := p.lock(proj.ID)
	defer unlock()
	if proj, err = p.store.ProjectByID(proj.ID); err != nil {
		return "", err
	}
	ref := p.ref(proj)
	wasLive := p.getLive(proj.ID) != nil
	if wasLive {
		p.logf(proj, "stopping to import %s", rel)
		if err := p.stopLocked(proj); err != nil {
			return "", err
		}
	}

	data := p.dir(name, "data")
	if store.IsSQLite(target) {
		base := strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
		backup = filepath.Join(backupsDir, base+"-"+time.Now().UTC().Format(backupLayout)+".db")
		if err := p.mkdirOwned(ref, filepath.Join(data, backupsDir)); err != nil {
			return "", err
		}
		if err := moveDB(target, filepath.Join(data, backup)); err != nil {
			return "", err
		}
	}
	if err := p.mkdirOwned(ref, filepath.Dir(target)); err != nil {
		return "", err
	}
	removeDB(target)
	if err := os.Rename(upload, target); err != nil {
		return "", err
	}
	os.Chmod(target, 0o640)
	if err := p.runner.Own(ref, target, runner.AppWritable); err != nil {
		return "", err
	}
	p.pruneBackups(data)
	if backup != "" {
		p.logf(proj, "imported %s; the previous version is %s", rel, filepath.ToSlash(backup))
	} else {
		p.logf(proj, "imported %s", rel)
	}

	if wasLive && proj.ActiveReleaseID != 0 {
		relse, err := p.store.Release(proj.ActiveReleaseID)
		if err != nil {
			return backup, err
		}
		if _, err := p.activate(ctx, proj, relse); err != nil {
			if backup == "" {
				return "", fmt.Errorf("the app failed its health check with the imported database: %w", err)
			}
			// The failed app may have left a WAL for the imported file;
			// replaying it into the restored one would corrupt it.
			removeDB(target)
			if rerr := moveDB(filepath.Join(data, backup), target); rerr != nil {
				return "", fmt.Errorf("the app failed with the imported database (%v), and restoring the previous one failed: %w", err, rerr)
			}
			p.logf(proj, "restored the previous %s", rel)
			p.activate(ctx, proj, relse)
			return "", fmt.Errorf("the app failed its health check with the imported database, so the previous one was restored: %w", err)
		}
	}
	return filepath.ToSlash(backup), nil
}

// moveDB moves a database file and its WAL (which may hold committed data
// not yet in the main file). The shared-memory file is rebuilt by SQLite.
func moveDB(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	if err := os.Rename(from+"-wal", to+"-wal"); err != nil && !os.IsNotExist(err) {
		return err
	}
	os.Remove(from + "-shm")
	os.Remove(from + "-journal")
	return nil
}

// removeDB deletes a database file and all of its side files.
func removeDB(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		os.Remove(path + suffix)
	}
}

func (p *Platform) mkdirOwned(ref runner.ProjectRef, dir string) error {
	if _, err := os.Stat(dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// Hand every directory MkdirAll created inside DATA_DIR to the app.
	for d := dir; d != ref.DataDir() && strings.HasPrefix(d, ref.DataDir()); d = filepath.Dir(d) {
		if err := p.runner.Own(ref, d, runner.AppWritable); err != nil {
			return err
		}
	}
	return nil
}

func (p *Platform) pruneBackups(data string) {
	dir := filepath.Join(data, backupsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var dbs []os.DirEntry
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db") {
			dbs = append(dbs, e)
		}
	}
	slices.SortFunc(dbs, func(a, b os.DirEntry) int { return strings.Compare(backupStamp(b.Name()), backupStamp(a.Name())) })
	for _, e := range dbs[min(len(dbs), keepBackups):] {
		path := filepath.Join(dir, e.Name())
		os.Remove(path)
		os.Remove(path + "-wal")
	}
}

// backupLayout timestamps a backup's name with when it was replaced. The
// milliseconds keep two imports in the same second from colliding.
const backupLayout = "20060102-150405.000"

// backupStamp extracts the timestamp, which sorts in time order.
func backupStamp(name string) string {
	name = strings.TrimSuffix(name, ".db")
	if len(name) < len(backupLayout) {
		return ""
	}
	return name[len(name)-len(backupLayout):]
}

// DatabaseSchema lists a database's tables and views with their columns,
// indexes and row counts.
func (p *Platform) DatabaseSchema(ctx context.Context, name, rel string) ([]store.TableInfo, error) {
	resp, err := p.dbHelper(ctx, name, rel, store.HelperRequest{Op: "schema"})
	return resp.Schema, err
}

// TableRows reads one page of a table, with each row's key for editing.
func (p *Platform) TableRows(ctx context.Context, name, rel string, req store.RowsRequest) (store.RowsResult, error) {
	resp, err := p.dbHelper(ctx, name, rel, store.HelperRequest{Op: "rows", Rows: &req})
	if err != nil {
		return store.RowsResult{}, err
	}
	return *resp.Rows, nil
}

// EditRows inserts, updates or deletes rows from the table editor. Every
// change is noted in the project's log.
func (p *Platform) EditRows(ctx context.Context, name, rel string, m store.Mutation) (store.MutationResult, error) {
	resp, err := p.dbHelper(ctx, name, rel, store.HelperRequest{Op: "mutate", Mutation: &m})
	if err != nil {
		return store.MutationResult{}, err
	}
	if proj, err := p.store.ProjectByName(name); err == nil {
		p.logf(proj, "table editor: %s %d row(s) in %s (%s)", pastTense[m.Kind], resp.Mutation.Affected, m.Table, rel)
	}
	return *resp.Mutation, nil
}

var pastTense = map[string]string{"insert": "inserted", "update": "updated", "delete": "deleted"}

// AlterDatabase changes a database's structure and returns the SQL it ran.
func (p *Platform) AlterDatabase(ctx context.Context, name, rel string, a store.Alter) (string, error) {
	q, err := store.AlterSQL(a)
	if err != nil {
		return "", invalid("%v", err)
	}
	if _, err := p.dbHelper(ctx, name, rel, store.HelperRequest{Op: "alter", Alter: &a}); err != nil {
		return q, err
	}
	if proj, err := p.store.ProjectByName(name); err == nil {
		p.logf(proj, "table editor ran on %s: %s", rel, strings.ReplaceAll(q, "\n", " "))
	}
	return q, nil
}

// ExecSQL runs a script from the SQL editor. Without write it runs
// read-only, and a script that would change data comes back with
// NeedsWrite set and nothing changed.
func (p *Platform) ExecSQL(ctx context.Context, name, rel, query string, write bool) (store.ExecResult, error) {
	if strings.TrimSpace(query) == "" {
		return store.ExecResult{}, invalid("type a query")
	}
	resp, err := p.dbHelper(ctx, name, rel, store.HelperRequest{Op: "exec", SQL: query, Write: write})
	if err != nil {
		return store.ExecResult{}, err
	}
	if write {
		if proj, err := p.store.ProjectByName(name); err == nil {
			p.logf(proj, "SQL editor ran a write on %s (%d row(s) changed): %s", rel, resp.Exec.Changes, oneLine(query, 200))
		}
	}
	return *resp.Exec, nil
}

// SavedQuery is a query kept in the SQL Editor. Sideyard stores them, so
// they're the same in every browser.
type SavedQuery struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

// queryIDRe accepts the IDs the dashboard makes (crypto.randomUUID).
var queryIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

func (p *Platform) SavedQueries(name string) ([]SavedQuery, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return nil, err
	}
	saved, err := p.store.SavedQueries(proj.ID)
	if err != nil {
		return nil, err
	}
	out := []SavedQuery{}
	for _, q := range saved {
		out = append(out, SavedQuery(q))
	}
	return out, nil
}

// SaveQuery creates or replaces a saved query.
func (p *Platform) SaveQuery(name string, q SavedQuery) error {
	if !queryIDRe.MatchString(q.ID) {
		return invalid("a query's ID is 1 to 64 letters, digits and dashes")
	}
	if utf8.RuneCountInString(q.Name) > 100 {
		return invalid("keep the query's name to 100 characters")
	}
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return err
	}
	return p.store.SaveQuery(proj.ID, store.SavedQuery(q))
}

func (p *Platform) DeleteQuery(name, id string) error {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return err
	}
	return p.store.DeleteQuery(proj.ID, id)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// dbHelper runs a database operation in-process, or under the systemd
// runner through `sideyard sqlite-query` as the project's user, so any
// files SQLite creates belong to the app.
func (p *Platform) dbHelper(ctx context.Context, name, rel string, req store.HelperRequest) (store.HelperResponse, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return store.HelperResponse{}, err
	}
	path, _, err := p.dbPath(name, rel)
	if err != nil {
		return store.HelperResponse{}, err
	}
	if !store.IsSQLite(path) {
		return store.HelperResponse{}, ErrNotFound
	}
	cred, err := p.runner.Credential(p.ref(proj))
	if err != nil {
		return store.HelperResponse{}, err
	}
	var resp store.HelperResponse
	if cred == nil {
		resp = store.Query(ctx, path, req)
	} else {
		in, _ := json.Marshal(req)
		var out bytes.Buffer
		if err := runHelperAs(ctx, cred, bytes.NewReader(in), &out, "sqlite-query", path); err != nil {
			return resp, err
		}
		if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
			return resp, fmt.Errorf("database helper: %w", err)
		}
	}
	if resp.Error != "" {
		return resp, invalid("%s", resp.Error)
	}
	return resp, nil
}

// runHelperAs runs a `sideyard` helper command as another user.
func runHelperAs(ctx context.Context, cred *syscall.Credential, stdin io.Reader, stdout io.Writer, args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
