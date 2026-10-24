// Package api serves the dashboard and the JSON API behind it, plus the
// deploy endpoint CI uploads binaries to.
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"

	"sideyard/internal/platform"
	"sideyard/internal/store"
)

type Server struct {
	p       *platform.Platform
	store   *store.Store
	web     fs.FS
	secure  bool // set the Secure flag on cookies
	limiter *loginLimiter
	tickets *deployTickets
}

func New(p *platform.Platform, st *store.Store, web fs.FS) *Server {
	return &Server{p: p, store: st, web: web, secure: !p.Config().Dev, limiter: newLoginLimiter(), tickets: newDeployTickets()}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	mux.HandleFunc("GET /api/session", s.session)
	// Authenticates with the project's deploy token, a ticket from the deploy
	// MCP tool, or a dashboard session.
	mux.HandleFunc("POST /api/projects/{name}/deploys", s.deploy)
	// Called by apps, with their SIDEYARD_TOKEN.
	mux.HandleFunc("POST /api/projects/{name}/email/send", s.appSendEmail)
	// Called by agents, with an access token from the MCP page.
	mux.Handle("/mcp", s.mcpHandler())

	auth := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireSession(h)) }
	auth("GET /api/server", s.server)
	auth("GET /api/projects", s.listProjects)
	auth("POST /api/projects", s.createProject)
	auth("GET /api/projects/{name}", s.getProject)
	auth("DELETE /api/projects/{name}", s.deleteProject)
	auth("POST /api/projects/{name}/restart", s.restart)
	auth("POST /api/projects/{name}/stop", s.stop)
	auth("POST /api/projects/{name}/releases/{number}/activate", s.rollback)
	auth("GET /api/projects/{name}/logs", s.logs)
	auth("GET /api/projects/{name}/variables", s.listVariables)
	auth("GET /api/projects/{name}/variables/{key}", s.revealVariable)
	auth("PUT /api/projects/{name}/variables", s.updateVariables)
	auth("PUT /api/projects/{name}/settings", s.updateSettings)
	auth("POST /api/projects/{name}/domains", s.addDomain)
	auth("DELETE /api/projects/{name}/domains/{host}", s.removeDomain)
	auth("POST /api/projects/{name}/token", s.regenerateToken)
	auth("GET /api/projects/{name}/databases", s.listDatabases)
	auth("GET /api/projects/{name}/databases/download", s.downloadDatabase)
	auth("POST /api/projects/{name}/databases/import", s.importDatabase)
	auth("GET /api/projects/{name}/db/schema", s.dbSchema)
	auth("POST /api/projects/{name}/db/rows", s.dbRows)
	auth("POST /api/projects/{name}/db/edit", s.dbEdit)
	auth("POST /api/projects/{name}/db/alter", s.dbAlter)
	auth("POST /api/projects/{name}/db/sql", s.dbSQL)
	auth("GET /api/projects/{name}/queries", s.savedQueries)
	auth("PUT /api/projects/{name}/queries/{id}", s.saveQuery)
	auth("DELETE /api/projects/{name}/queries/{id}", s.deleteQuery)
	auth("GET /api/projects/{name}/metrics", s.projectMetrics)
	auth("GET /api/server/metrics", s.serverMetrics)
	auth("GET /api/system", s.system)
	auth("GET /api/update", s.updateStatus)
	auth("POST /api/update/check", s.checkForUpdate)
	auth("POST /api/update/install", s.installUpdate)
	auth("PUT /api/update/settings", s.saveUpdateSettings)
	auth("GET /api/projects/{name}/databases/remote", s.remoteBackups)
	auth("GET /api/projects/{name}/databases/remote/download", s.downloadRemoteBackup)
	auth("POST /api/projects/{name}/databases/restore", s.restoreBackup)
	auth("GET /api/projects/{name}/domains/status", s.domainStatus)

	auth("GET /api/projects/{name}/storage", s.getStorage)
	auth("PUT /api/projects/{name}/storage", s.connectStorage)
	auth("DELETE /api/projects/{name}/storage", s.disconnectStorage)
	auth("GET /api/projects/{name}/storage/objects", s.listObjects)
	auth("DELETE /api/projects/{name}/storage/objects", s.deleteObject)
	auth("GET /api/projects/{name}/storage/download", s.downloadObject)
	auth("POST /api/projects/{name}/storage/upload", s.uploadObject)
	auth("GET /api/projects/{name}/storage/usage", s.storageUsage)

	auth("GET /api/projects/{name}/jobs", s.listJobs)
	auth("POST /api/projects/{name}/jobs", s.createJob)
	auth("PUT /api/projects/{name}/jobs/{job}", s.updateJob)
	auth("DELETE /api/projects/{name}/jobs/{job}", s.deleteJob)
	auth("POST /api/projects/{name}/jobs/{job}/run", s.runJob)
	auth("GET /api/projects/{name}/jobs/{job}/runs", s.jobRuns)

	auth("GET /api/projects/{name}/email", s.projectEmail)
	auth("PUT /api/projects/{name}/email", s.setProjectSender)
	auth("POST /api/projects/{name}/email/test", s.projectTestEmail)

	auth("GET /api/settings", s.getSettings)
	auth("PUT /api/settings/backups", s.saveBackupSettings)
	auth("POST /api/settings/backups/run", s.runBackup)
	auth("GET /api/settings/backups/runs", s.backupRuns)
	auth("PUT /api/settings/email", s.saveEmailSettings)
	auth("POST /api/settings/email/test", s.testEmail)
	auth("POST /api/settings/password", s.changePassword)
	auth("POST /api/settings/master-key", s.downloadMasterKey)

	auth("GET /api/mcp", s.mcpOverview)
	auth("POST /api/mcp/tokens", s.createAccessToken)
	auth("DELETE /api/mcp/tokens/{id}", s.revokeAccessToken)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})

	mux.HandleFunc("/", s.dashboard)

	// Rejects cross-site browser requests that change state.
	return http.NewCrossOriginProtection().Handler(mux)
}

// dashboard serves the embedded React build, falling back to index.html.
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if _, err := fs.Stat(s.web, name); err != nil {
		name = "index.html"
	}
	if _, err := fs.Stat(s.web, "index.html"); err != nil {
		http.Error(w, "Dashboard not built. Run `make web` and rebuild Sideyard.", http.StatusServiceUnavailable)
		return
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, s.web, name)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeErr maps platform errors to HTTP statuses.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, platform.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, platform.ErrConflict):
		writeError(w, http.StatusConflict, strings.TrimPrefix(err.Error(), platform.ErrConflict.Error()+": "))
	case errors.Is(err, platform.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, platform.ErrValidation), errors.Is(err, platform.ErrNoRelease):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), platform.ErrValidation.Error()+": "))
	default:
		log.Printf("api: %v", err)
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}
