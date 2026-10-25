// Package ghauthtest is a fake of the GitHub endpoints package ghauth calls —
// the device flow, the token endpoint, GET /user and token revocation — for
// tests, on an httptest server.
package ghauthtest

import (
	"crs/ghauth"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// ClientID and ClientSecret are the fake app's.
	ClientID     = "Iv1.fakeclient"
	ClientSecret = "fake-client-secret"
	// UserCode and VerificationURI are what every device flow shows the user.
	UserCode        = "WDJB-MJHT"
	VerificationURI = "https://github.com/login/device"
	// Second is how long the fake's seconds last in an App from App: a
	// device flow's interval of 1 polls every 10ms.
	Second = 10 * time.Millisecond
)

// Server fakes GitHub. Each device flow it starts answers its polls with the
// script given to Script, in order, then authorization_pending; "ok" grants a
// token for the login set by GrantAs.
type Server struct {
	*httptest.Server

	mu sync.Mutex
	// Device flow.
	interval, expiresIn int
	script              []string
	scopes              []string
	polls               []time.Time
	answers             []string
	grantAs             string
	expiring            bool
	// Tokens: access token → login, and refresh token → login.
	tokens        map[string]string
	refreshTokens map[string]string
	minted        int
	refreshes     int
	revoked       []string
}

// NewServer starts a fake GitHub that grants tokens for "octocat", closed
// when the test ends.
func NewServer(t testing.TB) *Server {
	s := &Server{interval: 1, expiresIn: 900, grantAs: "octocat",
		tokens: map[string]string{}, refreshTokens: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/device/code", s.deviceCode)
	mux.HandleFunc("POST /login/oauth/access_token", s.accessToken)
	mux.HandleFunc("GET /user", s.user)
	mux.HandleFunc("DELETE /applications/{client}/token", s.revoke)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// Endpoints are the fake's.
func (s *Server) Endpoints() ghauth.Endpoints {
	return ghauth.Endpoints{
		DeviceCode:  s.URL + "/login/device/code",
		AccessToken: s.URL + "/login/oauth/access_token",
		API:         s.URL,
	}
}

// App is an app on the fake, with its client secret, and fast seconds.
func (s *Server) App() *ghauth.App {
	return &ghauth.App{ClientID: ClientID, ClientSecret: ClientSecret, Scopes: ghauth.DefaultScopes,
		Endpoints: s.Endpoints(), HTTP: s.Client(), Second: Second}
}

// Timing sets the interval and expires_in, in the fake's seconds, of the
// device flows started from now on.
func (s *Server) Timing(interval, expiresIn int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interval, s.expiresIn = interval, expiresIn
}

// Script queues what the next polls are answered with: "ok", an OAuth error
// code such as "authorization_pending", "slow_down", "access_denied" or
// "expired_token", or "status:N" for a failure with HTTP status N.
func (s *Server) Script(answers ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script = append(s.script, answers...)
}

// GrantAs sets whose account the tokens granted from now on belong to.
func (s *Server) GrantAs(login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grantAs = login
}

// Expiring makes the tokens granted from now on expire after eight hours,
// with a refresh token, as a GitHub App's do.
func (s *Server) Expiring(expiring bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expiring = expiring
}

// AddToken makes token a valid access token of login's, and refresh, when
// not empty, a valid refresh token for them.
func (s *Server) AddToken(token, refresh, login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = login
	if refresh != "" {
		s.refreshTokens[refresh] = login
	}
}

// Invalidate makes GitHub refuse token from now on, as when its user revokes
// it on github.com.
func (s *Server) Invalidate(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

// Valid reports whether token is an access token GitHub accepts.
func (s *Server) Valid(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.tokens[token]
	return ok
}

// Scopes returns the scope each device flow asked for.
func (s *Server) Scopes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.scopes...)
}

// Polls returns when each device-flow poll arrived.
func (s *Server) Polls() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.polls...)
}

// Answers returns what each device-flow poll was answered with, in the order
// of Polls.
func (s *Server) Answers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.answers...)
}

// Refreshes counts the refresh-token exchanges granted.
func (s *Server) Refreshes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes
}

// Revoked returns the access tokens revoked, in order.
func (s *Server) Revoked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.revoked...)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// oauthJSON answers as GitHub's OAuth endpoints do: JSON only when asked for
// it, form-encoded otherwise.
func oauthJSON(w http.ResponseWriter, r *http.Request, v map[string]any) {
	if r.Header.Get("Accept") != "application/json" {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		parts := []string{}
		for k, val := range v {
			parts = append(parts, fmt.Sprintf("%s=%v", k, val))
		}
		_, _ = w.Write([]byte(strings.Join(parts, "&")))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deviceCode(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("client_id") != ClientID {
		oauthJSON(w, r, map[string]any{"error": "Not Found"})
		return
	}
	s.mu.Lock()
	s.scopes = append(s.scopes, r.FormValue("scope"))
	n := len(s.scopes)
	interval, expiresIn := s.interval, s.expiresIn
	s.mu.Unlock()
	oauthJSON(w, r, map[string]any{
		"device_code":      fmt.Sprintf("device-%d", n),
		"user_code":        UserCode,
		"verification_uri": VerificationURI,
		"expires_in":       expiresIn,
		"interval":         interval,
	})
}

func (s *Server) accessToken(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("client_id") != ClientID {
		oauthJSON(w, r, map[string]any{"error": "incorrect_client_credentials"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.FormValue("grant_type") {
	case "urn:ietf:params:oauth:grant-type:device_code":
		s.polls = append(s.polls, time.Now())
		answer := "authorization_pending"
		if len(s.script) > 0 {
			answer, s.script = s.script[0], s.script[1:]
		}
		s.answers = append(s.answers, answer)
		switch {
		case answer == "ok":
			oauthJSON(w, r, s.grantLocked(s.grantAs, s.expiring))
		case strings.HasPrefix(answer, "status:"):
			var status int
			fmt.Sscanf(answer, "status:%d", &status)
			writeJSON(w, status, map[string]string{"message": "scripted " + answer})
		default:
			oauthJSON(w, r, map[string]any{"error": answer, "error_description": "scripted " + answer})
		}
	case "refresh_token":
		login, ok := s.refreshTokens[r.FormValue("refresh_token")]
		if r.FormValue("client_secret") != ClientSecret || !ok {
			oauthJSON(w, r, map[string]any{"error": "bad_refresh_token", "error_description": "The refresh token passed is incorrect or expired."})
			return
		}
		// Refresh tokens are single use.
		delete(s.refreshTokens, r.FormValue("refresh_token"))
		s.refreshes++
		oauthJSON(w, r, s.grantLocked(login, true))
	default:
		oauthJSON(w, r, map[string]any{"error": "unsupported_grant_type"})
	}
}

// grantLocked mints a token for login, an expiring one with a refresh token
// if expiring.
func (s *Server) grantLocked(login string, expiring bool) map[string]any {
	s.minted++
	token := fmt.Sprintf("gho_token_%d", s.minted)
	s.tokens[token] = login
	grant := map[string]any{"access_token": token, "token_type": "bearer", "scope": "repo"}
	if expiring {
		refresh := fmt.Sprintf("ghr_refresh_%d", s.minted)
		s.refreshTokens[refresh] = login
		grant["expires_in"], grant["refresh_token"], grant["refresh_token_expires_in"] = 28800, refresh, 15897600
	}
	return grant
}

func (s *Server) user(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	login, ok := s.tokens[token]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": login, "id": 1})
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	if !ok || id != ClientID || secret != ClientSecret || r.PathValue("client") != ClientID {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Invalid request"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tokens[body.AccessToken]; !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	delete(s.tokens, body.AccessToken)
	s.revoked = append(s.revoked, body.AccessToken)
	w.WriteHeader(http.StatusNoContent)
}
