package forge

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/go-github/v74/github"
)

// The data model every forge speaks. Pull requests, comments, reviews,
// commits and CI results are GitHub's REST schema as go-github models it:
// the DB caches hold them as that JSON, and the workflows, filters and
// renderer read their fields, so a driver for another forge translates its
// own objects into these. They are aliases, so code written against
// go-github's names and code written against these is the same code — and
// moving to forge-neutral structs later is a change here that the compiler
// then walks through every use of.
type (
	PullRequest              = github.PullRequest
	PullRequestComment       = github.PullRequestComment
	IssueComment             = github.IssueComment
	PullRequestReview        = github.PullRequestReview
	PullRequestReviewRequest = github.PullRequestReviewRequest
	PullRequestMergeResult   = github.PullRequestMergeResult
	RepositoryCommit         = github.RepositoryCommit
	Reviewers                = github.Reviewers
	CombinedStatus           = github.CombinedStatus
	ListCheckRunsResults     = github.ListCheckRunsResults
)

// PRRef identifies a single pull request.
type PRRef struct {
	Owner  string
	Repo   string
	Number int
}

func (r PRRef) String() string {
	return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
}

// ReviewThread is the resolution state of one review-comment thread, keyed back
// to REST by the database IDs of the comments it contains.
type ReviewThread struct {
	// ID is the GraphQL node ID of the thread (usable with resolveReviewThread).
	ID string `json:"id"`
	// IsResolved is true once someone marked the conversation resolved.
	IsResolved bool `json:"is_resolved"`
	// IsOutdated is GitHub's own judgement that the thread's lines no longer
	// exist in the current diff. It is tracked separately from the position-based
	// outdated flag the REST path computes, and is the more reliable of the two.
	IsOutdated bool `json:"is_outdated"`
	// ResolvedBy is the login of whoever resolved it, empty when unresolved.
	ResolvedBy string `json:"resolved_by"`
	Path       string `json:"path"`
	// CommentIDs are the REST/database IDs of every comment in the thread, in
	// posting order. These match CommentJSON.ID on the wire.
	CommentIDs []int64 `json:"comment_ids"`
}

// Reaction is one emoji's worth of reactions on a single comment or review:
// which emoji it is, and who left it.
type Reaction struct {
	// Content is the REST-style name of the emoji ("+1", "heart", "rocket"),
	// not GraphQL's THUMBS_UP spelling, so it matches what the rest of GitHub's
	// API — and anything a client already knows about reactions — calls it.
	Content string `json:"content"`
	// Emoji is the character itself. It lives on the wire so the web client and
	// the Emacs client don't each carry their own copy of the mapping.
	Emoji string `json:"emoji"`
	// Users are the logins that left this reaction, capped by the driver.
	// Count is the forge's own total, so Count > len(Users) means the list
	// was truncated.
	Users []string `json:"users"`
	Count int      `json:"count"`
	// ViewerReacted is true when the account the forge's token belongs to is
	// one of the reactors, which is what lets a client render its own reaction
	// differently from everyone else's.
	ViewerReacted bool `json:"viewer_reacted"`
}

// PRReactions is every reaction on a PR's comments and reviews, keyed by the
// REST database ID of the thing that was reacted to (as a string, matching
// CommentJSON.ID on the wire).
//
// Comments covers both review comments and conversation comments, which is the
// same single ID space the comment caches already merge them into.
type PRReactions struct {
	Comments map[string][]Reaction `json:"comments"`
	Reviews  map[string][]Reaction `json:"reviews"`
}

// Empty reports whether nothing on the PR has been reacted to. A non-nil but
// empty result is a successful fetch, not a missing one.
func (r *PRReactions) Empty() bool {
	return r == nil || (len(r.Comments) == 0 && len(r.Reviews) == 0)
}

// ForComment returns the reactions on one comment, by its REST ID.
func (r *PRReactions) ForComment(id string) []Reaction {
	if r == nil {
		return nil
	}
	return r.Comments[id]
}

// ForReview returns the reactions on one review body, by its REST ID.
func (r *PRReactions) ForReview(id int64) []Reaction {
	if r == nil {
		return nil
	}
	return r.Reviews[strconv.FormatInt(id, 10)]
}

// A PR's mergeability: REST's mergeable true/false/null, or GraphQL's
// MergeableState.
const (
	// MergeabilityMergeable means the head merges into the base cleanly.
	MergeabilityMergeable = "mergeable"
	// MergeabilityConflicting means the head conflicts with the base: the
	// forge won't merge the PR until the conflicts are resolved.
	MergeabilityConflicting = "conflicting"
	// MergeabilityUnknown means the forge hasn't worked it out yet.
	MergeabilityUnknown = "unknown"
)

// Mergeability is the forge's answer for one PR, together with the head commit
// it was given for: the answer is about that commit, so a push makes it stale.
type Mergeability struct {
	State   string
	HeadSHA string
}

// TeamRef identifies one team.
type TeamRef struct {
	// Name is the display name, e.g. "Platform Engineering".
	Name string `json:"name"`
	// Slug is the API name, e.g. "platform-engineering".
	Slug string `json:"slug"`
	// Org is the login of the organization that owns the team.
	Org string `json:"org"`
}

// Key is the "org/slug" form GitHub uses for team references, and the key both
// the member cache and MyTeamRefs are indexed by.
func (t TeamRef) Key() string {
	return t.Org + "/" + t.Slug
}

// ReviewRequestHistory is every reviewer a PR has ever been assigned, including
// the ones the forge has since cleared because they reviewed.
type ReviewRequestHistory struct {
	Teams []TeamRef
	Users []string
}

// CIStatusInfo is a branch's CI standing as the CI filters and the review
// list read it: one line per workflow, and an org-mode keyword summing them
// up ("DONE", "WAITING" or "TODO").
type CIStatusInfo struct {
	Statuses      []string
	OverallStatus string
}

// InteractionSet is the set of open PRs a login has reviewed or commented on,
// keyed by InteractionKey. A nil set means the forge couldn't say; see
// MayHaveInteracted for how that fails open.
type InteractionSet map[string]bool

// InteractionKey is a PR's key in an InteractionSet: lowercased
// "owner/repo#number".
func InteractionKey(owner, repo string, number int) string {
	return strings.ToLower(fmt.Sprintf("%s/%s#%d", owner, repo, number))
}

// MayHaveInteracted reports whether pr might carry a review or comment from
// the login this set was built for. A nil set (searches unavailable) and a PR
// with incomplete repo data both return true: the only safe direction to be
// wrong in is doing a full InteractionState check unnecessarily.
func (s InteractionSet) MayHaveInteracted(pr *PullRequest) bool {
	if s == nil {
		return true
	}
	if pr == nil || pr.Number == nil || pr.Base == nil || pr.Base.Repo == nil ||
		pr.Base.Repo.Owner == nil || pr.Base.Repo.Owner.Login == nil || pr.Base.Repo.Name == nil {
		return true
	}
	return s[InteractionKey(*pr.Base.Repo.Owner.Login, *pr.Base.Repo.Name, *pr.Number)]
}

// Statuses CompareStatus reports, as GitHub's compare endpoint names them.
const (
	CompareAhead     = "ahead"
	CompareBehind    = "behind"
	CompareDiverged  = "diverged"
	CompareIdentical = "identical"
	// CompareMissing is a base the forge no longer has (or never had) a
	// history in common with head — a commit a force-push left behind and that
	// has since been collected. Not a GitHub status.
	CompareMissing = "missing"
)

// ErrNoCommonHistory is CompareDiff's answer when the forge can't relate the
// two commits at all.
var ErrNoCommonHistory = errors.New("the commits share no history GitHub can compare")
