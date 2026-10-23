package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sideyard/internal/api"
	"sideyard/internal/config"
	"sideyard/internal/mail/mailtest"
	"sideyard/internal/platform"
	"sideyard/internal/proxy"
	"sideyard/internal/runner"
	"sideyard/internal/s3/s3test"
	"sideyard/internal/secrets"
	"sideyard/internal/store"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (c *client) do(method, path string, body any, wantStatus int) []byte {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("%s %s: got %d %s, want %d", method, path, resp.StatusCode, out, wantStatus)
	}
	return out
}

// TestAPI drives the HTTP API end to end: sign in, deploy over multipart,
// the email API apps call, jobs, and that settings never echo secrets.
func TestAPI(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	cfg := config.Config{DataDir: dataDir, Domain: "localhost", DashboardHost: "sideyard.localhost",
		Dev: true, PublicAddr: freeAddr(t), APIAddr: freeAddr(t)}
	st, err := store.Open(filepath.Join(dataDir, "sideyard.db"))
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secrets.LoadOrCreate(filepath.Join(dataDir, "master.key"))
	password, err := api.EnsurePassword(st)
	if err != nil {
		t.Fatal(err)
	}
	px := proxy.New(cfg)
	p := platform.New(cfg, st, box, px, runner.NewProcess())
	if err := p.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(p, st, os.DirFS(t.TempDir())).Handler())
	t.Cleanup(func() {
		srv.Close()
		p.Shutdown()
		px.Stop()
		st.Close()
	})
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, base: srv.URL, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}

	c.do("GET", "/api/projects", nil, 401)
	c.do("POST", "/api/login", map[string]string{"password": "wrong"}, 401)
	c.do("POST", "/api/login", map[string]string{"password": password}, 200)

	var created struct {
		DeployToken string `json:"deployToken"`
	}
	json.Unmarshal(c.do("POST", "/api/projects", map[string]string{"name": "demo"}, 201), &created)

	// The System page and updates. A local build has no release source.
	var sys platform.System
	json.Unmarshal(c.do("GET", "/api/system", nil, 200), &sys)
	if len(sys.Apps) != 1 || sys.Apps[0].State != "not-deployed" || len(sys.Mounts) == 0 || sys.Sideyard.Version != "dev" {
		t.Fatalf("system: %+v", sys)
	}
	c.do("GET", "/api/server/metrics?range=6h", nil, 200)
	c.do("GET", "/api/projects/demo/metrics?range=24h", nil, 200)

	// SQL Editor queries are kept per project; saving one again replaces it
	// in place.
	c.do("PUT", "/api/projects/demo/queries/q-1", map[string]string{"name": "First", "sql": "SELECT 1"}, 204)
	c.do("PUT", "/api/projects/demo/queries/q-2", map[string]string{"name": "Second", "sql": "SELECT 2"}, 204)
	c.do("PUT", "/api/projects/demo/queries/q-1", map[string]string{"name": "First", "sql": "SELECT 11"}, 204)
	c.do("PUT", "/api/projects/demo/queries/bad%20id", map[string]string{"name": "x"}, 400)
	c.do("PUT", "/api/projects/nope/queries/q-3", map[string]string{"name": "x"}, 404)
	var queries []platform.SavedQuery
	json.Unmarshal(c.do("GET", "/api/projects/demo/queries", nil, 200), &queries)
	if len(queries) != 2 || queries[0].ID != "q-2" || queries[1].SQL != "SELECT 11" {
		t.Fatalf("saved queries: %+v", queries)
	}
	c.do("DELETE", "/api/projects/demo/queries/q-2", nil, 204)
	json.Unmarshal(c.do("GET", "/api/projects/demo/queries", nil, 200), &queries)
	if len(queries) != 1 || queries[0].ID != "q-1" {
		t.Fatalf("saved queries after a delete: %+v", queries)
	}
	var upd platform.UpdateStatus
	json.Unmarshal(c.do("GET", "/api/update", nil, 200), &upd)
	if upd.Current != "dev" || upd.Source != "" || upd.State != "idle" {
		t.Fatalf("update status: %+v", upd)
	}
	c.do("POST", "/api/update/install", nil, 400)
	json.Unmarshal(c.do("PUT", "/api/update/settings", map[string]bool{"autoInstall": true}, 200), &upd)
	if !upd.AutoInstall {
		t.Fatal("automatic updates not saved")
	}

	// Deploy over HTTP, as CI does.
	bin := filepath.Join(t.TempDir(), "app")
	if out, err := exec.Command("go", "build", "-o", bin, "../platform/testdata/app").CombinedOutput(); err != nil {
		t.Fatalf("build test app: %v\n%s", err, out)
	}
	deploy := func(token string) int {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("binary", "app")
		b, _ := os.ReadFile(bin)
		fw.Write(b)
		mw.WriteField("commit", "abc123")
		mw.Close()
		req, _ := http.NewRequest("POST", srv.URL+"/api/projects/demo/deploys", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := deploy("sy_wrong"); code != 401 {
		t.Fatalf("deploy with a wrong token: %d", code)
	}
	if code := deploy(created.DeployToken); code != 200 {
		t.Fatalf("deploy: %d", code)
	}
	token := appGet(t, cfg, "/env?key=SIDEYARD_TOKEN")

	// Email: apps authenticate with SIDEYARD_TOKEN.
	smtp, _ := mailtest.Start()
	defer smtp.Close()
	sc := smtp.Config()
	c.do("PUT", "/api/settings/email", map[string]any{"enabled": true, "host": sc.Host, "port": sc.Port, "security": "none",
		"username": "", "password": "smtp-secret-value", "from": "App <app@example.com>"}, 204)
	send := func(tok string) int {
		b, _ := json.Marshal(map[string]any{"to": []string{"a@example.org"}, "subject": "Hi", "text": "Hello"})
		req, _ := http.NewRequest("POST", srv.URL+"/api/projects/demo/email/send", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := send("syt_wrong"); code != 401 {
		t.Fatalf("email with a wrong token: %d", code)
	}
	if code := send(token); code != 200 || len(smtp.Messages()) != 1 {
		t.Fatalf("email: %d, %d delivered", code, len(smtp.Messages()))
	}

	// Jobs through the API.
	c.do("POST", "/api/projects/demo/jobs", map[string]any{"name": "report", "schedule": "@daily", "enabled": true}, 201)
	c.do("POST", "/api/projects/demo/jobs", map[string]any{"name": "report", "schedule": "@daily"}, 409)
	c.do("POST", "/api/projects/demo/jobs/report/run", nil, 202)
	var runs []platform.RunView
	for range 100 {
		json.Unmarshal(c.do("GET", "/api/projects/demo/jobs/report/runs", nil, 200), &runs)
		if len(runs) == 1 && runs[0].Status != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(runs) != 1 || runs[0].Status != "succeeded" {
		t.Fatalf("job runs: %+v", runs)
	}

	// Secrets go in, never out.
	s3cfg, stop := s3test.FakeServer("backups")
	defer stop()
	c.do("PUT", "/api/settings/backups", map[string]any{"enabled": true, "endpoint": s3cfg.Endpoint, "region": s3cfg.Region,
		"bucket": s3cfg.Bucket, "accessKey": s3cfg.AccessKey, "secretKey": s3cfg.SecretKey, "intervalHours": 6, "retentionDays": 7}, 204)
	settings := string(c.do("GET", "/api/settings", nil, 200))
	for _, secret := range []string{s3cfg.SecretKey, "smtp-secret-value", "sealed"} {
		if strings.Contains(settings, secret) {
			t.Errorf("GET /api/settings leaks %q: %s", secret, settings)
		}
	}
	if !strings.Contains(settings, `"hasSecret":true`) || !strings.Contains(settings, `"hasPassword":true`) {
		t.Errorf("settings don't say secrets are set: %s", settings)
	}

	// The master key needs the password again.
	c.do("POST", "/api/settings/master-key", map[string]string{"password": "nope"}, 403)
	if key := c.do("POST", "/api/settings/master-key", map[string]string{"password": password}, 200); len(key) != 32 {
		t.Fatalf("master key is %d bytes", len(key))
	}

	// Changing the password keeps this browser signed in, with the new one.
	c.do("POST", "/api/settings/password", map[string]string{"current": password, "new": "short"}, 400)
	c.do("POST", "/api/settings/password", map[string]string{"current": password, "new": "a-much-longer-password"}, 204)
	c.do("GET", "/api/projects", nil, 200)
	c.do("POST", "/api/login", map[string]string{"password": password}, 401)
}

// appGet requests a path on the demo app through the proxy.
func appGet(t *testing.T, cfg config.Config, path string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+cfg.PublicAddr+path, nil)
	req.Host = "demo.localhost"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
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
