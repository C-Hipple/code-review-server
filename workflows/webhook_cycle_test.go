package workflows

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub answers the REST API for the rest of the test — the PR list and
// the rate limit; everything else is a 404 — and returns the paths asked for.
// The REST client's transports end in http.DefaultTransport, so a real client
// is used, response cache and rate limiter included.
func fakeGitHub(t *testing.T) func() []string {
	t.Helper()
	t.Setenv("CRS_GITHUB_TOKEN", "test-token")
	var mu sync.Mutex
	var paths []string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/pulls", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
		  "number": 1, "title": "Add widgets", "state": "open", "draft": false,
		  "user": {"login": "author"}, "html_url": "https://github.com/acme/widgets/pull/1",
		  "requested_reviewers": [], "requested_teams": [], "labels": [],
		  "head": {"sha": "head1", "ref": "feature", "label": "acme:feature", "repo": {"name": "widgets"}},
		  "base": {"sha": "base-sha", "ref": "main", "repo": {"name": "widgets", "full_name": "acme/widgets", "owner": {"login": "acme"}}}
		}]`))
	})
	mux.HandleFunc("/rate_limit", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"resources":{"core":{"limit":5000,"remaining":4999,"reset":0}}}`))
	})
	recording := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		mux.ServeHTTP(w, r)
	})

	old := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		recording.ServeHTTP(rec, r)
		resp := rec.Result()
		resp.Request = r
		return resp, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(paths)
		paths = nil
		sort.Strings(out)
		return slices.Compact(out)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// runManagerCycle runs one cycle the way ManagerService.Run does.
func runManagerCycle(ms ManagerService) {
	var wg sync.WaitGroup
	wg.Add(1)
	ms.workflow_chan = make(chan FileChanges)
	go ListenChanges(ms.workflow_chan, &wg)
	ms.RunOnce(&wg)
	close(ms.workflow_chan)
	wg.Done()
	waitTimeout(&wg, 30*time.Second)
}

func TestWebhookCycleOnlyListsAPRWhoseCachesAreCurrent(t *testing.T) {
	env := newWebhookTestEnv(t)
	requested := fakeGitHub(t)
	cfg := config.C()
	cfg.Repos = []string{"acme/widgets"}
	cfg.GithubUsername = "me"
	cfg.SleepDuration = 10 * time.Minute
	cfg.RawWorkflows = []config.RawWorkflow{{WorkflowType: "SyncReviewRequestsWorkflow", Name: "all", SectionTitle: "Reviews"}}
	config.SetC(cfg)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	trackItem(t, env.db, key)
	env.db.RecordPRMergeability(whOwner, whRepo, 1, "head1", git_tools.MergeabilityMergeable)
	markMergeabilityChecked(git_tools.PRRef{Owner: whOwner, Repo: whRepo, Number: 1}, time.Now())
	t.Cleanup(func() { pruneWebhookMemory(time.Now().Add(time.Hour)) })
	ms := NewManagerService(MatchWorkflows(cfg.RawWorkflows, &cfg.Repos, ""), false, cfg.SleepDuration)
	// Mergeability lookups are made through their own seam, which the
	// package's tests keep away from GitHub; count them there instead.
	asked := 0
	oldLookup := lookupMergeability
	lookupMergeability = func(refs []git_tools.PRRef) (map[git_tools.PRRef]git_tools.Mergeability, error) {
		asked += len(refs)
		return map[git_tools.PRRef]git_tools.Mergeability{}, nil
	}
	t.Cleanup(func() { lookupMergeability = oldLookup })

	// Polling: everything about the PR is asked for again.
	runManagerCycle(ms)
	polled := requested()
	t.Logf("polling cycle asked for %v", polled)
	for _, want := range []string{"/repos/acme/widgets/pulls/1/reviews", "/repos/acme/widgets/commits/head1/check-runs"} {
		if !slices.Contains(polled, want) {
			t.Errorf("polling cycle didn't ask for %s: %v", want, polled)
		}
	}
	if asked == 0 {
		t.Error("polling cycle didn't look the PR's mergeability up")
	}
	asked = 0

	// A webhook cycle: the listing, and nothing else about the PR.
	fakeListening(t, time.Now().Add(-time.Hour))
	lastFullRefresh.mu.Lock()
	lastFullRefresh.at = time.Now().Add(-time.Minute)
	lastFullRefresh.mu.Unlock()
	runManagerCycle(ms)
	got := requested()
	t.Logf("webhook cycle asked for %v", got)
	for _, path := range got {
		if path != "/repos/acme/widgets/pulls" && path != "/rate_limit" {
			t.Errorf("webhook cycle asked GitHub for %s (all: %v)", path, got)
		}
	}
	if !slices.Contains(got, "/repos/acme/widgets/pulls") {
		t.Errorf("webhook cycle didn't list the PRs: %v", got)
	}
	if asked != 0 {
		t.Errorf("webhook cycle looked up mergeability that still stands (%d PRs)", asked)
	}

	// And the PR's row was still rendered, from the caches.
	section, _ := env.db.GetSection("", "Reviews")
	item, err := env.db.GetItem(section.ID, "acme/widgets-1")
	if err != nil || item == nil {
		t.Fatalf("item: %v", err)
	}
	if details, _ := item.GetDetails(); !strings.Contains(strings.Join(details, "\n"), "Approved By:") {
		t.Errorf("item details not rebuilt: %v", details)
	}

	// Each cycle's row says which kind it was and what went out on the wire,
	// which is what comparing the two modes comes down to.
	rows, err := env.db.GetAPICallStatsSince(time.Now().Add(-time.Hour))
	if err != nil || len(rows) != 2 {
		t.Fatalf("APICallStats rows = %d, %v", len(rows), err)
	}
	pollingRow, webhookRow := rows[0], rows[1]
	if pollingRow.Mode != database.CycleModePolling || webhookRow.Mode != database.CycleModeWebhook {
		t.Errorf("modes = %q, %q", pollingRow.Mode, webhookRow.Mode)
	}
	// The PR list and /rate_limit, nothing else.
	if webhookRow.Requests != 2 {
		t.Errorf("webhook cycle row: requests = %d, want 2", webhookRow.Requests)
	}
	if pollingRow.Requests <= webhookRow.Requests {
		t.Errorf("polling row: requests = %d, not more than the webhook cycle's %d", pollingRow.Requests, webhookRow.Requests)
	}
	// The per-type counts miss calls the wire count sees: the polling cycle's
	// Actions-runs and comments fallbacks while rendering the row.
	if pollingRow.Requests <= pollingRow.Total {
		t.Errorf("polling row: requests %d should exceed the per-type total %d", pollingRow.Requests, pollingRow.Total)
	}
}
