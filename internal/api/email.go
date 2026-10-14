package api

import (
	"net/http"
	"strings"

	"sideyard/internal/platform"
)

func (s *Server) projectEmail(w http.ResponseWriter, r *http.Request) {
	pe, err := s.p.ProjectEmail(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pe)
}

func (s *Server) setProjectSender(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Sender string `json:"sender"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.p.SetProjectSender(r.PathValue("name"), body.Sender); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// projectTestEmail sends as the project, so it shows in its delivery log.
func (s *Server) projectTestEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To string `json:"to"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	name := r.PathValue("name")
	err := s.p.SendEmail(r.Context(), name, platform.EmailRequest{
		To:      []string{body.To},
		Subject: "Test email from " + name,
		Text:    "This is a test email sent by Sideyard as " + name + ". Your app's email will look like this.",
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// appSendEmail is the API apps call to send email:
//
//	POST $SIDEYARD_API_URL/api/projects/$SIDEYARD_PROJECT/email/send
//	Authorization: Bearer $SIDEYARD_TOKEN
//	{"to": ["a@example.com"], "subject": "…", "text": "…", "html": "…"}
func (s *Server) appSendEmail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !s.p.AppAuthorized(name, strings.TrimSpace(token)) {
		writeError(w, http.StatusUnauthorized, "invalid SIDEYARD_TOKEN")
		return
	}
	var req platform.EmailRequest
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.p.SendEmail(r.Context(), name, req); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}
