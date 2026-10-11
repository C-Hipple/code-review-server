package server

import (
	"crs/config"
	"crs/git_tools"
	"errors"
	"log/slog"

	"github.com/google/go-github/v74/github"
)

// The write resolver: which GitHub account a write to GitHub goes out as.
//
// Everything the server reads from GitHub — the workflow cycle, the caches,
// GetPR on a miss — goes through its own token, CRS_GITHUB_TOKEN, for every
// connection alike. A write is different: GitHub records a review or a merge as
// the token's user, whoever pressed the button. On the local connection that is
// the person who started the server with their own token. On a remote one it
// would be someone else putting their words under the server owner's name, so
// a remote write only goes out under the server's token when the server's
// configuration says that is what its token is for (AllowServerIdentityActions,
// for an organization whose token is a bot account).
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

// errServerIdentityRefused is a remote write on a server that does not lend
// its own account to users. Clients match on the "refused:" prefix and report
// it as final.
var errServerIdentityRefused = errors.New("refused: this server posts to GitHub as its own account, so it does not submit reviews or merge on users' behalf; an administrator can enable AllowServerIdentityActions")

// errMergeAsServerNeedsAdmin is a remote merge under the server's account by
// someone who is not an administrator. Reviews carry their author's name in
// the text a reviewer sees; a merge carries nothing but the account, and lands
// the code.
var errMergeAsServerNeedsAdmin = errors.New("permission denied: RPCHandler.MergePR requires an administrator to merge as this server's GitHub account")

// writeClientFor returns the GitHub client a write from this connection goes
// out under, or why the connection may not make it. viewer is the login the
// handler serves the call as (read once, like everything else the call does).
//
// In order:
//   - the local connection writes as the server's token, as it always has;
//   - a remote connection writes as the server's token only when
//     AllowServerIdentityActions is on, and a merge then also needs an
//     administrator;
//   - otherwise the write is refused.
func (h *RPCHandler) writeClientFor(viewer string, action writeAction) (*github.Client, error) {
	if h.isLocal() {
		return git_tools.GetGithubClient(), nil
	}
	if config.C().AllowServerIdentityActions {
		if action == writeMerge && !h.isAdmin() {
			slog.Info("Refused a merge as the server's GitHub account to a user who is not an administrator", "login", viewer)
			return nil, errMergeAsServerNeedsAdmin
		}
		return git_tools.GetGithubClient(), nil
	}
	slog.Info("Refused a remote write as the server's GitHub account", "login", viewer, "action", string(action))
	return nil, errServerIdentityRefused
}
