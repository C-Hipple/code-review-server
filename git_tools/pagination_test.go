package git_tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-github/v74/github"
)

// pagedServer serves `total` items from a GitHub-style list endpoint, honoring the
// per_page and page query params and emitting the Link headers go-github reads to
// discover the next page. Each item is rendered by item(i) for a 1-based index.
//
// It records every per_page value it was asked for so tests can assert the caller
// actually requested a full page instead of silently accepting GitHub's default of
// 30 — passing nil options is exactly the bug these tests guard against.
type pagedServer struct {
	server   *httptest.Server
	perPages []string
	pageHits int
}

func newPagedServer(t *testing.T, path string, total int, item func(i int) string) *pagedServer {
	t.Helper()
	ps := &pagedServer{}
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		ps.perPages = append(ps.perPages, q.Get("per_page"))
		ps.pageHits++

		// Mirror GitHub: absent per_page means 30.
		perPage := 30
		if v := q.Get("per_page"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				perPage = n
			}
		}
		page := 1
		if v := q.Get("page"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				page = n
			}
		}

		start := (page-1)*perPage + 1
		end := start + perPage - 1
		if end > total {
			end = total
		}
		items := []string{}
		for i := start; i <= end; i++ {
			items = append(items, item(i))
		}

		if end < total {
			next := fmt.Sprintf("%s%s?page=%d&per_page=%d", ps.server.URL, path, page+1, perPage)
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "[%s]", strings.Join(items, ","))
	})
	ps.server = httptest.NewServer(mux)
	t.Cleanup(ps.server.Close)
	return ps
}

func (ps *pagedServer) client(t *testing.T) *github.Client {
	t.Helper()
	base, err := url.Parse(ps.server.URL + "/")
	if err != nil {
		t.Fatalf("parsing base URL: %v", err)
	}
	c := github.NewClient(nil)
	c.BaseURL = base
	return c
}

// TestListAllPRReviewsFetchesEveryPage is the regression guard for the bug where
// a PR with more than 30 reviews had its newest reviews silently dropped. The
// reviews endpoint returns oldest-first, so a single unpaginated call kept only
// the oldest 30 — hiding the most recent CHANGES_REQUESTED from the client.
func TestListAllPRReviewsFetchesEveryPage(t *testing.T) {
	const total = 37
	ps := newPagedServer(t, "/repos/owner/repo/pulls/30195/reviews", total, func(i int) string {
		state := "COMMENTED"
		if i == total {
			state = "CHANGES_REQUESTED"
		}
		return fmt.Sprintf(`{"id":%d,"state":%q,"user":{"login":"reviewer%d"}}`, i, state, i)
	})

	reviews, err := ListAllPRReviews(context.Background(), ps.client(t), "owner", "repo", 30195)
	if err != nil {
		t.Fatalf("ListAllPRReviews returned error: %v", err)
	}

	if len(reviews) != total {
		t.Errorf("got %d reviews, want %d — pagination dropped %d", len(reviews), total, total-len(reviews))
	}

	// The newest review is the one that matters and the one truncation loses first.
	var newest *github.PullRequestReview
	for _, r := range reviews {
		if r.GetID() == int64(total) {
			newest = r
		}
	}
	if newest == nil {
		t.Fatalf("newest review (id %d) missing; the last page was never fetched", total)
	}
	if got := newest.GetState(); got != "CHANGES_REQUESTED" {
		t.Errorf("newest review state = %q, want CHANGES_REQUESTED", got)
	}

	// Guard the root cause directly: a nil-options call sends no per_page and
	// silently caps at 30. Every request must ask for a full page.
	for i, pp := range ps.perPages {
		if pp != strconv.Itoa(githubPageSize) {
			t.Errorf("request %d sent per_page=%q, want %d (nil options would send none)", i, pp, githubPageSize)
		}
	}
}

func TestListAllPRReviewsSinglePage(t *testing.T) {
	ps := newPagedServer(t, "/repos/owner/repo/pulls/1/reviews", 3, func(i int) string {
		return fmt.Sprintf(`{"id":%d,"state":"APPROVED"}`, i)
	})

	reviews, err := ListAllPRReviews(context.Background(), ps.client(t), "owner", "repo", 1)
	if err != nil {
		t.Fatalf("ListAllPRReviews returned error: %v", err)
	}
	if len(reviews) != 3 {
		t.Errorf("got %d reviews, want 3", len(reviews))
	}
	// A PR that fits in one page must not cost a second call.
	if ps.pageHits != 1 {
		t.Errorf("made %d requests for a single-page result, want 1", ps.pageHits)
	}
}

func TestListAllPagesStopsAtPageCap(t *testing.T) {
	// More data than the cap allows: the helper must return the bounded prefix
	// rather than looping forever.
	total := githubPageSize*githubMaxPages + 50
	ps := newPagedServer(t, "/repos/owner/repo/pulls/2/commits", total, func(i int) string {
		return fmt.Sprintf(`{"sha":"sha%d"}`, i)
	})

	commits, err := ListAllPRCommits(context.Background(), ps.client(t), "owner", "repo", 2)
	if err != nil {
		t.Fatalf("ListAllPRCommits returned error: %v", err)
	}
	if want := githubPageSize * githubMaxPages; len(commits) != want {
		t.Errorf("got %d commits, want %d (the page cap)", len(commits), want)
	}
	if ps.pageHits != githubMaxPages {
		t.Errorf("made %d requests, want %d", ps.pageHits, githubMaxPages)
	}
}

func TestListAllPagesReturnsPartialResultsOnError(t *testing.T) {
	// Page 1 succeeds, page 2 fails. Callers prefer a partial history to nothing,
	// but the error must still surface so the failure is not mistaken for "no data".
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/pulls/3/reviews", func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits > 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		next := fmt.Sprintf("%s?page=2&per_page=%d", r.URL.Path, githubPageSize)
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":1,"state":"COMMENTED"}]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL + "/")
	c := github.NewClient(nil)
	c.BaseURL = base

	reviews, err := ListAllPRReviews(context.Background(), c, "owner", "repo", 3)
	if err == nil {
		t.Fatal("expected an error when a page fetch fails")
	}
	if len(reviews) != 1 {
		t.Errorf("got %d reviews alongside the error, want the 1 already collected", len(reviews))
	}
}

func TestListAllRequestedReviewersAccumulatesPages(t *testing.T) {
	// This endpoint returns a struct of two slices rather than a list, so it has
	// its own loop; assert both slices accumulate across pages.
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/pulls/4/requested_reviewers", func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		if hits == 1 {
			next := fmt.Sprintf("%s?page=2&per_page=%d", r.URL.Path, githubPageSize)
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next))
			fmt.Fprint(w, `{"users":[{"login":"alice"}],"teams":[{"slug":"team-a"}]}`)
			return
		}
		fmt.Fprint(w, `{"users":[{"login":"bob"}],"teams":[{"slug":"team-b"}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL + "/")
	c := github.NewClient(nil)
	c.BaseURL = base

	reviewers, err := ListAllRequestedReviewers(context.Background(), c, "owner", "repo", 4)
	if err != nil {
		t.Fatalf("ListAllRequestedReviewers returned error: %v", err)
	}
	if len(reviewers.Users) != 2 {
		t.Errorf("got %d users, want 2 (pages did not accumulate)", len(reviewers.Users))
	}
	if len(reviewers.Teams) != 2 {
		t.Errorf("got %d teams, want 2 (pages did not accumulate)", len(reviewers.Teams))
	}
}

// TestCIStatusRequestsFullPageOfRuns guards the workflow-runs window. This
// endpoint returns runs newest-first, so unlike the reviews and comments lists
// the default page size does not drop the *newest* data — it drops the oldest.
// That still loses a whole workflow: ProcessWorkflowRuns keeps the latest run
// per name, so a workflow whose most recent run has been pushed past the window
// by newer runs of other workflows disappears from the status list, and its
// conclusion stops counting toward the overall status.
func TestCIStatusRequestsFullPageOfRuns(t *testing.T) {
	const total = 40
	// nightlyAt is inside a 100-run page but outside the default 30-run one.
	const nightlyAt = 35

	var perPages []string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		perPages = append(perPages, q.Get("per_page"))
		if got := q.Get("branch"); got != "feature" {
			t.Errorf("branch = %q, want %q (the user: prefix must be stripped)", got, "feature")
		}

		perPage := 30
		if v := q.Get("per_page"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				perPage = n
			}
		}
		if perPage > total {
			perPage = total
		}

		// Index 1 is the newest run, matching GitHub's ordering.
		runs := []string{}
		for i := 1; i <= perPage; i++ {
			name := "CI"
			if i == nightlyAt {
				name = "Nightly"
			}
			runs = append(runs, fmt.Sprintf(
				`{"id":%d,"name":%q,"status":"completed","conclusion":"success","created_at":%q}`,
				i, name, fmt.Sprintf("2026-08-%02dT00:00:00Z", 1+(total-i)%28)))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"total_count":%d,"workflow_runs":[%s]}`, total, strings.Join(runs, ","))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	base, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("parsing base URL: %v", err)
	}
	client := github.NewClient(nil)
	client.BaseURL = base

	info := ciStatusWithClient(client, "owner", "repo", "someuser:feature")

	for i, pp := range perPages {
		if pp != strconv.Itoa(githubPageSize) {
			t.Errorf("request %d sent per_page=%q, want %d (nil ListOptions would send none)", i, pp, githubPageSize)
		}
	}

	var sawNightly bool
	for _, s := range info.Statuses {
		if strings.Contains(s, "Nightly") {
			sawNightly = true
		}
	}
	if !sawNightly {
		t.Errorf("Nightly missing from statuses %v; run %d fell outside the requested page", info.Statuses, nightlyAt)
	}
	if info.OverallStatus != "DONE" {
		t.Errorf("OverallStatus = %q, want DONE (every run succeeded)", info.OverallStatus)
	}
}
