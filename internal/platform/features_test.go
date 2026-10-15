package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"sideyard/internal/mail/mailtest"
	"sideyard/internal/s3"
	"sideyard/internal/s3/s3test"
	"sideyard/internal/store"
)

func TestStorage(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p := e.p
	if _, _, err := p.CreateProject("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.deploy("demo"); err != nil {
		t.Fatal(err)
	}
	cfg, stop := s3test.FakeServer("uploads", "other")
	defer stop()

	// A variable that the bucket would set has to go first.
	if _, err := p.UpdateVariables(ctx, "demo", map[string]string{"S3_BUCKET": "old"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ConnectStorage(ctx, "demo", cfg); err == nil || !strings.Contains(err.Error(), "S3_BUCKET") {
		t.Fatalf("connect over a clashing variable: %v", err)
	}
	if _, err := p.UpdateVariables(ctx, "demo", nil, []string{"S3_BUCKET"}); err != nil {
		t.Fatal(err)
	}

	bad := cfg
	bad.Bucket = "missing"
	if _, err := p.ConnectStorage(ctx, "demo", bad); err == nil {
		t.Fatal("a missing bucket should be refused")
	}

	restarted, err := p.ConnectStorage(ctx, "demo", cfg)
	if err != nil || !restarted {
		t.Fatalf("connect: restarted=%v err=%v", restarted, err)
	}
	for key, want := range map[string]string{"S3_BUCKET": "uploads", "AWS_ENDPOINT_URL_S3": cfg.Endpoint, "AWS_SECRET_ACCESS_KEY": cfg.SecretKey} {
		if _, got := e.get("demo", "/env?key="+key); got != want {
			t.Errorf("app sees %s=%q, want %q", key, got, want)
		}
	}
	if _, err := p.UpdateVariables(ctx, "demo", map[string]string{"AWS_REGION": "x"}, nil); err == nil {
		t.Error("variables the bucket sets should be refused while it's connected")
	}

	// Editing without retyping the secret keeps it.
	keep := cfg
	keep.SecretKey = ""
	if _, err := p.ConnectStorage(ctx, "demo", keep); err != nil {
		t.Fatalf("reconnect keeping the secret: %v", err)
	}

	body := "hello"
	if err := p.PutObject(ctx, "demo", "docs/a.txt", strings.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	top, _, err := p.Objects(ctx, "demo", "", "")
	if err != nil || len(top) != 1 || top[0].Key != "docs/" || !top[0].IsPrefix {
		t.Fatalf("top level: %+v %v", top, err)
	}
	inner, _, err := p.Objects(ctx, "demo", "docs/", "")
	if err != nil || len(inner) != 1 || inner[0].Key != "docs/a.txt" {
		t.Fatalf("docs/: %+v %v", inner, err)
	}
	if u, err := p.ObjectURL(ctx, "demo", "docs/a.txt"); err != nil || !strings.Contains(u, "Signature") {
		t.Fatalf("download URL: %q %v", u, err)
	}
	if u, err := p.StorageUsage(ctx, "demo"); err != nil || u.Objects != 1 || u.Bytes != 5 {
		t.Fatalf("usage: %+v %v", u, err)
	}
	if err := p.DeleteObject(ctx, "demo", "docs/a.txt"); err != nil {
		t.Fatal(err)
	}

	if restarted, err := p.DisconnectStorage(ctx, "demo"); err != nil || !restarted {
		t.Fatalf("disconnect: %v %v", restarted, err)
	}
	if _, got := e.get("demo", "/env?key=S3_BUCKET"); got != "" {
		t.Errorf("after disconnecting the app still sees S3_BUCKET=%q", got)
	}
}

func TestJobs(t *testing.T) {
	e := newTestEnv(t)
	p := e.p
	if _, _, err := p.CreateProject("demo"); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []JobSpec{
		{Name: "x", Schedule: "not cron"},
		{Name: "x", Schedule: "@every 10s"},
		{Name: "Bad Name", Schedule: "@hourly"},
		{Name: "x", Schedule: "@hourly", Retries: 9},
	} {
		if err := p.CreateJob("demo", bad); err == nil {
			t.Errorf("%+v should be refused", bad)
		}
	}
	for _, j := range []JobSpec{
		{Name: "report", Schedule: "0 9 * * *", Enabled: true},
		{Name: "fail", Schedule: "@hourly", Retries: 1, Enabled: true},
		{Name: "slow", Schedule: "@daily", TimeoutSec: 1, Enabled: true},
	} {
		if err := p.CreateJob("demo", j); err != nil {
			t.Fatal(err)
		}
	}

	// Before a deploy there's nothing to call.
	if run := e.runJob("report"); run.Status != "skipped" {
		t.Fatalf("job with no app running: %+v", run)
	}

	if _, err := e.deploy("demo"); err != nil {
		t.Fatal(err)
	}
	if run := e.runJob("report"); run.Status != "succeeded" || run.HTTPStatus != 200 || !strings.Contains(run.Output, "ran report") {
		t.Fatalf("report: %+v", run)
	}
	if run := e.runJob("fail"); run.Status != "failed" || run.Attempts != 2 || run.HTTPStatus != 500 {
		t.Fatalf("fail with one retry: %+v", run)
	}
	if run := e.runJob("slow"); run.Status != "failed" || !strings.Contains(run.Output, "timed out") {
		t.Fatalf("slow with a 1s timeout: %+v", run)
	}

	// Only Sideyard can call a job: the proxy hides /_jobs/ from outside.
	if status, _ := e.get("demo", "/_jobs/report"); status != 404 {
		t.Fatalf("/_jobs/ through the proxy: %d, want 404", status)
	}

	jobs, err := p.Jobs("demo")
	if err != nil || len(jobs) != 3 {
		t.Fatalf("jobs: %+v %v", jobs, err)
	}
	for _, j := range jobs {
		if j.NextRun.IsZero() || j.LastRun == nil {
			t.Errorf("%s: next run %v, last run %v", j.Name, j.NextRun, j.LastRun)
		}
	}

	if err := p.UpdateJob("demo", "report", JobSpec{Schedule: "30 9 * * *", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteJob("demo", "slow"); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := p.Jobs("demo"); len(jobs) != 2 || jobs[1].Enabled || !jobs[1].NextRun.IsZero() {
		t.Fatalf("after update and delete: %+v", jobs)
	}
}

// runJob starts one of demo's jobs by hand and waits for it to finish.
func (e *testEnv) runJob(name string) RunView {
	e.Helper()
	return e.runJobIn("demo", name)
}

func (e *testEnv) runJobIn(project, name string) RunView {
	e.Helper()
	run, err := e.p.RunJob(project, name)
	if err != nil {
		e.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := e.p.JobRuns(project, name, 1)
		if err != nil {
			e.Fatal(err)
		}
		if len(runs) > 0 && runs[0].ID == run.ID && runs[0].Status != "running" {
			return runs[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.Fatalf("job %s didn't finish", name)
	return RunView{}
}

func TestJobBusy(t *testing.T) {
	e := newTestEnv(t)
	p := e.p
	p.CreateProject("demo")
	e.deploy("demo")
	p.CreateJob("demo", JobSpec{Name: "slow", Schedule: "@daily", Enabled: true})
	if _, err := p.RunJob("demo", "slow"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.RunJob("demo", "slow"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second run while the first is going: %v", err)
	}
}

func TestDueSlot(t *testing.T) {
	hourly, _ := cron.ParseStandard("0 * * * *")
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		base, now, want string
	}{
		{"2026-10-14 10:30", "2026-10-14 10:59", ""},                 // not due yet
		{"2026-10-14 10:30", "2026-10-14 11:00", "2026-10-14 11:00"}, // exactly due
		{"2026-10-14 11:00", "2026-10-14 11:30", ""},                 // already ran
		{"2026-10-14 10:30", "2026-10-14 13:10", "2026-10-14 13:00"}, // missed three: one catch-up
	}
	for _, c := range cases {
		due, ok := dueSlot(hourly, at(c.base), at(c.now))
		got := ""
		if ok {
			got = due.Format("2006-01-02 15:04")
		}
		if got != c.want {
			t.Errorf("base %s now %s: got %q, want %q", c.base, c.now, got, c.want)
		}
	}
	// A long outage only looks back a week.
	due, ok := dueSlot(hourly, at("2020-01-01 00:00"), at("2026-10-14 13:10"))
	if !ok || due != at("2026-10-14 13:00") {
		t.Errorf("long outage: %v %v", due, ok)
	}
}

func TestBackups(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p := e.p
	p.CreateProject("demo")
	if _, err := p.ImportDatabase(ctx, "demo", "app.db", makeDB(t, e.cfg, "v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.deploy("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.RunBackup("manual"); err == nil {
		t.Fatal("a backup without a bucket should be refused")
	}

	cfg, stop := s3test.FakeServer("backups")
	defer stop()
	settings := BackupSettings{Enabled: true, Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: "missing",
		AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, Prefix: "/sy/", IntervalHours: 6, RetentionDays: 14}
	if err := p.SaveBackupSettings(ctx, settings); err == nil {
		t.Fatal("a missing bucket should be refused")
	}
	settings.Bucket = cfg.Bucket
	if err := p.SaveBackupSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	saved, _ := p.BackupSettings()
	if !saved.HasSecret || saved.SecretKey != "" || saved.Prefix != "sy/" {
		t.Fatalf("saved settings: %+v", saved)
	}

	run := e.waitBackup(t)
	if run.Status != "succeeded" || run.Files != 2 {
		t.Fatalf("backup run: %+v", run)
	}
	remote, ok, err := p.RemoteBackups(ctx, "demo", "app.db")
	if err != nil || !ok || len(remote) != 1 || !strings.HasPrefix(remote[0].Key, "sy/projects/demo/app.db/") {
		t.Fatalf("remote backups: %+v %v %v", remote, ok, err)
	}
	if u, err := p.BackupURL(ctx, "demo", "app.db", remote[0].Key); err != nil || u == "" {
		t.Fatalf("backup URL: %v", err)
	}

	// Restoring goes back to v1 after the database has moved on.
	if _, err := p.ImportDatabase(ctx, "demo", "app.db", makeDB(t, e.cfg, "v2")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.RestoreBackup(ctx, "demo", "app.db", remote[0].Key); err != nil {
		t.Fatal(err)
	}
	if got := readNote(t, filepath.Join(e.cfg.DataDir, "projects/demo/data/app.db")); got != "v1" {
		t.Fatalf("after restore the database has %q, want v1", got)
	}
	e.expect("demo", 200, "release 1")
	if _, err := p.RestoreBackup(ctx, "demo", "app.db", "sy/sideyard/sideyard.db/x.db.gz"); err == nil {
		t.Fatal("restoring another database's backup should be refused")
	}
}

func (e *testEnv) waitBackup(t *testing.T) BackupRunView {
	t.Helper()
	if _, err := e.p.RunBackup("manual"); err != nil {
		t.Fatal(err)
	}
	for range 200 {
		runs, err := e.p.BackupRuns(1)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 1 && runs[0].Status != "running" {
			return runs[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("backup didn't finish")
	return BackupRunView{}
}

func TestPruneBackups(t *testing.T) {
	ctx := context.Background()
	cfg, stop := s3test.FakeServer("prune-test")
	defer stop()
	c, _ := s3.New(cfg)
	dir := "projects/demo/app.db/"
	recent := time.Now().UTC().Add(-time.Hour).Format(remoteStamp)
	for _, name := range []string{"20200101T000000Z", "20200102T000000Z", "20200103T000000Z", "20200104T000000Z", "20200105T000000Z", recent} {
		c.Put(ctx, dir+name+".db.gz", strings.NewReader("x"), 1, "")
	}
	removed, err := pruneBackups(ctx, c, dir, 14*24*time.Hour)
	if err != nil || removed != 3 {
		t.Fatalf("removed %d, %v; want the 3 old ones beyond the newest 3", removed, err)
	}
	left, _, _ := c.Walk(ctx, dir, 100)
	if len(left) != 3 || !strings.Contains(left[2].Key, recent) {
		t.Fatalf("left: %+v", left)
	}
}

func TestEmail(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p := e.p
	p.CreateProject("demo")
	srv, err := mailtest.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	req := EmailRequest{To: []string{"reader@example.org"}, Subject: "Hello", Text: "Hi there"}
	if err := p.SendEmail(ctx, "demo", req); err == nil {
		t.Fatal("sending before email is set up should fail")
	}

	smtp := srv.Config()
	if err := p.SaveEmailSettings(ctx, EmailSettings{Enabled: true, Host: smtp.Host, Port: smtp.Port, Security: "none",
		From: "Sideyard <noreply@example.com>", SenderDomains: []string{" Example.com "}}); err != nil {
		t.Fatal(err)
	}
	if err := p.SetProjectSender("demo", "Notes <notes@other.com>"); err == nil {
		t.Fatal("a sender outside the sender domains should be refused")
	}
	if err := p.SetProjectSender("demo", "Notes <notes@example.com>"); err != nil {
		t.Fatal(err)
	}
	if err := p.SendEmail(ctx, "demo", req); err != nil {
		t.Fatal(err)
	}
	got := srv.Messages()
	if len(got) != 1 || got[0].From != "notes@example.com" || got[0].To[0] != "reader@example.org" {
		t.Fatalf("delivered: %+v", got)
	}

	srv.RejectRcpt = "bounce"
	if err := p.SendEmail(ctx, "demo", EmailRequest{To: []string{"bounce@example.org"}, Subject: "x", Text: "x"}); err == nil {
		t.Fatal("a refused recipient should fail")
	}
	pe, err := p.ProjectEmail("demo")
	if err != nil || len(pe.Log) != 2 || pe.Log[0].Status != "failed" || pe.Log[1].Status != "sent" {
		t.Fatalf("delivery log: %+v %v", pe.Log, err)
	}
	if err := p.SendTestEmail(ctx, "admin@example.org"); err != nil {
		t.Fatal(err)
	}

	// Apps authenticate to the email API with the token in their environment.
	if _, err := e.deploy("demo"); err != nil {
		t.Fatal(err)
	}
	_, token := e.get("demo", "/env?key=SIDEYARD_TOKEN")
	if !strings.HasPrefix(token, "syt_") || !p.AppAuthorized("demo", token) || p.AppAuthorized("demo", token+"x") {
		t.Fatalf("app token %q not accepted, or a wrong one was", token)
	}
	if _, url := e.get("demo", "/env?key=SIDEYARD_API_URL"); url != "http://"+e.cfg.APIAddr {
		t.Fatalf("SIDEYARD_API_URL = %q", url)
	}
}

func TestTableEditor(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p := e.p
	p.CreateProject("demo")
	if _, err := p.ImportDatabase(ctx, "demo", "app.db", makeDB(t, e.cfg, "v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.deploy("demo"); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, write bool) store.ExecResult {
		t.Helper()
		res, err := p.ExecSQL(ctx, "demo", "app.db", q, write)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return res
	}

	// The SQL editor is read-only until a write is confirmed.
	setup := `CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE, joined DATETIME DEFAULT CURRENT_TIMESTAMP, avatar BLOB, big INTEGER);
		INSERT INTO users (email, joined, avatar, big) VALUES
			('a@x.io', '2026-01-02 03:04:05', x'89504e47', 9007199254740993),
			('b@x.io', '2026-02-03 04:05:06', NULL, 1),
			('c_50%@x.io', NULL, NULL, 2);
		CREATE TABLE tags (owner TEXT, name TEXT, PRIMARY KEY (owner, name)) WITHOUT ROWID;
		INSERT INTO tags VALUES ('a', 'x'), ('a', 'y')`
	if res := exec(setup, false); !res.NeedsWrite {
		t.Fatalf("a write without confirmation should come back as needsWrite: %+v", res)
	}
	if res := exec("SELECT count(*) FROM sqlite_schema WHERE name = 'users'", false); res.Rows[0][0] != int64(0) {
		t.Fatalf("the refused write changed the database: %+v", res)
	}
	if res := exec(setup, true); res.Changes != 2 {
		t.Fatalf("setup changes = %d, want 2 (the last statement's)", res.Changes)
	}
	if res := exec("SELECT email FROM users ORDER BY id", false); len(res.Rows) != 3 || res.Rows[0][0] != "a@x.io" {
		t.Fatalf("select: %+v", res)
	}

	tables, err := p.DatabaseSchema(ctx, "demo", "app.db")
	if err != nil || len(tables) != 3 {
		t.Fatalf("schema: %+v %v", tables, err)
	}
	users := tables[slices.IndexFunc(tables, func(x store.TableInfo) bool { return x.Name == "users" })]
	if users.Rows != 3 || len(users.Columns) != 5 || users.Columns[0].PK != 1 || !users.Columns[1].NotNull ||
		users.Columns[2].Default == nil || len(users.Indexes) != 1 || !users.Indexes[0].Unique {
		t.Fatalf("users table: %+v", users)
	}

	rows, err := p.TableRows(ctx, "demo", "app.db", store.RowsRequest{Table: "users", Sort: []store.Sort{{Column: "email", Desc: true}}})
	if err != nil || rows.Total != 3 || !rows.Editable || len(rows.Keys) != 3 {
		t.Fatalf("rows: %+v %v", rows, err)
	}
	first := rows.Rows[2] // sorted descending: a@x.io last
	if first[2] != "2026-01-02 03:04:05" {
		t.Errorf("DATETIME text must come back as stored, got %#v", first[2])
	}
	if b, ok := first[3].(store.Blob); !ok || b.Blob != 4 {
		t.Errorf("blob cell = %#v", first[3])
	}
	if first[4] != "9007199254740993" {
		t.Errorf("an integer too big for JavaScript should be a string, got %#v", first[4])
	}
	if r, _ := p.TableRows(ctx, "demo", "app.db", store.RowsRequest{Table: "users",
		Filters: []store.Filter{{Column: "email", Op: "contains", Value: "50%"}}}); r.Total != 1 {
		t.Errorf("a %% in a contains filter should be literal: %+v", r)
	}
	if r, _ := p.TableRows(ctx, "demo", "app.db", store.RowsRequest{Table: "users", Limit: 1, Offset: 1}); len(r.Rows) != 1 || r.Total != 3 {
		t.Errorf("paging: %+v", r)
	}

	key := rows.Keys[2]
	if _, err := p.EditRows(ctx, "demo", "app.db", store.Mutation{Table: "users", Kind: "update", Keys: []store.RowKey{key},
		Values: map[string]any{"email": "a2@x.io", "joined": nil}}); err != nil {
		t.Fatal(err)
	}
	ins, err := p.EditRows(ctx, "demo", "app.db", store.Mutation{Table: "users", Kind: "insert", Values: map[string]any{"email": "d@x.io", "big": "42"}})
	if err != nil || ins.Rowid != 4 {
		t.Fatalf("insert: %+v %v", ins, err)
	}
	if res := exec("SELECT email, joined, typeof(big) FROM users WHERE id IN (1, 4) ORDER BY id", false); res.Rows[0][0] != "a2@x.io" ||
		res.Rows[0][1] != nil || res.Rows[1][2] != "integer" {
		t.Fatalf("after edits: %+v", res)
	}
	if _, err := p.EditRows(ctx, "demo", "app.db", store.Mutation{Table: "users", Kind: "insert", Values: map[string]any{"email": nil}}); err == nil {
		t.Error("NOT NULL should be enforced")
	}
	if del, err := p.EditRows(ctx, "demo", "app.db", store.Mutation{Table: "users", Kind: "delete", Keys: rows.Keys[:2]}); err != nil || del.Affected != 2 {
		t.Fatalf("delete: %+v %v", del, err)
	}

	// A WITHOUT ROWID table is edited through its primary key.
	tags, _ := p.TableRows(ctx, "demo", "app.db", store.RowsRequest{Table: "tags"})
	if len(tags.Keys) != 2 || tags.Keys[0].Rowid != nil || tags.Keys[0].PK["name"] != "x" {
		t.Fatalf("tags keys: %+v", tags.Keys)
	}
	if _, err := p.EditRows(ctx, "demo", "app.db", store.Mutation{Table: "tags", Kind: "update", Keys: tags.Keys[:1],
		Values: map[string]any{"name": "z"}}); err != nil {
		t.Fatal(err)
	}

	for _, a := range []store.Alter{
		{Kind: "create_table", Table: "events", Columns: []store.ColumnDef{
			{Name: "id", Type: "INTEGER", PK: true}, {Name: "kind", Type: "TEXT", NotNull: true, Default: ptr("signup")}}},
		{Kind: "add_column", Table: "events", Def: &store.ColumnDef{Name: "at", Type: "DATETIME", Default: ptr("CURRENT_TIMESTAMP")}},
		{Kind: "rename_column", Table: "events", Column: "kind", NewName: "type"},
		{Kind: "drop_column", Table: "events", Column: "at"},
		{Kind: "rename_table", Table: "events", NewName: "activity"},
	} {
		if _, err := p.AlterDatabase(ctx, "demo", "app.db", a); err != nil {
			t.Fatalf("%s: %v", a.Kind, err)
		}
	}
	if _, err := p.EditRows(ctx, "demo", "app.db", store.Mutation{Table: "activity", Kind: "insert"}); err != nil {
		t.Fatal(err)
	}
	if res := exec(`SELECT type FROM activity`, false); res.Rows[0][0] != "signup" {
		t.Fatalf("default after rename: %+v", res)
	}
	if _, err := p.AlterDatabase(ctx, "demo", "app.db", store.Alter{Kind: "add_column", Table: "activity",
		Def: &store.ColumnDef{Name: "x", Type: "TEXT); DROP TABLE users; --"}}); err == nil {
		t.Error("a type with SQL in it should be refused")
	}
	if _, err := p.AlterDatabase(ctx, "demo", "app.db", store.Alter{Kind: "drop_table", Table: "activity"}); err != nil {
		t.Fatal(err)
	}

	// Edits from the dashboard are logged.
	logs, _ := p.Logs("demo", 100, "")
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "table editor: updated 1 row(s) in users") ||
		!strings.Contains(joined, "SQL editor ran a write") {
		t.Errorf("edits weren't logged:\n%s", joined)
	}
	// A search returns only the lines that match, ignoring case.
	found, err := p.Logs("demo", 100, "SQL EDITOR")
	if err != nil || len(found) == 0 {
		t.Fatalf("searching the logs: %q, %v", found, err)
	}
	for _, line := range found {
		if !strings.Contains(strings.ToLower(line), "sql editor") {
			t.Errorf("search returned a line without the match: %q", line)
		}
	}
	e.expect("demo", 200, "release 1") // the app kept running throughout
	if _, err := p.ExecSQL(ctx, "demo", "../../sideyard.db", "SELECT 1", false); err == nil {
		t.Error("a path outside DATA_DIR should be refused")
	}
}

func ptr[T any](v T) *T { return &v }

func TestMonitoring(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	p := e.p
	p.CreateProject("demo")
	if _, err := e.deploy("demo"); err != nil {
		t.Fatal(err)
	}
	proj := mustProject(t, p, "demo")
	if h := p.Status(proj).Health; h.Status != "unknown" {
		t.Fatalf("health before any check: %+v", h)
	}
	p.checkHealth()
	if h := p.Status(proj).Health; h.Status != "healthy" {
		t.Fatalf("health: %+v", h)
	}

	// The test app's /healthz fails while app.db contains this marker.
	db := filepath.Join(e.cfg.DataDir, "projects/demo/data/app.db")
	os.WriteFile(db, []byte("BREAK-HEALTH"), 0o644)
	p.checkHealth()
	if h := p.Status(proj).Health; h.Status != "unhealthy" || !strings.Contains(h.Error, "503") {
		t.Fatalf("health with a broken database: %+v", h)
	}
	os.Remove(db)
	p.checkHealth()
	if h := p.Status(proj).Health; h.Status != "healthy" {
		t.Fatalf("health after fixing: %+v", h)
	}
	logs, _ := p.Logs("demo", 100, "")
	if joined := strings.Join(logs, "\n"); !strings.Contains(joined, "health check failing") || !strings.Contains(joined, "passing again") {
		t.Errorf("health changes weren't logged:\n%s", joined)
	}

	os.WriteFile(filepath.Join(e.cfg.DataDir, "projects/demo/data/blob"), make([]byte, 4096), 0o644)
	if n := p.DataSize("demo"); n < 4096 {
		t.Errorf("data size %d", n)
	}

	if err := p.AddDomain("demo", "demo.sideyard-test.invalid"); err != nil {
		t.Fatal(err)
	}
	statuses, err := p.DomainStatus(ctx, "demo")
	if err != nil || len(statuses) != 2 {
		t.Fatalf("domain status: %+v %v", statuses, err)
	}
	for _, s := range statuses {
		switch s.Host {
		case "demo.localhost":
			if s.PointsHere == nil || !*s.PointsHere {
				t.Errorf("%s should point here: %+v", s.Host, s)
			}
		default:
			if s.DNSError == "" {
				t.Errorf("%s should have no DNS record: %+v", s.Host, s)
			}
		}
	}

	// Two seconds apart with one CPU-second used is 50%.
	p.cpuSamples["x"] = cpuSample{nanos: 0, at: time.Now().Add(-2 * time.Second)}
	if pct := p.cpuPercent("x", 1e9); pct < 45 || pct > 55 {
		t.Errorf("cpu percent = %.1f, want about 50", pct)
	}
}
