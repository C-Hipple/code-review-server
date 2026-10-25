package workflows

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v74/github"
)

// While GitHub webhooks keep a PR's DB caches current, a workflow cycle reads
// the PR's aux data from those caches instead of asking GitHub for it again.
// This file is that read side: deciding whether the caches can answer for a
// PR, and turning them back into what the cycle and the filters work with.

// prLocks serializes the writes to one PR's caches between a cycle's fetch
// and a webhook delivery. A fetch holds the lock from its first request until
// its writes land, so a delivery that arrives meanwhile patches the fetched
// data instead of being overwritten by an older read of it. Keyed by the
// folded key: the fetch knows the PR by the config's spelling, the delivery by
// GitHub's, and both must take the same lock.
var prLocks sync.Map // folded PRKey -> *sync.Mutex

// lockPR locks key's caches and returns the unlock. Not reentrant: nothing
// holding it may call fetchAuxDataForPR, which takes it too.
func lockPR(key PRKey) func() {
	v, _ := prLocks.LoadOrStore(key.folded(), &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// cacheServes reports whether the DB caches hold everything a cycle reads
// about pr, for its current head: the diff, comments, reviews, commits, CI and
// metadata. A webhook-covered PR that passes is served from them; one that
// doesn't — new to us, pushed to, or with a fetch that failed — is fetched in
// full, and passes from then on.
//
// Review threads, reactions and required teams aren't required: they are
// best-effort in polling mode too, and a PR whose lookup of one keeps failing
// would otherwise be refetched in full every cycle.
func cacheServes(db *database.DB, key PRKey, pr *github.PullRequest) bool {
	if db == nil || pr == nil {
		return false
	}
	sha := pr.GetHead().GetSHA()
	if sha == "" {
		return false
	}
	diff, cachedSHA, err := db.GetPullRequest(key.Owner, key.Repo, key.Number)
	if err != nil || cachedSHA != sha || diff == "" {
		return false
	}
	for _, read := range []func() (string, error){
		func() (string, error) { return db.GetPRComments(key.Owner, key.Repo, key.Number) },
		func() (string, error) { return db.GetPRReviews(key.Owner, key.Repo, key.Number) },
		func() (string, error) { return db.GetPRCommits(key.Owner, key.Repo, key.Number) },
		func() (string, error) { return db.GetCIStatus(key.Owner, key.Repo, key.Number, sha) },
		func() (string, error) { return db.GetPRMetadataCache(key.Owner, key.Repo, key.Number) },
	} {
		if row, err := read(); err != nil || row == "" {
			return false
		}
	}
	return true
}

// planWebhookRefresh splits the PRs the manager is about to prefetch, during a
// webhook cycle, between the caches and GitHub. A covered PR whose caches are
// current is moved from prRequirements to fromCache; any other covered PR the
// cache warm hasn't already picked is warmed in full (and added to warmed, so
// it gets the post-update hooks a warmed PR gets). A PR outside the coverage
// keeps the requirements polling gave it. Outside a webhook cycle it changes
// nothing.
func planWebhookRefresh(db *database.DB,
	prObjects map[PRKey]*github.PullRequest,
	prRequirements map[PRKey]AuxDataRequirement,
	warmed []PRKey) (fromCache map[PRKey]AuxDataRequirement, warmedOut []PRKey) {

	fromCache = map[PRKey]AuxDataRequirement{}
	warmedOut = warmed
	for key, pr := range prObjects {
		if !webhookHandles(key) || slices.Contains(warmed, key) {
			continue
		}
		if cacheServes(db, key, pr) {
			fromCache[key] = prRequirements[key]
			delete(prRequirements, key)
			continue
		}
		prRequirements[key] = warmRequirement(prRequirements[key])
		warmedOut = append(warmedOut, key)
	}
	return fromCache, warmedOut
}

// fillCachedCIStatus gives a covered PR that a webhook cycle just fetched the
// same CI lines as one it reads from the caches, from the check runs and
// statuses the fetch wrote, so that rendering its item doesn't ask the Actions
// API for them.
func fillCachedCIStatus(db *database.DB, key PRKey, aux *PRAuxData) {
	if aux == nil || aux.CIStatus != nil || !webhookHandles(key) {
		return
	}
	if status, checkRuns, ok := loadCachedCI(db, key, aux.HeadSHA); ok {
		info := ciStatusInfo(status, checkRuns)
		aux.CIStatus = &info
	}
}

// loadCachedAuxData builds the aux data a cycle would otherwise have fetched
// for pr, from the DB caches. Every field Details() reads is filled — with
// empty values where the cache has none — so that rendering the PR's item
// never falls back to GitHub; the diff only when req asks for it, as it is the
// one that can be large and only IncludeDiff sections show it.
func loadCachedAuxData(db *database.DB, key PRKey, pr *github.PullRequest, req AuxDataRequirement) *PRAuxData {
	aux := &PRAuxData{
		HeadSHA: pr.GetHead().GetSHA(),
		BaseSHA: pr.GetBase().GetSHA(),
	}

	reviewComments := []*github.PullRequestComment{}
	if comments, ok := loadCachedComments(db, key); ok {
		for _, c := range comments {
			if !isConversationComment(c) && c.DiffHunk != nil && c.User.GetLogin() != "" && c.CreatedAt != nil {
				reviewComments = append(reviewComments, c)
			}
		}
	}
	// Review comments only, as when fetched: formatComments reads each
	// thread's diff hunk, which a conversation comment hasn't got.
	aux.Comments = reviewComments
	aux.CommentsCount = len(reviewComments)

	if reviews, ok := loadCachedReviews(db, key); ok {
		aux.Reviews = reviews
	}

	if req.Diff {
		if diff, sha, err := db.GetPullRequest(key.Owner, key.Repo, key.Number); err == nil && diff != "" && sha == aux.HeadSHA {
			aux.Diff = diff
			aux.DiffLines = []string{"*** Diff\n", diff}
		}
	}

	info := git_tools.CIStatusInfo{Statuses: []string{}, OverallStatus: "DONE"}
	if status, checkRuns, ok := loadCachedCI(db, key, aux.HeadSHA); ok {
		info = ciStatusInfo(status, checkRuns)
	}
	aux.CIStatus = &info
	return aux
}

// loadCachedComments reads the PR's comments — review comments and
// conversation comments together, as the PRComments cache keeps them. ok is
// false when nothing is cached.
func loadCachedComments(db *database.DB, key PRKey) ([]*github.PullRequestComment, bool) {
	raw, err := db.GetPRComments(key.Owner, key.Repo, key.Number)
	if err != nil || raw == "" {
		return nil, false
	}
	var comments []*github.PullRequestComment
	if err := json.Unmarshal([]byte(raw), &comments); err != nil {
		slog.Warn("Failed to decode cached PR comments", "pr", key.Number, "repo", key.Repo, "error", err)
		return nil, false
	}
	kept := make([]*github.PullRequestComment, 0, len(comments))
	for _, c := range comments {
		if c != nil {
			kept = append(kept, c)
		}
	}
	return kept, true
}

// isConversationComment tells a PR's conversation comments apart from its
// review comments in the PRComments cache, which keeps both as
// PullRequestComments (see convertIssueToPRComment). Their IDs come from
// different sequences, so the kind is part of a comment's identity.
func isConversationComment(c *github.PullRequestComment) bool {
	if u := c.GetURL(); u != "" {
		return strings.Contains(u, "/issues/comments/")
	}
	return c.DiffHunk == nil && c.Path == nil
}

// sortCommentsByCreation orders comments the way the fetch path does.
func sortCommentsByCreation(comments []*github.PullRequestComment) {
	sort.SliceStable(comments, func(i, j int) bool {
		if comments[i].CreatedAt == nil {
			return comments[j].CreatedAt != nil
		}
		if comments[j].CreatedAt == nil {
			return false
		}
		return comments[i].CreatedAt.Before(comments[j].CreatedAt.Time)
	})
}

// loadCachedReviews reads the PR's reviews back into the shape GitHub returns
// them in. ok is false when nothing is cached; a PR with no reviews is an
// empty, non-nil slice.
func loadCachedReviews(db *database.DB, key PRKey) ([]*github.PullRequestReview, bool) {
	raw, err := db.GetPRReviews(key.Owner, key.Repo, key.Number)
	if err != nil || raw == "" {
		return nil, false
	}
	var cached []cachedReview
	if err := json.Unmarshal([]byte(raw), &cached); err != nil {
		slog.Warn("Failed to decode cached PR reviews", "pr", key.Number, "repo", key.Repo, "error", err)
		return nil, false
	}
	reviews := make([]*github.PullRequestReview, 0, len(cached))
	for _, r := range cached {
		review := &github.PullRequestReview{
			ID:      github.Ptr(r.ID),
			User:    &github.User{Login: github.Ptr(r.User)},
			Body:    github.Ptr(r.Body),
			State:   github.Ptr(r.State),
			HTMLURL: github.Ptr(r.HTMLURL),
		}
		if !r.SubmittedAt.IsZero() {
			review.SubmittedAt = &github.Timestamp{Time: r.SubmittedAt}
		}
		reviews = append(reviews, review)
	}
	return reviews, true
}

// loadCachedCI reads a commit's statuses and check runs. ok is false when
// nothing is cached for sha; either half may still be nil when it is.
func loadCachedCI(db *database.DB, key PRKey, sha string) (*github.CombinedStatus, *github.ListCheckRunsResults, bool) {
	if sha == "" {
		return nil, nil, false
	}
	raw, err := db.GetCIStatus(key.Owner, key.Repo, key.Number, sha)
	if err != nil || raw == "" {
		return nil, nil, false
	}
	var row cachedCIRow
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		slog.Warn("Failed to decode cached CI status", "pr", key.Number, "repo", key.Repo, "sha", sha, "error", err)
		return nil, nil, false
	}
	return row.Status, row.CheckRuns, true
}

// loadCachedReviewThreads reads the PR's review threads. ok is false when
// nothing is cached.
func loadCachedReviewThreads(db *database.DB, key PRKey) ([]git_tools.ReviewThread, bool) {
	raw, err := db.GetPRReviewThreads(key.Owner, key.Repo, key.Number)
	if err != nil || raw == "" {
		return nil, false
	}
	var threads []git_tools.ReviewThread
	if err := json.Unmarshal([]byte(raw), &threads); err != nil {
		slog.Warn("Failed to decode cached review threads", "pr", key.Number, "repo", key.Repo, "error", err)
		return nil, false
	}
	if threads == nil {
		threads = []git_tools.ReviewThread{}
	}
	return threads, true
}

// latestCachedCommitTime is the newest time any of the PR's cached commits was
// authored or committed — GetInteractionState's LastCommitTime, from the
// cache. ok is false when nothing is cached.
func latestCachedCommitTime(db *database.DB, key PRKey) (time.Time, bool) {
	raw, err := db.GetPRCommits(key.Owner, key.Repo, key.Number)
	if err != nil || raw == "" {
		return time.Time{}, false
	}
	var commits []cachedCommit
	if err := json.Unmarshal([]byte(raw), &commits); err != nil {
		return time.Time{}, false
	}
	var latest time.Time
	for _, c := range commits {
		for _, stamp := range []string{c.Date, c.CommittedAt} {
			if t, err := time.Parse(time.RFC3339, stamp); err == nil && t.After(latest) {
				latest = t
			}
		}
	}
	return latest, true
}

// ciStatusInfo renders a commit's statuses and check runs in the shape
// git_tools.GetCIStatus gives the Actions runs on a branch: a line per check,
// and TODO (something failed), WAITING (something is still running) or DONE.
// A webhook-covered PR's item is drawn from this rather than from the Actions
// API, since check-run and status deliveries are what keep it current.
func ciStatusInfo(status *github.CombinedStatus, checkRuns *github.ListCheckRunsResults) git_tools.CIStatusInfo {
	lines := []string{}
	failed, running := false, false
	line := func(mark, state, name string) {
		lines = append(lines, fmt.Sprintf("[%s] %s %s", mark, state, name))
	}
	if checkRuns != nil {
		for _, run := range checkRuns.CheckRuns {
			if run == nil {
				continue
			}
			switch run.GetConclusion() {
			case "success":
				line("✅", "", run.GetName())
			case "failure", "timed_out", "cancelled", "action_required", "startup_failure":
				failed = true
				line("❌", "", run.GetName())
			case "":
				running = true
				line(" ", "["+run.GetStatus()+"]", run.GetName())
			default: // neutral, skipped, stale
				line(" ", "["+run.GetConclusion()+"]", run.GetName())
			}
		}
	}
	if status != nil {
		for _, s := range status.Statuses {
			if s == nil {
				continue
			}
			switch s.GetState() {
			case "success":
				line("✅", "", s.GetContext())
			case "failure", "error":
				failed = true
				line("❌", "", s.GetContext())
			default:
				running = true
				line(" ", "["+s.GetState()+"]", s.GetContext())
			}
		}
	}
	overall := "DONE"
	if failed {
		overall = "TODO"
	} else if running {
		overall = "WAITING"
	}
	return git_tools.CIStatusInfo{Statuses: lines, OverallStatus: overall}
}

// webhookPRState is the git_tools.PRStateSource registered while webhooks are
// on: it answers for a PR only during a webhook cycle, only when the PR's repo
// is covered, and only from caches that hold the PR's current head.
type webhookPRState struct{}

func (webhookPRState) InteractionState(owner, repo, login string, pr *github.PullRequest) (git_tools.InteractionState, bool) {
	key := PRKey{Owner: owner, Repo: repo, Number: pr.GetNumber()}
	db := config.C().DB
	if !webhookHandles(key) || !cacheServes(db, key, pr) {
		return git_tools.InteractionState{}, false
	}
	reviews, ok := loadCachedReviews(db, key)
	if !ok {
		return git_tools.InteractionState{}, false
	}
	comments, ok := loadCachedComments(db, key)
	if !ok {
		return git_tools.InteractionState{}, false
	}
	lastCommit, ok := latestCachedCommitTime(db, key)
	if !ok {
		return git_tools.InteractionState{}, false
	}
	var reviewComments []*github.PullRequestComment
	var conversation []*github.IssueComment
	for _, c := range comments {
		if isConversationComment(c) {
			conversation = append(conversation, &github.IssueComment{ID: c.ID, User: c.User, CreatedAt: c.CreatedAt})
		} else {
			reviewComments = append(reviewComments, c)
		}
	}
	// The caches say nothing about any one login, so they answer for
	// whichever is asking, as GetInteractionState's own fetch does.
	state := git_tools.CalculateInteractionState(login, pr, reviews, reviewComments, conversation)
	state.LastCommitTime = lastCommit
	return state, true
}

func (webhookPRState) CIStatus(pr *github.PullRequest) (git_tools.CIStatusInfo, bool) {
	key, ok := prKey(pr)
	db := config.C().DB
	if !ok || !webhookHandles(key) || !cacheServes(db, key, pr) {
		return git_tools.CIStatusInfo{}, false
	}
	status, checkRuns, ok := loadCachedCI(db, key, pr.GetHead().GetSHA())
	if !ok {
		return git_tools.CIStatusInfo{}, false
	}
	return ciStatusInfo(status, checkRuns), true
}
