package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"sideyard/internal/secrets"
	"sideyard/internal/store"
)

// Sign-in: the operator sets up one OAuth client per provider (Google,
// GitHub) in the dashboard, and every app on the server signs people in
// through it. Sideyard runs the OAuth exchange on the dashboard host, the
// one redirect URI registered with the provider; apps keep their own users
// and sessions.
//
//  1. The app asks Sideyard to start a sign-in (StartSignIn, with its
//     SIDEYARD_TOKEN), keeps the returned state in a cookie and sends the
//     browser to the returned URL.
//  2. The provider sends the browser back to Sideyard (FinishSignIn), which
//     fetches the person's identity and redirects to the app with a
//     one-time code and the state.
//  3. The app checks the state against its cookie and trades the code for
//     the identity (ExchangeSignIn), server to server.
//
// The browser only ever carries the opaque code, the code works once, for
// one project and one state, and the state cookie stops someone else's
// sign-in being planted in a browser. Flows live in memory: a restart only
// interrupts sign-ins in progress.
const (
	settingOAuth  = "oauth"
	signInFlowTTL = 10 * time.Minute
	signInCodeTTL = 2 * time.Minute
	maxSignIns    = 10000 // flows and codes waiting, across all projects
)

// Identity is who signed in, as the provider reported it. ID is stable per
// provider; Email can change and, from GitHub, can be empty.
type Identity struct {
	Provider      string `json:"provider"`
	ID            string `json:"id"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	Name          string `json:"name"`
	AvatarURL     string `json:"avatarUrl"`
}

// oauthProvider is how Sideyard talks to one provider. Tests point the URLs
// at a fake.
type oauthProvider struct {
	name     string
	authURL  string
	tokenURL string
	scopes   []string
	// consoleURL is where the operator creates the OAuth client.
	consoleURL string
	identity   func(ctx context.Context, c *http.Client, prov *oauthProvider) (Identity, error)
	apiURL     string // userinfo (Google) or the REST API root (GitHub)
}

var oauthProviders = map[string]*oauthProvider{
	"google": {
		name:       "Google",
		authURL:    "https://accounts.google.com/o/oauth2/v2/auth",
		tokenURL:   "https://oauth2.googleapis.com/token",
		apiURL:     "https://openidconnect.googleapis.com/v1/userinfo",
		scopes:     []string{"openid", "email", "profile"},
		consoleURL: "https://console.cloud.google.com/auth/clients",
		identity:   googleIdentity,
	},
	"github": {
		name:       "GitHub",
		authURL:    "https://github.com/login/oauth/authorize",
		tokenURL:   "https://github.com/login/oauth/access_token",
		apiURL:     "https://api.github.com",
		scopes:     []string{"read:user", "user:email"},
		consoleURL: "https://github.com/settings/developers",
		identity:   githubIdentity,
	},
}

// providerOrder is the order the dashboard lists providers in.
var providerOrder = []string{"google", "github"}

// --- settings ---

// OAuthProviderSettings is one provider's client as the dashboard sees it.
// ClientSecret is only ever sent in.
type OAuthProviderSettings struct {
	Enabled      bool   `json:"enabled"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret,omitempty"`
	HasSecret    bool   `json:"hasSecret"`
}

type storedOAuthProvider struct {
	OAuthProviderSettings
	SealedSecret []byte `json:"sealedSecret"`
}

// OAuthProviderView adds what the operator needs to set the provider up.
type OAuthProviderView struct {
	OAuthProviderSettings
	ID          string `json:"id"`
	Name        string `json:"name"`
	CallbackURL string `json:"callbackUrl"`
	ConsoleURL  string `json:"consoleUrl"`
}

func (p *Platform) loadOAuth() (map[string]storedOAuthProvider, error) {
	st := map[string]storedOAuthProvider{}
	err := p.store.JSONSetting(settingOAuth, &st)
	for id, s := range st {
		s.ClientSecret, s.HasSecret = "", len(s.SealedSecret) > 0
		st[id] = s
	}
	return st, err
}

// OAuthProviders lists every provider Sideyard supports and its setup.
func (p *Platform) OAuthProviders() ([]OAuthProviderView, error) {
	st, err := p.loadOAuth()
	if err != nil {
		return nil, err
	}
	out := []OAuthProviderView{}
	for _, id := range providerOrder {
		prov := oauthProviders[id]
		out = append(out, OAuthProviderView{OAuthProviderSettings: st[id].OAuthProviderSettings, ID: id,
			Name: prov.name, CallbackURL: p.oauthCallbackURL(id), ConsoleURL: prov.consoleURL})
	}
	return out, nil
}

// SaveOAuthProvider sets up one provider. An empty ClientSecret keeps the
// saved one.
func (p *Platform) SaveOAuthProvider(id string, in OAuthProviderSettings) error {
	if oauthProviders[id] == nil {
		return ErrNotFound
	}
	st, err := p.loadOAuth()
	if err != nil {
		return err
	}
	in.ClientID = strings.TrimSpace(in.ClientID)
	in.ClientSecret = strings.TrimSpace(in.ClientSecret)
	sealed := st[id].SealedSecret
	if in.ClientSecret != "" {
		sealed = p.box.Seal([]byte(in.ClientSecret))
	}
	if in.Enabled && (in.ClientID == "" || len(sealed) == 0) {
		return invalid("a client ID and client secret are required")
	}
	in.ClientSecret, in.HasSecret = "", false
	st[id] = storedOAuthProvider{OAuthProviderSettings: in, SealedSecret: sealed}
	return p.store.SetJSONSetting(settingOAuth, st)
}

// dashboardURL is the dashboard's public address, which providers redirect
// browsers back to.
func (p *Platform) dashboardURL() string {
	if !p.cfg.Dev {
		return "https://" + p.cfg.DashboardHost
	}
	host := p.cfg.DashboardHost
	if _, port, err := net.SplitHostPort(p.cfg.PublicAddr); err == nil && port != "80" {
		host = net.JoinHostPort(host, port)
	}
	return "http://" + host
}

func (p *Platform) oauthCallbackURL(id string) string {
	return p.dashboardURL() + "/api/oauth/" + id + "/callback"
}

// oauthConfig is the provider's client, if it's set up.
func (p *Platform) oauthConfig(id string) (*oauth2.Config, error) {
	prov := oauthProviders[id]
	if prov == nil {
		return nil, invalid("unknown provider %q; use one of %s", id, strings.Join(providerOrder, ", "))
	}
	st, err := p.loadOAuth()
	if err != nil {
		return nil, err
	}
	s := st[id]
	if !s.Enabled {
		return nil, invalid("sign-in with %s isn't set up; enable it on the dashboard's OAuth page", prov.name)
	}
	secret, err := p.box.Open(s.SealedSecret)
	if err != nil {
		return nil, err
	}
	return &oauth2.Config{
		ClientID: s.ClientID, ClientSecret: string(secret), Scopes: prov.scopes,
		RedirectURL: p.oauthCallbackURL(id),
		Endpoint:    oauth2.Endpoint{AuthURL: prov.authURL, TokenURL: prov.tokenURL, AuthStyle: oauth2.AuthStyleInParams},
	}, nil
}

// --- flows ---

type signInFlow struct {
	projectID int64 // 0 for the dashboard's test sign-in
	provider  string
	redirect  string
	verifier  string
	expires   time.Time
}

type signInCode struct {
	projectID int64
	state     string
	identity  Identity
	expires   time.Time
}

type signIns struct {
	mu    sync.Mutex
	flows map[string]signInFlow // by state
	codes map[string]signInCode
}

// add stores a flow or code under a new random key, after dropping expired
// ones.
func (s *signIns) add(prefix string, put func(key string)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flows == nil {
		s.flows, s.codes = map[string]signInFlow{}, map[string]signInCode{}
	}
	now := time.Now()
	for k, f := range s.flows {
		if now.After(f.expires) {
			delete(s.flows, k)
		}
	}
	for k, c := range s.codes {
		if now.After(c.expires) {
			delete(s.codes, k)
		}
	}
	if len(s.flows)+len(s.codes) >= maxSignIns {
		return "", ErrRateLimited
	}
	key := secrets.NewToken(prefix)
	put(key)
	return key, nil
}

func (s *signIns) takeFlow(state string) (signInFlow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flows[state]
	delete(s.flows, state)
	return f, ok && time.Now().Before(f.expires)
}

func (s *signIns) takeCode(code string) (signInCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[code]
	delete(s.codes, code)
	return c, ok && time.Now().Before(c.expires)
}

// SignInStart is where an app sends the browser, and the state it keeps in
// a cookie until the browser comes back.
type SignInStart struct {
	URL   string `json:"url"`
	State string `json:"state"`
}

// StartSignIn begins a sign-in for an app. redirect is the app's own URL the
// browser returns to, on one of the project's domains.
func (p *Platform) StartSignIn(name, provider, redirect string) (SignInStart, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return SignInStart{}, err
	}
	if err := p.checkRedirect(proj, redirect); err != nil {
		return SignInStart{}, err
	}
	return p.startFlow(signInFlow{projectID: proj.ID, provider: provider, redirect: redirect})
}

// StartTestSignIn begins a sign-in that ends on a Sideyard page showing who
// signed in, so the operator can check a provider's setup.
func (p *Platform) StartTestSignIn(provider string) (SignInStart, error) {
	return p.startFlow(signInFlow{provider: provider})
}

func (p *Platform) startFlow(f signInFlow) (SignInStart, error) {
	conf, err := p.oauthConfig(f.provider)
	if err != nil {
		return SignInStart{}, err
	}
	f.verifier = oauth2.GenerateVerifier()
	f.expires = time.Now().Add(signInFlowTTL)
	state, err := p.signIns.add("sys_", func(k string) { p.signIns.flows[k] = f })
	if err != nil {
		return SignInStart{}, err
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(f.verifier)}
	if f.provider == "google" {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "select_account"))
	}
	return SignInStart{URL: conf.AuthCodeURL(state, opts...), State: state}, nil
}

// checkRedirect accepts only the project's own domains, so Sideyard can't be
// used to send people, or their codes, anywhere else.
func (p *Platform) checkRedirect(proj store.Project, redirect string) error {
	u, err := url.Parse(redirect)
	if err != nil || !u.IsAbs() || u.User != nil || u.Fragment != "" {
		return invalid("redirect must be an absolute URL on one of the app's domains, without a #fragment")
	}
	if u.Scheme != "https" && !(p.cfg.Dev && u.Scheme == "http") {
		return invalid("redirect must use https")
	}
	domains, err := p.store.Domains()
	if err != nil {
		return err
	}
	if !slices.Contains(domains[proj.ID], strings.ToLower(u.Hostname())) {
		return invalid("redirect host %q isn't one of %s's domains", u.Hostname(), proj.Name)
	}
	return nil
}

// SignInResult is where the callback sends the browser: back to the app,
// or, for a test sign-in, a page reporting the outcome.
type SignInResult struct {
	Redirect string
	Test     bool
	Identity Identity
	Err      error
}

// FinishSignIn handles the provider's redirect back to Sideyard. providerErr
// is the provider's error parameter: the person declined, say.
func (p *Platform) FinishSignIn(ctx context.Context, provider, state, code, providerErr string) SignInResult {
	f, ok := p.signIns.takeFlow(state)
	if !ok || f.provider != provider {
		return SignInResult{Test: true, Err: errors.New("this sign-in has expired or was already used; start again from the app")}
	}
	res := SignInResult{Test: f.projectID == 0}
	fail := func(reason string, err error) SignInResult {
		if res.Test {
			res.Err = err
			return res
		}
		if proj, perr := p.store.ProjectByID(f.projectID); perr == nil {
			p.logf(proj, "sign-in with %s failed: %v", oauthProviders[provider].name, err)
		}
		res.Redirect = withQuery(f.redirect, url.Values{"error": {reason}, "state": {state}})
		return res
	}
	if providerErr != "" {
		return fail(providerErr, fmt.Errorf("%s returned %q", oauthProviders[provider].name, providerErr))
	}
	id, err := p.fetchIdentity(ctx, provider, code, f.verifier)
	if err != nil {
		return fail("sign_in_failed", err)
	}
	if res.Test {
		res.Identity = id
		return res
	}
	proj, err := p.store.ProjectByID(f.projectID)
	if err != nil {
		return fail("sign_in_failed", err)
	}
	c := signInCode{projectID: proj.ID, state: state, identity: id, expires: time.Now().Add(signInCodeTTL)}
	key, err := p.signIns.add("syc_", func(k string) { p.signIns.codes[k] = c })
	if err != nil {
		return fail("sign_in_failed", err)
	}
	if err := p.store.RecordSignIn(store.SignIn{ProjectID: proj.ID, Provider: id.Provider, Subject: id.ID,
		Email: id.Email, Name: id.Name, AvatarURL: id.AvatarURL}); err != nil {
		p.logf(proj, "recording a sign-in: %v", err)
	}
	res.Redirect = withQuery(f.redirect, url.Values{"code": {key}, "state": {state}})
	return res
}

func withQuery(raw string, add url.Values) string {
	u, _ := url.Parse(raw) // checked when the flow started
	q := u.Query()
	for k, v := range add {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ExchangeSignIn trades a one-time code for the identity it stands for. The
// state must be the one the app started the sign-in with.
func (p *Platform) ExchangeSignIn(name, code, state string) (Identity, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return Identity{}, err
	}
	c, ok := p.signIns.takeCode(code)
	if !ok || c.projectID != proj.ID || state == "" || c.state != state {
		return Identity{}, invalid("the code is invalid, expired or already used, or the state doesn't match")
	}
	return c.identity, nil
}

func (p *Platform) fetchIdentity(ctx context.Context, provider, code, verifier string) (Identity, error) {
	conf, err := p.oauthConfig(provider)
	if err != nil {
		return Identity{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("exchanging the code: %w", err)
	}
	id, err := oauthProviders[provider].identity(ctx, conf.Client(ctx, tok), oauthProviders[provider])
	if err != nil {
		return Identity{}, fmt.Errorf("reading the profile: %w", err)
	}
	if id.ID == "" {
		return Identity{}, errors.New("the provider didn't say who signed in")
	}
	id.Provider = provider
	return id, nil
}

func getJSON(ctx context.Context, c *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Sideyard")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s %s", url, resp.Status, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
	return json.Unmarshal(body, v)
}

func googleIdentity(ctx context.Context, c *http.Client, prov *oauthProvider) (Identity, error) {
	var u struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Picture       string `json:"picture"`
	}
	if err := getJSON(ctx, c, prov.apiURL, &u); err != nil {
		return Identity{}, err
	}
	return Identity{ID: u.Sub, Email: u.Email, EmailVerified: u.EmailVerified, Name: u.Name, AvatarURL: u.Picture}, nil
}

// githubIdentity uses the primary address from /user/emails: the profile's
// public email may be empty and says nothing about verification.
func githubIdentity(ctx context.Context, c *http.Client, prov *oauthProvider) (Identity, error) {
	var u struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := getJSON(ctx, c, prov.apiURL+"/user", &u); err != nil {
		return Identity{}, err
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, c, prov.apiURL+"/user/emails", &emails); err != nil {
		return Identity{}, err
	}
	id := Identity{Name: cmpOr(u.Name, u.Login), AvatarURL: u.AvatarURL}
	if u.ID != 0 {
		id.ID = strconv.FormatInt(u.ID, 10)
	}
	for _, e := range emails {
		if e.Primary {
			id.Email, id.EmailVerified = e.Email, e.Verified
		}
	}
	return id, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// --- the project's people ---

type SignInView struct {
	Provider  string    `json:"provider"`
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	AvatarURL string    `json:"avatarUrl"`
	Count     int       `json:"count"`
	FirstAt   time.Time `json:"firstAt"`
	LastAt    time.Time `json:"lastAt"`
}

type ProjectSignIns struct {
	// Providers are the ones set up, which the app can use.
	Providers []string     `json:"providers"`
	Total     int          `json:"total"`
	People    []SignInView `json:"people"`
}

// ProjectSignIns lists who has signed in to a project's app, newest first.
func (p *Platform) ProjectSignIns(name string) (ProjectSignIns, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return ProjectSignIns{}, err
	}
	st, err := p.loadOAuth()
	if err != nil {
		return ProjectSignIns{}, err
	}
	v := ProjectSignIns{Providers: []string{}, People: []SignInView{}}
	for _, id := range providerOrder {
		if st[id].Enabled {
			v.Providers = append(v.Providers, id)
		}
	}
	people, total, err := p.store.SignIns(proj.ID, 500)
	if err != nil {
		return ProjectSignIns{}, err
	}
	v.Total = total
	for _, u := range people {
		v.People = append(v.People, SignInView{u.Provider, u.Subject, u.Email, u.Name, u.AvatarURL, u.Count, u.FirstAt, u.LastAt})
	}
	return v, nil
}
