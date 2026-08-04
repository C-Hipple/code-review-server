package server

import (
	"crs/config"
	"crs/llm"
	"crs/utils"
	"log/slog"
)

// RunPostUpdatePRHooks runs the side-effecting work that should happen after a
// PR is fetched or updated: configured plugins, plus the experimental LLM diff
// analysis (file ordering and review-ease rating) when enabled. Each hook is
// dispatched in its own goroutine so they run in parallel and the call returns
// immediately.
//
// This is the central post-update hook. Callers that just need to trigger
// plugins should still go through here so any new side effects (like the
// ordering) stay in lockstep with plugin runs.
func RunPostUpdatePRHooks(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string) {
	go RunPlugins(owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch)
	go ensureDiffAnalysis(repo, number, sha, diff)
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
