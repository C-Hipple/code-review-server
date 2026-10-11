package server

import (
	"sync"
	"time"
)

// Session is who a remote connection acts as. Each connection the server
// accepts gets an RPCHandler and a Session of its own; a handler with no
// Session is the local stdio connection (see RPCHandler).
//
// net/rpc serves a connection's calls concurrently, so the identity is read and
// replaced under a lock: authenticating replaces it while calls already in
// flight on the same connection read it.
//
// The zero Session is a connection that has not authenticated yet. It is not
// an administrator, but its login reads as "", the server's own, so a call
// made before authenticating has to be refused before it reaches a handler
// that reads the viewer — which the method policy does (policy.go).
type Session struct {
	mu            sync.RWMutex
	login         string
	admin         bool
	authenticated bool
	// tokenHash is the token_hash of the users row the session authenticated
	// against. Every call checks the row still holds it (RPCHandler.admit), so
	// a user deleted — or deleted and registered again by someone holding
	// another token — stops being this session's identity. Empty for the
	// administrator token, which has no row.
	tokenHash string

	// touchInterval is how often at most a call refreshes the user's last_seen,
	// and touched when it last did. Set when the connection is accepted.
	touchInterval time.Duration
	touched       time.Time
}

// sessionState is one consistent reading of a Session's identity.
type sessionState struct {
	login         string
	tokenHash     string
	admin         bool
	authenticated bool
}

// SetIdentity records who the connection authenticated as: a registered
// user's login, or "" for the server's own identity.
func (s *Session) SetIdentity(login string, admin bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.login, s.admin, s.authenticated = login, admin, true
}

// signIn records a successful Authenticate or RegisterUser: the users row the
// connection proved it holds the token of (tokenHash, "" for the
// administrator token), and that last_seen was just refreshed.
func (s *Session) signIn(login, tokenHash string, admin bool, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.login, s.tokenHash, s.admin, s.authenticated = login, tokenHash, admin, true
	s.touched = now
}

// signOutIf ends the session's authentication if it is still the identity
// was describes: a RegisterUser that signed the connection in again meanwhile
// is not undone by a check that read the identity before it.
//
// The login is kept. A call already admitted under it may still be running
// and reads its viewer when it gets to it; the zero login "" is the server's
// own dashboard, which such a call must never be handed. Nothing new is
// admitted under the kept login: every call but the public ones is refused
// until the connection authenticates again.
func (s *Session) signOutIf(was sessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state() != was {
		return
	}
	s.admin, s.authenticated, s.tokenHash = false, false, ""
	s.touched = time.Time{}
}

// setAdminIf brings the session's administrator flag in line with its users
// row, if the session is still the identity was describes.
func (s *Session) setAdminIf(was sessionState, admin bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state() == was {
		s.admin = admin
	}
}

// touchDue reports whether a call at now should refresh the user's last_seen,
// and if so counts it as refreshed. last_seen decides whether the workflow
// cycle runs the user's workflows and when retention deletes them; minutes
// matter to neither, so a busy connection writes it once per touchInterval
// rather than on every call.
func (s *Session) touchDue(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.touched) < s.touchInterval {
		return false
	}
	s.touched = now
	return true
}

// Identity returns who the connection acts as, and whether it has
// authenticated at all.
func (s *Session) Identity() (login string, admin, authenticated bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.login, s.admin, s.authenticated
}

// snapshot reads the whole identity at once.
func (s *Session) snapshot() sessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state()
}

// state is the identity; the caller holds the lock.
func (s *Session) state() sessionState {
	return sessionState{login: s.login, tokenHash: s.tokenHash, admin: s.admin, authenticated: s.authenticated}
}

// viewer is the login whose dashboard, drafts and feedback the connection reads
// and writes. The local connection's is "", the server's own: its dashboard is
// the config file's workflows, and its drafts are the ones local mode has
// always kept.
func (h *RPCHandler) viewer() string {
	if h.session == nil {
		return ""
	}
	login, _, _ := h.session.Identity()
	return login
}

// isAdmin reports whether the connection may manage the server itself — its
// configuration and its users. The local connection always may: it is whoever
// started the server.
func (h *RPCHandler) isAdmin() bool {
	if h.session == nil {
		return true
	}
	_, admin, _ := h.session.Identity()
	return admin
}

// isLocal reports whether the connection is the local stdio one. Actions
// GitHub records as the server token's user are only ever the token owner's
// own when they come from it.
func (h *RPCHandler) isLocal() bool {
	return h.session == nil
}

// authenticated reports whether the connection has proved who it is. The
// local connection needs no proof: it is whoever started the server.
func (h *RPCHandler) authenticated() bool {
	if h.session == nil {
		return true
	}
	_, _, authenticated := h.session.Identity()
	return authenticated
}
