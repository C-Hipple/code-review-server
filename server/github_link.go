package server

import (
	"context"
	"crs/config"
	"crs/database"
	"crs/ghauth"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// --- Linking a user's own GitHub account ---
//
// A hosted server reads GitHub with its own token for everyone, but GitHub
// records a review or a merge as whoever's token sends it. A user links their
// own GitHub account so that theirs go out as themselves (write_identity.go).
//
// Linking is GitHub's OAuth device flow (package ghauth): StartGitHubLink asks
// GitHub for a code and shows it to the user, the user enters it at
// github.com, and the server polls GitHub in the background until it hands
// over a token, while the client polls GetGitHubLink. The token is stored
// only once GitHub says it belongs to the registered login — registration is
// by claim, so this is what makes every write a verified identity's — and
// only sealed with CRS_SECRET_KEY, on the user's row.
//
// Linking needs the config's GitHubOAuthClientID and a valid CRS_SECRET_KEY.
// CRS_GITHUB_OAUTH_CLIENT_SECRET is optional; with it the server refreshes a
// GitHub App's expiring tokens and revokes a token on GitHub when its user
// unlinks it or is deleted.

const (
	secretKeyEnv         = "CRS_SECRET_KEY"
	oauthClientSecretEnv = "CRS_GITHUB_OAUTH_CLIENT_SECRET"
)

// The status of a user's link, as StartGitHubLink and GetGitHubLink report it.
const (
	linkUnavailable = "unavailable" // the server, or this connection, cannot link accounts
	linkUnlinked    = "unlinked"
	linkPending     = "pending" // a device flow is waiting for the user to enter the code
	linkLinked      = "linked"
	linkExpired     = "expired"  // the code expired before the user entered it
	linkDenied      = "denied"   // the user declined on GitHub
	linkMismatch    = "mismatch" // GitHub's login is not the registered one; nothing was stored
	linkError       = "error"
)

const (
	// maxLinkFlows bounds the device flows polling GitHub at once, one
	// goroutine each. A user has at most one; this caps them across users.
	maxLinkFlows = 64
	// linkResultTTL is how long a flow that ended without a link keeps its
	// outcome for GetGitHubLink to report before it is forgotten.
	linkResultTTL = 10 * time.Minute
	// linkCallTimeout bounds one call to GitHub made on the way to a link, a
	// refresh or a revocation, and how long unlinking waits for a flow to
	// wind down.
	linkCallTimeout = 30 * time.Second
)

// newLinkApp builds the app linking reaches GitHub through. Tests point it at
// a fake GitHub.
var newLinkApp = func(clientID, clientSecret, scopes string) *ghauth.App {
	return &ghauth.App{ClientID: clientID, ClientSecret: clientSecret, Scopes: scopes}
}

// linkSetup is what linking needs, read afresh each time: the client ID can
// change with UpdateConfig.
type linkSetup struct {
	// app is nil when no GitHubOAuthClientID is configured.
	app *ghauth.App
	// key seals and opens credentials; keyOK is false, and keyProblem says
	// why, when CRS_SECRET_KEY is unset or invalid.
	key        ghauth.Key
	keyOK      bool
	keyProblem string
}

func currentLinkSetup() linkSetup {
	cfg := config.C()
	var s linkSetup
	if cfg.GitHubOAuthClientID != "" {
		s.app = newLinkApp(cfg.GitHubOAuthClientID, strings.TrimSpace(os.Getenv(oauthClientSecretEnv)), cfg.GitHubOAuthScopes)
	}
	raw := os.Getenv(secretKeyEnv)
	if raw == "" {
		s.keyProblem = secretKeyEnv + " is not set"
	} else if key, err := ghauth.ParseKey(raw); err != nil {
		s.keyProblem = fmt.Sprintf("%s is invalid: %v", secretKeyEnv, err)
	} else {
		s.key, s.keyOK = key, true
	}
	return s
}

// unavailable says why the server cannot link accounts, or "" when it can.
func (s linkSetup) unavailable() string {
	switch {
	case s.app == nil:
		return "This server has no GitHubOAuthClientID configured, so GitHub accounts cannot be linked"
	case !s.keyOK:
		return fmt.Sprintf("This server cannot store GitHub credentials (%s), so GitHub accounts cannot be linked", s.keyProblem)
	}
	return ""
}

// hasClientSecret reports whether the server can refresh and revoke tokens.
func (s linkSetup) hasClientSecret() bool {
	return s.app != nil && s.app.ClientSecret != ""
}

// LogGitHubLinkSetup says at startup whether users can link their GitHub
// accounts, and if not what is missing. A key that is set but invalid is an
// error whatever else is configured: it quietly turns linking off. Never the
// secrets themselves.
func LogGitHubLinkSetup() {
	s := currentLinkSetup()
	if os.Getenv(secretKeyEnv) != "" && !s.keyOK {
		slog.Error("GitHub account linking is unavailable: "+s.keyProblem+"; generate a key with `openssl rand -hex 32`",
			"client_id_configured", s.app != nil)
		return
	}
	switch {
	case s.app == nil:
		slog.Info("GitHub account linking is off: no GitHubOAuthClientID is configured, so remote users cannot submit reviews or merge as themselves")
	case !s.keyOK:
		slog.Error("GitHub account linking is unavailable: " + s.keyProblem + ", and linked tokens are never stored unencrypted")
	case !s.hasClientSecret():
		slog.Info("GitHub account linking is available; " + oauthClientSecretEnv + " is not set, so expiring tokens cannot be refreshed and tokens are not revoked on GitHub when unlinked")
	default:
		slog.Info("GitHub account linking is available")
	}
}

// --- Device flows in progress ---

// linkFlow is one user's device flow. It lives in memory only: a restart
// drops it, and the user starts again.
type linkFlow struct {
	login string
	// tokenHash is the bearer token of the connection that started the flow.
	// The link is stored only while the login is still registered under it.
	tokenHash string
	cancel    context.CancelFunc
	// started is closed once GitHub has answered the request for a code, and
	// done once the flow's goroutine has returned.
	started chan struct{}
	done    chan struct{}

	// Guarded by linkFlows.mu.
	status          string
	message         string
	userCode        string
	verificationURI string
	expiresAt       time.Time
	interval        int
	endedAt         time.Time
}

// linkView is a flow as a reply shows it.
type linkView struct {
	status          string
	message         string
	userCode        string
	verificationURI string
	expiresAt       time.Time
	interval        int
}

// linkFlows tracks the device flows in progress, one per user, and for
// linkResultTTL the outcome of one that ended without a link.
type linkFlows struct {
	mu    sync.Mutex
	flows map[string]*linkFlow // by lower-cased login
	// slots holds a token per flow polling GitHub, bounding the goroutines.
	slots chan struct{}
}

func newLinkFlows(max int) *linkFlows {
	return &linkFlows{flows: map[string]*linkFlow{}, slots: make(chan struct{}, max)}
}

var githubLinks = newLinkFlows(maxLinkFlows)

// start starts a device flow for login, or returns the one already waiting
// for them, once GitHub has answered for its code.
func (l *linkFlows) start(login, tokenHash string, setup linkSetup) linkView {
	key := strings.ToLower(login)
	l.mu.Lock()
	l.pruneLocked(time.Now())
	if f := l.flows[key]; f != nil && f.status == linkPending && f.tokenHash == tokenHash {
		l.mu.Unlock()
		<-f.started
		return l.view(f)
	}
	select {
	case l.slots <- struct{}{}:
	default:
		l.mu.Unlock()
		slog.Warn("Refused a GitHub link: too many in progress", "login", login, "max", cap(l.slots))
		return linkView{status: linkError, message: "Too many GitHub links are in progress on this server; try again in a few minutes"}
	}
	// Whatever was here ended, or belongs to an earlier holder of the login.
	previous := l.flows[key]
	ctx, cancel := context.WithCancel(context.Background())
	f := &linkFlow{login: login, tokenHash: tokenHash, cancel: cancel,
		started: make(chan struct{}), done: make(chan struct{}), status: linkPending}
	l.flows[key] = f
	l.mu.Unlock()
	if previous != nil {
		previous.cancel()
	}

	go l.run(ctx, f, setup)
	<-f.started
	return l.view(f)
}

// run is a flow's goroutine: it asks GitHub for a code, polls until the user
// authorizes it or the flow ends, and stores the link.
func (l *linkFlows) run(ctx context.Context, f *linkFlow, setup linkSetup) {
	defer func() {
		f.cancel()
		<-l.slots
		close(f.done)
	}()

	requestCtx, cancelRequest := context.WithTimeout(ctx, linkCallTimeout)
	dc, err := setup.app.RequestDeviceCode(requestCtx)
	cancelRequest()
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("GitHub did not start a device flow", "login", f.login, "error", err)
		}
		l.end(f, linkError, fmt.Sprintf("GitHub did not start the link: %v", err))
		close(f.started)
		return
	}
	l.mu.Lock()
	f.userCode, f.verificationURI, f.expiresAt, f.interval = dc.UserCode, dc.VerificationURI, dc.ExpiresAt, dc.Interval
	l.mu.Unlock()
	close(f.started)
	slog.Info("Started a GitHub link", "login", f.login, "expires_at", dc.ExpiresAt.Format(time.RFC3339))

	tok, err := setup.app.PollToken(ctx, dc)
	switch {
	case err == nil:
		// complete revokes the token if the flow was cancelled meanwhile.
		l.complete(ctx, f, tok, setup)
	case ctx.Err() != nil:
		// Unlinked, deleted, or linking again: whoever cancelled the flow
		// has already dropped it.
		return
	case errors.Is(err, ghauth.ErrExpired):
		l.end(f, linkExpired, "The code expired before it was entered on GitHub; start the link again")
	case errors.Is(err, ghauth.ErrDenied):
		l.end(f, linkDenied, "The authorization was declined on GitHub, so nothing was linked")
	default:
		slog.Warn("A GitHub link failed", "login", f.login, "error", err)
		l.end(f, linkError, fmt.Sprintf("GitHub did not complete the link: %v", err))
	}
}

// complete stores the token a flow was granted, once GitHub confirms it is
// the registered login's. A token that is not stored is revoked, so nothing
// lingers that nobody holds — before the flow's outcome is recorded, so a
// client that sees the outcome finds the token already gone.
func (l *linkFlows) complete(ctx context.Context, f *linkFlow, tok *ghauth.Token, setup linkSetup) {
	discard := func(why, status, message string) {
		_ = revokeToken(setup, f.login, tok.AccessToken, why)
		if status != "" {
			l.end(f, status, message)
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, linkCallTimeout)
	githubLogin, err := setup.app.Login(callCtx, tok.AccessToken)
	cancel()
	switch {
	case ctx.Err() != nil:
		// Unlinked, deleted, or linking again meanwhile.
		discard("cancelled link", "", "")
		return
	case err != nil:
		slog.Warn("Could not tell whose GitHub account a link is", "login", f.login, "error", err)
		discard("unverified link", linkError, fmt.Sprintf("GitHub granted the link but did not say whose account it is (%v); start the link again", err))
		return
	case !strings.EqualFold(githubLogin, f.login):
		slog.Warn("A GitHub link was for another account; nothing was stored", "login", f.login, "github_login", githubLogin)
		discard("another account's link", linkMismatch, fmt.Sprintf("GitHub says that authorization is %s's, but this connection is registered as %s, so nothing was stored; sign in to GitHub as %s and link again",
			githubLogin, f.login, f.login))
		return
	}

	sealed, err := setup.key.Seal(f.login, ghauth.Credential{Token: *tok, GitHubLogin: githubLogin, LinkedAt: time.Now().UTC()})
	if err != nil {
		slog.Error("Could not seal a GitHub credential", "login", f.login, "error", err)
		discard("unstored link", linkError, "The link could not be stored; start it again")
		return
	}
	db := config.C().DB
	previous, _ := db.GetUserGitHubCredential(f.login)
	if ctx.Err() != nil {
		discard("cancelled link", "", "")
		return
	}
	if err := db.SetUserGitHubCredential(f.login, f.tokenHash, sealed); err != nil {
		message := fmt.Sprintf("The link could not be stored: %v", err)
		if errors.Is(err, database.ErrNotFound) {
			message = "You are no longer registered here under the token that started this link, so it was not stored; register and link again"
		}
		slog.Warn("Could not store a GitHub link", "login", f.login, "error", err)
		discard("unstored link", linkError, message)
		return
	}
	slog.Info("Linked a user's GitHub account", "login", f.login, "github_login", githubLogin, "expires", !tok.ExpiresAt.IsZero())
	l.drop(f)

	// Linking again replaces an earlier link; its token is no longer anyone's.
	if previous != "" && setup.keyOK {
		if old, err := setup.key.Open(f.login, previous); err == nil && old.AccessToken != tok.AccessToken {
			_ = revokeToken(setup, f.login, old.AccessToken, "replaced link")
		}
	}
}

// end records how a flow ended without a link, for GetGitHubLink to report.
func (l *linkFlows) end(f *linkFlow, status, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f.status, f.message, f.endedAt = status, message, time.Now()
}

// drop forgets a flow that linked: the stored credential reports it now.
func (l *linkFlows) drop(f *linkFlow) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if key := strings.ToLower(f.login); l.flows[key] == f {
		delete(l.flows, key)
	}
}

// lookup returns the flow login started under tokenHash: one in progress, or
// one that ended without a link within linkResultTTL. A flow an earlier
// holder of the login started is not theirs to see.
func (l *linkFlows) lookup(login, tokenHash string) (linkView, bool) {
	l.mu.Lock()
	l.pruneLocked(time.Now())
	f := l.flows[strings.ToLower(login)]
	l.mu.Unlock()
	if f == nil || f.tokenHash != tokenHash {
		return linkView{}, false
	}
	return l.view(f), true
}

// stop cancels login's flow, if any, and forgets it, reporting whether one
// was waiting for the user. With wait it also waits for the flow to wind
// down, after which nothing of it can store a link.
func (l *linkFlows) stop(login string, wait bool) bool {
	key := strings.ToLower(login)
	l.mu.Lock()
	f := l.flows[key]
	delete(l.flows, key)
	pending := f != nil && f.status == linkPending
	l.mu.Unlock()
	if f == nil {
		return false
	}
	f.cancel()
	if wait {
		select {
		case <-f.done:
		case <-time.After(linkCallTimeout):
			slog.Warn("A cancelled GitHub link did not wind down in time", "login", login)
		}
	}
	return pending
}

func (l *linkFlows) view(f *linkFlow) linkView {
	l.mu.Lock()
	defer l.mu.Unlock()
	return linkView{status: f.status, message: f.message, userCode: f.userCode,
		verificationURI: f.verificationURI, expiresAt: f.expiresAt, interval: f.interval}
}

// pruneLocked forgets the outcomes older than linkResultTTL.
func (l *linkFlows) pruneLocked(now time.Time) {
	for key, f := range l.flows {
		if f.status != linkPending && now.Sub(f.endedAt) > linkResultTTL {
			delete(l.flows, key)
		}
	}
}

// --- Stored links ---

// storedLink is a user's stored credential: sealed as the row holds it, and
// cred when this server can open it.
type storedLink struct {
	sealed string
	cred   *ghauth.Credential
}

// unreadable reports whether a credential is stored that the server cannot
// open: CRS_SECRET_KEY changed since it was sealed.
func (s storedLink) unreadable() bool {
	return s.sealed != "" && s.cred == nil
}

// loadLink reads login's stored credential. A user who does not exist has
// none.
func loadLink(login string, setup linkSetup) (storedLink, error) {
	sealed, err := config.C().DB.GetUserGitHubCredential(login)
	if errors.Is(err, database.ErrNotFound) {
		return storedLink{}, nil
	}
	if err != nil {
		return storedLink{}, err
	}
	if sealed == "" || !setup.keyOK {
		return storedLink{sealed: sealed}, nil
	}
	cred, err := setup.key.Open(login, sealed)
	if err != nil {
		slog.Warn("A stored GitHub credential does not open with this server's key", "login", login)
		return storedLink{sealed: sealed}, nil
	}
	return storedLink{sealed: sealed, cred: &cred}, nil
}

// githubLinkFieldsFor fills the reply flags for login: whether the server can
// link their account, and whether a link is stored that it can use. The
// server's own identity ("") never links.
func githubLinkFieldsFor(login string) githubLinkFields {
	if login == "" {
		return githubLinkFields{}
	}
	setup := currentLinkSetup()
	fields := githubLinkFields{GitHubLinkAvailable: setup.unavailable() == ""}
	if stored, err := loadLink(login, setup); err != nil {
		slog.Warn("Error reading a user's GitHub link", "login", login, "error", err)
	} else {
		fields.GitHubLinked = stored.cred != nil
	}
	return fields
}

// revokeToken revokes an access token on GitHub, best effort: without the
// client secret GitHub allows none, and a failure is only logged — the token
// is forgotten here either way.
func revokeToken(setup linkSetup, login, accessToken, why string) error {
	if !setup.hasClientSecret() {
		return fmt.Errorf("this server has no %s to revoke tokens with", oauthClientSecretEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), linkCallTimeout)
	defer cancel()
	if err := setup.app.Revoke(ctx, accessToken); err != nil {
		slog.Warn("Could not revoke a GitHub token", "login", login, "why", why, "error", err)
		return err
	}
	slog.Info("Revoked a GitHub token", "login", login, "why", why)
	return nil
}

// revokeSealed revokes the token in a stored credential, best effort.
func revokeSealed(setup linkSetup, login, sealed, why string) error {
	if !setup.hasClientSecret() {
		return fmt.Errorf("this server has no %s to revoke tokens with", oauthClientSecretEnv)
	}
	if !setup.keyOK {
		return errors.New(setup.keyProblem)
	}
	cred, err := setup.key.Open(login, sealed)
	if err != nil {
		return err
	}
	return revokeToken(setup, login, cred.AccessToken, why)
}

// revokeOutcome is the clause a reply adds about revoking a removed token.
func revokeOutcome(err error, whose string) string {
	if err == nil {
		return fmt.Sprintf("; %s token was revoked on GitHub", whose)
	}
	return fmt.Sprintf("; %s token could not be revoked on GitHub (%v), so revoke it under Settings → Applications on github.com", whose, err)
}

// ForgetDeletedUsers lets go of what the server still holds for users the
// retention sweep deleted: a link in progress is cancelled, and a linked
// token revoked on GitHub, in the background and best effort — deleting the
// row only forgot it. main registers it with workflows.SetUsersDeletedHook.
func ForgetDeletedUsers(deleted []database.DeletedUser) {
	var linked []database.DeletedUser
	for _, d := range deleted {
		// Not waited for: the cycle is running, and a flow finishing now
		// finds its user gone and stores nothing.
		githubLinks.stop(d.Login, false)
		if d.GitHubCredential != "" {
			linked = append(linked, d)
		}
	}
	if len(linked) == 0 {
		return
	}
	setup := currentLinkSetup()
	go func() {
		for _, d := range linked {
			_ = revokeSealed(setup, d.Login, d.GitHubCredential, "user deleted for inactivity")
		}
	}()
}

// --- RPCs ---

// linkIdentity returns the registered user a connection links an account
// for, or why it cannot: the local connection and the administrator token's
// session are the server's own identity, which writes with the server's
// token.
func (h *RPCHandler) linkIdentity() (login, tokenHash, refusal string) {
	if h.session == nil {
		return "", "", "This is the local connection: it writes to GitHub with the server's own token and links no account"
	}
	st := h.session.snapshot()
	if st.login == "" {
		return "", "", "This is the administrator token's session, the server's own identity, which writes to GitHub with the server's token; GitHub accounts are linked by registered users"
	}
	return st.login, st.tokenHash, ""
}

// formatLinkTime renders a time for a reply, "" for none.
func formatLinkTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type StartGitHubLinkArgs struct{}

type StartGitHubLinkReply struct {
	// Okay is true when a device flow is waiting for the user (status
	// "pending").
	Okay   bool   `json:"okay"`
	Status string `json:"status"`
	// UserCode is what the user enters at VerificationURI before ExpiresAt
	// (RFC 3339).
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresAt       string `json:"expires_at"`
	// Interval is the seconds GitHub asks between polls: how often a client
	// usefully polls GetGitHubLink.
	Interval int    `json:"interval"`
	Message  string `json:"message"`
}

// StartGitHubLink starts linking the connection's user's GitHub account, or
// returns the link already waiting for them. The server polls GitHub in the
// background; the client shows the code and polls GetGitHubLink until the
// status is no longer "pending". Linking again while linked replaces the
// link once the new one completes.
func (h *RPCHandler) StartGitHubLink(args *StartGitHubLinkArgs, reply *StartGitHubLinkReply) error {
	login, tokenHash, refusal := h.linkIdentity()
	if refusal != "" {
		reply.Status, reply.Message = linkUnavailable, refusal
		return nil
	}
	setup := currentLinkSetup()
	if why := setup.unavailable(); why != "" {
		reply.Status, reply.Message = linkUnavailable, why
		return nil
	}

	v := githubLinks.start(login, tokenHash, setup)
	reply.Status, reply.Message = v.status, v.message
	if v.status != linkPending {
		return nil
	}
	reply.Okay = true
	reply.UserCode, reply.VerificationURI = v.userCode, v.verificationURI
	reply.ExpiresAt, reply.Interval = formatLinkTime(v.expiresAt), v.interval
	reply.Message = fmt.Sprintf("Enter %s at %s to link your GitHub account (signed in to GitHub as %s); the code expires at %s",
		v.userCode, v.verificationURI, login, reply.ExpiresAt)
	return nil
}

type GetGitHubLinkArgs struct{}

type GetGitHubLinkReply struct {
	// Okay is false only when the connection cannot link at all: status
	// "unavailable".
	Okay   bool   `json:"okay"`
	Status string `json:"status"`
	// GitHubLogin is the linked GitHub account's login, whenever one is
	// linked — also while a new link is pending or after one failed.
	GitHubLogin string `json:"github_login"`
	// UserCode, VerificationURI and ExpiresAt are set while pending, as in
	// StartGitHubLink's reply.
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresAt       string `json:"expires_at"`
	Message         string `json:"message"`
}

// GetGitHubLink reports the connection's user's link: a link in progress, how
// the last one ended if it failed recently, or what is stored. It only reads.
func (h *RPCHandler) GetGitHubLink(args *GetGitHubLinkArgs, reply *GetGitHubLinkReply) error {
	login, tokenHash, refusal := h.linkIdentity()
	if refusal != "" {
		reply.Status, reply.Message = linkUnavailable, refusal
		return nil
	}
	// The flow first: one that links stores the credential before it is
	// forgotten, so a flow gone by now has left its link to be read below.
	v, inProgress := githubLinks.lookup(login, tokenHash)
	setup := currentLinkSetup()
	stored, err := loadLink(login, setup)
	if err != nil {
		return err
	}
	if stored.cred != nil {
		reply.GitHubLogin = stored.cred.GitHubLogin
	}

	reply.Okay = true
	if inProgress {
		reply.Status, reply.Message = v.status, v.message
		switch {
		case v.status == linkPending && v.userCode == "":
			reply.Message = "Asking GitHub for a code"
		case v.status == linkPending:
			reply.UserCode, reply.VerificationURI, reply.ExpiresAt = v.userCode, v.verificationURI, formatLinkTime(v.expiresAt)
			reply.Message = fmt.Sprintf("Waiting for %s to be entered at %s", v.userCode, v.verificationURI)
		}
		if stored.cred != nil {
			reply.Message += fmt.Sprintf("; your current link, to %s, stays until a new one completes", stored.cred.GitHubLogin)
		}
		return nil
	}

	switch {
	case stored.cred != nil:
		reply.Status = linkLinked
		reply.Message = fmt.Sprintf("Linked to GitHub as %s since %s: your reviews and merges go out as you",
			stored.cred.GitHubLogin, formatLinkTime(stored.cred.LinkedAt))
	case setup.unavailable() != "":
		reply.Okay = false
		reply.Status, reply.Message = linkUnavailable, setup.unavailable()
	case stored.unreadable():
		reply.Status = linkError
		reply.Message = "Your stored GitHub link can no longer be read by this server (its " + secretKeyEnv + " changed); link your account again"
	default:
		reply.Status = linkUnlinked
		reply.Message = "No GitHub account is linked; StartGitHubLink links yours"
	}
	return nil
}

type UnlinkGitHubArgs struct{}

type UnlinkGitHubReply struct {
	Okay    bool   `json:"okay"`
	Message string `json:"message"`
}

// UnlinkGitHub deletes the connection's user's stored GitHub credential —
// revoking its token on GitHub when the server has the client secret — and
// cancels a link in progress. Their writes no longer go out as them.
func (h *RPCHandler) UnlinkGitHub(args *UnlinkGitHubArgs, reply *UnlinkGitHubReply) error {
	login, _, refusal := h.linkIdentity()
	if refusal != "" {
		reply.Message = refusal
		return nil
	}
	// Waited for, so a flow completing now cannot store a link after the
	// credential is cleared below.
	cancelled := githubLinks.stop(login, true)
	sealed, err := config.C().DB.ClearUserGitHubCredential(login)
	if err != nil && !errors.Is(err, database.ErrNotFound) {
		slog.Error("Error unlinking a GitHub account", "login", login, "error", err)
		return err
	}

	reply.Okay = true
	switch {
	case sealed == "" && cancelled:
		reply.Message = "Cancelled the GitHub link in progress; no account is linked"
	case sealed == "":
		reply.Message = "No GitHub account was linked"
	default:
		revokeErr := revokeSealed(currentLinkSetup(), login, sealed, "unlinked")
		reply.Message = "Unlinked your GitHub account: your reviews and merges no longer go out as you" + revokeOutcome(revokeErr, "its")
		slog.Info("Unlinked a user's GitHub account", "login", login, "revoked", revokeErr == nil)
	}
	return nil
}
