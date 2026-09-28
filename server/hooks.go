package server

import (
	"context"
	"crs/ai"
	"crs/config"
	"crs/git_tools"
	"crs/llm"
	"crs/utils"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"
)

// RunPostUpdatePRHooks runs the side-effecting work that should happen after a
// PR is fetched or updated: configured plugins, the experimental LLM diff
// analysis (file ordering and review-ease rating) when enabled, and the AI
// features configured to run automatically. Each hook is dispatched in its own
// goroutine so they run in parallel and the call returns immediately.
//
// This is the central post-update hook. Callers that just need to trigger
// plugins should still go through here so any new side effects (like the
// ordering) stay in lockstep with plugin runs.
func RunPostUpdatePRHooks(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string) {
	go RunPlugins(owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch)
	go ensureDiffAnalysis(repo, number, sha, diff)
	go dispatchAutomaticAIFeatures(owner, repo, number)
}

// aiRunner runs the AI features for the RPCs and the post-update hooks, so
// both share its in-flight tracking and automatic-run cap. Tests swap it.
var aiRunner = ai.DefaultRunner

// dispatchAutomaticAIFeatures starts each AI feature config enables to run
// automatically, unless its stored result already covers the PR's current
// inputs. Nothing is configured by default, so by default this does nothing.
func dispatchAutomaticAIFeatures(owner, repo string, number int) {
	for _, entry := range config.C().AIFeatures {
		if entry.Enabled && entry.Automatic {
			aiRunner.Dispatch(aiJob(owner, repo, number, entry.ID, ai.TriggerAutomatic))
		}
	}
}

// aiJob is a runner job for one AI feature on a PR, keyed by the PR's inputs as
// the DB caches have them right now.
func aiJob(owner, repo string, number int, feature string, trigger ai.Trigger) ai.Job {
	sha, digest := currentAIInputs(repo, number)
	return ai.Job{
		Owner:   owner,
		Repo:    repo,
		Number:  number,
		Feature: feature,
		Trigger: trigger,
		SHA:     sha,
		Digest:  digest,
		Build: func(context.Context) (ai.Request, error) {
			return buildAIRequest(owner, repo, number)
		},
	}
}

// currentAIInputs reads the key a stored AI result is compared against: the
// PR's head SHA and the digest of its discussion, both from the DB caches, so
// working out whether a result is stale never reaches GitHub.
func currentAIInputs(repo string, number int) (sha, digest string) {
	db := config.C().DB
	if db == nil {
		return "", ""
	}
	_, sha, _ = db.GetPullRequest(number, repo)
	comments, _ := db.GetPRComments(number, repo)
	reviews, _ := db.GetPRReviews(number, repo)
	threads, _ := db.GetPRReviewThreads(number, repo)
	return sha, ai.InputsDigest(comments, reviews, threads)
}

// buildAIRequest assembles what an AI feature reads about a PR. GetPRDetails
// serves it from the caches (reaching GitHub only on a miss), and the key is
// read afterwards so it describes the caches as the feature saw them.
func buildAIRequest(owner, repo string, number int) (ai.Request, error) {
	details, err := GetPRDetails(owner, repo, number, false)
	if err != nil {
		return ai.Request{}, err
	}
	sha, digest := currentAIInputs(repo, number)
	rawComments, _ := config.C().DB.GetPRComments(number, repo)
	metadataJSON, err := json.Marshal(details.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}
	return ai.Request{
		Owner:         owner,
		Repo:          repo,
		Number:        number,
		HeadSHA:       sha,
		Digest:        digest,
		Diff:          details.Diff,
		CommentsJSON:  rawComments,
		MetadataJSON:  string(metadataJSON),
		ReviewThreads: details.reviewThreads,
		Discussion:    aiDiscussion(details, rawComments),
		ReadFile:      fileReaderAt(owner, repo, sha),
	}, nil
}

// fileReaderAt reads files as of ref for an agent's read_file tool: from the
// local clone when there is one, otherwise from GitHub. Nil without a ref.
func fileReaderAt(owner, repo, ref string) func(ctx context.Context, path string) (string, error) {
	if ref == "" {
		return nil
	}
	return func(_ context.Context, p string) (string, error) {
		clean, err := cleanRepoPath(p)
		if err != nil {
			return "", err
		}
		if content, err := getFileContentLocal(repo, ref, clean); err == nil {
			return content, nil
		}
		return git_tools.GetFileContent(git_tools.GetGithubClient(), owner, repo, clean, ref)
	}
}

// cleanRepoPath accepts a path relative to the repository root and rejects
// anything that could name a file outside it. The path comes from a model.
func cleanRepoPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "", fmt.Errorf("%q is not a path relative to the repository root", p)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%q is outside the repository", p)
	}
	return clean, nil
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
func WarmPRAnalysis(owner, repo string, number int) {
	details, err := GetPRDetails(owner, repo, number, false)
	if err != nil {
		slog.Warn("Error loading PR details to warm post-update hooks", "repo", repo, "pr", number, "error", err)
		return
	}
	ensurePostUpdateHooks(owner, repo, number, details)
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
