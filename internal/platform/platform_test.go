package platform

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"sideyard/internal/config"
	"sideyard/internal/proxy"
	"sideyard/internal/runner"
	"sideyard/internal/s3/s3test"
	"sideyard/internal/secrets"
	"sideyard/internal/store"
)

func TestMain(m *testing.M) {
	// Under the systemd runner, the platform runs `<self> sqlite-snapshot`
	// and `<self> sqlite-query` as the project's user. Let the test binary
	// play that part.
	if handled, err := store.RunHelper(os.Args[1:], os.Stdin, os.Stdout); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type testEnv struct {
	*testing.T
	p   *Platform
	cfg config.Config
	st  *store.Store
	px  *proxy.Proxy
	bin string // the test app
}

// newTestEnv boots a Platform in dev mode on free ports with the process
// runner, and builds the test app.
func newTestEnv(t *testing.T, opts ...func(*config.Config)) *testEnv {
	t.Helper()
	return bootTestEnv(t, runner.NewProcess(), t.TempDir(), testApp(t), opts...)
}

func bootTestEnv(t *testing.T, rn runner.Runner, dataDir, bin string, opts ...func(*config.Config)) *testEnv {
	t.Helper()
	cfg := config.Config{
		DataDir: dataDir, Domain: "localhost", DashboardHost: "sideyard.localhost",
		Dev: true, PublicAddr: freeAddr(t), APIAddr: freeAddr(t),
	}
	for _, o := range opts {
		o(&cfg)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "sideyard.db"))
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.LoadOrCreate(filepath.Join(cfg.DataDir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	px := proxy.New(cfg)
	p := New(cfg, st, box, px, rn)
	p.healthTimeout = 5 * time.Second
	p.jobRetryDelay = 10 * time.Millisecond
	if err := p.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := &testEnv{T: t, p: p, cfg: cfg, st: st, px: px, bin: bin}
	t.Cleanup(e.close)
	return e
}

func (e *testEnv) close() {
	e.p.Shutdown()
	e.px.Stop()
	e.st.Close()
}

// testApp builds testdata/app, or uses SIDEYARD_TEST_APP (a prebuilt binary,
// for machines without Go).
func testApp(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("SIDEYARD_TEST_APP"); bin != "" {
		return bin
	}
	bin := filepath.Join(t.TempDir(), "app")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/app").CombinedOutput(); err != nil {
		t.Fatalf("build test app: %v\n%s", err, out)
	}
	return bin
}

func (e *testEnv) deploy(name string) (store.Release, error) {
	e.Helper()
	return e.p.Deploy(context.Background(), name, copyFile(e.T, e.bin, e.cfg.DataDir), "abc123", "test")
}

// get requests path on the project's domain through the proxy.
func (e *testEnv) get(name, path string) (int, string) {
	e.Helper()
	req, _ := http.NewRequest("GET", "http://"+e.cfg.PublicAddr+path, nil)
	req.Host = name + ".localhost"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (e *testEnv) expect(name string, wantStatus int, wantBody string) {
	e.Helper()
	status, body := e.get(name, "/")
	if status != wantStatus || !strings.Contains(body, wantBody) {
		e.Fatalf("got %d %q, want %d containing %q", status, body, wantStatus, wantBody)
	}
}

// TestDeployLifecycle drives a real app binary through the embedded proxy:
// deploy, a failed deploy, variables, rollback, stop and start.
func TestDeployLifecycle(t *testing.T) {
	runLifecycle(newTestEnv(t), "demo")
}

func runLifecycle(e *testEnv, name string) {
	e.Helper()
	ctx := context.Background()
	p := e.p
	if _, _, err := p.CreateProject(name); err != nil {
		e.Fatal(err)
	}
	e.expect(name, 503, "hasn't been deployed")

	rel, err := e.deploy(name)
	if err != nil || rel.Status != store.ReleaseActive || rel.Number != 1 {
		e.Fatalf("first deploy: %+v, %v", rel, err)
	}
	e.expect(name, 200, "release 1 greeting ")

	// Quotes and dollars must reach the app verbatim (the systemd runner
	// passes them through an EnvironmentFile).
	greeting := `hi "there" $HOME \o/`
	if restarted, err := p.UpdateVariables(ctx, name, map[string]string{"GREETING": greeting}, nil); err != nil || !restarted {
		e.Fatalf("set variable: restarted=%v err=%v", restarted, err)
	}
	e.expect(name, 200, "release 1 greeting "+greeting)

	// A release that never becomes healthy must leave the old one serving.
	if _, err := p.UpdateVariables(ctx, name, map[string]string{"FAIL": "1"}, nil); err == nil {
		e.Fatal("restart with FAIL=1 should fail")
	}
	rel, err = e.deploy(name)
	if err == nil || rel.Status != store.ReleaseFailed {
		e.Fatalf("deploy with FAIL=1: %+v, %v", rel, err)
	}
	e.expect(name, 200, "release 1 greeting hi")
	if _, err := p.UpdateVariables(ctx, name, nil, []string{"FAIL"}); err != nil {
		e.Fatal(err)
	}

	if rel, err = e.deploy(name); err != nil || rel.Number != 3 {
		e.Fatalf("third deploy: %+v, %v", rel, err)
	}
	e.expect(name, 200, "release 3")

	if _, err := p.Rollback(ctx, name, 1); err != nil {
		e.Fatal(err)
	}
	e.expect(name, 200, "release 1")

	if err := p.Stop(name); err != nil {
		e.Fatal(err)
	}
	e.expect(name, 503, "is stopped")

	if err := p.Restart(ctx, name); err != nil {
		e.Fatal(err)
	}
	e.expect(name, 200, "release 1")

	// journald writes asynchronously, so give the log a moment.
	var joined string
	for range 20 {
		logs, err := p.Logs(name, 300)
		if err != nil {
			e.Fatal(err)
		}
		joined = strings.Join(logs, "\n")
		if strings.Contains(joined, "[sideyard] release #1: live") && strings.Contains(joined, "refusing to start") {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	e.Fatalf("logs are missing Sideyard events or app output:\n%s", joined)
}

// Routing changes reload the proxy. Long-lived connections (device
// WebSockets) to apps that didn't change must stay open through them.
func TestUpgradedConnectionsSurviveReload(t *testing.T) {
	e := newTestEnv(t)
	if _, _, err := e.p.CreateProject("demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.deploy("demo"); err != nil {
		t.Fatal(err)
	}

	conn, err := net.Dial("tcp", e.cfg.PublicAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /echo HTTP/1.1\r\nHost: demo.localhost\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	echo := func(msg string) error {
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := fmt.Fprintln(conn, msg); err != nil {
			return err
		}
		line, err := br.ReadString('\n')
		if err == nil && strings.TrimSpace(line) != msg {
			err = fmt.Errorf("echoed %q", line)
		}
		return err
	}
	if err := echo("before"); err != nil {
		t.Fatal(err)
	}

	// Another project's change reloads the whole routing table.
	if _, _, err := e.p.CreateProject("other"); err != nil {
		t.Fatal(err)
	}
	if err := e.p.AddDomain("other", "other.example.com"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := echo("after"); err != nil {
		t.Fatalf("connection dropped by an unrelated reload: %v", err)
	}
}

func TestDatabaseImportAndSnapshot(t *testing.T) {
	runDatabase(newTestEnv(t), "demo")
}

func runDatabase(e *testEnv, name string) {
	e.Helper()
	t := e.T
	ctx := context.Background()
	p := e.p
	if _, _, err := p.CreateProject(name); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(e.cfg.DataDir, "projects", name, "data")

	// Importing before the first deploy seeds the database.
	if backup, err := p.ImportDatabase(ctx, name, "app.db", makeDB(t, e.cfg, "v1")); err != nil || backup != "" {
		t.Fatalf("first import: backup=%q err=%v", backup, err)
	}
	if _, err := e.deploy(name); err != nil {
		t.Fatal(err)
	}

	// A running app is stopped, swapped and started again.
	backup, err := p.ImportDatabase(ctx, name, "app.db", makeDB(t, e.cfg, "v2"))
	if err != nil || !strings.HasPrefix(backup, ".sideyard/backups/app-") {
		t.Fatalf("second import: backup=%q err=%v", backup, err)
	}
	e.expect(name, 200, "release 1")
	if got := readNote(t, filepath.Join(data, "app.db")); got != "v2" {
		t.Fatalf("live database has %q, want v2", got)
	}
	if got := readNote(t, filepath.Join(data, backup)); got != "v1" {
		t.Fatalf("backup has %q, want v1", got)
	}

	// An import the app can't run with is undone.
	if _, err := p.ImportDatabase(ctx, name, "app.db", makeDB(t, e.cfg, "BREAK-HEALTH")); err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("bad import should be rolled back, got %v", err)
	}
	e.expect(name, 200, "release 1")
	if got := readNote(t, filepath.Join(data, "app.db")); got != "v2" {
		t.Fatalf("after rollback the database has %q, want v2", got)
	}

	dbs, backups, err := p.Databases(name)
	if err != nil || len(dbs) != 1 || dbs[0].Path != "app.db" || len(backups) == 0 {
		t.Fatalf("Databases: %+v %+v %v", dbs, backups, err)
	}

	snap, cleanup, err := p.SnapshotDatabase(ctx, name, "app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got := readNote(t, snap); got != "v2" {
		t.Fatalf("snapshot has %q, want v2", got)
	}

	for _, bad := range []string{"../escape.db", "/etc/passwd", ".sideyard/backups/x.db"} {
		if _, err := p.ImportDatabase(ctx, name, bad, makeDB(t, e.cfg, "x")); err == nil {
			t.Errorf("import to %q should be refused", bad)
		}
	}
	if _, err := p.ImportDatabase(ctx, name, "app.db", copyFile(t, e.bin, e.cfg.DataDir)); err == nil {
		t.Error("importing a non-SQLite file should be refused")
	}
}

// TestSystemdRunner runs the lifecycle and database tests under the systemd
// runner, then checks what only it provides: a per-project user, resource
// limits, and apps that keep running across a Sideyard restart. It creates
// real users and units, named with its own instance ID so that a real
// Sideyard on the same machine is left alone, and only runs when asked, as
// root on Linux.
// From the repo, `make vm-test` builds the kit; on the VM:
//
//	sudo SIDEYARD_SYSTEMD_TEST=1 SIDEYARD_TEST_APP=$PWD/testapp-amd64 ./platform-amd64.test -test.run Systemd -test.v
//
// Test data goes in SIDEYARD_TEST_DIR (default /var/lib); every directory
// above it must be traversable by other users.
//
// Put the kit somewhere every user can read, such as /opt/sideyard-test,
// because the snapshot helper runs the test binary as the project's user.
func TestSystemdRunner(t *testing.T) {
	if os.Getenv("SIDEYARD_SYSTEMD_TEST") != "1" {
		t.Skip("set SIDEYARD_SYSTEMD_TEST=1 to run (root, Linux with systemd)")
	}
	rn, err := runner.NewSystemd()
	if err != nil {
		t.Fatal(err)
	}
	// Project users must be able to traverse the path, so not a 0700 TempDir.
	base := os.Getenv("SIDEYARD_TEST_DIR")
	if base == "" {
		base = "/var/lib"
	}
	dataDir, err := os.MkdirTemp(base, "sideyard-test-")
	if err != nil {
		t.Fatal(err)
	}
	bin := testApp(t)
	e := bootTestEnv(t, rn, dataDir, bin)
	// This installation's units and users carry its instance ID, so a real
	// Sideyard on the same machine is untouched, including by cleanup.
	instance, _ := e.p.InstanceID()
	t.Cleanup(func() {
		// Whatever a failed run left behind: units, then users, then files.
		exec.Command("systemctl", "stop", "sy-"+instance+"-*").Run()
		if passwd, err := os.ReadFile("/etc/passwd"); err == nil {
			for line := range strings.SplitSeq(string(passwd), "\n") {
				if user, _, _ := strings.Cut(line, ":"); strings.HasPrefix(user, "sy"+instance+"-") {
					exec.Command("userdel", user).Run()
				}
			}
		}
		os.RemoveAll(dataDir)
	})
	runLifecycle(e, "sytest-demo")
	runDatabase(e, "sytest-db")

	proj := mustProject(t, e.p, "sytest-demo")
	if _, uid := e.get("sytest-demo", "/whoami"); uid == "0" || uid == strconv.Itoa(os.Getuid()) {
		t.Fatalf("app runs as uid %s, want the project's own user", uid)
	}
	out, err := exec.Command("systemctl", "show", proj.LiveInstance, "--property=MemoryMax").Output()
	if err != nil || strings.TrimSpace(string(out)) != fmt.Sprintf("MemoryMax=%d", 512<<20) {
		t.Fatalf("MemoryMax: %q %v", out, err)
	}
	// Another project's files are out of reach.
	other := filepath.Join(dataDir, "projects", "sytest-db", "data", "app.db")
	if _, body := e.get("sytest-demo", "/read?path="+other); !strings.Contains(body, "permission denied") {
		t.Fatalf("app could read another project's database: %q", body)
	}

	// The table and SQL editors run as the project's user through the
	// helper, so a write leaves SQLite's files owned by the app.
	if res, err := e.p.ExecSQL(context.Background(), "sytest-db", "app.db", "SELECT body FROM notes", false); err != nil || len(res.Rows) != 1 {
		t.Fatalf("query as the project's user: %+v %v", res, err)
	}
	if _, err := e.p.EditRows(context.Background(), "sytest-db", "app.db", store.Mutation{Table: "notes", Kind: "insert",
		Values: map[string]any{"body": "from the table editor"}}); err != nil {
		t.Fatalf("edit as the project's user: %v", err)
	}
	dbDir := filepath.Join(dataDir, "projects", "sytest-db", "data")
	entries, _ := os.ReadDir(dbDir)
	for _, de := range entries {
		info, _ := de.Info()
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid == 0 && strings.HasPrefix(de.Name(), "app.db") {
			t.Fatalf("%s is owned by root after an edit", de.Name())
		}
	}
	// Logs belong to one installation: another Sideyard on this machine
	// logging for a project with the same ID doesn't show up in ours.
	stranger, _ := runner.NewSystemd()
	if err := stranger.Init(t.TempDir(), "0ther"); err != nil {
		t.Fatal(err)
	}
	dbProj := mustProject(t, e.p, "sytest-db")
	strangerRef := runner.ProjectRef{ID: dbProj.ID, Name: dbProj.Name}
	stranger.Logf(strangerRef, "from another installation")
	for i := 0; ; i++ {
		theirs, _ := stranger.Tail(strangerRef, 10)
		ours, _ := e.p.Logs("sytest-db", 300)
		if strings.Contains(strings.Join(ours, "\n"), "from another installation") {
			t.Fatalf("another installation's log line shows up in ours:\n%s", strings.Join(ours, "\n"))
		}
		if strings.Contains(strings.Join(theirs, "\n"), "from another installation") &&
			strings.Contains(strings.Join(ours, "\n"), "table editor: inserted") {
			break
		}
		if i == 20 {
			t.Fatalf("logs never arrived: theirs %q, ours %q", theirs, ours)
		}
		time.Sleep(250 * time.Millisecond)
	}
	// Jobs reach the sandboxed app on its loopback port.
	if err := e.p.CreateJob("sytest-demo", JobSpec{Name: "report", Schedule: "@daily", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if run := e.runJobIn("sytest-demo", "report"); run.Status != "succeeded" {
		t.Fatalf("job under systemd: %+v", run)
	}
	e.p.checkHealth()
	if h := e.p.Status(mustProject(t, e.p, "sytest-demo")).Health; h.Status != "healthy" {
		t.Fatalf("health under systemd: %+v", h)
	}
	if st := e.p.Status(mustProject(t, e.p, "sytest-demo")); st.MemoryBytes == 0 {
		t.Fatalf("no memory figure from systemd: %+v", st)
	}
	// Backups snapshot every database as its project's user.
	cfg, stop := s3test.FakeServer("backups")
	defer stop()
	if err := e.p.SaveBackupSettings(context.Background(), BackupSettings{Enabled: true, Endpoint: cfg.Endpoint,
		Region: cfg.Region, Bucket: cfg.Bucket, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, IntervalHours: 6, RetentionDays: 7}); err != nil {
		t.Fatal(err)
	}
	if run := e.waitBackup(t); run.Status != "succeeded" || run.Files < 2 {
		t.Fatalf("backup under systemd: %+v", run)
	}

	// Sideyard restarts: the app keeps serving and is adopted, not restarted.
	before := proj.LiveInstance
	e.close()
	e2 := bootTestEnv(t, rn, dataDir, bin)
	e2.expect("sytest-demo", 200, "release 1")
	if after := e2.p.Status(mustProject(t, e2.p, "sytest-demo")).Instance; after != before {
		t.Fatalf("instance changed across a Sideyard restart: %s → %s", before, after)
	}

	// Deleting a project stops its unit and removes its user.
	for _, name := range []string{"sytest-demo", "sytest-db"} {
		if err := e2.p.DeleteProject(name); err != nil {
			t.Fatal(err)
		}
	}
	if out, _ := exec.Command("systemctl", "is-active", before).Output(); strings.TrimSpace(string(out)) == "active" {
		t.Fatalf("%s still active after delete", before)
	}
}

func mustProject(t *testing.T, p *Platform, name string) store.Project {
	t.Helper()
	proj, err := p.Project(name)
	if err != nil {
		t.Fatal(err)
	}
	return proj
}

// makeDB creates a SQLite file holding one note, staged like an upload.
func makeDB(t *testing.T, cfg config.Config, note string) string {
	t.Helper()
	f, err := os.CreateTemp(cfg.DataDir, ".import-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	os.Remove(f.Name())
	db, err := sql.Open("sqlite", "file:"+f.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE notes (body TEXT); INSERT INTO notes VALUES (?)`, note); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}

func readNote(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var body string
	if err := db.QueryRow(`SELECT body FROM notes`).Scan(&body); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return body
}

func TestReservedVariables(t *testing.T) {
	for _, k := range []string{"PORT", "DATA_DIR", "DATABASE_PATH", "SIDEYARD_ANYTHING", "PATH"} {
		if !reservedKey(k) {
			t.Errorf("%s should be reserved", k)
		}
	}
	if reservedKey("STRIPE_SECRET_KEY") {
		t.Error("STRIPE_SECRET_KEY should not be reserved")
	}
}

func TestExpandArgs(t *testing.T) {
	got := expandArgs("serve --http=127.0.0.1:$PORT --dir=${DATA_DIR}/pb_data", []string{"PORT=4000", "DATA_DIR=/d"})
	want := []string{"serve", "--http=127.0.0.1:4000", "--dir=/d/pb_data"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// copyFile stands in for an upload, which Deploy moves into place.
func copyFile(t *testing.T, src, dir string) string {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	return f.Name()
}
