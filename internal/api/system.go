package api

import (
	"net/http"

	"sideyard/internal/platform"
)

// projectMetrics and serverMetrics take ?range=1h (up to 24h).
func (s *Server) projectMetrics(w http.ResponseWriter, r *http.Request) {
	points, err := s.p.Metrics(r.PathValue("name"), platform.MetricsRange(r.URL.Query().Get("range")))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, points)
}

func (s *Server) serverMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.p.ServerMetrics(platform.MetricsRange(r.URL.Query().Get("range"))))
}

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	sys, err := s.p.System()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sys)
}

func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.p.UpdateStatus())
}

func (s *Server) checkForUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.p.CheckForUpdate(r.Context()))
}

// installUpdate starts the install and answers at once; the dashboard
// follows it through updateStatus, then waits out the restart. The body
// may name the version being installed, {"version": "v1.2.3"}.
func (s *Server) installUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version string `json:"version"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, &body) {
		return
	}
	if err := s.p.InstallUpdate(body.Version); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.p.UpdateStatus())
}

func (s *Server) saveUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AutoInstall bool `json:"autoInstall"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.p.SetAutoUpdate(body.AutoInstall); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.p.UpdateStatus())
}
