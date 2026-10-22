package platform

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"sideyard/internal/s3"
	"sideyard/internal/store"
)

// Backups copy every database off the server: Sideyard's own and each
// app's, as consistent snapshots, gzipped, to an S3 bucket configured in
// Platform settings. Nothing runs until a bucket is connected and enabled.
//
//	<prefix>sideyard/sideyard.db/<UTC time>.db.gz
//	<prefix>projects/<project>/<path in DATA_DIR>/<UTC time>.db.gz
const (
	settingBackups = "backups"
	remoteStamp    = "20060102T150405Z"
	// keepNewestBackups survive retention no matter how old they are, so a
	// server whose backups stopped working still has its last good copies.
	keepNewestBackups = 3
)

// BackupSettings is the backup configuration as the dashboard sees it.
// SecretKey is only ever sent in, never out.
type BackupSettings struct {
	Enabled       bool   `json:"enabled"`
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	AccessKey     string `json:"accessKey"`
	SecretKey     string `json:"secretKey,omitempty"`
	HasSecret     bool   `json:"hasSecret"`
	Prefix        string `json:"prefix"`
	IntervalHours int    `json:"intervalHours"`
	RetentionDays int    `json:"retentionDays"`
}

// storedBackups is how the settings are saved, with the secret encrypted.
type storedBackups struct {
	BackupSettings
	SealedSecret []byte `json:"sealedSecret"`
}

func (p *Platform) loadBackupSettings() (storedBackups, error) {
	st := storedBackups{BackupSettings: BackupSettings{IntervalHours: 6, RetentionDays: 14}}
	err := p.store.JSONSetting(settingBackups, &st)
	st.SecretKey = ""
	st.HasSecret = len(st.SealedSecret) > 0
	return st, err
}

func (p *Platform) BackupSettings() (BackupSettings, error) {
	st, err := p.loadBackupSettings()
	return st.BackupSettings, err
}

// SaveBackupSettings checks Sideyard can write to the bucket before saving.
// An empty SecretKey keeps the saved one.
func (p *Platform) SaveBackupSettings(ctx context.Context, in BackupSettings) error {
	old, err := p.loadBackupSettings()
	if err != nil {
		return err
	}
	in.Prefix = strings.Trim(strings.TrimSpace(in.Prefix), "/")
	if in.Prefix != "" {
		in.Prefix += "/"
	}
	switch {
	case in.IntervalHours < 1 || in.IntervalHours > 168:
		return invalid("back up every 1 to 168 hours")
	case in.RetentionDays < 1 || in.RetentionDays > 3650:
		return invalid("keep backups for 1 to 3650 days")
	}
	sealed := old.SealedSecret
	if in.SecretKey != "" {
		sealed = p.box.Seal([]byte(in.SecretKey))
	}
	st := storedBackups{BackupSettings: in, SealedSecret: sealed}
	st.SecretKey, st.HasSecret = "", false
	if in.Enabled {
		client, err := p.backupClient(st)
		if err != nil {
			return invalid("%v", err)
		}
		if err := checkWritable(ctx, client, in.Prefix); err != nil {
			return invalid("couldn't write to the bucket: %v", err)
		}
	}
	return p.store.SetJSONSetting(settingBackups, st)
}

// checkWritable proves the credentials can write and delete, not just list.
func checkWritable(ctx context.Context, c *s3.Client, prefix string) error {
	if err := c.Check(ctx); err != nil {
		return err
	}
	key := prefix + ".sideyard-write-check"
	if err := c.Put(ctx, key, strings.NewReader("ok"), 2, "text/plain"); err != nil {
		return err
	}
	return c.Delete(ctx, key)
}

func (p *Platform) backupClient(st storedBackups) (*s3.Client, error) {
	if len(st.SealedSecret) == 0 {
		return nil, errors.New("secret key is required")
	}
	secret, err := p.box.Open(st.SealedSecret)
	if err != nil {
		return nil, err
	}
	return s3.New(s3.Config{Endpoint: st.Endpoint, Region: st.Region, Bucket: st.Bucket,
		AccessKey: st.AccessKey, SecretKey: string(secret)})
}

// activeBackups returns the client and settings, or an error saying why
// backups aren't available.
func (p *Platform) activeBackups() (*s3.Client, storedBackups, error) {
	st, err := p.loadBackupSettings()
	if err != nil {
		return nil, st, err
	}
	if !st.Enabled {
		return nil, st, invalid("backups aren't set up; connect a bucket in Platform settings")
	}
	c, err := p.backupClient(st)
	return c, st, err
}

type BackupRunView struct {
	ID         int64     `json:"id"`
	Trigger    string    `json:"trigger"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Status     string    `json:"status"`
	Files      int       `json:"files"`
	Bytes      int64     `json:"bytes"`
	Log        string    `json:"log"`
}

func viewBackupRun(r store.BackupRun) BackupRunView {
	return BackupRunView{r.ID, r.Trigger, r.StartedAt, r.FinishedAt, r.Status, r.Files, r.Bytes, r.Log}
}

func (p *Platform) BackupRuns(limit int) ([]BackupRunView, error) {
	runs, err := p.store.BackupRuns(limit)
	if err != nil {
		return nil, err
	}
	out := []BackupRunView{}
	for _, r := range runs {
		out = append(out, viewBackupRun(r))
	}
	return out, nil
}

// RunBackup starts a backup in the background.
func (p *Platform) RunBackup(trigger string) (BackupRunView, error) {
	client, st, err := p.activeBackups()
	if err != nil {
		return BackupRunView{}, err
	}
	p.mu.Lock()
	if p.backupRunning {
		p.mu.Unlock()
		return BackupRunView{}, fmt.Errorf("%w: a backup is already running", ErrConflict)
	}
	p.backupRunning = true
	p.mu.Unlock()
	run, err := p.store.StartBackupRun(trigger)
	if err != nil {
		p.mu.Lock()
		p.backupRunning = false
		p.mu.Unlock()
		return BackupRunView{}, err
	}
	p.bgWG.Go(func() {
		defer func() {
			p.mu.Lock()
			p.backupRunning = false
			p.mu.Unlock()
		}()
		p.runBackup(client, st, run)
	})
	return viewBackupRun(run), nil
}

func (p *Platform) backupLoop() {
	if err := p.store.AbandonRunningBackups(); err != nil {
		log.Printf("backups: %v", err)
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-p.bg.Done():
			return
		case <-t.C:
		}
		st, err := p.loadBackupSettings()
		if err != nil || !st.Enabled {
			continue
		}
		runs, err := p.store.BackupRuns(1)
		if err != nil {
			continue
		}
		if len(runs) == 0 || time.Since(runs[0].StartedAt) >= time.Duration(st.IntervalHours)*time.Hour {
			p.RunBackup("schedule")
		}
	}
}

func (p *Platform) runBackup(client *s3.Client, st storedBackups, run store.BackupRun) {
	ctx := p.bg
	stamp := run.StartedAt.UTC().Format(remoteStamp)
	var logb strings.Builder
	var failed, done int
	upload := func(label, dir string, snapshot func() (string, func(), error)) {
		key := dir + stamp + ".db.gz"
		size, err := uploadSnapshot(ctx, client, key, snapshot)
		if err != nil {
			failed++
			fmt.Fprintf(&logb, "FAILED %s: %v\n", label, err)
			return
		}
		done++
		run.Files++
		run.Bytes += size
		fmt.Fprintf(&logb, "%s → %s (%s)\n", label, key, HumanBytes(size))
		if n, err := pruneBackups(ctx, client, dir, time.Duration(st.RetentionDays)*24*time.Hour); err != nil {
			fmt.Fprintf(&logb, "  couldn't prune old copies: %v\n", err)
		} else if n > 0 {
			fmt.Fprintf(&logb, "  removed %d copies older than %d days\n", n, st.RetentionDays)
		}
	}

	upload("sideyard.db", st.Prefix+"sideyard/sideyard.db/", func() (string, func(), error) {
		dir, err := os.MkdirTemp("", "sideyard-backup-")
		if err != nil {
			return "", nil, err
		}
		dst := filepath.Join(dir, "sideyard.db")
		return dst, func() { os.RemoveAll(dir) }, p.store.Snapshot(dst)
	})
	projects, err := p.store.Projects()
	if err != nil {
		failed++
		fmt.Fprintf(&logb, "FAILED listing projects: %v\n", err)
	}
	for _, proj := range projects {
		dbs, _, err := p.Databases(proj.Name)
		if err != nil {
			failed++
			fmt.Fprintf(&logb, "FAILED %s: %v\n", proj.Name, err)
			continue
		}
		for _, db := range dbs {
			upload(proj.Name+"/"+db.Path, p.remoteDir(st.Prefix, proj.Name, db.Path), func() (string, func(), error) {
				return p.SnapshotDatabase(ctx, proj.Name, db.Path)
			})
		}
	}

	run.Status = "succeeded"
	switch {
	case failed > 0 && done == 0:
		run.Status = "failed"
	case failed > 0:
		run.Status = "partial"
	}
	run.FinishedAt = time.Now().UTC()
	run.Log = logb.String()
	if err := p.store.FinishBackupRun(run); err != nil {
		log.Printf("backups: %v", err)
	}
	if failed > 0 {
		log.Printf("backups: run %d %s: %d of %d copies failed", run.ID, run.Status, failed, failed+done)
	}
}

func (p *Platform) remoteDir(prefix, project, dbPath string) string {
	return prefix + "projects/" + project + "/" + dbPath + "/"
}

// uploadSnapshot gzips a snapshot to a temp file (so its size is known)
// and uploads it. It returns the compressed size.
func uploadSnapshot(ctx context.Context, client *s3.Client, key string, snapshot func() (string, func(), error)) (int64, error) {
	src, cleanup, err := snapshot()
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return 0, err
	}
	gz, err := os.CreateTemp("", "sideyard-backup-*.gz")
	if err != nil {
		return 0, err
	}
	defer os.Remove(gz.Name())
	defer gz.Close()
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	zw := gzip.NewWriter(gz)
	if _, err := io.Copy(zw, in); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	size, err := gz.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if _, err := gz.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	return size, client.Put(ctx, key, gz, size, "application/gzip")
}

// pruneBackups deletes copies in dir older than retention, keeping the
// newest few regardless.
func pruneBackups(ctx context.Context, client *s3.Client, dir string, retention time.Duration) (int, error) {
	objs, _, err := client.Walk(ctx, dir, 100_000)
	if err != nil {
		return 0, err
	}
	// Names start with a UTC timestamp, so they sort by age.
	slices.SortFunc(objs, func(a, b s3.Object) int { return strings.Compare(b.Key, a.Key) })
	removed := 0
	for i, o := range objs {
		if i < keepNewestBackups || strings.Contains(strings.TrimPrefix(o.Key, dir), "/") {
			continue
		}
		taken, err := time.Parse(remoteStamp, strings.TrimSuffix(path.Base(o.Key), ".db.gz"))
		if err != nil || time.Since(taken) < retention {
			continue
		}
		if err := client.Delete(ctx, o.Key); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

type RemoteBackup struct {
	Key     string    `json:"key"`
	Size    int64     `json:"size"`
	TakenAt time.Time `json:"takenAt"`
}

// RemoteBackups lists a database's copies in the backup bucket, newest
// first. ok is false when backups aren't set up.
func (p *Platform) RemoteBackups(ctx context.Context, name, dbPath string) (backups []RemoteBackup, ok bool, err error) {
	if _, _, err := p.dbPath(name, dbPath); err != nil {
		return nil, false, err
	}
	client, st, err := p.activeBackups()
	if err != nil {
		return []RemoteBackup{}, false, nil
	}
	dir := p.remoteDir(st.Prefix, name, dbPath)
	objs, _, err := client.Walk(ctx, dir, 10_000)
	if err != nil {
		return nil, true, err
	}
	backups = []RemoteBackup{}
	for _, o := range objs {
		taken, err := time.Parse(remoteStamp, strings.TrimSuffix(path.Base(o.Key), ".db.gz"))
		if err != nil || strings.Contains(strings.TrimPrefix(o.Key, dir), "/") {
			continue
		}
		backups = append(backups, RemoteBackup{Key: o.Key, Size: o.Size, TakenAt: taken})
	}
	slices.SortFunc(backups, func(a, b RemoteBackup) int { return b.TakenAt.Compare(a.TakenAt) })
	return backups, true, nil
}

// remoteKey checks a key belongs to this project's database.
func (p *Platform) remoteKey(name, dbPath, key string) (*s3.Client, error) {
	client, st, err := p.activeBackups()
	if err != nil {
		return nil, err
	}
	dir := p.remoteDir(st.Prefix, name, dbPath)
	if !strings.HasPrefix(key, dir) || strings.Contains(strings.TrimPrefix(key, dir), "/") {
		return nil, invalid("that backup isn't one of %s's copies of %s", name, dbPath)
	}
	return client, nil
}

// RestoreBackup replaces a database with a copy from the backup bucket,
// through the same path as an import: the current file is kept locally,
// and put back if the app fails its health check.
func (p *Platform) RestoreBackup(ctx context.Context, name, dbPath, key string) (string, error) {
	client, err := p.remoteKey(name, dbPath, key)
	if err != nil {
		return "", err
	}
	obj, err := client.Get(ctx, key)
	if err != nil {
		return "", err
	}
	defer obj.Close()
	zr, err := gzip.NewReader(obj)
	if err != nil {
		return "", fmt.Errorf("backup isn't gzip: %w", err)
	}
	staged, err := os.CreateTemp(p.ImportUploadDir(name), ".restore-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(staged.Name()) // a no-op once the import has moved it
	if _, err := io.Copy(staged, zr); err != nil {
		staged.Close()
		return "", err
	}
	if err := staged.Close(); err != nil {
		return "", err
	}
	if proj, err := p.store.ProjectByName(name); err == nil {
		p.logf(proj, "restoring %s from %s", dbPath, key)
	}
	return p.ImportDatabase(ctx, name, dbPath, staged.Name())
}

// BackupURL is a five-minute download link for a backup copy.
func (p *Platform) BackupURL(ctx context.Context, name, dbPath, key string) (string, error) {
	client, err := p.remoteKey(name, dbPath, key)
	if err != nil {
		return "", err
	}
	return client.PresignGet(ctx, key, 5*time.Minute)
}

// HumanBytes formats a size for people: 512 B, 14 KB, 3.2 MB, 1.1 GB.
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
