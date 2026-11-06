package platform

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeProvider is an OAuth server standing in for Google and GitHub: it
// accepts the code "good" when the PKCE verifier matches the challenge the
// sign-in started with.
type fakeProvider struct {
	*httptest.Server
	mu         sync.Mutex
	challenges map[string]string // code → challenge; the test plays the browser
}

func startFakeProvider(t *testing.T) *fakeProvider {
	f := &fakeProvider{challenges: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		challenge, ok := f.challenges[r.PostForm.Get("code")]
		delete(f.challenges, r.PostForm.Get("code"))
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.PostForm.Get("client_secret") != "shh" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
	})
	respond := func(body any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer tok" {
				http.Error(w, "no token", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(body)
		}
	}
	mux.HandleFunc("GET /userinfo", respond(map[string]any{"sub": "g-1", "email": "ada@example.com",
		"email_verified": true, "name": "Ada", "picture": "https://example.com/ada.png"}))
	mux.HandleFunc("GET /user", respond(map[string]any{"id": 42, "login": "grace", "avatar_url": "https://example.com/g.png"}))
	mux.HandleFunc("GET /user/emails", respond([]map[string]any{
		{"email": "old@example.com", "primary": false, "verified": true},
		{"email": "grace@example.com", "primary": true, "verified": true},
	}))
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)

	saved := oauthProviders
	oauthProviders = map[string]*oauthProvider{}
	for id, prov := range saved {
		fake := *prov
		fake.authURL, fake.tokenURL = f.URL+"/auth", f.URL+"/token"
		fake.apiURL = f.URL
		if id == "google" {
			fake.apiURL = f.URL + "/userinfo"
		}
		oauthProviders[id] = &fake
	}
	t.Cleanup(func() { oauthProviders = saved })
	return f
}

// authorize plays the browser at the provider: it checks the URL Sideyard
// sent it to and returns the code the provider would hand back.
func (f *fakeProvider) authorize(t *testing.T, start SignInStart, wantRedirect string) string {
	t.Helper()
	u, err := url.Parse(start.URL)
	if err != nil || !strings.HasPrefix(start.URL, f.URL+"/auth?") {
		t.Fatalf("sign-in URL %q", start.URL)
	}
	q := u.Query()
	if q.Get("state") != start.State || q.Get("redirect_uri") != wantRedirect || q.Get("client_id") != "client" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("sign-in URL parameters: %v", q)
	}
	code := "good-" + start.State
	f.mu.Lock()
	f.challenges[code] = q.Get("code_challenge")
	f.mu.Unlock()
	return code
}

func TestSignIn(t *testing.T) {
	e := newTestEnv(t)
	p := e.p
	ctx := context.Background()
	p.CreateProject("demo")
	p.CreateProject("other")
	f := startFakeProvider(t)
	appURL := "http://demo.localhost:8080/auth/callback?next=%2Fnotes"

	if _, err := p.StartSignIn("demo", "google", appURL); err == nil {
		t.Fatal("signing in before Google is set up should fail")
	}
	if err := p.SaveOAuthProvider("google", OAuthProviderSettings{Enabled: true, ClientID: "client"}); err == nil {
		t.Fatal("enabling without a secret should fail")
	}
	for _, id := range []string{"google", "github"} {
		if err := p.SaveOAuthProvider(id, OAuthProviderSettings{Enabled: true, ClientID: " client ", ClientSecret: "shh"}); err != nil {
			t.Fatal(err)
		}
	}
	// Saving again without the secret keeps it.
	if err := p.SaveOAuthProvider("google", OAuthProviderSettings{Enabled: true, ClientID: "client"}); err != nil {
		t.Fatal(err)
	}
	views, err := p.OAuthProviders()
	if err != nil || len(views) != 2 || views[0].ID != "google" || !views[0].HasSecret || views[0].ClientSecret != "" ||
		views[0].ClientID != "client" || views[0].CallbackURL != "http://sideyard.localhost:"+portOf(e.cfg.PublicAddr)+"/api/oauth/google/callback" {
		t.Fatalf("providers: %+v %v", views, err)
	}
	if err := p.SaveOAuthProvider("myspace", OAuthProviderSettings{}); err == nil {
		t.Fatal("an unknown provider should be refused")
	}

	// Redirects only go back to the project's own domains.
	for _, bad := range []string{"https://evil.example/cb", "http://other.localhost/cb", "/auth/callback",
		"http://demo.localhost/cb#x", "http://user@demo.localhost/cb", "javascript:alert(1)"} {
		if _, err := p.StartSignIn("demo", "google", bad); err == nil {
			t.Errorf("redirect %q was accepted", bad)
		}
	}

	signIn := func(project, provider string) (code, state string) {
		t.Helper()
		redirect := strings.Replace(appURL, "demo", project, 1)
		start, err := p.StartSignIn(project, provider, redirect)
		if err != nil {
			t.Fatal(err)
		}
		res := p.FinishSignIn(ctx, provider, start.State, f.authorize(t, start, p.oauthCallbackURL(provider)), "")
		if res.Test || res.Err != nil {
			t.Fatalf("finish: %+v", res)
		}
		u, _ := url.Parse(res.Redirect)
		q := u.Query()
		if !strings.HasPrefix(res.Redirect, "http://"+project+".localhost:8080/auth/callback?") ||
			q.Get("next") != "/notes" || q.Get("state") != start.State || !strings.HasPrefix(q.Get("code"), "syc_") {
			t.Fatalf("back to the app at %q", res.Redirect)
		}
		return q.Get("code"), start.State
	}

	code, state := signIn("demo", "google")
	id, err := p.ExchangeSignIn("demo", code, state)
	want := Identity{Provider: "google", ID: "g-1", Email: "ada@example.com", EmailVerified: true, Name: "Ada", AvatarURL: "https://example.com/ada.png"}
	if err != nil || id != want {
		t.Fatalf("identity %+v %v", id, err)
	}
	if _, err := p.ExchangeSignIn("demo", code, state); err == nil {
		t.Fatal("a code should work once")
	}
	code, _ = signIn("demo", "google")
	if _, err := p.ExchangeSignIn("demo", code, "sys_planted"); err == nil {
		t.Fatal("a code should only work with its own state")
	}
	code, state = signIn("demo", "google")
	if _, err := p.ExchangeSignIn("other", code, state); err == nil {
		t.Fatal("a code should only work for its own project")
	}

	code, state = signIn("demo", "github")
	id, err = p.ExchangeSignIn("demo", code, state)
	want = Identity{Provider: "github", ID: "42", Email: "grace@example.com", EmailVerified: true, Name: "grace", AvatarURL: "https://example.com/g.png"}
	if err != nil || id != want {
		t.Fatalf("GitHub identity %+v %v", id, err)
	}

	// Declining at the provider, or a code the provider refuses, sends the
	// browser back to the app with an error.
	start, _ := p.StartSignIn("demo", "google", appURL)
	if res := p.FinishSignIn(ctx, "google", start.State, "", "access_denied"); !strings.Contains(res.Redirect, "error=access_denied") {
		t.Fatalf("declined: %+v", res)
	}
	start, _ = p.StartSignIn("demo", "google", appURL)
	if res := p.FinishSignIn(ctx, "google", start.State, "forged", ""); !strings.Contains(res.Redirect, "error=sign_in_failed") {
		t.Fatalf("forged code: %+v", res)
	}
	// A state Sideyard didn't issue, or one for another provider, ends on
	// Sideyard's own page.
	if res := p.FinishSignIn(ctx, "google", "sys_unknown", "good", ""); !res.Test || res.Err == nil {
		t.Fatalf("unknown state: %+v", res)
	}
	start, _ = p.StartSignIn("demo", "google", appURL)
	if res := p.FinishSignIn(ctx, "github", start.State, "good", ""); !res.Test || res.Err == nil {
		t.Fatalf("wrong provider: %+v", res)
	}

	// The dashboard's test sign-in reports who signed in and records nothing.
	start, err = p.StartTestSignIn("github")
	if err != nil {
		t.Fatal(err)
	}
	res := p.FinishSignIn(ctx, "github", start.State, f.authorize(t, start, p.oauthCallbackURL("github")), "")
	if !res.Test || res.Err != nil || res.Identity.ID != "42" {
		t.Fatalf("test sign-in: %+v", res)
	}

	people, err := p.ProjectSignIns("demo")
	if err != nil || people.Total != 2 || len(people.Providers) != 2 || people.People[0].Provider != "github" ||
		people.People[1].Email != "ada@example.com" || people.People[1].Count != 3 {
		t.Fatalf("people: %+v %v", people, err)
	}
	if other, _ := p.ProjectSignIns("other"); other.Total != 0 {
		t.Fatalf("other project's people: %+v", other)
	}

	// Turning a provider off stops new sign-ins with it.
	p.SaveOAuthProvider("github", OAuthProviderSettings{ClientID: "client"})
	if _, err := p.StartSignIn("demo", "github", appURL); err == nil {
		t.Fatal("a disabled provider should refuse new sign-ins")
	}
}

func portOf(addr string) string {
	return addr[strings.LastIndex(addr, ":")+1:]
}
