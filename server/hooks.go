package server

import (
	"crs/config"
	"crs/utils"
	"log/slog"
)

// RunPostUpdatePRHooks runs the side-effecting work that should happen after a
// PR is fetched or updated: configured plugins, plus the experimental LLM diff
// file ordering and review-ease rating when enabled. Each hook is dispatched
// in its own goroutine so they run in parallel and the call returns
// immediately.
//
// This is the central post-update hook. Callers that just need to trigger
// plugins should still go through here so any new side effects (like the
// ordering) stay in lockstep with plugin runs.
func RunPostUpdatePRHooks(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string) {
	go RunPlugins(owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch)
	go ensureDiffLLMAnalysis(repo, number, sha, diff)
}

// ensureDiffLLMAnalysis pre-computes and caches the LLM diff file ordering and
// review-ease rating for a PR SHA so subsequent renders hit the cache. The
// underlying functions are cache-first and SHA-keyed, so repeated calls for
// the same SHA are cheap. When both features are enabled the ordering request
// computes the ease rating too, so a single LLM call covers both.
func ensureDiffLLMAnalysis(repo string, prNumber int, sha, diff string) {
	cfg := config.C()
	if !cfg.ExperimentalLLMFileOrdering && !cfg.ExperimentalLLMReviewEase {
		return
	}
	if sha == "" || diff == "" {
		return
	}
	parsed, err := utils.Parse(diff)
	if err != nil || parsed == nil {
		slog.Warn("Failed to parse diff for LLM analysis hook", "repo", repo, "pr", prNumber, "error", err)
		return
	}
	if cfg.ExperimentalLLMFileOrdering {
		orderDiffFiles(parsed.Files, repo, prNumber, sha)
	}
	// Pick up any rating the ordering request did not produce: ordering
	// disabled, ordering already cached, or no usable rating in the response.
	ensureReviewEase(parsed.Files, repo, prNumber, sha)
}
