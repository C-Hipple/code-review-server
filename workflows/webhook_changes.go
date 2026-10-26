package workflows

import (
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/google/go-github/v74/github"
)

// Telling the clients what a webhook delivery changed. Once a delivery has
// been applied to the caches, each PR it rewrote anything for is handed to
// the registered PR-changed hook — server.NotifyPRChanged in server mode,
// which sends a subscribed client a Server.PRUpdated notification — so a page
// showing the PR can fetch it again and say what happened. So is a PR whose
// section rows the re-evaluation that follows rewrote (webhook_membership.go),
// with "sections" as the change.

// PRChange is what one delivery changed about a PR we hold.
type PRChange struct {
	Key PRKey
	// Event is the delivery's event and action
	// ("pull_request_review.submitted"), or "sections" for a re-evaluation.
	Event string
	// Changes names what was rewritten: a ChangeHead or ChangePR, the cache
	// fields the delivery patched ("reviews", "comments", "review_threads",
	// "ci_status", "metadata", ...), or ChangeSections.
	Changes []string
	// HeadSHA is the PR's head as the delivery saw it; empty when it carries
	// no PR (a conversation comment, a check run, a commit status).
	HeadSHA string
	// Actor is who did it, from the delivery's sender; empty for CI.
	Actor string
	// Summary says what happened in a few words ("alice approved"), or is
	// empty for a change not worth announcing: a check starting, a row
	// rewritten.
	Summary string
}

const (
	// ChangeHead: the PR's head or base moved, so it was fetched again in
	// full — diff, commits and everything else.
	ChangeHead = "head"
	// ChangePR: a pull_request delivery's PR object — title, description,
	// state, draft, labels, assignees, review requests — and the review list
	// rows built from it.
	ChangePR = "pr"
	// ChangeSections: the sections listing the PR, or their rows' details.
	ChangeSections = "sections"
)

// prChangedHook is called, on the webhook worker, for each PR a delivery
// changed. Nil when nothing is registered.
var prChangedHook atomic.Pointer[func(PRChange)]

// SetPRChangedHook registers the callback told about each PR a webhook
// delivery changed. It runs on the goroutine applying deliveries, in delivery
// order, so it must not block. A nil fn clears it.
func SetPRChangedHook(fn func(PRChange)) {
	if fn == nil {
		prChangedHook.Store(nil)
		return
	}
	prChangedHook.Store(&fn)
}

func notifyPRChanged(c PRChange) {
	hook := prChangedHook.Load()
	if hook == nil || len(c.Changes) == 0 {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			slog.Error("Panic in the PR-changed hook", "pr", c.Key.Number, "repo", c.Key.Repo, "panic", p)
		}
	}()
	(*hook)(c)
}

// actorName is who a summary says did something.
func actorName(login string) string {
	if login == "" {
		return "Someone"
	}
	return login
}

// pullRequestSummary says what a pull_request delivery reports; empty for an
// action not worth announcing (auto-merge settings, milestones, locks).
func pullRequestSummary(e *github.PullRequestEvent) string {
	who := actorName(e.GetSender().GetLogin())
	pr := e.GetPullRequest()
	switch e.GetAction() {
	case "opened":
		return who + " opened the PR"
	case "reopened":
		return who + " reopened the PR"
	case "closed":
		if pr.GetMerged() {
			return who + " merged the PR"
		}
		return who + " closed the PR"
	case "synchronize":
		return who + " pushed new commits"
	case "edited":
		changes := e.GetChanges()
		switch {
		case changes.GetBase() != nil:
			return fmt.Sprintf("%s changed the base branch to %s", who, pr.GetBase().GetRef())
		case changes.GetTitle() != nil:
			return who + " edited the title"
		case changes.GetBody() != nil:
			return who + " edited the description"
		}
		return who + " edited the PR"
	case "ready_for_review":
		return who + " marked the PR ready for review"
	case "converted_to_draft":
		return who + " converted the PR to a draft"
	case "review_requested":
		return fmt.Sprintf("%s requested a review from %s", who, requestedName(e))
	case "review_request_removed":
		return fmt.Sprintf("%s removed the review request for %s", who, requestedName(e))
	case "labeled":
		return fmt.Sprintf("%s added the label %s", who, e.GetLabel().GetName())
	case "unlabeled":
		return fmt.Sprintf("%s removed the label %s", who, e.GetLabel().GetName())
	case "assigned":
		return fmt.Sprintf("%s assigned %s", who, e.GetAssignee().GetLogin())
	case "unassigned":
		return fmt.Sprintf("%s unassigned %s", who, e.GetAssignee().GetLogin())
	}
	return ""
}

// requestedName is the reviewer or team a review-request delivery names.
func requestedName(e *github.PullRequestEvent) string {
	if login := e.GetRequestedReviewer().GetLogin(); login != "" {
		return login
	}
	if team := e.GetRequestedTeam(); team != nil {
		if team.GetSlug() != "" {
			return team.GetSlug()
		}
		return team.GetName()
	}
	return "a reviewer"
}

// reviewSummary says what a pull_request_review delivery reports.
func reviewSummary(e *github.PullRequestReviewEvent) string {
	who := actorName(e.GetSender().GetLogin())
	review := e.GetReview()
	switch e.GetAction() {
	case "submitted":
		who = actorName(review.GetUser().GetLogin())
		switch strings.ToLower(review.GetState()) {
		case "approved":
			return who + " approved"
		case "changes_requested":
			return who + " requested changes"
		}
		return who + " reviewed"
	case "edited":
		return who + " edited a review"
	case "dismissed":
		return fmt.Sprintf("%s dismissed %s's review", who, actorName(review.GetUser().GetLogin()))
	}
	return ""
}

// reviewCommentSummary says what a pull_request_review_comment delivery
// reports.
func reviewCommentSummary(e *github.PullRequestReviewCommentEvent) string {
	who := actorName(e.GetSender().GetLogin())
	comment := e.GetComment()
	where := ""
	if path := comment.GetPath(); path != "" {
		where = " on " + path
	}
	switch e.GetAction() {
	case "created":
		if comment.InReplyTo != nil {
			return who + " replied" + where
		}
		return who + " commented" + where
	case "edited":
		return who + " edited a comment" + where
	case "deleted":
		return who + " deleted a comment" + where
	}
	return ""
}

// reviewThreadSummary says what a pull_request_review_thread delivery reports.
func reviewThreadSummary(e *github.PullRequestReviewThreadEvent) string {
	who := actorName(e.GetSender().GetLogin())
	where := ""
	for _, c := range e.GetThread().Comments {
		if c.GetPath() != "" {
			where = " on " + c.GetPath()
			break
		}
	}
	switch e.GetAction() {
	case "resolved":
		return who + " resolved a thread" + where
	case "unresolved":
		return who + " reopened a thread" + where
	}
	return ""
}

// issueCommentSummary says what an issue_comment delivery on a PR reports.
func issueCommentSummary(e *github.IssueCommentEvent) string {
	who := actorName(e.GetSender().GetLogin())
	switch e.GetAction() {
	case "created":
		return who + " commented"
	case "edited":
		return who + " edited a comment"
	case "deleted":
		return who + " deleted a comment"
	}
	return ""
}

// ciOutcome is a commit's CI in a word: "failure" once any check or status
// has failed, "pending" while any is still running, "success" when all have
// passed, and "" before any has reported. It also returns the failed ones'
// names.
func ciOutcome(status *github.CombinedStatus, checkRuns *github.ListCheckRunsResults) (string, []string) {
	reported, pending := false, false
	var failed []string
	if status != nil {
		for _, s := range status.Statuses {
			if s == nil {
				continue
			}
			reported = true
			switch s.GetState() {
			case "failure", "error":
				failed = append(failed, s.GetContext())
			case "pending":
				pending = true
			}
		}
	}
	if checkRuns != nil {
		for _, run := range checkRuns.CheckRuns {
			if run == nil {
				continue
			}
			reported = true
			if run.GetStatus() != "completed" {
				pending = true
				continue
			}
			// As ciSummary counts them.
			switch run.GetConclusion() {
			case "success", "neutral", "skipped", "":
			default:
				failed = append(failed, run.GetName())
			}
		}
	}
	switch {
	case len(failed) > 0:
		return "failure", failed
	case pending:
		return "pending", nil
	case reported:
		return "success", nil
	}
	return "", nil
}

// ciSummaryChange announces a head commit's CI settling: passing, or failing
// where it wasn't before. A check starting, or another failing once one has,
// goes unannounced — a push sets off a delivery per check.
func ciSummaryChange(before, after string, failed []string) string {
	if before == after {
		return ""
	}
	switch after {
	case "success":
		return "Checks passed"
	case "failure":
		const shown = 3
		names := failed
		if len(names) > shown {
			names = append(names[:shown:shown], fmt.Sprintf("%d more", len(failed)-shown))
		}
		return "Checks failed: " + strings.Join(names, ", ")
	}
	return ""
}
