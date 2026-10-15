package server

import (
	"context"
	"crs/config"
	"crs/ghauth"
	"crs/git_tools"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v74/github"
	"golang.org/x/oauth2"
)

// The write resolver: which GitHub account a write to GitHub goes out as.
//
// Everything the server reads from GitHub — the workflow cycle, the caches,
// GetPR on a miss — goes through its own token, CRS_GITHUB_TOKEN, for every
// connection alike. A write is different: GitHub records a review or a merge as
// the token's user, whoever pressed the button. On the local connection that is
// the person who started the server with their own token. On a remote one it
// is the user's own account when they have linked it (github_link.go), and
// otherwise would be someone else putting their words under the server
// owner's name, so a remote write only goes out under the server's token when
// the server's configuration says that is what its token is for
// (AllowServerIdentityActions, for an organization whose token is a bot
// account).
//
// Every write path asks writeClientFor, and asks before it changes anything,
// so the decision lives in one place.

// writeAction is a write a client asks the server to make on GitHub.
type writeAction string

const (
	// writeSubmitReview is SubmitReview: the review and the replies posted
	// with it.
	writeSubmitReview writeAction = "SubmitReview"
	// writeMerge is MergePR.
	writeMerge writeAction = "MergePR"
)

// refreshMargin is how close to expiring a linked token is refreshed before a
// write, so it does not expire halfway through a submit's calls.
const refreshMargin = 5 * time.Minute

// errServerIdentityRefused is a remote write on a server that does not lend
// its own account to users. Clients match on the "refused:" prefix and report
// it as final.
var errServerIdentityRefused = errors.New("refused: this server posts to GitHub as its own account, so it does not submit reviews or merge on users' behalf; an administrator can enable AllowServerIdentityActions")

// The refusals a user can do something about, by linking their GitHub
// account: clients match on "refused: link-github:" and offer to link.
var (
	// errLinkRequired is a write from a user who has not linked an account,
	// on a server that can link one and does not lend its own.
	errLinkRequired = errors.New("refused: link-github: link your GitHub account to submit reviews as yourself")
	// errLinkExpired is a write whose linked token GitHub no longer accepts:
	// revoked on github.com, or expired past refreshing. The stored
	// credential is cleared.
	errLinkExpired = errors.New("refused: link-github: your GitHub authorization has expired or was revoked; link your account again")
	// errLinkUnreadable is a write whose stored credential the server cannot
	// open since its CRS_SECRET_KEY changed. Never quietly replaced by the
	// server's account: the user linked to write as themselves.
	errLinkUnreadable = errors.New("refused: link-github: this server can no longer read your stored GitHub authorization; link your account again")
)

// errLinkUnusable is a write whose link has stopped working — expired,
// revoked, or unreadable — on a server that cannot link accounts any more:
// its GitHubOAuthClientID or CRS_SECRET_KEY was removed or is invalid. Not a
// link-github refusal, which would send the user to a link the server cannot
// make, and never quietly replaced by the server's account either.
var errLinkUnusable = errors.New("refused: this server can no longer use your linked GitHub account, and cannot link one until its administrator restores GitHub account linking")

// linkRefusal is the refusal of a write whose link stopped working: refusal,
// which offers to link again, when the server can link an account, else
// errLinkUnusable.
func linkRefusal(setup linkSetup, refusal error) error {
	if setup.unavailable() != "" {
		return errLinkUnusable
	}
	return refusal
}

// errMergeAsServerNeedsAdmin is a remote merge under the server's account by
// someone who is not an administrator. Reviews carry their author's name in
// the text a reviewer sees; a merge carries nothing but the account, and lands
// the code.
var errMergeAsServerNeedsAdmin = errors.New("permission denied: RPCHandler.MergePR requires an administrator to merge as this server's GitHub account")

// writeIdentity is the account a write goes out as, and the client that
// sends it.
type writeIdentity struct {
	client *github.Client
	// login and sealed are set when client sends a linked user's own token:
	// whose, and the stored credential it came from.
	login  string
	sealed string
}

// refused turns GitHub refusing a linked user's token (401) into the
// link-github refusal the handler returns, clearing the stored credential —
// only if it is still the one the write used, never a link made since. Any
// other error is the handler's to report, and refused returns nil.
func (w *writeIdentity) refused(err error) error {
	if w.sealed == "" || !isUnauthorized(err) {
		return nil
	}
	if _, clearErr := config.C().DB.ReplaceUserGitHubCredential(w.login, w.sealed, ""); clearErr != nil {
		slog.Error("Error clearing a linked GitHub token GitHub refused", "login", w.login, "error", clearErr)
	}
	slog.Info("GitHub refused a user's linked token; cleared it", "login", w.login)
	return linkRefusal(currentLinkSetup(), errLinkExpired)
}

// isUnauthorized reports whether GitHub answered a call with 401: the token
// is not, or no longer, one it accepts.
func isUnauthorized(err error) bool {
	var ghErr *github.ErrorResponse
	return errors.As(err, &ghErr) && ghErr.Response != nil && ghErr.Response.StatusCode == http.StatusUnauthorized
}

// writeClientFor returns the account a write from this connection goes out
// as, or why the connection may not make it. viewer is the login the handler
// serves the call as (read once, like everything else the call does).
//
// In order:
//   - the local connection writes as the server's token, as it always has;
//   - a remote user who linked their GitHub account writes as themselves,
//     their token refreshed first if it is about to expire;
//   - otherwise a remote connection writes as the server's token only when
//     AllowServerIdentityActions is on, and a merge then also needs an
//     administrator;
//   - otherwise the write is refused: with the link-github refusal when the
//     server can link the user's account, so their client offers to.
func (h *RPCHandler) writeClientFor(viewer string, action writeAction) (*writeIdentity, error) {
	if h.isLocal() {
		return &writeIdentity{client: git_tools.GetGithubClient()}, nil
	}
	setup := currentLinkSetup()
	if viewer != "" {
		linked, err := linkedWriteIdentity(viewer, setup)
		if err != nil || linked != nil {
			return linked, err
		}
	}
	if config.C().AllowServerIdentityActions {
		if action == writeMerge && !h.isAdmin() {
			slog.Info("Refused a merge as the server's GitHub account to a user who is not an administrator", "login", viewer)
			return nil, errMergeAsServerNeedsAdmin
		}
		return &writeIdentity{client: git_tools.GetGithubClient()}, nil
	}
	if viewer != "" && setup.unavailable() == "" {
		slog.Info("Refused a remote write from a user who has not linked their GitHub account", "login", viewer, "action", string(action))
		return nil, errLinkRequired
	}
	slog.Info("Refused a remote write as the server's GitHub account", "login", viewer, "action", string(action))
	return nil, errServerIdentityRefused
}

// linkedWriteIdentity returns the identity of login's linked GitHub account,
// or nil when they have linked none. A link that has stopped working is
// refused rather than replaced by the server's account — including one the
// server cannot open because CRS_SECRET_KEY is missing or invalid, say after
// a restart that lost it: the user linked to write as themselves.
func linkedWriteIdentity(login string, setup linkSetup) (*writeIdentity, error) {
	stored, err := loadLink(login, setup)
	if err != nil {
		return nil, err
	}
	switch {
	case stored.sealed == "":
		return nil, nil
	case stored.cred == nil:
		if !setup.keyOK {
			slog.Warn("Refused a write from a user whose stored GitHub link this server cannot open: "+setup.keyProblem, "login", login)
		}
		return nil, linkRefusal(setup, errLinkUnreadable)
	}
	cred, sealed := *stored.cred, stored.sealed
	if cred.ExpiresWithin(time.Now(), refreshMargin) {
		if cred, sealed, err = refreshLink(login, setup); err != nil {
			return nil, err
		}
	}
	return &writeIdentity{client: userGitHubClient(cred.AccessToken), login: login, sealed: sealed}, nil
}

// refreshLocks holds a mutex per lower-cased login. GitHub spends a refresh
// token on use, so two writes refreshing one user's token at once would leave
// the second holding a spent one — and clear a link that works.
var refreshLocks sync.Map

// refreshLink refreshes login's linked token and stores its successor. A
// token GitHub will not refresh — its refresh token spent or expired, or no
// client ID or secret to refresh with once it has expired — is cleared and
// refused as expired; a refresh that fails on the way is an ordinary error,
// and the link stays for the next try.
func refreshLink(login string, setup linkSetup) (ghauth.Credential, string, error) {
	mu, _ := refreshLocks.LoadOrStore(strings.ToLower(login), &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	// Read again: another write may have refreshed or cleared it meanwhile.
	stored, err := loadLink(login, setup)
	switch {
	case err != nil:
		return ghauth.Credential{}, "", err
	case stored.sealed == "":
		return ghauth.Credential{}, "", linkRefusal(setup, errLinkExpired)
	case stored.cred == nil:
		return ghauth.Credential{}, "", linkRefusal(setup, errLinkUnreadable)
	}
	cred, now := *stored.cred, time.Now()
	if !cred.ExpiresWithin(now, refreshMargin) {
		return cred, stored.sealed, nil
	}
	db := config.C().DB
	expire := func(why string) (ghauth.Credential, string, error) {
		if _, err := db.ReplaceUserGitHubCredential(login, stored.sealed, ""); err != nil {
			slog.Error("Error clearing an expired GitHub link", "login", login, "error", err)
		}
		slog.Info("A user's linked GitHub token expired; cleared it", "login", login, "why", why)
		return ghauth.Credential{}, "", linkRefusal(setup, errLinkExpired)
	}
	if !setup.hasClientSecret() || !cred.Refreshable(now) {
		if cred.ExpiresWithin(now, 0) {
			switch {
			case setup.app == nil:
				return expire("no GitHubOAuthClientID to refresh it with")
			case !setup.hasClientSecret():
				return expire("no client secret to refresh it with")
			}
			return expire("its refresh token has expired")
		}
		// Not expired yet: good for the write at hand.
		return cred, stored.sealed, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), linkCallTimeout)
	defer cancel()
	tok, err := setup.app.Refresh(ctx, cred.RefreshToken)
	if err != nil {
		var apiErr *ghauth.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "bad_refresh_token" {
			return expire("GitHub refused its refresh token")
		}
		slog.Warn("Could not refresh a user's linked GitHub token", "login", login, "error", err)
		return ghauth.Credential{}, "", fmt.Errorf("could not refresh your GitHub authorization: %w", err)
	}
	refreshed := ghauth.Credential{Token: *tok, GitHubLogin: cred.GitHubLogin, LinkedAt: cred.LinkedAt}
	sealed, err := setup.key.Seal(login, refreshed)
	if err != nil {
		return ghauth.Credential{}, "", err
	}
	swapped, err := db.ReplaceUserGitHubCredential(login, stored.sealed, sealed)
	if err != nil {
		return ghauth.Credential{}, "", err
	}
	if !swapped {
		// Unlinked while GitHub was refreshing it: the new token is nobody's.
		go revokeToken(setup, login, tok.AccessToken, "refreshed after unlinking")
		return ghauth.Credential{}, "", errLinkRequired
	}
	slog.Info("Refreshed a user's linked GitHub token", "login", login)
	return refreshed, sealed, nil
}

// userGitHubClient is a GitHub client that sends a linked user's own token.
// Unlike the server's (git_tools.GetGithubClient) it does not draw on the
// shared token's rate-limit budget: the user's token has a budget of its own.
func userGitHubClient(accessToken string) *github.Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken})
	return github.NewClient(oauth2.NewClient(context.Background(), ts))
}
