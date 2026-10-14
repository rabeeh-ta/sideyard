package api

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"sideyard/internal/secrets"
	"sideyard/internal/store"
)

const (
	sessionCookie = "sideyard_session"
	sessionTTL    = 30 * 24 * time.Hour
	// PasswordSetting holds the bcrypt hash of the admin password.
	PasswordSetting = "admin_password_hash"
)

// SetPassword stores a new admin password and signs out every session.
// CheckNewPassword enforces the rules for a new admin password: at least
// 12 characters, and no more than bcrypt's 72 bytes.
func CheckNewPassword(password string) error {
	switch {
	case utf8.RuneCountInString(password) < 12:
		return errors.New("use at least 12 characters")
	case len(password) > 72:
		return errors.New("use at most 72 bytes (bcrypt ignores the rest)")
	}
	return nil
}

func SetPassword(st *store.Store, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := st.SetSetting(PasswordSetting, string(hash)); err != nil {
		return err
	}
	return st.DeleteAllSessions()
}

// EnsurePassword creates a random admin password on first run. It returns
// the new password, or "" if one was already set.
func EnsurePassword(st *store.Store) (string, error) {
	if _, err := st.Setting(PasswordSetting); err == nil {
		return "", nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	password := secrets.NewToken("")[:20]
	return password, SetPassword(st, password)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts; try again in a few minutes")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	hash, err := s.store.Setting(PasswordSetting)
	if err != nil {
		writeErr(w, err)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)) != nil {
		s.limiter.fail(ip)
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	if err := s.startSession(w); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

// startSession signs the browser in with a new session cookie.
func (s *Server) startSession(w http.ResponseWriter) error {
	token := secrets.NewToken("")
	if err := s.store.CreateSession(secrets.Hash(token), time.Now().Add(sessionTTL)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		MaxAge: int(sessionTTL.Seconds()), HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.store.DeleteSession(secrets.Hash(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": s.hasSession(r)})
}

func (s *Server) hasSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	ok, err := s.store.SessionValid(secrets.Hash(c.Value))
	return err == nil && ok
}

func (s *Server) requireSession(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hasSession(r) {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		h(w, r)
	})
}

// clientIP trusts X-Forwarded-For because the API only listens behind the
// embedded proxy, which replaces any client-supplied value.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

// loginLimiter allows 10 failed logins per IP per 10 minutes.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: map[string][]time.Time{}}
}

func (l *loginLimiter) recent(ip string) []time.Time {
	cutoff := time.Now().Add(-10 * time.Minute)
	var keep []time.Time
	for _, t := range l.failures[ip] {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if keep == nil {
		delete(l.failures, ip)
	} else {
		l.failures[ip] = keep
	}
	return keep
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip)) < 10
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip] = append(l.recent(ip), time.Now())
}
