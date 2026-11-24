// Package forge is the seam between the review dashboard and the services its
// pull requests live on — GitHub today, others later.
//
// The workflows and the RPC server never talk to a forge's API themselves:
// they hold a Forge, which a workflow gets from the config's name for it
// (For) and the server from the repository a client asks about (ForRepo).
// Each kind of forge is a driver that registers itself here (Register); the
// GitHub driver is in git_tools. A new forge is a new driver and a kind in
// config.ForgeKinds, not a change to the code that calls it.
//
// What a forge hands back is the model in model.go: GitHub's REST schema,
// which the DB caches are written in, so a driver for another forge
// translates its objects into it.
package forge

import "context"

// Forge is one forge, as the workflows and the server use it.
//
// Methods that take a context honor it; the others bound their own requests.
// Owner and repo name a repository the way the forge does ("C-Hipple",
// "code-review-server").
type Forge interface {
	// ID is the config's name for this forge (config.ForgeGitHub for the
	// built-in one), and Kind the driver serving it (a config.ForgeKinds
	// entry).
	ID() string
	Kind() string

	Identity
	PRSource
	PRContent
	CI
	Repository
	Reviewing
}

// Identity is who the forge's token belongs to.
type Identity interface {
	// Configured reports whether the forge has what it needs to be reached —
	// a token. Code that runs before anything has proven the forge reachable
	// checks it first.
	Configured() bool
	// AuthenticatedLogin is the login the token belongs to, "" when it
	// can't be looked up.
	AuthenticatedLogin() string
	// MyTeamRefs is every team that login belongs to, as "org/team-slug".
	MyTeamRefs() []string
}

// PRSource finds pull requests.
type PRSource interface {
	// ListPRs lists a repository's PRs in state ("open", "closed", "all"),
	// most recently updated first. A successful fetch is never nil, even with
	// no PRs, so callers can tell it from a failed one.
	ListPRs(owner, repo, state string) ([]*PullRequest, error)
	// GetPR fetches one PR, with the fields only a single-PR fetch carries
	// (mergeability).
	GetPR(ctx context.Context, owner, repo string, number int) (*PullRequest, error)
	// GetPRs fetches each of numbers, failing as a whole if any fails.
	GetPRs(owner, repo string, numbers []int) ([]*PullRequest, error)
	// GetPRsForRefs fetches the PRs refs name, across repositories.
	GetPRsForRefs(refs []PRRef) ([]*PullRequest, error)
	// ReviewRequestedRefs finds every open PR whose review is requested from
	// login or a team login is on, without a repository list.
	ReviewRequestedRefs(login string) ([]PRRef, error)
	// MyInteractionRefs finds every open PR login has reviewed or commented
	// on, without a repository list.
	MyInteractionRefs(login string) ([]PRRef, error)
	// OpenInteractions is the same set as MyInteractionRefs, shaped for
	// testing membership; nil when the forge can't say, which callers treat
	// as "anything may be in it".
	OpenInteractions(login string) InteractionSet
}

// PRContent reads what is on a pull request.
type PRContent interface {
	// Diff is the PR's unified diff.
	Diff(ctx context.Context, owner, repo string, number int) (string, error)
	// ReviewComments are the comments on the PR's code; IssueComments those
	// in its conversation.
	ReviewComments(ctx context.Context, owner, repo string, number int) ([]*PullRequestComment, error)
	IssueComments(ctx context.Context, owner, repo string, number int) ([]*IssueComment, error)
	Reviews(ctx context.Context, owner, repo string, number int) ([]*PullRequestReview, error)
	Commits(ctx context.Context, owner, repo string, number int) ([]*RepositoryCommit, error)
	// RequestedReviewers are the users and teams whose review is still
	// requested.
	RequestedReviewers(ctx context.Context, owner, repo string, number int) (*Reviewers, error)
	// ReviewThreads is whether each review-comment thread is resolved.
	ReviewThreads(owner, repo string, number int) ([]ReviewThread, error)
	// Reactions is who reacted to each comment and review.
	Reactions(owner, repo string, number int) (*PRReactions, error)
	// ReviewRequestHistory is every reviewer the PR was ever asked for,
	// including those the forge has since cleared because they reviewed.
	ReviewRequestHistory(owner, repo string, number int) (ReviewRequestHistory, error)
	// TeamMembers maps each team's Key to its members' logins, leaving out
	// teams whose members can't be listed.
	TeamMembers(teams []TeamRef) map[string][]string
	// Mergeability is whether each PR merges cleanly into its base.
	Mergeability(refs []PRRef) (map[PRRef]Mergeability, error)
}

// CI reads a PR's checks.
type CI interface {
	// CIStatus sums up the CI runs on a branch, as the CI filters read it.
	// branch may carry the "user:" prefix a PR's head label has.
	CIStatus(owner, repo, branch string) CIStatusInfo
	// CombinedStatus and CheckRuns are every status and check on a commit,
	// for the review view and the DB cache.
	CombinedStatus(owner, repo, ref string) (*CombinedStatus, error)
	CheckRuns(owner, repo, ref string) (*ListCheckRunsResults, error)
}

// Repository reads a repository's files and history.
type Repository interface {
	// FileContent is a file's content at ref (a branch, tag or SHA).
	FileContent(owner, repo, path, ref string) (string, error)
	// CompareDiff is the unified diff from base to head, when base is an
	// ancestor of head; ErrNoCommonHistory when the two aren't related.
	CompareDiff(ctx context.Context, owner, repo, base, head string) (string, error)
	// CompareStatus is how head relates to base: one of the Compare*
	// constants.
	CompareStatus(ctx context.Context, owner, repo, base, head string) (string, error)
	// PRHeadRef is the ref the forge keeps every PR's head at, for fetching a
	// PR whose branch is gone or on a fork: "refs/pull/<n>/head" on GitHub.
	PRHeadRef(number int) string
}

// Reviewing writes to a pull request.
type Reviewing interface {
	SubmitReview(owner, repo string, number int, review *PullRequestReviewRequest) error
	// SubmitReply posts body in reply to the review comment replyToID,
	// joining its thread.
	SubmitReply(owner, repo string, number int, body string, replyToID int64) error
	// MergePR merges the PR with method ("merge", "squash" or "rebase"),
	// handing back the forge's answer for the caller to read.
	MergePR(owner, repo string, number int, method string) (*PullRequestMergeResult, error)
}
