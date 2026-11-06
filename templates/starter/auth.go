package main

import (
	"crypto/subtle"
	"errors"
	"net/http"
)

// Sign-in with Google or GitHub goes through Sideyard, which holds the OAuth
// clients (set up once on the dashboard's OAuth page) and tells the app who
// signed in. Users and sessions are the app's own. To use it:
//
//	mux.HandleFunc("GET /auth/{provider}", startSignIn) // link "Sign in with Google" to /auth/google
//	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
//		user, err := finishSignIn(w, r)
//		// on success: upsert a users row keyed by (user.Provider, user.ID),
//		// start a session, set its cookie, redirect into the app
//	})

// signedInUser is who signed in. ID is stable for the provider; Email can
// change, and from GitHub can be empty, so key users on Provider and ID.
type signedInUser struct {
	Provider      string `json:"provider"` // "google" or "github"
	ID            string `json:"id"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"emailVerified"`
	Name          string `json:"name"`
	AvatarURL     string `json:"avatarUrl"`
}

const signInCookie = "signin_state"

// startSignIn sends the browser to the provider named in the path. It comes
// back to /auth/callback on the domain it started from.
func startSignIn(w http.ResponseWriter, r *http.Request) {
	secure := r.Header.Get("X-Forwarded-Proto") != "http"
	scheme := "https"
	if !secure {
		scheme = "http"
	}
	var start struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	err := sideyardAPI(r.Context(), "oauth/start", map[string]string{
		"provider": r.PathValue("provider"),
		"redirect": scheme + "://" + r.Host + "/auth/callback",
	}, &start)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// The state ties the callback to this browser, so nobody can sign it in
	// as someone else by sending it a link.
	http.SetCookie(w, &http.Cookie{Name: signInCookie, Value: start.State, Path: "/auth", MaxAge: 600,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, start.URL, http.StatusFound)
}

// finishSignIn checks the callback belongs to this browser and asks Sideyard
// who signed in. An error means the person declined, took too long, or the
// link wasn't theirs: send them back to sign in again.
func finishSignIn(w http.ResponseWriter, r *http.Request) (signedInUser, error) {
	var user signedInUser
	c, err := r.Cookie(signInCookie)
	http.SetCookie(w, &http.Cookie{Name: signInCookie, Path: "/auth", MaxAge: -1})
	q := r.URL.Query()
	switch {
	case q.Get("error") != "":
		return user, errors.New("sign-in didn't complete: " + q.Get("error"))
	case err != nil || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(q.Get("state"))) != 1:
		return user, errors.New("sign-in expired or was started in another browser")
	}
	err = sideyardAPI(r.Context(), "oauth/exchange", map[string]string{"code": q.Get("code"), "state": q.Get("state")}, &user)
	return user, err
}
