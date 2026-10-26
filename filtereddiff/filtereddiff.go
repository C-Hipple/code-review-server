// Package filtereddiff narrows a PR's diff to a range of its commits: the
// changes since the reviewer last reviewed it, or since they last took part in
// it at all. A Filter picks the commit the range starts after (its Baseline)
// from what the DB caches already hold about the PR — reviews, comments and
// commits — so choosing one never reaches GitHub. The server diffs the
// baseline against the PR's head (server/filtered_diffs.go), keeps the diff in
// the PRFilteredDiffs table keyed by the two SHAs, and serves it from there.
//
// The package imports nothing from the module, so the ai package can hand
// features a Result without pulling in the server.
package filtereddiff

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// IDs of the built-in filters.
const (
	SinceLastReview      = "since-last-review"
	SinceLastInteraction = "since-last-interaction"
)

// Statuses of a Result.
const (
	// StatusReady means Diff holds the changes since the baseline; empty when
	// the head hasn't moved since.
	StatusReady = "ready"
	// StatusNoBaseline means the filter found no commit to start from — the
	// reviewer never reviewed the PR, say. Message says why.
	StatusNoBaseline = "no-baseline"
	// StatusRebased means the baseline commit is no longer part of the PR's
	// history (a force-push rewrote it), so no diff from it describes what
	// changed. Clients fall back to the full diff.
	StatusRebased = "rebased"
	// StatusError means the diff could not be computed; Message says why.
	StatusError = "error"
)

// Kinds of interaction a Baseline is taken from.
const (
	KindReview        = "review"
	KindReviewComment = "review_comment"
	KindComment       = "comment"
)

// Filter picks where a filtered diff starts.
type Filter interface {
	// ID is the stable identifier the RPCs take, e.g. "since-last-review".
	ID() string
	// Name is the label clients show.
	Name() string
	// Description says what the filter shows, for a tooltip or help text.
	Description() string
	// Baseline picks the commit the range starts after. A PR that offers none
	// returns a *NoBaselineError saying why.
	Baseline(in Inputs) (Baseline, error)
}

// NoBaselineError is a filter finding nothing to start from. It is an
// answer, not a failure: the server reports it as StatusNoBaseline.
type NoBaselineError struct {
	Reason string
}

func (e *NoBaselineError) Error() string { return e.Reason }

// Inputs is what a filter reads about a PR, from the DB caches.
type Inputs struct {
	// Me is the reviewer's GitHub login.
	Me      string
	HeadSHA string
	Reviews []Review
	// Comments holds review comments and conversation comments alike.
	Comments []Comment
	// Commits are the PR's commits in the order GitHub lists them, oldest
	// first.
	Commits []Commit
}

// Review is one review on the PR.
type Review struct {
	ID          int64
	User        string
	State       string
	SubmittedAt time.Time
	// CommitID is the commit the review was submitted against. Empty for a
	// review cached before it was recorded.
	CommitID string
}

// Comment is one comment on the PR.
type Comment struct {
	ID        int64
	Author    string
	CreatedAt time.Time
	// CommitID is the commit a review comment was left on; empty for a
	// conversation comment, which belongs to no commit.
	CommitID string
}

// Commit is one of the PR's commits.
type Commit struct {
	SHA string
	// At is when the commit was committed, or authored when that is all the
	// cache holds. It stands in for when the commit reached the PR, which
	// GitHub doesn't report.
	At time.Time
}

// Baseline is the commit a filtered diff starts after, and the interaction
// it was taken from.
type Baseline struct {
	SHA string `json:"sha"`
	// At is when the interaction happened.
	At time.Time `json:"at"`
	// Kind is KindReview, KindReviewComment or KindComment.
	Kind string `json:"kind"`
	// Detail describes the interaction, e.g. "your APPROVED review".
	Detail string `json:"detail"`
	// Inferred is set when the interaction names no commit, so SHA is the
	// newest commit older than it — the head the reviewer most likely saw.
	Inferred bool `json:"inferred"`
}

// Result is a filtered diff as the server serves it.
type Result struct {
	Filter  string `json:"filter"`
	Status  string `json:"status"`
	HeadSHA string `json:"head_sha"`
	// BaseSHA is the baseline commit; empty without one.
	BaseSHA  string    `json:"base_sha"`
	Baseline *Baseline `json:"baseline"`
	// Diff is the unified diff from BaseSHA to HeadSHA, rendered the way the
	// server renders a PR's full diff. Empty unless Status is StatusReady.
	Diff string `json:"diff"`
	// Commits are the SHAs of the PR's commits after the baseline, oldest
	// first, as far as the cached commit list knows them.
	Commits []string `json:"commits"`
	// Message explains any status but StatusReady.
	Message string `json:"message"`
	// ComputedAt is when the diff was fetched; zero when none was needed.
	ComputedAt time.Time `json:"computed_at"`
}

// Info describes a filter to clients.
type Info struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

var (
	registryMu sync.RWMutex
	registry   []Filter
)

func init() {
	Register(sinceLastReview{})
	Register(sinceLastInteraction{})
}

// Register adds a filter, replacing any registered under the same ID. Filters
// are listed in registration order.
func Register(f Filter) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for i, existing := range registry {
		if existing.ID() == f.ID() {
			registry[i] = f
			return
		}
	}
	registry = append(registry, f)
}

// Filters returns every registered filter in registration order.
func Filters() []Filter {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return append([]Filter(nil), registry...)
}

// Lookup returns the filter registered under id.
func Lookup(id string) (Filter, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	for _, f := range registry {
		if f.ID() == id {
			return f, true
		}
	}
	return nil, false
}

// Describe returns what clients show of each registered filter.
func Describe() []Info {
	filters := Filters()
	infos := make([]Info, 0, len(filters))
	for _, f := range filters {
		infos = append(infos, Info{ID: f.ID(), Name: f.Name(), Description: f.Description()})
	}
	return infos
}

// ContainsCommit reports whether sha is one of the PR's commits — that is,
// whether the head still descends from it.
func (in Inputs) ContainsCommit(sha string) bool {
	return commitIndex(in.Commits, sha) >= 0
}

// CommitsAfter returns the SHAs of the PR's commits after sha, oldest first.
// Nil when sha isn't among them.
func (in Inputs) CommitsAfter(sha string) []string {
	i := commitIndex(in.Commits, sha)
	if i < 0 {
		return nil
	}
	after := []string{}
	for _, c := range in.Commits[i+1:] {
		after = append(after, c.SHA)
	}
	return after
}

func commitIndex(commits []Commit, sha string) int {
	if sha == "" {
		return -1
	}
	for i, c := range commits {
		if c.SHA == sha {
			return i
		}
	}
	return -1
}

// headAt returns the commit most likely to have been the PR's head at t: the
// last one, in PR order, that existed by then. Empty when none had.
func headAt(commits []Commit, t time.Time) string {
	sha := ""
	for _, c := range commits {
		if !c.At.IsZero() && !c.At.After(t) {
			sha = c.SHA
		}
	}
	return sha
}

// latestReview returns the reviewer's most recent submitted review. A pending
// review (a draft GitHub holds unsubmitted) has no submission time and is
// skipped.
func latestReview(in Inputs) (Review, bool) {
	var latest Review
	found := false
	for _, r := range in.Reviews {
		if r.SubmittedAt.IsZero() || !strings.EqualFold(r.User, in.Me) || strings.EqualFold(r.State, "PENDING") {
			continue
		}
		if !found || r.SubmittedAt.After(latest.SubmittedAt) {
			latest = r
			found = true
		}
	}
	return latest, found
}

// reviewBaseline is where a review leaves the reviewer: the commit it was
// submitted against.
func reviewBaseline(in Inputs, r Review) (Baseline, error) {
	b := Baseline{
		SHA:    r.CommitID,
		At:     r.SubmittedAt,
		Kind:   KindReview,
		Detail: fmt.Sprintf("your %s review", reviewStateLabel(r.State)),
	}
	if b.SHA == "" {
		b.SHA = headAt(in.Commits, r.SubmittedAt)
		b.Inferred = true
	}
	if b.SHA == "" {
		return Baseline{}, &NoBaselineError{Reason: "Couldn't tell which commit your last review covered."}
	}
	return b, nil
}

func reviewStateLabel(state string) string {
	switch strings.ToUpper(state) {
	case "APPROVED":
		return "approving"
	case "CHANGES_REQUESTED":
		return "changes-requested"
	case "DISMISSED":
		return "dismissed"
	default:
		return "comment"
	}
}

type sinceLastReview struct{}

func (sinceLastReview) ID() string   { return SinceLastReview }
func (sinceLastReview) Name() string { return "Since my last review" }
func (sinceLastReview) Description() string {
	return "Changes pushed since the commit your most recent review was submitted against."
}

func (sinceLastReview) Baseline(in Inputs) (Baseline, error) {
	if in.Me == "" {
		return Baseline{}, &NoBaselineError{Reason: "The server doesn't know your GitHub login (set GithubUsername in the config)."}
	}
	r, ok := latestReview(in)
	if !ok {
		return Baseline{}, &NoBaselineError{Reason: "You haven't reviewed this PR yet."}
	}
	return reviewBaseline(in, r)
}

type sinceLastInteraction struct{}

func (sinceLastInteraction) ID() string   { return SinceLastInteraction }
func (sinceLastInteraction) Name() string { return "Since my last interaction" }
func (sinceLastInteraction) Description() string {
	return "Changes pushed since you last reviewed or commented on the PR."
}

// Baseline takes the reviewer's most recent review or comment. A review or a
// review comment names the commit it was left on; a conversation comment
// doesn't, so the head at the time stands in.
func (sinceLastInteraction) Baseline(in Inputs) (Baseline, error) {
	if in.Me == "" {
		return Baseline{}, &NoBaselineError{Reason: "The server doesn't know your GitHub login (set GithubUsername in the config)."}
	}

	type interaction struct {
		at       time.Time
		baseline func() (Baseline, error)
	}
	var candidates []interaction
	if r, ok := latestReview(in); ok {
		candidates = append(candidates, interaction{r.SubmittedAt, func() (Baseline, error) { return reviewBaseline(in, r) }})
	}
	for _, c := range in.Comments {
		if c.CreatedAt.IsZero() || !strings.EqualFold(c.Author, in.Me) {
			continue
		}
		candidates = append(candidates, interaction{c.CreatedAt, func() (Baseline, error) { return commentBaseline(in, c) }})
	}
	if len(candidates) == 0 {
		return Baseline{}, &NoBaselineError{Reason: "You haven't reviewed or commented on this PR yet."}
	}
	// Stable, so of a review and its own comments — posted together — the
	// review wins a tie.
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].at.After(candidates[j].at) })
	return candidates[0].baseline()
}

func commentBaseline(in Inputs, c Comment) (Baseline, error) {
	b := Baseline{SHA: c.CommitID, At: c.CreatedAt, Kind: KindReviewComment, Detail: "your review comment"}
	if b.SHA == "" {
		b = Baseline{SHA: headAt(in.Commits, c.CreatedAt), At: c.CreatedAt, Kind: KindComment, Detail: "your comment", Inferred: true}
	}
	if b.SHA == "" {
		return Baseline{}, &NoBaselineError{Reason: "Couldn't tell which commit was the head when you last commented."}
	}
	return b, nil
}
