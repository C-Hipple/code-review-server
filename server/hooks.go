package server

import (
	"crs/config"
	"crs/llm"
	"crs/utils"
	"crs/workflows"
	"log/slog"
)

// PostUpdateOptions tunes a post-update hook dispatch. The zero value is the
// ordinary dispatch: automatic plugins and, when enabled, the LLM diff
// analysis.
type PostUpdateOptions struct {
	// ForceOnDemandPlugins includes the deferred (OnlyOnDemand) plugins in the
	// run, for a PR in a section configured with ForceRunOnDemand. The run is
	// still automatic — the per-SHA skip applies to every plugin — so this
	// pre-computes the expensive plugins once per revision rather than on every
	// dispatch.
	ForceOnDemandPlugins bool
}

// RunPostUpdatePRHooks runs the side-effecting work that should happen after a
// PR is fetched or updated: configured plugins, plus the experimental LLM diff
// analysis (file ordering and review-ease rating) when enabled. Each hook is
// dispatched in its own goroutine so they run in parallel and the call returns
// immediately.
//
// This is the central post-update hook. Callers that just need to trigger
// plugins should still go through here so any new side effects (like the
// ordering) stay in lockstep with plugin runs.
func RunPostUpdatePRHooks(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string, opts PostUpdateOptions) {
	if opts.ForceOnDemandPlugins {
		go RunPluginsIncludingOnDemand(owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch)
	} else {
		go RunPlugins(owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch)
	}
	go ensureDiffAnalysis(repo, number, sha, diff)
}

// WarmPRAnalysis runs the post-update hooks for a PR whose caches a workflow
// has just filled, so the plugin results and the LLM diff analysis are already
// computed by the time anyone opens the review. It is the entry point the
// workflow layer is wired to (see workflows.SetPRUpdatedHook): workflows
// cannot call into this package directly, because server imports workflows.
//
// Everything the hooks need was written to the DB by the workflow that
// triggered this, so the GetPRDetails call below is expected to be all cache
// hits. It blocks for that read, so callers run it in a goroutine.
func WarmPRAnalysis(owner, repo string, number int, opts workflows.PRUpdateOptions) {
	if opts.ForceOnDemandPlugins && !onDemandWorkOutstanding(owner, repo, number) {
		// A ForceRunOnDemand section re-emits an item write for every PR it
		// holds on every cycle, so this dispatch arrives constantly and almost
		// always has nothing to do. Answering that from the plugin-result rows
		// alone keeps it off the PR load below.
		return
	}
	details, err := GetPRDetails(owner, repo, number, false)
	if err != nil {
		slog.Warn("Error loading PR details to warm post-update hooks", "repo", repo, "pr", number, "error", err)
		return
	}
	ensurePostUpdateHooksWith(owner, repo, number, details, PostUpdateOptions{
		ForceOnDemandPlugins: opts.ForceOnDemandPlugins,
	})
}

// onDemandWorkOutstanding reports whether any deferred plugin still owes a
// result for the PR's cached head SHA.
func onDemandWorkOutstanding(owner, repo string, number int) bool {
	_, sha, err := config.C().DB.GetPullRequest(number, repo)
	if err != nil {
		slog.Warn("Error reading cached PR SHA to check for pending on-demand plugins",
			"repo", repo, "pr", number, "error", err)
		return true
	}
	return OnDemandPluginsPending(owner, repo, number, sha)
}

// ensureDiffAnalysis pre-computes and caches the LLM diff analysis (file
// ordering and review-ease rating, per the enabled config flags) for a PR SHA
// so subsequent renders hit the cache. The underlying analysis is cache-first
// and SHA-keyed, so repeated calls for the same SHA are cheap.
func ensureDiffAnalysis(repo string, prNumber int, sha, diff string) {
	cfg := config.C()
	if !cfg.ExperimentalLLMFileOrdering && !cfg.ExperimentalLLMReviewEase {
		return
	}
	if sha == "" || diff == "" {
		return
	}
	parsed, err := utils.Parse(diff)
	if err != nil || parsed == nil {
		slog.Warn("Failed to parse diff for LLM diff analysis hook", "repo", repo, "pr", prNumber, "error", err)
		return
	}
	llm.EnsureDiffAnalysis(parsed.Files, repo, prNumber, sha, llm.TriggerPostUpdateHook)
}
