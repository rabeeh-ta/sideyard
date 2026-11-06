package api

import (
	"html/template"
	"net/http"
	"strings"

	"sideyard/internal/platform"
)

func (s *Server) oauthProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := s.p.OAuthProviders()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
}

func (s *Server) saveOAuthProvider(w http.ResponseWriter, r *http.Request) {
	var in platform.OAuthProviderSettings
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.p.SaveOAuthProvider(r.PathValue("provider"), in); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testOAuthProvider starts a sign-in that ends on a Sideyard page saying
// who signed in.
func (s *Server) testOAuthProvider(w http.ResponseWriter, r *http.Request) {
	start, err := s.p.StartTestSignIn(r.PathValue("provider"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": start.URL})
}

func (s *Server) projectSignIns(w http.ResponseWriter, r *http.Request) {
	v, err := s.p.ProjectSignIns(r.PathValue("name"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// appAuthorized checks the app's SIDEYARD_TOKEN, answering 401 if it's wrong.
func (s *Server) appAuthorized(w http.ResponseWriter, r *http.Request) bool {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !s.p.AppAuthorized(r.PathValue("name"), strings.TrimSpace(token)) {
		writeError(w, http.StatusUnauthorized, "invalid SIDEYARD_TOKEN")
		return false
	}
	return true
}

// appStartSignIn is the API apps call to begin a sign-in:
//
//	POST $SIDEYARD_API_URL/api/projects/$SIDEYARD_PROJECT/oauth/start
//	Authorization: Bearer $SIDEYARD_TOKEN
//	{"provider": "google", "redirect": "https://app.example.com/auth/callback"}
//	→ {"url": "<send the browser here>", "state": "<keep in a cookie>"}
func (s *Server) appStartSignIn(w http.ResponseWriter, r *http.Request) {
	if !s.appAuthorized(w, r) {
		return
	}
	var body struct {
		Provider string `json:"provider"`
		Redirect string `json:"redirect"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	start, err := s.p.StartSignIn(r.PathValue("name"), body.Provider, body.Redirect)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, start)
}

// appExchangeSignIn trades the code the browser came back with for who
// signed in:
//
//	POST $SIDEYARD_API_URL/api/projects/$SIDEYARD_PROJECT/oauth/exchange
//	Authorization: Bearer $SIDEYARD_TOKEN
//	{"code": "…", "state": "<from the cookie>"}
//	→ {"provider": "google", "id": "…", "email": "…", "emailVerified": true, "name": "…", "avatarUrl": "…"}
func (s *Server) appExchangeSignIn(w http.ResponseWriter, r *http.Request) {
	if !s.appAuthorized(w, r) {
		return
	}
	var body struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	id, err := s.p.ExchangeSignIn(r.PathValue("name"), body.Code, body.State)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, id)
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res := s.p.FinishSignIn(r.Context(), r.PathValue("provider"), q.Get("state"), q.Get("code"), q.Get("error"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !res.Test {
		http.Redirect(w, r, res.Redirect, http.StatusFound)
		return
	}
	status := http.StatusOK
	if res.Err != nil {
		status = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	signInPage.Execute(w, res)
}

var signInPage = template.Must(template.New("").Parse(`<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sideyard sign-in</title>
<style>
body { font: 15px/1.5 system-ui, sans-serif; margin: 0; min-height: 100vh; display: grid; place-items: center; background: #f6f6f4; color: #1c1c1a }
main { background: #fff; border: 1px solid #e3e3df; border-radius: 10px; padding: 28px 32px; max-width: 420px; margin: 16px }
h1 { font-size: 18px; margin: 0 0 8px } p { margin: 6px 0; color: #55554f } code { font-size: 13px }
img { width: 48px; height: 48px; border-radius: 50%; float: right; margin-left: 12px }
@media (prefers-color-scheme: dark) { body { background: #151514; color: #ededea } main { background: #1d1d1b; border-color: #2e2e2b } p { color: #a5a59f } }
</style>
<main>
{{if .Err}}
<h1>Sign-in didn't work</h1>
<p>{{.Err}}</p>
{{else}}
{{with .Identity}}{{if .AvatarURL}}<img src="{{.AvatarURL}}" alt="">{{end}}
<h1>Signed in as {{if .Name}}{{.Name}}{{else}}{{.Email}}{{end}}</h1>
<p>{{.Email}}{{if not .EmailVerified}} (not verified){{end}}</p>
<p><code>{{.Provider}}:{{.ID}}</code></p>
<p>This provider is set up correctly: apps on this server can sign people in with it. You can close this tab.</p>
{{end}}
{{end}}
</main>
`))
