package api

import (
	"crypto/subtle"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"sideyard/internal/platform"
	"sideyard/internal/secrets"
	"sideyard/internal/store"
	"sideyard/internal/version"
)

type releaseView struct {
	Number    int       `json:"number"`
	Commit    string    `json:"commit"`
	Message   string    `json:"message"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	Status    string    `json:"status"`
	Log       string    `json:"log"`
	Pruned    bool      `json:"pruned"`
	CreatedAt time.Time `json:"createdAt"`
}

func viewRelease(r store.Release) releaseView {
	return releaseView{r.Number, r.Commit, r.Message, r.Size, r.SHA256, r.Status, r.Log, r.Pruned, r.CreatedAt}
}

type projectView struct {
	Name          string                 `json:"name"`
	HealthPath    string                 `json:"healthPath"`
	Args          string                 `json:"args"`
	MemoryMB      int                    `json:"memoryMB"`
	CPUPercent    int                    `json:"cpuPercent"`
	DesiredState  string                 `json:"desiredState"`
	DataBytes     int64                  `json:"dataBytes"`
	CreatedAt     time.Time              `json:"createdAt"`
	Domains       []string               `json:"domains"`
	Status        platform.RuntimeStatus `json:"status"`
	ActiveRelease *releaseView           `json:"activeRelease"`
	Releases      []releaseView          `json:"releases,omitempty"`
}

func (s *Server) viewProject(proj store.Project, domains []string) projectView {
	v := projectView{
		Name: proj.Name, HealthPath: proj.HealthPath, Args: proj.Args, MemoryMB: proj.MemoryMB, CPUPercent: proj.CPUPercent,
		DesiredState: proj.DesiredState, DataBytes: s.p.DataSize(proj.Name),
		CreatedAt: proj.CreatedAt, Domains: domains, Status: s.p.Status(proj),
	}
	if v.Domains == nil {
		v.Domains = []string{}
	}
	if proj.ActiveReleaseID != 0 {
		if rel, err := s.store.Release(proj.ActiveReleaseID); err == nil {
			rv := viewRelease(rel)
			rv.Log = ""
			v.ActiveRelease = &rv
		}
	}
	return v
}

func (s *Server) server(w http.ResponseWriter, r *http.Request) {
	cfg := s.p.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"domain":        cfg.Domain,
		"dashboardHost": cfg.DashboardHost,
		"dev":           cfg.Dev,
		"publicAddr":    cfg.PublicAddr,
		"runner":        s.p.RunnerName(),
		"version":       version.Version,
		"arch":          runtime.GOARCH,
		"resources":     s.p.Server(),
	})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.p.Projects()
	if err != nil {
		writeErr(w, err)
		return
	}
	domains, err := s.p.Domains()
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]projectView, 0, len(projects))
	for _, proj := range projects {
		out = append(out, s.viewProject(proj, domains[proj.ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	proj, token, err := s.p.CreateProject(body.Name)
	if err != nil {
		writeErr(w, err)
		return
	}
	domains, _ := s.p.Domains()
	writeJSON(w, http.StatusCreated, map[string]any{
		"project":     s.viewProject(proj, domains[proj.ID]),
		"deployToken": token,
	})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	proj, err := s.p.Project(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	domains, err := s.p.Domains()
	if err != nil {
		writeErr(w, err)
		return
	}
	releases, err := s.p.Releases(proj, 30)
	if err != nil {
		writeErr(w, err)
		return
	}
	v := s.viewProject(proj, domains[proj.ID])
	v.Releases = []releaseView{}
	for _, rel := range releases {
		v.Releases = append(v.Releases, viewRelease(rel))
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := r.PathValue("name")
	if body.Confirm != name {
		writeError(w, http.StatusBadRequest, "type the project name to confirm")
		return
	}
	if err := s.p.DeleteProject(name); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) restart(w http.ResponseWriter, r *http.Request) {
	if err := s.p.Restart(r.Context(), r.PathValue("name")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	if err := s.p.Stop(r.PathValue("name")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad release number")
		return
	}
	rel, err := s.p.Rollback(r.Context(), r.PathValue("name"), number)
	if err != nil && rel.Number == 0 {
		writeErr(w, err)
		return
	}
	writeRelease(w, rel, err)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines <= 0 || lines > 5000 {
		lines = 500
	}
	out, err := s.p.Logs(r.PathValue("name"), lines)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"lines": out})
}

func (s *Server) listVariables(w http.ResponseWriter, r *http.Request) {
	vars, err := s.p.Variables(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	builtins, err := s.p.Builtins(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"builtins": builtins, "variables": vars})
}

func (s *Server) revealVariable(w http.ResponseWriter, r *http.Request) {
	value, err := s.p.RevealVariable(r.PathValue("name"), r.PathValue("key"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": r.PathValue("key"), "value": value})
}

func (s *Server) updateVariables(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Set    map[string]string `json:"set"`
		Delete []string          `json:"delete"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	restarted, err := s.p.UpdateVariables(r.Context(), r.PathValue("name"), body.Set, body.Delete)
	if err != nil && !restarted {
		writeErr(w, err)
		return
	}
	resp := map[string]any{"restarted": restarted}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var body platform.Settings
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.p.UpdateSettings(r.Context(), r.PathValue("name"), body); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addDomain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host string `json:"host"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.p.AddDomain(r.PathValue("name"), body.Host); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeDomain(w http.ResponseWriter, r *http.Request) {
	if err := s.p.RemoveDomain(r.PathValue("name"), r.PathValue("host")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) regenerateToken(w http.ResponseWriter, r *http.Request) {
	token, err := s.p.RegenerateToken(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deployToken": token})
}

// deploy accepts a multipart upload with a "binary" file and optional
// "commit" and "message" fields. It responds once the release is live (200)
// or has failed (422), so CI fails when the deploy does.
func (s *Server) deploy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	proj, err := s.p.Project(name)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !s.deployAllowed(r, proj) {
		writeError(w, http.StatusUnauthorized, "invalid deploy token")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data with a \"binary\" file field")
		return
	}
	tmpDir := filepath.Join(s.p.Config().DataDir, "projects", name, "releases")
	var upload, commit, message string
	defer func() {
		if upload != "" {
			os.Remove(upload) // no-op once Deploy has moved it
		}
	}()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "reading upload: "+err.Error())
			return
		}
		switch part.FormName() {
		case "binary":
			if upload, err = saveUpload(tmpDir, part); err != nil {
				writeError(w, http.StatusBadRequest, "saving upload: "+err.Error())
				return
			}
		case "commit":
			commit = readField(part)
		case "message":
			message = readField(part)
		}
		part.Close()
	}
	if upload == "" {
		writeError(w, http.StatusBadRequest, "missing \"binary\" file field")
		return
	}
	rel, err := s.p.Deploy(r.Context(), name, upload, commit, message)
	if err != nil && rel.Number == 0 {
		writeErr(w, err)
		return
	}
	writeRelease(w, rel, err)
}

func (s *Server) deployAllowed(r *http.Request, proj store.Project) bool {
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		got := secrets.Hash(strings.TrimSpace(token))
		return subtle.ConstantTimeCompare([]byte(got), []byte(proj.DeployTokenHash)) == 1
	}
	return s.hasSession(r)
}

func writeRelease(w http.ResponseWriter, rel store.Release, err error) {
	resp := map[string]any{"release": viewRelease(rel)}
	status := http.StatusOK
	if err != nil {
		resp["error"] = err.Error()
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, resp)
}

func saveUpload(dir string, part *multipart.Part) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, part); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func readField(part *multipart.Part) string {
	b, _ := io.ReadAll(io.LimitReader(part, 4096))
	return strings.TrimSpace(string(b))
}

func (s *Server) domainStatus(w http.ResponseWriter, r *http.Request) {
	statuses, err := s.p.DomainStatus(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statuses)
}
