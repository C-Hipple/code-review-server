package workflows

import (
	"crs/database"
	"crs/git_tools"
	"log/slog"
	"time"

	"github.com/google/go-github/v74/github"
)

// Every open PR on the review list shows whether it still merges cleanly. That
// changes whenever the PR's base branch moves, with nothing on the PR itself
// changing, so it is checked afresh for the PRs each cycle writes into a
// section rather than only when a PR is pushed to.

// lookupMergeability asks GitHub about a batch of PRs. A seam for tests.
var lookupMergeability = git_tools.GetMergeability

// mergeabilityRetryDelay is how long GitHub gets to finish the answers it
// reported unknown before they are asked about once more. Asking is what sets
// GitHub computing, so without the second ask a new or just-pushed PR would
// wait a whole cycle for its answer.
var mergeabilityRetryDelay = 3 * time.Second

// recordMergeability records whether each open PR in prs merges cleanly into
// its base. A PR fetched from the single-PR endpoint already carries the
// answer; the rest — every PR from the list endpoint, and any GitHub was still
// computing — are looked up together. Unknown answers are recorded too:
// RecordPRMergeability keeps the last real answer while the head is unchanged
// and drops it once the head moves.
func recordMergeability(db *database.DB, prs []*github.PullRequest) {
	if db == nil {
		return
	}
	answers := map[git_tools.PRRef]git_tools.Mergeability{}
	var ask []git_tools.PRRef
	for _, pr := range prs {
		ref, ok := openPRRef(pr)
		if !ok {
			continue
		}
		if state := git_tools.RESTMergeability(pr); state != git_tools.MergeabilityUnknown {
			answers[ref] = git_tools.Mergeability{State: state, HeadSHA: pr.GetHead().GetSHA()}
			continue
		}
		ask = append(ask, ref)
	}
	if len(ask) > 0 {
		for ref, answer := range lookupMergeabilityWithRetry(ask) {
			answers[ref] = answer
		}
	}

	conflicting := 0
	for ref, answer := range answers {
		if answer.State == git_tools.MergeabilityConflicting {
			conflicting++
		}
		if err := db.RecordPRMergeability(ref.Number, ref.Repo, answer.HeadSHA, answer.State); err != nil {
			slog.Error("Failed to cache PR mergeability", "pr", ref.Number, "repo", ref.Repo, "error", err)
		}
	}
	slog.Debug("Recorded PR mergeability", "prs", len(answers), "looked_up", len(ask), "conflicting", conflicting)
}

// lookupMergeabilityWithRetry looks refs up, then asks once more, after
// mergeabilityRetryDelay, about any GitHub answered unknown. A PR missing from
// the result keeps whatever was recorded for it before.
func lookupMergeabilityWithRetry(refs []git_tools.PRRef) map[git_tools.PRRef]git_tools.Mergeability {
	answers, err := lookupMergeability(refs)
	if err != nil {
		// What it did answer is still good.
		slog.Warn("Failed to look up PR mergeability; the rest keep their last answer",
			"prs", len(refs), "answered", len(answers), "error", err)
		return answers
	}

	var pending []git_tools.PRRef
	for _, ref := range refs {
		if answers[ref].State == git_tools.MergeabilityUnknown {
			pending = append(pending, ref)
		}
	}
	if len(pending) == 0 {
		return answers
	}
	time.Sleep(mergeabilityRetryDelay)
	retried, err := lookupMergeability(pending)
	if err != nil {
		slog.Warn("Failed to look up PR mergeability again", "prs", len(pending), "error", err)
	}
	for ref, answer := range retried {
		answers[ref] = answer
	}
	return answers
}

// openPRRef identifies an open PR by its base repository, under the short repo
// name every PR cache is keyed by. Closed and merged PRs have no conflicts
// worth showing.
func openPRRef(pr *github.PullRequest) (git_tools.PRRef, bool) {
	if pr == nil || pr.GetState() != "open" || pr.GetNumber() == 0 {
		return git_tools.PRRef{}, false
	}
	repo := pr.GetBase().GetRepo()
	owner, name := repo.GetOwner().GetLogin(), repo.GetName()
	if owner == "" || name == "" {
		return git_tools.PRRef{}, false
	}
	return git_tools.PRRef{Owner: owner, Repo: name, Number: pr.GetNumber()}, true
}
