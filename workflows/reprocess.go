package workflows

// Reprocessing one PR, outside the background cycle.
//
// Submitting a review changes the answers the section filters give for that PR
// — the review request GitHub was tracking is gone, my latest review is no
// longer DISMISSED, the ball is now in the author's court — but nothing in the
// dashboard reflects that until the next workflow cycle, up to ten minutes
// later. ReprocessPR closes that gap for a single PR by re-asking every
// workflow that draws from the PR's repo whether the PR still belongs in its
// section, and writing the answer through the same DB path a cycle uses.
//
// Every workflow targeting the repo is consulted, not just the ones that
// currently hold the PR: a filter can just as easily start matching after a
// review (a "waiting on author" section) as stop matching (a "needs review"
// section), and a section the PR has never been in has no way to notice it
// otherwise.
//
// What this deliberately does NOT do is re-run the workflows. A workflow's Run
// treats its PR list as the complete contents of its section and emits a Delete
// for every item not in that list, so handing it a one-PR slice would empty the
// section. Membership for the single PR is decided by MatchesPR instead, and
// only that PR's row is touched.

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v74/github"
)

// reprocessItemTTL matches the TTL ProcessPRsDB stamps on items during a normal
// cycle, so an item written by a reprocess expires on the same schedule as one
// written by the background manager.
const reprocessItemTTL = 2 * time.Hour

// RepoTargeter is implemented by workflows that can say which repos they draw
// PRs from. It answers "which sections could this PR possibly belong to" without
// running the workflow.
type RepoTargeter interface {
	TargetsRepo(owner, repo string) bool
}

// SinglePRMatcher is implemented by workflows that can decide membership for one
// PR without re-fetching their whole PR set.
//
// ownedByWorkflow reports whether the workflow already claims this PR in its
// section. It matters for workflows whose PR set comes from somewhere other than
// the PR's own attributes (ProjectListWorkflow gets its PR numbers from a Jira
// epic): for those, the existing item is the only record of membership we have
// here, so they can drop a PR that no longer passes their filters but must never
// adopt one they were never given.
type SinglePRMatcher interface {
	MatchesPR(pr *github.PullRequest, ownedByWorkflow bool) bool
}

// ReprocessPR re-evaluates a single PR against every configured workflow that
// targets its repo and syncs the PR's membership in each of those workflows'
// sections. Call it after an action that changes how the filters see a PR —
// submitting a review, most notably.
//
// It fetches the PR and its auxiliary data fresh: the whole point is to judge
// the PR by its post-action state, and the caches still hold the pre-action one.
func ReprocessPR(owner, repo string, number int) error {
	cfg := config.C()
	targets := WorkflowsTargetingRepo(MatchWorkflows(cfg.RawWorkflows, &cfg.Repos, cfg.JiraDomain), owner, repo)
	if len(targets) == 0 {
		slog.Debug("No workflows target this repo, nothing to reprocess",
			"owner", owner, "repo", repo, "pr", number)
		return nil
	}

	client := git_tools.GetGithubClient()
	prs, err := git_tools.GetSpecificPRs(client, owner, repo, []int{number})
	if err != nil {
		return fmt.Errorf("fetching PR %s/%s#%d for reprocess: %w", owner, repo, number, err)
	}
	if len(prs) == 0 || prs[0] == nil {
		return fmt.Errorf("PR %s/%s#%d not found", owner, repo, number)
	}

	names := make([]string, 0, len(targets))
	for _, wf := range targets {
		names = append(names, wf.GetName())
	}
	// Refreshes the DB caches as a side effect, so the PR the user is still
	// looking at reads back its new review state rather than a cache miss.
	prefetchAuxDataForPRs(client, strings.Join(names, ","), owner, repo, prs,
		reprocessAuxRequirements(targets, owner, repo))

	applyReprocessedPR(config.C().DB, targets, prs[0])
	return nil
}

// WorkflowsTargetingRepo returns the workflows that draw PRs from owner/repo.
// Workflows that can't report their repos are skipped: without that answer we
// would have to run them to find out, which is exactly what reprocessing avoids.
func WorkflowsTargetingRepo(wfs []Workflow, owner, repo string) []Workflow {
	targets := []Workflow{}
	for _, wf := range wfs {
		targeter, ok := wf.(RepoTargeter)
		if !ok {
			slog.Warn("Workflow cannot report its repos; skipping it during reprocess",
				"workflow", wf.GetName())
			continue
		}
		if targeter.TargetsRepo(owner, repo) {
			targets = append(targets, wf)
		}
	}
	return targets
}

// applyReprocessedPR decides, for each workflow, whether pr belongs in that
// workflow's section and writes the difference: an upsert when it belongs, a
// release of this workflow's ownership when it does not. Items left with no
// owners are pruned, which is what actually clears a PR out of a section.
func applyReprocessedPR(db *database.DB, wfs []Workflow, pr *github.PullRequest) {
	if db == nil || pr == nil {
		return
	}
	identifier := PRToOrgBridge{PR: pr}.Identifier()
	ttl := time.Now().Add(reprocessItemTTL).Unix()

	changes := []FileChanges{}
	for _, wf := range wfs {
		matcher, ok := wf.(SinglePRMatcher)
		if !ok {
			slog.Warn("Workflow cannot evaluate a single PR; skipping it during reprocess",
				"workflow", wf.GetName())
			continue
		}

		sectionName := wf.GetOrgSectionName()
		section, err := db.GetOrCreateSection(sectionName, config.C().SectionPriority[sectionName])
		if err != nil {
			slog.Error("Error getting section during reprocess",
				"section", sectionName, "workflow", wf.GetName(), "error", err)
			continue
		}

		item, err := db.GetItem(section.ID, identifier)
		owned := err == nil && item != nil && slices.Contains(item.GetWorkflows(), wf.GetName())

		if matcher.MatchesPR(pr, owned) {
			change := SyncTODOToSectionDB(db, wf.GetName(), pr, section, workflowIncludesDiff(wf))
			change.TTL = ttl
			change.NotifyOnAdd = workflowShouldNotify(wf)
			changes = append(changes, change)
			continue
		}

		if !owned {
			continue
		}
		// Mirrors the cycle's Delete: ownership is released here and the row is
		// removed by the prune below only if no other workflow still claims it.
		// Details and tags are omitted because nothing on the Delete path reads
		// them.
		changes = append(changes, FileChanges{
			ChangeType:   "Delete",
			Identifier:   identifier,
			Status:       item.Status,
			Title:        item.Title,
			SectionID:    section.ID,
			SectionName:  section.SectionName,
			TTL:          0,
			WorkflowName: wf.GetName(),
		})
	}

	applyReprocessChanges(changes)
	pruneOrphanedItems()
}

// applyReprocessChanges runs the changes through the same ApplyChanges consumer
// the background cycle uses, so item writes, worktree handling, and the workflow
// action log all behave identically to a cycle.
func applyReprocessChanges(changes []FileChanges) {
	pending := make([]FileChanges, 0, len(changes))
	for _, change := range changes {
		if change.ChangeType == "No Change" {
			continue
		}
		change.Report()
		pending = append(pending, change)
	}
	if len(pending) == 0 {
		return
	}

	serialized := make(chan SerializedFileChange)
	var wg sync.WaitGroup
	go ApplyChanges(serialized, &wg)
	for i := range pending {
		var lines []string
		if pending[i].ChangeType != "Delete" {
			lines = pending[i].GetLines(2)
		}
		wg.Add(1)
		serialized <- SerializedFileChange{FileChange: &pending[i], Lines: lines}
	}
	close(serialized)
	wg.Wait()
}

// reprocessAuxRequirements is the union of what the targeted workflows' filters
// need, plus comments, reviews and team standing unconditionally. Reviews are
// the data the reprocess exists to act on — the identity filters read them out
// of the aux store and the section display summarizes them — team standing is
// derived from them, so the review that triggered this reprocess is reflected
// in the row's team chips, and comments come along because Details() would
// otherwise fetch them one PR at a time anyway.
func reprocessAuxRequirements(wfs []Workflow, owner, repo string) AuxDataRequirement {
	req := AuxDataRequirement{Comments: true, Reviews: true, Teams: true}
	for _, wf := range wfs {
		for _, prReq := range wf.GetPRRequirements() {
			if !strings.EqualFold(prReq.Owner, owner) || !strings.EqualFold(prReq.Repo, repo) {
				continue
			}
			req = mergeAuxRequirements(req, prReq.AuxData)
		}
		if srw, ok := wf.(SyncReviewRequestsWorkflow); ok {
			// Fetched for survivors only during a cycle; here there is exactly
			// one candidate, so the distinction costs nothing.
			req = mergeAuxRequirements(req, srw.PostFilterAux)
		}
		if workflowIncludesDiff(wf) {
			req.Diff = true
		}
	}
	return req
}

func mergeAuxRequirements(a, b AuxDataRequirement) AuxDataRequirement {
	return AuxDataRequirement{
		Comments: a.Comments || b.Comments,
		CIStatus: a.CIStatus || b.CIStatus,
		Diff:     a.Diff || b.Diff,
		Reviews:  a.Reviews || b.Reviews,
		Commits:  a.Commits || b.Commits,
		Teams:    a.Teams || b.Teams,
	}
}

func workflowIncludesDiff(wf Workflow) bool {
	switch w := wf.(type) {
	case SyncReviewRequestsWorkflow:
		return w.IncludeDiff
	case ProjectListWorkflow:
		return w.IncludeDiff
	}
	return false
}

func workflowShouldNotify(wf Workflow) bool {
	switch w := wf.(type) {
	case SyncReviewRequestsWorkflow:
		return w.ShouldNotify()
	case ProjectListWorkflow:
		return w.ShouldNotify()
	}
	return false
}

// repoEntriesInclude reports whether any configured repo entry names owner/repo.
// defaultOwner covers the deprecated single-repo config shape, where the entry
// is a bare repo name and the owner lives in a separate field.
func repoEntriesInclude(entries []string, defaultOwner, owner, repo string) bool {
	for _, entry := range entries {
		entryOwner, entryRepo, err := git_tools.ParseRepoName(strings.TrimSpace(entry))
		if err != nil {
			entryOwner, entryRepo = defaultOwner, strings.TrimSpace(entry)
		}
		if entryOwner == "" || entryRepo == "" {
			continue
		}
		if strings.EqualFold(entryOwner, owner) && strings.EqualFold(entryRepo, repo) {
			return true
		}
	}
	return false
}

// TargetsRepo reports whether this workflow fetches PRs from owner/repo.
func (w SyncReviewRequestsWorkflow) TargetsRepo(owner, repo string) bool {
	return repoEntriesInclude(w.Repos, w.Owner, owner, repo)
}

// MatchesPR reports whether pr belongs in this workflow's section right now.
// The workflow's PR set is "every PR of my repos in my state", so membership is
// exactly: right state, survives the filters. Whether the workflow already owns
// the PR is irrelevant — that is the point of reprocessing every targeting
// workflow rather than only the ones currently holding the PR.
func (w SyncReviewRequestsWorkflow) MatchesPR(pr *github.PullRequest, _ bool) bool {
	if pr == nil {
		return false
	}
	state := w.PRState
	if state == "" {
		state = "open"
	}
	if state != "all" && !strings.EqualFold(pr.GetState(), state) {
		return false
	}
	return len(git_tools.ApplyPRFilters([]*github.PullRequest{pr}, w.Filters)) > 0
}

// TargetsRepo reports whether this workflow can draw PRs from owner/repo.
func (w ProjectListWorkflow) TargetsRepo(owner, repo string) bool {
	return repoEntriesInclude(w.Repos, "", owner, repo)
}

// MatchesPR reports whether pr belongs in this workflow's section right now.
//
// Membership in a project list is decided by the Jira epic, not by anything on
// the PR, and resolving the epic is the slow lookup this path exists to avoid.
// So a reprocess can only ever narrow the section: a PR the workflow already
// holds is kept while it still passes the filters and dropped when it stops,
// and a PR the workflow does not hold is left alone for the next cycle's Jira
// lookup to add.
func (w ProjectListWorkflow) MatchesPR(pr *github.PullRequest, ownedByWorkflow bool) bool {
	if pr == nil || !ownedByWorkflow {
		return false
	}
	return len(git_tools.ApplyPRFilters([]*github.PullRequest{pr}, w.Filters)) > 0
}
