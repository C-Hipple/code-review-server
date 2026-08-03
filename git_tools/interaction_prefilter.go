// This file is the cheap prefilter in front of the identity filters
// (FilterWaitingOnMe / FilterWaitingOnAuthor).
//
// # Why this exists
//
// Both filters decide each PR from InteractionState, and GetInteractionState
// costs four paginated API fetches per PR (reviews, review comments, issue
// comments, commits). On small repos that was fine; pointing a workflow at a
// repo with hundreds of open PRs (multimediallc/chaturbate holds ~300) makes a
// single cold filter pass cost over a thousand core API requests — a large
// slice of the 5,000/hour authenticated rate limit, every cycle, delivered in
// a concurrent burst big enough to trip GitHub's secondary limits.
//
// # The insight
//
// Almost none of those fetches can change the answer. The filters match on:
//
//  1. "I have an open review request" — already answered by
//     pr.RequestedReviewers on the PR object the list fetch handed us. Free.
//  2. "My last review was dismissed" — impossible unless I once reviewed the PR.
//  3. "A comment thread is waiting on my reply" — impossible unless I once
//     commented (CalculateInteractionState only flags threads I participated in).
//  4. (WaitingOnAuthor) "I acted last" — impossible unless I acted at all:
//     actedSinceOthers needs a nonzero LastMeTime, which only a review or
//     comment of mine can set.
//
// Signals 2–4 all require some past review or comment of mine, and GitHub's
// search API can list every open PR with any such interaction in exactly two
// queries:
//
//	is:pr is:open reviewed-by:<login>   every review I submitted, in any state.
//	                                    This also covers standalone inline diff
//	                                    comments: GitHub wraps each one in an
//	                                    implicit review object.
//	is:pr is:open commenter:<login>     conversation-tab (issue) comments.
//
// involves:<login> looks like a one-query replacement but expands to
// author/assignee/mentions/commenter only — it misses a comment-less approval,
// which is exactly the review that gets dismissed and puts a PR back in my
// court. Hence two queries, not one.
//
// A PR outside the union of those two result sets cannot match signals 2–4,
// so the filters skip its InteractionState fetch entirely. Two requests on the
// search API's separate rate budget (30/minute) replace hundreds of calls on
// the 5,000/hour core budget.
//
// # Why there are no repo: qualifiers
//
// Search queries are capped at 256 characters, so qualifying by every
// configured repo can't fit. Instead the searches run over everything the
// token can see and membership is only ever tested for PRs the workflow
// already fetched — over-matching costs nothing.
//
// # Failure policy: fail open, loudly
//
// A failed or unavailable search yields a nil InteractionSet, which
// MayHaveInteracted treats as "unknown — check everything": the filters fall
// back to the old exhaustive behavior, expensive but never wrong. The set is
// never allowed to silently narrow results beyond what the searches actually
// returned: truncated pagination and IncompleteResults are treated as
// failures, not partial sets. An identity section quietly missing PRs is this
// project's least-debuggable failure mode (see cmd/debug_waiting_on_me), so
// slowness always wins over wrongness.
//
// # Staleness
//
// The search index is eventually consistent and the set is cached for
// interactionSetTTL, so entries can lag reality by a cycle. That is benign for
// the filters' semantics: getting INTO the set requires an action of mine, so
// a lagging entry is a PR I just acted on — precisely a PR that is not waiting
// on me right now. Signal 1 (an open review request) never goes through the
// set at all, so a fresh request or re-request still matches the same cycle.

package git_tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/go-github/v74/github"
)

// InteractionSet is the set of open PRs a login has reviewed or commented on,
// keyed by lowercased "owner/repo#number". A nil set means the searches were
// unavailable; see MayHaveInteracted for how that fails open.
type InteractionSet map[string]bool

const (
	// interactionSetTTL caches the search results for roughly one workflow
	// cycle, so the two searches run once per cycle no matter how many
	// identity filters (or repos) share them.
	interactionSetTTL = 10 * time.Minute

	// interactionSetErrTTL caches a failure (as a nil set) briefly, so a
	// broken search API isn't retried by every filter call, but recovery
	// still happens well within a cycle or two.
	interactionSetErrTTL = 2 * time.Minute

	// searchInteractionMaxPages bounds each search at 5×100 results. More
	// than 500 open PRs with my involvement almost certainly means a query
	// matched something unintended; the fetch fails (→ nil, fail open)
	// rather than returning a silently truncated set.
	searchInteractionMaxPages = 5
	searchInteractionPageSize = 100
)

// fetchInteractionSet is a seam for tests; production always points it at
// fetchInteractionSetFromGithub.
var fetchInteractionSet = fetchInteractionSetFromGithub

func interactionSetCacheKey(login string) string {
	return "open_interactions:" + strings.ToLower(login)
}

func interactionKey(owner, repo string, number int) string {
	return strings.ToLower(fmt.Sprintf("%s/%s#%d", owner, repo, number))
}

// SearchMyOpenInteractions returns the set of open PRs login has reviewed or
// commented on, built from two GitHub search queries and cached for
// interactionSetTTL. On any failure it returns nil, which callers must treat
// as "unknown" — see MayHaveInteracted.
func SearchMyOpenInteractions(login string) InteractionSet {
	if login == "" {
		return nil
	}
	cacheKey := interactionSetCacheKey(login)
	if val, found := GlobalCache.Get(cacheKey); found {
		return val.(InteractionSet)
	}

	set, err := fetchInteractionSet(login)
	if err != nil {
		// Fail open: the filters revert to per-PR fetches for a while —
		// expensive, never wrong. Cached so a dead search API isn't hit
		// again by every filter call in the meantime.
		slog.Error("Interaction prefilter unavailable; identity filters fall back to per-PR fetches",
			"login", login, "error", err)
		GlobalCache.Set(cacheKey, InteractionSet(nil), interactionSetErrTTL)
		return nil
	}

	slog.Info("Interaction prefilter refreshed", "login", login, "open_prs_interacted", len(set))
	GlobalCache.Set(cacheKey, set, interactionSetTTL)
	return set
}

// MayHaveInteracted reports whether pr might carry a review or comment from
// the login this set was built for. A nil set (searches unavailable) and a PR
// with incomplete repo data both return true: the only safe direction to be
// wrong in is doing a full InteractionState check unnecessarily.
func (s InteractionSet) MayHaveInteracted(pr *github.PullRequest) bool {
	if s == nil {
		return true
	}
	if pr == nil || pr.Number == nil || pr.Base == nil || pr.Base.Repo == nil ||
		pr.Base.Repo.Owner == nil || pr.Base.Repo.Owner.Login == nil || pr.Base.Repo.Name == nil {
		return true
	}
	return s[interactionKey(*pr.Base.Repo.Owner.Login, *pr.Base.Repo.Name, *pr.Number)]
}

func fetchInteractionSetFromGithub(login string) (InteractionSet, error) {
	client := GetGithubClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	set := InteractionSet{}
	for _, query := range []string{
		fmt.Sprintf("is:pr is:open reviewed-by:%s", login),
		fmt.Sprintf("is:pr is:open commenter:%s", login),
	} {
		opts := &github.SearchOptions{ListOptions: github.ListOptions{PerPage: searchInteractionPageSize, Page: 1}}
		for page := 0; ; page++ {
			if page == searchInteractionMaxPages {
				return nil, fmt.Errorf("search %q did not terminate within %d pages", query, searchInteractionMaxPages)
			}
			result, resp, err := client.Search.Issues(ctx, query, opts)
			if err != nil {
				return nil, fmt.Errorf("search %q: %w", query, err)
			}
			if result.GetIncompleteResults() {
				return nil, fmt.Errorf("search %q returned incomplete results", query)
			}
			for _, issue := range result.Issues {
				owner, repo, ok := splitRepositoryAPIURL(issue.GetRepositoryURL())
				if !ok || issue.Number == nil {
					continue
				}
				set[interactionKey(owner, repo, *issue.Number)] = true
			}
			if resp == nil || resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}
	}
	return set, nil
}

// splitRepositoryAPIURL extracts owner and repo from an issue's RepositoryURL
// ("https://api.github.com/repos/OWNER/REPO") — search results carry their
// repository only in that form.
func splitRepositoryAPIURL(url string) (owner, repo string, ok bool) {
	_, path, found := strings.Cut(url, "/repos/")
	if !found {
		return "", "", false
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
