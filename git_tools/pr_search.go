// Search-driven PR discovery.
//
// The rest of this package finds pull requests by listing the configured
// repositories. That requires a Repos list, which is the bulk of what a new
// user has to write down before the server does anything useful. GitHub's
// search API answers the two questions a review dashboard actually starts from
// — "who wants a review from me?" and "which PRs have I touched?" — across
// every repository the token can see, with no repo list at all.
//
// Search results are issues, not pull requests: they carry the repository only
// as an API URL and none of the reviewer/branch data the filters read. So the
// helpers here return PRRefs and callers fetch the full PR for the ones they
// still care about (GetPRsForRefs). That fetch is one core API call per
// candidate, which is why the candidate searches are kept narrow and the result
// is capped (maxCandidateRefs).

package git_tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v74/github"
)

// PRRef identifies a single pull request.
type PRRef struct {
	Owner  string
	Repo   string
	Number int
}

func (r PRRef) String() string {
	return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
}

const (
	searchRefsPageSize = 100
	// searchRefsMaxPages bounds a single search at 5×100 results. Anything
	// past that means the query matched far more than a review queue, and a
	// silently truncated list is worse than a loud failure.
	searchRefsMaxPages = 5

	// maxTeamSearches bounds how many team-review-requested: searches one
	// cycle issues. The search API allows 30 requests a minute; someone in
	// dozens of teams would otherwise spend the whole budget here.
	maxTeamSearches = 10

	// maxCandidateRefs caps how many PRs a workflow will fetch in one cycle,
	// since each ref costs a core API call. Well past any real review queue.
	maxCandidateRefs = 250

	// candidateRefsTTL is short enough that a new review request shows up on
	// the next cycle, long enough that workflows sharing a search in the same
	// cycle only pay for it once.
	candidateRefsTTL = 2 * time.Minute

	// prFetchConcurrency caps the parallel PR fetches behind GetPRsForRefs.
	prFetchConcurrency = 8
)

// Seams for tests; production always points these at the real lookups.
var (
	searchPRRefs = searchPRRefsFromGithub
	myTeamRefs   = GetMyTeamRefs
)

// ReviewRequestedRefs returns the open PRs GitHub is asking login to review —
// both the requests made of them directly and the requests made of a team they
// belong to. That union is what github.com's own "Review requests" list shows,
// and the two halves need separate search qualifiers: review-requested: does
// not expand to team membership.
func ReviewRequestedRefs(login string) ([]PRRef, error) {
	if login == "" {
		return nil, fmt.Errorf("cannot search for review requests without a GitHub login")
	}
	return cachedRefSearch("review_requested:"+strings.ToLower(login), func() []string {
		return ReviewRequestedQueries(login, myTeamRefs())
	})
}

// MyInteractionRefs returns the open PRs login has reviewed or commented on.
// These are the PRs that can be waiting on login for a reason other than an
// open review request: a dismissed approval, or a thread of theirs that needs
// an answer. See interaction_prefilter.go for why it takes two queries.
func MyInteractionRefs(login string) ([]PRRef, error) {
	if login == "" {
		return nil, fmt.Errorf("cannot search for interactions without a GitHub login")
	}
	return cachedRefSearch("my_interactions:"+strings.ToLower(login), func() []string {
		return []string{
			fmt.Sprintf("is:pr is:open reviewed-by:%s", login),
			fmt.Sprintf("is:pr is:open commenter:%s", login),
		}
	})
}

// ReviewRequestedQueries builds the searches behind ReviewRequestedRefs.
// archived:false matches what github.com's review-request list shows: a PR in
// an archived repo can't be reviewed. Teams beyond maxTeamSearches are dropped
// rather than spending the search rate limit on them.
func ReviewRequestedQueries(login string, teams []string) []string {
	queries := []string{fmt.Sprintf("is:pr is:open archived:false review-requested:%s", login)}
	if len(teams) > maxTeamSearches {
		slog.Warn("You belong to more teams than one cycle searches; team review requests from the rest will be missed",
			"teams", len(teams), "searched", maxTeamSearches)
		teams = teams[:maxTeamSearches]
	}
	for _, team := range teams {
		if strings.TrimSpace(team) == "" {
			continue
		}
		queries = append(queries, fmt.Sprintf("is:pr is:open archived:false team-review-requested:%s", team))
	}
	return queries
}

// cachedRefSearch runs queries (built lazily, so team lookups are skipped on a
// cache hit) and caches the union. Failures are not cached: unlike the
// interaction prefilter, which fails open into a slower-but-correct path, a
// failed search here means the workflow has no candidates at all, and caching
// that would extend one bad minute across several cycles.
func cachedRefSearch(cacheKey string, buildQueries func() []string) ([]PRRef, error) {
	if val, found := GlobalCache.Get(cacheKey); found {
		return val.([]PRRef), nil
	}
	var refs []PRRef
	for _, query := range buildQueries() {
		found, err := searchPRRefs(query)
		if err != nil {
			return nil, err
		}
		refs = append(refs, found...)
	}
	refs = DedupeRefs(refs)
	GlobalCache.Set(cacheKey, refs, candidateRefsTTL)
	return refs, nil
}

// DedupeRefs removes duplicates, keeping first-seen order. Searches overlap by
// design — a PR can be both directly requested and requested from a team.
func DedupeRefs(refs []PRRef) []PRRef {
	seen := make(map[PRRef]struct{}, len(refs))
	out := make([]PRRef, 0, len(refs))
	for _, ref := range refs {
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

func searchPRRefsFromGithub(query string) ([]PRRef, error) {
	client := GetGithubClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	refs := []PRRef{}
	opts := &github.SearchOptions{ListOptions: github.ListOptions{PerPage: searchRefsPageSize, Page: 1}}
	for page := 0; ; page++ {
		if page == searchRefsMaxPages {
			return nil, fmt.Errorf("search %q did not terminate within %d pages", query, searchRefsMaxPages)
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
			refs = append(refs, PRRef{Owner: owner, Repo: repo, Number: *issue.Number})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return refs, nil
}

// GetPRsForRefs fetches the full pull request behind each ref, concurrently.
//
// A ref GitHub answers with 404/410 is skipped with a warning rather than
// failing the batch: the search index lags repository transfers and deletions,
// and one dead ref must not be able to hold a section hostage every cycle. Any
// other error fails the whole call, so a rate-limited or broken cycle leaves
// the caller's section alone instead of emptying it.
func GetPRsForRefs(client *github.Client, refs []PRRef) ([]*github.PullRequest, error) {
	if len(refs) > maxCandidateRefs {
		slog.Warn("Candidate PR list is longer than one cycle fetches; ignoring the excess",
			"candidates", len(refs), "fetching", maxCandidateRefs)
		refs = refs[:maxCandidateRefs]
	}

	prs := make([]*github.PullRequest, len(refs))
	errs := make([]error, len(refs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, prFetchConcurrency)
	for i, ref := range refs {
		wg.Add(1)
		go func(i int, ref PRRef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			pr, resp, err := client.PullRequests.Get(context.Background(), ref.Owner, ref.Repo, ref.Number)
			if err != nil {
				if resp != nil && (resp.StatusCode == 404 || resp.StatusCode == 410) {
					slog.Warn("Skipping search result the API no longer serves", "pr", ref.String(), "status", resp.StatusCode)
					return
				}
				errs[i] = fmt.Errorf("fetching %s: %w", ref.String(), err)
				return
			}
			prs[i] = pr
		}(i, ref)
	}
	wg.Wait()

	found := make([]*github.PullRequest, 0, len(refs))
	for i := range refs {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if prs[i] != nil {
			found = append(found, prs[i])
		}
	}
	return found, nil
}
