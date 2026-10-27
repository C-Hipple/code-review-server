package workflows

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/go-github/v74/github"
)

// Applying a webhook delivery to the DB caches. Only PRs we already hold are
// touched (see trackedPR) — a delivery about any other PR is dropped, and the
// cycle that first lists it fetches it in full. Every cache is patched in place
// from the delivery wherever the delivery says enough to; the GitHub API is called only
// for what no delivery carries: the diff and commits after a push, and the
// GraphQL ID of a review thread someone just started.
//
// A list cache (reviews, comments, review threads, a commit's CI) is only ever
// patched, never started from a delivery: a list holding just the one review a
// delivery mentions would read as the PR's complete set. A PR whose cache is
// missing is left to the next cycle, which fetches it in full.

// refetchPR fetches a PR in full, as a cycle fetches one it is adding, and runs
// the post-update hooks (plugins, automatic AI features) for its new head. A
// variable so tests can stand in for GitHub.
var refetchPR = func(key PRKey, pr *github.PullRequest, detail string) {
	apiCalls := &apiCallCounter{}
	fetchAuxDataForPR(git_tools.GetGithubClient(), apiCalls, webhookWorkflowName, key, warmRequirement(AuxDataRequirement{}), pr)
	webhookActivity.refetches.Add(1)
	webhookActivity.apiCalls.Add(apiCalls.total())
	slog.Info("Fetched a PR in full after a webhook delivery", "pr", key.Number, "repo", key.Repo, "event", detail, "api_calls", apiCalls.total())
	notifyPRsUpdated([]PRKey{key})
}

// fetchReviewThreads looks up a PR's review threads. A variable so tests can
// stand in for GitHub.
var fetchReviewThreads = git_tools.GetReviewThreads

// heldPR is a PR a delivery was about that we hold, with the PR as the delivery
// describes it — nil for a delivery that carries no PR object (a comment on
// the conversation, a check run, a commit status).
type heldPR struct {
	key PRKey
	pr  *github.PullRequest
	// changes names what the delivery rewrote for the PR, and summary what it
	// says happened (see PRChange). A PR it changed nothing for — its caches
	// missing, say — isn't announced.
	changes []string
	summary string
}

// applyWebhookEvent applies one delivery to the caches of the PRs it is about,
// tells the clients about each one it changed (see webhook_changes.go), then
// has each of them checked against the workflows again: what changed may move
// it into a section or out of one (see webhook_membership.go).
func applyWebhookEvent(d webhookDelivery) {
	db := config.C().DB
	if db == nil {
		return
	}
	var held []heldPR
	reevaluate := true
	actor := ""
	if e, ok := d.event.(interface{ GetSender() *github.User }); ok {
		actor = e.GetSender().GetLogin()
	}
	switch e := d.event.(type) {
	case *github.PullRequestEvent:
		held = applyPullRequestEvent(db, d, e)
	case *github.PullRequestReviewEvent:
		held = applyReviewEvent(db, d, e)
	case *github.PullRequestReviewCommentEvent:
		held = applyReviewCommentEvent(db, d, e)
	case *github.PullRequestReviewThreadEvent:
		held = applyReviewThreadEvent(db, d, e)
		// No filter reads whether a thread is resolved.
		reevaluate = false
	case *github.IssueCommentEvent:
		held = applyIssueCommentEvent(db, d, e)
	case *github.CheckRunEvent:
		held = applyCheckRunEvent(db, d, e)
		// The app that ran it, which nobody did.
		actor = ""
	case *github.StatusEvent:
		held = applyStatusEvent(db, d, e)
		actor = ""
	case *github.PushEvent:
		if applyPushEvent(e) {
			webhookActivity.applied.Add(1)
		} else {
			webhookActivity.ignored.Add(1)
		}
		return
	}
	if len(held) == 0 {
		webhookActivity.ignored.Add(1)
		return
	}
	webhookActivity.applied.Add(1)
	event := d.eventName(deliveryAction(d.event))
	for _, h := range held {
		if h.pr != nil {
			rememberPR(h.key, h.pr)
		}
		notifyPRChanged(PRChange{
			Key:     h.key,
			Event:   event,
			Changes: h.changes,
			HeadSHA: h.pr.GetHead().GetSHA(),
			Actor:   actor,
			Summary: h.summary,
		})
		if reevaluate {
			scheduleReevaluation(h.key)
		}
	}
}

// deliveryAction is what a delivery says happened: its action, or a commit
// status's state.
func deliveryAction(event any) string {
	switch e := event.(type) {
	case *github.StatusEvent:
		return e.GetState()
	case interface{ GetAction() string }:
		return e.GetAction()
	}
	return ""
}

// trackedPR identifies the PR a delivery is about and reports whether we hold
// it: a section lists it, or a cycle has cached it. The second matters as much
// as the first. A cycle fetches every PR its repositories list, not only the
// ones that end up in a section, and the filters decide section membership
// from those caches — a PR whose review was just dismissed joins "waiting on
// me" because its cached reviews say so. Left stale, they would keep it out.
//
// The repository is the PR's base — the one a delivery is sent from — and the
// key uses its short name, as every PR cache does.
func trackedPR(db *database.DB, repo *github.Repository, number int) (PRKey, bool) {
	owner, name := repo.GetOwner().GetLogin(), repo.GetName()
	if owner == "" || name == "" || number <= 0 {
		return PRKey{}, false
	}
	key := PRKey{Owner: owner, Repo: name, Number: number}
	fullName := repo.GetFullName()
	if fullName == "" {
		fullName = owner + "/" + name
	}
	listed, err := db.HasItem(fmt.Sprintf("%s-%d", fullName, number))
	if err != nil {
		slog.Warn("Failed to look up whether a webhook's PR is tracked", "repo", fullName, "pr", number, "error", err)
		return PRKey{}, false
	}
	if listed {
		return key, true
	}
	sha, _, err := db.GetPullRequestSHAs(number, name)
	if err != nil {
		slog.Warn("Failed to look up whether a webhook's PR is cached", "repo", fullName, "pr", number, "error", err)
		return PRKey{}, false
	}
	return key, sha != ""
}

// logWebhookWrite records the cache fields a delivery wrote in the action log,
// alongside the ones cycles write.
func logWebhookWrite(key PRKey, sha string, written []string, detail string) {
	if len(written) == 0 {
		return
	}
	slog.Info("Webhook delivery updated a PR", "event", detail,
		"pr", fmt.Sprintf("%s/%s#%d", key.Owner, key.Repo, key.Number), "fields", strings.Join(written, ","))
	logWorkflowAction(database.WorkflowAction{
		WorkflowName:  webhookWorkflowName,
		Action:        database.WorkflowActionCacheWrite,
		Owner:         key.Owner,
		Repo:          key.Repo,
		PRNumber:      key.Number,
		SHA:           sha,
		FieldsWritten: written,
		Detail:        detail,
	})
}

// refetchIfHeadMoved fetches the PR in full when pr — as a delivery describes
// it — has a head other than the one cached, and reports whether it did. A push
// changes the diff and commits, which no delivery carries; it also moves review
// comments' positions, so a review or comment delivery that finds the head
// moved (its push's delivery missed, or still queued) takes the full fetch,
// which picks up its own change along with the rest.
//
// A delivery sent before a later push can also arrive after it — Pub/Sub
// promises no order — and what it says about its own head (a comment's
// position, that head's CI) is no use against the cached one. It is fetched
// the same way, but as the newest the PR has been seen at (newestPR):
// fetching it as the delivery's head would cache the current diff under a
// head the PR has moved on from.
func refetchIfHeadMoved(db *database.DB, key PRKey, pr *github.PullRequest, detail string) bool {
	sha := pr.GetHead().GetSHA()
	if sha == "" {
		return false
	}
	_, cachedSHA, err := db.GetPullRequest(key.Number, key.Repo)
	if err != nil || cachedSHA == sha {
		return false
	}
	refetchPR(key, newestPR(key, pr), detail)
	return true
}

// newestPR is pr, a delivery's PR, unless a listing or delivery has since
// shown it with a later updated_at (rememberPR) — Pub/Sub promises no order,
// and webhook deliveries can be retried — in which case it is that.
func newestPR(key PRKey, pr *github.PullRequest) *github.PullRequest {
	held := rememberedPRFor(key)
	if held != nil && held.UpdatedAt != nil && pr.UpdatedAt != nil && held.UpdatedAt.After(pr.UpdatedAt.Time) {
		return held
	}
	return pr
}

func applyPullRequestEvent(db *database.DB, d webhookDelivery, e *github.PullRequestEvent) []heldPR {
	pr := e.GetPullRequest()
	if pr == nil {
		return nil
	}
	key, ok := trackedPR(db, e.GetRepo(), pr.GetNumber())
	if !ok {
		return nil
	}
	detail := d.describe(e.GetAction())
	held := heldPR{key: key, pr: pr, changes: []string{ChangePR}, summary: pullRequestSummary(e)}
	// What the PR object says is written from the newest seen (newestPR): a
	// late delivery's title, state and reviewers are already out of date.
	// Unless the PR is fetched in full, only from one at the delivery's head,
	// which is the cached one.
	newest, latest := newestPR(key, pr), pr

	// A synchronize is a push, and a new base branch changes the diff as surely
	// as one: both need a fetch. Everything else this event reports — title,
	// body, draft, state, labels, assignees, review requests — is in the
	// payload.
	switch {
	case refetchIfHeadMoved(db, key, pr, detail):
		latest = newest
		held.changes = append(held.changes, ChangeHead)
	case e.GetChanges().GetBase() != nil:
		latest = newest
		refetchPR(key, latest, detail)
		held.changes = append(held.changes, ChangeHead)
	default:
		if newest.GetHead().GetSHA() == pr.GetHead().GetSHA() {
			latest = newest
		}
		unlock := lockPR(key)
		written := refreshFromPRPayload(db, key, latest)
		unlock()
		logWebhookWrite(key, latest.GetHead().GetSHA(), written, detail)
		held.changes = append(held.changes, written...)
	}
	recordPayloadMergeability(db, key, latest)
	updateItemSummaries(db, key, latest)
	return []heldPR{held}
}

// refreshFromPRPayload rewrites what the PR object alone determines: who is
// still asked to review, the metadata the review view reads, and the required
// teams' standing. It returns the fields it wrote. The caller holds lockPR.
func refreshFromPRPayload(db *database.DB, key PRKey, pr *github.PullRequest) []string {
	var written []string
	if err := cacheRequestedReviewers(db, key, pr); err != nil {
		slog.Error("Failed to cache requested reviewers from a webhook", "pr", key.Number, "repo", key.Repo, "error", err)
	} else {
		written = append(written, "requested_reviewers")
	}

	// The metadata also says who approved, which comes from the reviews: with
	// none cached, rebuilding it would blank that out, so it waits for the
	// cycle's full fetch.
	reviews, ok := loadCachedReviews(db, key)
	if !ok {
		return written
	}
	status, checkRuns, _ := loadCachedCI(db, key, pr.GetHead().GetSHA())
	if buildAndCacheMetadata(db, key, pr, reviews, status, checkRuns) {
		written = append(written, "metadata")
	}

	// Only refreshed where a cycle has resolved the teams before: a first
	// resolution reads the PR's whole review-request history, which is the
	// cycle's job.
	if _, hasRow := loadCachedTeamReviews(key); hasRow {
		if teams := resolvePRTeamReviews(&apiCallCounter{}, key, pr, reviews); teams != nil {
			if j, err := json.Marshal(teams); err == nil {
				if err := db.UpsertPRTeamReviews(key.Number, key.Repo, string(j)); err != nil {
					slog.Error("Failed to cache team reviews from a webhook", "pr", key.Number, "repo", key.Repo, "error", err)
				} else {
					written = append(written, "team_reviews")
				}
			}
		}
	}
	return written
}

// recordPayloadMergeability records the mergeability a pull_request delivery
// carries. GitHub often hasn't worked it out yet when it sends one — always
// right after a push — and "unknown" for a new head clears the old answer, so
// the next cycle asks.
func recordPayloadMergeability(db *database.DB, key PRKey, pr *github.PullRequest) {
	if pr.GetState() != "open" || pr.GetHead().GetSHA() == "" {
		return
	}
	state := git_tools.RESTMergeability(pr)
	if err := db.RecordPRMergeability(key.Number, key.Repo, pr.GetHead().GetSHA(), state); err != nil {
		slog.Error("Failed to cache PR mergeability from a webhook", "pr", key.Number, "repo", key.Repo, "error", err)
		return
	}
	if state != git_tools.MergeabilityUnknown {
		markMergeabilityChecked(git_tools.PRRef{Owner: key.Owner, Repo: key.Repo, Number: key.Number}, time.Now())
	}
}

// updateItemSummaries brings the PR's rows on the review list — in every
// section listing it — up to date with its status, title and tags, which is
// what the list shows. Their details follow on the next cycle.
func updateItemSummaries(db *database.DB, key PRKey, pr *github.PullRequest) {
	// PRToOrgBridge dereferences these.
	if pr.Number == nil || pr.Title == nil || pr.State == nil || pr.Draft == nil ||
		pr.GetBase().GetRepo().GetFullName() == "" || pr.GetHead().GetRepo().Name == nil {
		return
	}
	bridge := PRToOrgBridge{PR: pr}
	if _, err := db.UpdateItemSummary(bridge.Identifier(), bridge.GetStatus(), bridge.Title(), bridge.GetTags()); err != nil {
		slog.Error("Failed to update review list rows from a webhook", "pr", key.Number, "repo", key.Repo, "error", err)
	}
}

func applyReviewEvent(db *database.DB, d webhookDelivery, e *github.PullRequestReviewEvent) []heldPR {
	pr, review := e.GetPullRequest(), e.GetReview()
	if pr == nil || review == nil || review.ID == nil {
		return nil
	}
	key, ok := trackedPR(db, e.GetRepo(), pr.GetNumber())
	if !ok {
		return nil
	}
	// Submitting a review clears the reviewer's own review request — that is
	// what "Re-request review" puts back — so the PR leaves their review
	// queue. Applied here too in case the delivery's PR predates it.
	if e.GetAction() == "submitted" {
		pr.RequestedReviewers = withoutReviewer(pr.RequestedReviewers, review.GetUser().GetLogin())
	}
	held := heldPR{key: key, pr: pr, summary: reviewSummary(e)}
	detail := d.describe(e.GetAction())
	if refetchIfHeadMoved(db, key, pr, detail) {
		held.changes = []string{ChangeHead}
		return []heldPR{held}
	}

	unlock := lockPR(key)
	defer unlock()
	reviews, ok := loadCachedReviews(db, key)
	if !ok {
		return []heldPR{held}
	}
	if err := cacheReviews(db, key, upsertReview(reviews, review)); err != nil {
		slog.Error("Failed to cache PR reviews from a webhook", "pr", key.Number, "repo", key.Repo, "error", err)
		return []heldPR{held}
	}
	// A review clears its author's review request, approves or blocks the PR,
	// and may settle a required team: everything that reads the reviews. The
	// PR is taken as last seen at this head (newestPR): a late review's title
	// and reviewers are out of date, and a newer PR already reflects it.
	src := pr
	if newest := newestPR(key, pr); newest.GetHead().GetSHA() == pr.GetHead().GetSHA() {
		src = newest
	}
	written := append([]string{"reviews"}, refreshFromPRPayload(db, key, src)...)
	logWebhookWrite(key, pr.GetHead().GetSHA(), written, detail)
	held.changes = written
	return []heldPR{held}
}

// withoutReviewer is reviewers less login's personal review request.
func withoutReviewer(reviewers []*github.User, login string) []*github.User {
	if login == "" {
		return reviewers
	}
	return slices.DeleteFunc(slices.Clone(reviewers), func(u *github.User) bool {
		return u != nil && strings.EqualFold(u.GetLogin(), login)
	})
}

// upsertReview puts review into reviews in place of the version with its ID,
// or after the rest. Deliveries spell states in lower case; the review list
// GitHub's API returns — which is what the cache holds — in upper.
func upsertReview(reviews []*github.PullRequestReview, review *github.PullRequestReview) []*github.PullRequestReview {
	normalized := &github.PullRequestReview{
		ID:          review.ID,
		User:        review.User,
		Body:        review.Body,
		State:       github.Ptr(strings.ToUpper(review.GetState())),
		SubmittedAt: review.SubmittedAt,
		HTMLURL:     review.HTMLURL,
		CommitID:    review.CommitID,
	}
	for i, r := range reviews {
		if r.GetID() == review.GetID() {
			reviews[i] = normalized
			return reviews
		}
	}
	return append(reviews, normalized)
}

func applyReviewCommentEvent(db *database.DB, d webhookDelivery, e *github.PullRequestReviewCommentEvent) []heldPR {
	pr, comment := e.GetPullRequest(), e.GetComment()
	if pr == nil || comment == nil || comment.ID == nil {
		return nil
	}
	key, ok := trackedPR(db, e.GetRepo(), pr.GetNumber())
	if !ok {
		return nil
	}
	held := heldPR{key: key, pr: pr, summary: reviewCommentSummary(e)}
	action := e.GetAction()
	detail := d.describe(action)
	if refetchIfHeadMoved(db, key, pr, detail) {
		held.changes = []string{ChangeHead}
		return []heldPR{held}
	}

	unlock := lockPR(key)
	written := patchCachedComments(db, key, action, comment)
	threadsStale := false
	if len(written) > 0 {
		if threads, ok := loadCachedReviewThreads(db, key); ok {
			patched, changed, stale := patchThreadsForComment(threads, action, comment)
			threadsStale = stale
			if changed && cacheReviewThreads(db, key, patched) {
				written = append(written, "review_threads")
			}
		}
	}
	unlock()

	// A new thread's ID — which the review view resolves it by — is a GraphQL
	// node ID no delivery carries.
	if threadsStale && refreshReviewThreads(db, key) {
		written = append(written, "review_threads")
	}
	logWebhookWrite(key, pr.GetHead().GetSHA(), written, detail)
	held.changes = written
	return []heldPR{held}
}

// patchCachedComments applies a created, edited or deleted comment to the
// PRComments cache, and returns the fields it wrote — none when there is no
// cache to patch. The caller holds lockPR.
func patchCachedComments(db *database.DB, key PRKey, action string, comment *github.PullRequestComment) []string {
	comments, ok := loadCachedComments(db, key)
	if !ok {
		return nil
	}
	switch action {
	case "created", "edited":
		comments = upsertComment(comments, comment)
	case "deleted":
		comments = removeComment(comments, comment)
	default:
		return nil
	}
	sortCommentsByCreation(comments)
	j, err := json.Marshal(comments)
	if err != nil {
		return nil
	}
	if err := db.UpsertPRComments(key.Number, key.Repo, string(j)); err != nil {
		slog.Error("Failed to cache PR comments from a webhook", "pr", key.Number, "repo", key.Repo, "error", err)
		return nil
	}
	return []string{"comments"}
}

// sameComment matches a comment by ID and kind: review comments and
// conversation comments are numbered separately.
func sameComment(a, b *github.PullRequestComment) bool {
	return a.GetID() == b.GetID() && isConversationComment(a) == isConversationComment(b)
}

func upsertComment(comments []*github.PullRequestComment, comment *github.PullRequestComment) []*github.PullRequestComment {
	for i, c := range comments {
		if sameComment(c, comment) {
			comments[i] = comment
			return comments
		}
	}
	return append(comments, comment)
}

func removeComment(comments []*github.PullRequestComment, comment *github.PullRequestComment) []*github.PullRequestComment {
	return slices.DeleteFunc(comments, func(c *github.PullRequestComment) bool { return sameComment(c, comment) })
}

// patchThreadsForComment applies a review comment to the cached review
// threads. A reply joins its thread and a deleted comment leaves one; a comment
// that starts a thread, or replies to one the cache doesn't know, leaves the
// threads stale, since only GraphQL knows the new thread's ID.
func patchThreadsForComment(threads []git_tools.ReviewThread, action string, comment *github.PullRequestComment) (patched []git_tools.ReviewThread, changed, stale bool) {
	id := comment.GetID()
	switch action {
	case "created":
		if comment.InReplyTo == nil {
			return threads, false, true
		}
		for i, t := range threads {
			if slices.Contains(t.CommentIDs, comment.GetInReplyTo()) {
				if slices.Contains(t.CommentIDs, id) {
					return threads, false, false
				}
				threads[i].CommentIDs = append(threads[i].CommentIDs, id)
				return threads, true, false
			}
		}
		return threads, false, true
	case "deleted":
		patched = threads[:0]
		for _, t := range threads {
			if slices.Contains(t.CommentIDs, id) {
				t.CommentIDs = slices.DeleteFunc(slices.Clone(t.CommentIDs), func(c int64) bool { return c == id })
				changed = true
				if len(t.CommentIDs) == 0 {
					continue
				}
			}
			patched = append(patched, t)
		}
		return patched, changed, false
	}
	return threads, false, false
}

// cacheReviewThreads writes the PR's review threads, reporting whether they
// landed.
func cacheReviewThreads(db *database.DB, key PRKey, threads []git_tools.ReviewThread) bool {
	j, err := json.Marshal(threads)
	if err != nil {
		return false
	}
	if err := db.UpsertPRReviewThreads(key.Number, key.Repo, string(j)); err != nil {
		slog.Error("Failed to cache review threads from a webhook", "pr", key.Number, "repo", key.Repo, "error", err)
		return false
	}
	return true
}

// refreshReviewThreads looks the PR's review threads up again and caches them,
// reporting whether they landed.
func refreshReviewThreads(db *database.DB, key PRKey) bool {
	threads, err := fetchReviewThreads(key.Owner, key.Repo, key.Number)
	webhookActivity.apiCalls.Add(1)
	if err != nil {
		slog.Warn("Failed to fetch review threads after a webhook", "pr", key.Number, "repo", key.Repo, "error", err)
		return false
	}
	defer lockPR(key)()
	return cacheReviewThreads(db, key, threads)
}

func applyReviewThreadEvent(db *database.DB, d webhookDelivery, e *github.PullRequestReviewThreadEvent) []heldPR {
	pr, thread := e.GetPullRequest(), e.GetThread()
	action := e.GetAction()
	if pr == nil || thread == nil || (action != "resolved" && action != "unresolved") {
		return nil
	}
	key, ok := trackedPR(db, e.GetRepo(), pr.GetNumber())
	if !ok {
		return nil
	}
	held := heldPR{key: key, pr: pr, summary: reviewThreadSummary(e)}
	detail := d.describe(action)
	if refetchIfHeadMoved(db, key, pr, detail) {
		held.changes = []string{ChangeHead}
		return []heldPR{held}
	}

	unlock := lockPR(key)
	defer unlock()
	threads, ok := loadCachedReviewThreads(db, key)
	if !ok {
		return []heldPR{held}
	}
	resolved := action == "resolved"
	resolvedBy := ""
	if resolved {
		resolvedBy = e.GetSender().GetLogin()
	}
	var ids []int64
	path := ""
	for _, c := range thread.Comments {
		if c == nil {
			continue
		}
		ids = append(ids, c.GetID())
		if path == "" {
			path = c.GetPath()
		}
	}
	found := false
	for i, t := range threads {
		matches := thread.GetNodeID() != "" && t.ID == thread.GetNodeID()
		for _, id := range ids {
			matches = matches || slices.Contains(t.CommentIDs, id)
		}
		if !matches {
			continue
		}
		threads[i].IsResolved = resolved
		threads[i].ResolvedBy = resolvedBy
		if threads[i].ID == "" {
			threads[i].ID = thread.GetNodeID()
		}
		found = true
		break
	}
	if !found {
		threads = append(threads, git_tools.ReviewThread{
			ID:         thread.GetNodeID(),
			IsResolved: resolved,
			ResolvedBy: resolvedBy,
			Path:       path,
			CommentIDs: ids,
		})
	}
	if cacheReviewThreads(db, key, threads) {
		held.changes = []string{"review_threads"}
		logWebhookWrite(key, pr.GetHead().GetSHA(), held.changes, detail)
	}
	return []heldPR{held}
}

func applyIssueCommentEvent(db *database.DB, d webhookDelivery, e *github.IssueCommentEvent) []heldPR {
	issue, comment := e.GetIssue(), e.GetComment()
	// The same event reports comments on issues; only a PR's conversation
	// comments matter here.
	if issue == nil || !issue.IsPullRequest() || comment == nil || comment.ID == nil {
		return nil
	}
	key, ok := trackedPR(db, e.GetRepo(), issue.GetNumber())
	if !ok {
		return nil
	}
	unlock := lockPR(key)
	written := patchCachedComments(db, key, e.GetAction(), convertIssueToPRComment(comment))
	unlock()
	logWebhookWrite(key, "", written, d.describe(e.GetAction()))
	return []heldPR{{key: key, changes: written, summary: issueCommentSummary(e)}}
}

func applyCheckRunEvent(db *database.DB, d webhookDelivery, e *github.CheckRunEvent) []heldPR {
	run := e.GetCheckRun()
	sha := run.GetHeadSHA()
	if run == nil || run.ID == nil || sha == "" {
		return nil
	}
	repo := e.GetRepo()
	// The run names the PRs it ran for, except a fork's: those are found by
	// the head SHA.
	var numbers []int
	for _, p := range run.PullRequests {
		if baseID := p.GetBase().GetRepo().GetID(); baseID != 0 && repo.GetID() != 0 && baseID != repo.GetID() {
			continue
		}
		numbers = append(numbers, p.GetNumber())
	}
	numbers = append(numbers, prNumbersForSHA(db, repo, sha)...)
	detail := d.describe(e.GetAction())
	var held []heldPR
	for _, number := range uniqueInts(numbers) {
		if key, ok := trackedPR(db, repo, number); ok {
			written, summary := patchCachedCI(db, key, sha, detail, func(status *github.CombinedStatus, checkRuns *github.ListCheckRunsResults) (*github.CombinedStatus, *github.ListCheckRunsResults) {
				return status, upsertCheckRun(checkRuns, run)
			})
			held = append(held, heldPR{key: key, changes: written, summary: summary})
		}
	}
	return held
}

func applyStatusEvent(db *database.DB, d webhookDelivery, e *github.StatusEvent) []heldPR {
	sha := e.GetSHA()
	if sha == "" || e.GetContext() == "" {
		return nil
	}
	repo := e.GetRepo()
	detail := d.describe(e.GetState())
	var held []heldPR
	for _, number := range prNumbersForSHA(db, repo, sha) {
		if key, ok := trackedPR(db, repo, number); ok {
			written, summary := patchCachedCI(db, key, sha, detail, func(status *github.CombinedStatus, checkRuns *github.ListCheckRunsResults) (*github.CombinedStatus, *github.ListCheckRunsResults) {
				return upsertStatus(status, e), checkRuns
			})
			held = append(held, heldPR{key: key, changes: written, summary: summary})
		}
	}
	return held
}

// prNumbersForSHA finds the PRs in repo whose cached head has been sha.
func prNumbersForSHA(db *database.DB, repo *github.Repository, sha string) []int {
	numbers, err := db.FindPRNumbersBySHA(repo.GetName(), sha)
	if err != nil {
		slog.Warn("Failed to look up PRs by head SHA for a webhook", "repo", repo.GetFullName(), "sha", sha, "error", err)
	}
	return numbers
}

func uniqueInts(values []int) []int {
	slices.Sort(values)
	return slices.Compact(values)
}

// patchCachedCI applies patch to the CI cached for the PR's commit sha, and
// when that is the PR's head, to the CI line in its metadata. It returns the
// fields it wrote, and — when the head's CI just passed or failed — a summary
// saying so.
func patchCachedCI(db *database.DB, key PRKey, sha, detail string,
	patch func(*github.CombinedStatus, *github.ListCheckRunsResults) (*github.CombinedStatus, *github.ListCheckRunsResults)) ([]string, string) {

	unlock := lockPR(key)
	defer unlock()
	status, checkRuns, ok := loadCachedCI(db, key, sha)
	if !ok {
		return nil, ""
	}
	// Before the patch, which rewrites them in place.
	before, _ := ciOutcome(status, checkRuns)
	status, checkRuns = patch(status, checkRuns)
	if err := cacheCI(db, key, sha, status, checkRuns); err != nil {
		slog.Error("Failed to cache CI status from a webhook", "pr", key.Number, "repo", key.Repo, "error", err)
		return nil, ""
	}
	written := []string{"ci_status"}
	summary := ""
	if _, head, err := db.GetPullRequest(key.Number, key.Repo); err == nil && head == sha {
		if patchMetadataCI(db, key, status, checkRuns) {
			written = append(written, "metadata")
		}
		after, failed := ciOutcome(status, checkRuns)
		summary = ciSummaryChange(before, after, failed)
	}
	logWebhookWrite(key, sha, written, detail)
	return written, summary
}

// upsertCheckRun puts run into the commit's check runs. The cache holds the
// latest run of each check, as GitHub lists them by default, so a re-run
// replaces the attempt before it — and a delivery about an attempt older than
// the one cached is ignored.
func upsertCheckRun(checkRuns *github.ListCheckRunsResults, run *github.CheckRun) *github.ListCheckRunsResults {
	if checkRuns == nil {
		checkRuns = &github.ListCheckRunsResults{}
	}
	sameCheck := func(c *github.CheckRun) bool {
		return c.GetName() == run.GetName() && c.GetApp().GetID() == run.GetApp().GetID()
	}
	kept := make([]*github.CheckRun, 0, len(checkRuns.CheckRuns)+1)
	placed := false
	for _, c := range checkRuns.CheckRuns {
		switch {
		case c == nil:
			continue
		case c.GetID() == run.GetID():
			kept = append(kept, run)
			placed = true
		case sameCheck(c) && c.GetID() > run.GetID():
			return checkRuns
		case sameCheck(c):
			// An earlier attempt, superseded.
		default:
			kept = append(kept, c)
		}
	}
	if !placed {
		kept = append(kept, run)
	}
	checkRuns.CheckRuns = kept
	checkRuns.Total = github.Ptr(len(kept))
	return checkRuns
}

// upsertStatus puts the status a delivery reports into the commit's combined
// status, in place of the last one with its context, and works the combined
// state out again the way GitHub does.
func upsertStatus(status *github.CombinedStatus, e *github.StatusEvent) *github.CombinedStatus {
	if status == nil {
		status = &github.CombinedStatus{}
	}
	entry := &github.RepoStatus{
		ID:          e.ID,
		State:       e.State,
		Description: e.Description,
		TargetURL:   e.TargetURL,
		Context:     e.Context,
		CreatedAt:   e.CreatedAt,
		UpdatedAt:   e.UpdatedAt,
	}
	statuses := make([]*github.RepoStatus, 0, len(status.Statuses)+1)
	placed := false
	for _, s := range status.Statuses {
		if s == nil {
			continue
		}
		if s.GetContext() != e.GetContext() {
			statuses = append(statuses, s)
			continue
		}
		if s.UpdatedAt != nil && e.UpdatedAt != nil && s.UpdatedAt.After(e.UpdatedAt.Time) {
			// A delivery older than what is cached.
			return status
		}
		if !placed {
			statuses = append(statuses, entry)
			placed = true
		}
	}
	if !placed {
		statuses = append(statuses, entry)
	}
	status.Statuses = statuses
	status.TotalCount = github.Ptr(len(statuses))
	status.SHA = e.SHA
	status.State = github.Ptr(combinedState(statuses))
	return status
}

// combinedState is GitHub's combined state for a commit's statuses: failure if
// any failed or errored, pending if any is pending or there are none, and
// success otherwise.
func combinedState(statuses []*github.RepoStatus) string {
	state := "success"
	if len(statuses) == 0 {
		state = "pending"
	}
	for _, s := range statuses {
		switch s.GetState() {
		case "failure", "error":
			return "failure"
		case "pending":
			state = "pending"
		}
	}
	return state
}

// patchMetadataCI rewrites the CI line in the PR's cached metadata. A CI
// delivery carries no PR object to rebuild the metadata from, so only the two
// fields it changes are touched. It reports whether the metadata was written.
func patchMetadataCI(db *database.DB, key PRKey, status *github.CombinedStatus, checkRuns *github.ListCheckRunsResults) bool {
	raw, err := db.GetPRMetadataCache(key.Owner, key.Repo, key.Number)
	if err != nil || raw == "" {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return false
	}
	ciStatus, ciFailures := ciSummary(status, checkRuns)
	fields["ci_status"], _ = json.Marshal(ciStatus)
	fields["ci_failures"], _ = json.Marshal(ciFailures)
	j, err := json.Marshal(fields)
	if err != nil {
		return false
	}
	if err := db.UpsertPRMetadataCache(key.Owner, key.Repo, key.Number, string(j)); err != nil {
		slog.Error("Failed to cache PR metadata from a webhook", "pr", key.Number, "repo", key.Repo, "error", err)
		return false
	}
	return true
}

// applyPushEvent notes a push to a branch, which every PR merging into it may
// now conflict with (see mergeabilityCurrent), and reports whether it noted
// one. A push to a PR's own branch arrives as that PR's synchronize too, and is
// handled there.
func applyPushEvent(e *github.PushEvent) bool {
	branch, ok := strings.CutPrefix(e.GetRef(), "refs/heads/")
	if !ok || e.GetDeleted() {
		return false
	}
	repo := e.GetRepo()
	owner := repo.GetOwner().GetLogin()
	if owner == "" {
		owner, _, _ = strings.Cut(repo.GetFullName(), "/")
	}
	if owner == "" || repo.GetName() == "" {
		return false
	}
	recordBaseMoved(owner, repo.GetName(), branch, time.Now())
	return true
}
