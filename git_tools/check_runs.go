package git_tools

import (
	"context"
	"log/slog"

	"github.com/google/go-github/v74/github"
)

// LatestCheckRuns leaves out the check runs a commit's PR page no longer
// shows, which the check-runs API still lists: every run in a check suite
// that superseded names (a workflow run replaced by a later run of the same
// workflow, typically cancelled by its concurrency group), and every run but
// the newest of each check — the same check (name and app) run again, by a
// re-run or a new workflow run. Without this, a commit whose CI went green on
// a second run reads as failed from the cancelled first one. The order of the
// runs kept is unchanged.
func LatestCheckRuns(runs []*github.CheckRun, superseded map[int64]bool) []*github.CheckRun {
	type check struct {
		name string
		app  int64
	}
	newest := make(map[check]int64, len(runs))
	live := func(run *github.CheckRun) bool {
		return run != nil && !superseded[run.GetCheckSuite().GetID()]
	}
	for _, run := range runs {
		if !live(run) {
			continue
		}
		k := check{run.GetName(), run.GetApp().GetID()}
		if id, seen := newest[k]; !seen || run.GetID() > id {
			newest[k] = run.GetID()
		}
	}
	kept := make([]*github.CheckRun, 0, len(newest))
	for _, run := range runs {
		if live(run) && newest[check{run.GetName(), run.GetApp().GetID()}] == run.GetID() {
			kept = append(kept, run)
		}
	}
	return kept
}

// LatestCheckRunResults is LatestCheckRuns over a check-runs listing, with
// its total counting the runs kept. It changes results in place.
func LatestCheckRunResults(results *github.ListCheckRunsResults, superseded map[int64]bool) *github.ListCheckRunsResults {
	if results == nil {
		return nil
	}
	results.CheckRuns = LatestCheckRuns(results.CheckRuns, superseded)
	results.Total = github.Ptr(len(results.CheckRuns))
	return results
}

// supersededSuites picks, from a commit's Actions workflow runs, the check
// suites of those a later run of the same workflow, for the same event,
// replaced.
func supersededSuites(runs []*github.WorkflowRun) map[int64]bool {
	type workflow struct {
		id    int64
		event string
	}
	newest := make(map[workflow]*github.WorkflowRun, len(runs))
	for _, run := range runs {
		if run == nil || run.GetCheckSuiteID() == 0 {
			continue
		}
		k := workflow{run.GetWorkflowID(), run.GetEvent()}
		if cur, seen := newest[k]; !seen || run.GetID() > cur.GetID() {
			newest[k] = run
		}
	}
	superseded := map[int64]bool{}
	for _, run := range runs {
		if run == nil || run.GetCheckSuiteID() == 0 {
			continue
		}
		if newest[workflow{run.GetWorkflowID(), run.GetEvent()}] != run {
			superseded[run.GetCheckSuiteID()] = true
		}
	}
	return superseded
}

// SupersededCheckSuites lists the Actions workflow runs on a commit and
// returns the check suites of the ones superseded (see supersededSuites).
func SupersededCheckSuites(client *github.Client, owner, repo, sha string) (map[int64]bool, error) {
	ctx := context.Background()
	var all []*github.WorkflowRun
	opts := &github.ListWorkflowRunsOptions{HeadSHA: sha, ListOptions: github.ListOptions{PerPage: githubPageSize, Page: 1}}
	for i := 0; i < githubMaxPages; i++ {
		runs, resp, err := client.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, opts)
		if err != nil {
			return nil, err
		}
		if runs != nil {
			all = append(all, runs.WorkflowRuns...)
		}
		if resp == nil || resp.NextPage == 0 {
			return supersededSuites(all), nil
		}
		opts.Page = resp.NextPage
	}
	slog.Warn("Paginated fetch hit the page cap; results are truncated",
		"endpoint", "actions/runs", "max_pages", githubMaxPages, "ref", sha)
	return supersededSuites(all), nil
}
