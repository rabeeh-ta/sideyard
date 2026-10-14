package api

import (
	"net/http"
	"os"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"

	"sideyard/internal/platform"
)

// getSettings returns the platform settings. Secrets are never included,
// only whether they're set.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	backups, err := s.p.BackupSettings()
	if err != nil {
		writeErr(w, err)
		return
	}
	email, err := s.p.EmailSettings()
	if err != nil {
		writeErr(w, err)
		return
	}
	cfg := s.p.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"backups": backups,
		"email":   email,
		"server": map[string]any{
			"domain":        cfg.Domain,
			"dashboardHost": cfg.DashboardHost,
			"dataDir":       cfg.DataDir,
			"runner":        s.p.RunnerName(),
			"dev":           cfg.Dev,
		},
	})
}

func (s *Server) saveBackupSettings(w http.ResponseWriter, r *http.Request) {
	var in platform.BackupSettings
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.p.SaveBackupSettings(r.Context(), in); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runBackup(w http.ResponseWriter, r *http.Request) {
	run, err := s.p.RunBackup("manual")
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

func (s *Server) backupRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.p.BackupRuns(20)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) saveEmailSettings(w http.ResponseWriter, r *http.Request) {
	var in platform.EmailSettings
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.p.SaveEmailSettings(r.Context(), in); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To string `json:"to"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.p.SendTestEmail(r.Context(), body.To); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkPassword verifies the admin password for sensitive actions, with
// the same limits as signing in.
func (s *Server) checkPassword(w http.ResponseWriter, r *http.Request, password string) bool {
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts; try again in a few minutes")
		return false
	}
	hash, err := s.store.Setting(PasswordSetting)
	if err != nil {
		writeErr(w, err)
		return false
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		s.limiter.fail(ip)
		writeError(w, http.StatusForbidden, "wrong password")
		return false
	}
	return true
}

// changePassword signs out every session, then signs this browser back in.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := CheckNewPassword(body.New); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.checkPassword(w, r, body.Current) {
		return
	}
	if err := SetPassword(s.store, body.New); err != nil {
		writeErr(w, err)
		return
	}
	if err := s.startSession(w); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// downloadMasterKey hands out the key that decrypts every variable and
// saved credential, for safekeeping off the server. It asks for the
// password again.
func (s *Server) downloadMasterKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) || !s.checkPassword(w, r, body.Password) {
		return
	}
	key, err := os.ReadFile(filepath.Join(s.p.Config().DataDir, "master.key"))
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="sideyard-master.key"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(key)
}
