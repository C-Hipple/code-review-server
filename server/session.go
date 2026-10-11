package server

import "sync"

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
// that reads the viewer.
type Session struct {
	mu            sync.RWMutex
	login         string
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

// Identity returns who the connection acts as, and whether it has
// authenticated at all.
func (s *Session) Identity() (login string, admin, authenticated bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.login, s.admin, s.authenticated
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
