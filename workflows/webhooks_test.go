package workflows

import (
	"bytes"
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

const (
	whOwner  = "acme"
	whRepo   = "widgets"
	whSecret = "s3cret"
)

// webhookTestEnv points the config at a fresh DB with webhooks configured for
// acme/widgets, and keeps the tests from reaching GitHub: a full refetch or a
// review-thread lookup is recorded instead of made.
type webhookTestEnv struct {
	db           *database.DB
	refetched    []PRKey
	threadFetch  []PRKey
	fetchThreads []git_tools.ReviewThread
}

func newWebhookTestEnv(t *testing.T) *webhookTestEnv {
	t.Helper()
	env := &webhookTestEnv{db: warmTestDB(t)}
	config.SetC(config.Config{
		DB: env.db,
		Webhooks: config.WebhookSettings{
			ListenAddress: "127.0.0.1:0",
			Secret:        whSecret,
			Repos:         []string{whOwner + "/" + whRepo},
		},
	})
	// Deliveries remember the PRs they carry, process-wide.
	forgetRememberedPRs := func() { pruneRememberedPRs(time.Now().Add(time.Hour)) }
	forgetRememberedPRs()
	t.Cleanup(forgetRememberedPRs)
	oldRefetch, oldThreads := refetchPR, fetchReviewThreads
	refetchPR = func(key PRKey, _ *github.PullRequest, _ string) { env.refetched = append(env.refetched, key) }
	fetchReviewThreads = func(owner, repo string, number int) ([]git_tools.ReviewThread, error) {
		env.threadFetch = append(env.threadFetch, PRKey{Owner: owner, Repo: repo, Number: number})
		return env.fetchThreads, nil
	}
	t.Cleanup(func() {
		refetchPR, fetchReviewThreads = oldRefetch, oldThreads
		config.SetC(config.Config{})
	})
	return env
}

func whKey(number int) PRKey { return PRKey{Owner: whOwner, Repo: whRepo, Number: number} }

func whRepository() *github.Repository {
	return &github.Repository{
		ID:       github.Ptr(int64(99)),
		Name:     github.Ptr(whRepo),
		FullName: github.Ptr(whOwner + "/" + whRepo),
		Owner:    &github.User{Login: github.Ptr(whOwner)},
	}
}

func whPR(number int, sha string) *github.PullRequest {
	return &github.PullRequest{
		Number:  github.Ptr(number),
		Title:   github.Ptr("Add widgets"),
		State:   github.Ptr("open"),
		Draft:   github.Ptr(false),
		User:    &github.User{Login: github.Ptr("author")},
		HTMLURL: github.Ptr("https://github.com/acme/widgets/pull/1"),
		Head: &github.PullRequestBranch{
			SHA:   github.Ptr(sha),
			Ref:   github.Ptr("feature"),
			Label: github.Ptr("acme:feature"),
			Repo:  &github.Repository{Name: github.Ptr(whRepo)},
		},
		Base: &github.PullRequestBranch{
			SHA:  github.Ptr("base-sha"),
			Ref:  github.Ptr("main"),
			Repo: whRepository(),
		},
	}
}

// seedCachedPR writes everything cacheServes asks for, for head sha.
func seedCachedPR(t *testing.T, db *database.DB, key PRKey, sha string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	must(db.UpsertPullRequest(key.Owner, key.Repo, key.Number, sha, "base-sha", "diff --git a/w.go b/w.go\n+widget\n"))
	must(db.UpsertPRComments(key.Owner, key.Repo, key.Number, "[]"))
	must(cacheReviews(db, key, nil))
	must(db.UpsertPRCommits(key.Owner, key.Repo, key.Number, "[]"))
	must(cacheCI(db, key, sha, &github.CombinedStatus{State: github.Ptr("success")}, &github.ListCheckRunsResults{Total: github.Ptr(0)}))
	must(db.UpsertPRMetadataCache(key.Owner, key.Repo, key.Number, `{"number":1,"ci_status":"pending (0/0 checks passed)"}`))
	must(db.UpsertPRReviewThreads(key.Owner, key.Repo, key.Number, "[]"))
}

// trackItem puts the PR in a section, the way a workflow cycle would.
func trackItem(t *testing.T, db *database.DB, key PRKey) {
	t.Helper()
	section, err := db.GetOrCreateSection("", "Reviews", 1)
	if err != nil {
		t.Fatalf("section: %v", err)
	}
	identifier := key.Owner + "/" + key.Repo + "-" + itoa(key.Number)
	if _, err := db.UpsertItemWithWorkflow(section.ID, identifier, "TODO", "Old title", []string{"detail"}, []string{whRepo}, 0, "wf"); err != nil {
		t.Fatalf("item: %v", err)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// withCycleCoverage makes the test run as if inside a webhook cycle covering
// entries.
func withCycleCoverage(t *testing.T, entries ...string) {
	t.Helper()
	activeCoverage.Store(newWebhookCoverage(entries))
	t.Cleanup(func() { activeCoverage.Store(nil) })
}

// fakeListening makes webhooksListeningSince report a listener up since since,
// with no full refresh behind it yet.
func fakeListening(t *testing.T, since time.Time) {
	t.Helper()
	webhookListener.mu.Lock()
	webhookListener.server, webhookListener.since = &http.Server{}, since
	webhookListener.mu.Unlock()
	lastFullRefresh.mu.Lock()
	lastFullRefresh.at = time.Time{}
	lastFullRefresh.mu.Unlock()
	t.Cleanup(func() {
		webhookListener.mu.Lock()
		webhookListener.server, webhookListener.since = nil, time.Time{}
		webhookListener.mu.Unlock()
		lastFullRefresh.mu.Lock()
		lastFullRefresh.at = time.Time{}
		lastFullRefresh.mu.Unlock()
		activeCoverage.Store(nil)
	})
}

func cachedReviewsOf(t *testing.T, db *database.DB, key PRKey) []cachedReview {
	t.Helper()
	raw, _ := db.GetPRReviews(key.Owner, key.Repo, key.Number)
	var reviews []cachedReview
	if err := json.Unmarshal([]byte(raw), &reviews); err != nil {
		t.Fatalf("decode reviews %q: %v", raw, err)
	}
	return reviews
}

func cachedMetadataOf(t *testing.T, db *database.DB, key PRKey) map[string]any {
	t.Helper()
	raw, _ := db.GetPRMetadataCache(key.Owner, key.Repo, key.Number)
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decode metadata %q: %v", raw, err)
	}
	return m
}

func TestWebhookCoverage(t *testing.T) {
	cov := newWebhookCoverage([]string{"Acme/Widgets", "labs/*", "bogus", ""})
	for _, c := range []struct {
		owner, repo string
		want        bool
	}{
		{"acme", "widgets", true},
		{"ACME", "WIDGETS", true},
		{"acme", "gadgets", false},
		{"labs", "anything", true},
		{"Labs", "else", true},
		{"other", "widgets", false},
	} {
		if got := cov.covers(c.owner, c.repo); got != c.want {
			t.Errorf("covers(%s/%s) = %v, want %v", c.owner, c.repo, got, c.want)
		}
	}
	var none *webhookCoverage
	if none.covers("acme", "widgets") {
		t.Error("nil coverage covered something")
	}
}

func TestWebhookCycleNeedsAListenerAndAFullRefreshFirst(t *testing.T) {
	newWebhookTestEnv(t)
	cfg := config.C()
	now := time.Now()

	if mode := beginWebhookCycle(cfg, now); mode != database.CycleModePolling {
		t.Fatalf("mode = %q with nothing listening, want polling", mode)
	}

	fakeListening(t, now.Add(-time.Minute))
	if mode := beginWebhookCycle(cfg, now); mode != database.CycleModeFullRefresh {
		t.Fatalf("mode = %q for the first cycle after the listener came up, want a full refresh", mode)
	}
	if webhookHandles(whKey(1)) {
		t.Fatal("a refresh-everything cycle left a PR to webhooks")
	}

	// An incomplete refresh doesn't count.
	endWebhookCycle(cfg, now, database.CycleModeFullRefresh, false)
	if webhookHandles(whKey(1)) {
		t.Fatal("an incomplete full refresh started trusting webhooks")
	}
	if mode := beginWebhookCycle(cfg, now.Add(time.Minute)); mode != database.CycleModeFullRefresh {
		t.Fatalf("mode = %q after an incomplete full refresh", mode)
	}

	// A refresh that started before the listener doesn't either.
	endWebhookCycle(cfg, now.Add(-2*time.Minute), database.CycleModeFullRefresh, true)
	if mode := beginWebhookCycle(cfg, now.Add(time.Minute)); mode != database.CycleModeFullRefresh {
		t.Fatalf("mode = %q after a full refresh from before the listener came up", mode)
	}

	// A complete one starts trusting webhooks straight away, before the next
	// cycle: deliveries in between are acted on.
	endWebhookCycle(cfg, now, database.CycleModeFullRefresh, true)
	if !webhookHandles(whKey(1)) {
		t.Fatal("not trusting webhooks after a complete full refresh")
	}
	if mode := beginWebhookCycle(cfg, now.Add(time.Minute)); mode != database.CycleModeWebhook {
		t.Fatalf("mode = %q once a full refresh completed, want webhook", mode)
	}
	if !webhookHandles(whKey(1)) {
		t.Error("webhook cycle doesn't cover acme/widgets")
	}
	if webhookHandles(PRKey{Owner: "other", Repo: "repo", Number: 1}) {
		t.Error("webhook cycle covers a repository [Webhooks] doesn't name")
	}
	endWebhookCycle(cfg, now.Add(time.Minute), database.CycleModeWebhook, true)
	if !webhookHandles(whKey(1)) {
		t.Error("webhooks stopped being trusted between webhook cycles")
	}

	// The periodic refresh comes round again, and stops trusting them while
	// it runs.
	if mode := beginWebhookCycle(cfg, now.Add(config.DefaultWebhookFullRefresh)); mode != database.CycleModeFullRefresh {
		t.Errorf("mode = %q once the interval passed, want a full refresh", mode)
	}
	if webhookHandles(whKey(1)) {
		t.Error("a periodic full refresh left a PR to webhooks")
	}
}

func TestCacheServesNeedsEveryCacheAtTheCurrentHead(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	pr := whPR(1, "head1")
	if cacheServes(env.db, key, pr) {
		t.Fatal("served a PR with nothing cached")
	}
	seedCachedPR(t, env.db, key, "head1")
	if !cacheServes(env.db, key, pr) {
		t.Fatal("expected a fully cached PR to be served")
	}
	if cacheServes(env.db, key, whPR(1, "head2")) {
		t.Error("served a PR whose head moved")
	}
	env.db.Exec("DELETE FROM CIStatus")
	if cacheServes(env.db, key, pr) {
		t.Error("served a PR with no CI for its head")
	}
}

func TestLoadCachedAuxDataReadsWhatDetailsNeeds(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	created := &github.Timestamp{Time: time.Unix(1000, 0)}
	comments := []*github.PullRequestComment{
		{ID: github.Ptr(int64(1)), URL: github.Ptr("https://api.github.com/repos/acme/widgets/pulls/comments/1"),
			DiffHunk: github.Ptr("@@ -1 +1 @@"), Path: github.Ptr("w.go"), Body: github.Ptr("nit"),
			User: &github.User{Login: github.Ptr("rev")}, CreatedAt: created},
		convertIssueToPRComment(&github.IssueComment{ID: github.Ptr(int64(1)), URL: github.Ptr("https://api.github.com/repos/acme/widgets/issues/comments/1"),
			Body: github.Ptr("lgtm"), User: &github.User{Login: github.Ptr("rev")}, CreatedAt: created}),
	}
	j, _ := json.Marshal(comments)
	env.db.UpsertPRComments(key.Owner, key.Repo, key.Number, string(j))
	cacheReviews(env.db, key, []*github.PullRequestReview{{ID: github.Ptr(int64(5)), User: &github.User{Login: github.Ptr("rev")},
		State: github.Ptr("APPROVED"), SubmittedAt: created}})
	cacheCI(env.db, key, "head1", nil, &github.ListCheckRunsResults{CheckRuns: []*github.CheckRun{
		{ID: github.Ptr(int64(1)), Name: github.Ptr("build"), Status: github.Ptr("completed"), Conclusion: github.Ptr("failure")},
	}})

	aux := loadCachedAuxData(env.db, key, whPR(1, "head1"), AuxDataRequirement{})
	if len(aux.Comments) != 1 || aux.Comments[0].GetBody() != "nit" {
		t.Errorf("aux.Comments = %v, want just the review comment", aux.Comments)
	}
	if len(aux.Reviews) != 1 || aux.Reviews[0].GetState() != "APPROVED" || aux.Reviews[0].GetUser().GetLogin() != "rev" {
		t.Errorf("aux.Reviews = %v", aux.Reviews)
	}
	if aux.CIStatus == nil || aux.CIStatus.OverallStatus != "TODO" {
		t.Errorf("aux.CIStatus = %+v, want a failure", aux.CIStatus)
	}
	if aux.Diff != "" {
		t.Error("loaded the diff nobody asked for")
	}
	if aux = loadCachedAuxData(env.db, key, whPR(1, "head1"), AuxDataRequirement{Diff: true}); !strings.Contains(aux.Diff, "+widget") {
		t.Errorf("aux.Diff = %q", aux.Diff)
	}

	// Rendering the item from it must not reach for GitHub (there's no client
	// here to reach it with) — and must not trip over the conversation comment.
	store := NewAuxDataStore()
	store.Set(key, aux)
	SetCurrentAuxDataStore(store)
	t.Cleanup(func() { SetCurrentAuxDataStore(nil) })
	details := strings.Join(PRToOrgBridge{PR: whPR(1, "head1"), IncludeDiff: true}.Details(), "\n")
	for _, want := range []string{"Approved By: rev", "*** TODO CI Status", "[❌]  build", "+widget", "nit"} {
		if !strings.Contains(details, want) {
			t.Errorf("details missing %q:\n%s", want, details)
		}
	}
}

func TestPlanWebhookRefresh(t *testing.T) {
	env := newWebhookTestEnv(t)
	cached, uncached, elsewhere, pushed := whKey(1), whKey(2), PRKey{Owner: "other", Repo: "repo", Number: 3}, whKey(4)
	seedCachedPR(t, env.db, cached, "head1")
	seedCachedPR(t, env.db, pushed, "old")
	// Cached too, so that only coverage sets it apart from #1.
	seedCachedPR(t, env.db, elsewhere, "head3")
	prObjects := map[PRKey]*github.PullRequest{
		cached: whPR(1, "head1"), uncached: whPR(2, "head2"), elsewhere: whPR(3, "head3"), pushed: whPR(4, "new"),
	}
	reqs := map[PRKey]AuxDataRequirement{
		cached: {Reviews: true, Teams: true, Diff: true}, uncached: {Reviews: true}, elsewhere: {Reviews: true}, pushed: {},
	}

	// Outside a webhook cycle nothing changes.
	warmed := applyCacheWarmRequirements(env.db, prObjects, reqs)
	fromCache, warmedOut := planWebhookRefresh(env.db, prObjects, reqs, warmed)
	if len(fromCache) != 0 || len(warmedOut) != len(warmed) {
		t.Fatalf("polling cycle planned webhook work: fromCache=%v warmed=%v", fromCache, warmedOut)
	}

	withCycleCoverage(t, whOwner+"/"+whRepo)
	reqs = map[PRKey]AuxDataRequirement{
		cached: {Reviews: true, Teams: true, Diff: true}, uncached: {Reviews: true}, elsewhere: {Reviews: true}, pushed: {},
	}
	warmed = applyCacheWarmRequirements(env.db, prObjects, reqs)
	fromCache, warmedOut = planWebhookRefresh(env.db, prObjects, reqs, warmed)

	if _, ok := fromCache[cached]; !ok || len(fromCache) != 1 {
		t.Errorf("fromCache = %v, want only #1", fromCache)
	}
	if !fromCache[cached].Diff {
		t.Error("fromCache lost the requirement the diff is read for")
	}
	if _, fetched := reqs[cached]; fetched {
		t.Error("#1 is still to be fetched although its caches are current")
	}
	for _, key := range []PRKey{uncached, pushed} {
		if reqs[key] != warmRequirement(AuxDataRequirement{}) && reqs[key] != warmRequirement(AuxDataRequirement{Reviews: true}) {
			t.Errorf("%v not fetched in full: %+v", key, reqs[key])
		}
		if !slices.Contains(warmedOut, key) {
			t.Errorf("%v not warmed", key)
		}
	}
	if slices.Contains(warmedOut, elsewhere) {
		t.Error("a PR outside the coverage was warmed")
	}
	if _, ok := fromCache[elsewhere]; ok {
		t.Error("a PR outside the coverage was read from its caches")
	}
	if reqs[elsewhere] != (AuxDataRequirement{Reviews: true}) {
		t.Errorf("a PR outside the coverage lost its polling requirements: %+v", reqs[elsewhere])
	}
}

func signedDelivery(t *testing.T, path, event, secret string, payload []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return req
}

// withTestQueue swaps in a queue of the test's own, so a worker another test
// started can't take the deliveries.
func withTestQueue(t *testing.T) chan webhookDelivery {
	t.Helper()
	old := webhookQueue
	webhookQueue = make(chan webhookDelivery, 4)
	t.Cleanup(func() { webhookQueue = old })
	return webhookQueue
}

func TestServeWebhookAcceptsOnlySignedDeliveries(t *testing.T) {
	newWebhookTestEnv(t)
	queue := withTestQueue(t)
	payload := []byte(`{"action":"submitted"}`)
	path := config.DefaultWebhookPath

	for _, c := range []struct {
		name     string
		req      *http.Request
		wantCode int
		queued   bool
	}{
		{"signed", signedDelivery(t, path, "pull_request_review", whSecret, payload), http.StatusAccepted, true},
		{"wrong secret", signedDelivery(t, path, "pull_request_review", "guess", payload), http.StatusUnauthorized, false},
		{"unsigned", func() *http.Request {
			r := signedDelivery(t, path, "pull_request_review", whSecret, payload)
			r.Header.Del("X-Hub-Signature-256")
			return r
		}(), http.StatusUnauthorized, false},
		{"wrong path", signedDelivery(t, "/elsewhere", "pull_request_review", whSecret, payload), http.StatusNotFound, false},
		{"not a POST", httptest.NewRequest(http.MethodGet, path, nil), http.StatusMethodNotAllowed, false},
		{"ping", signedDelivery(t, path, "ping", whSecret, []byte(`{"zen":"hi"}`)), http.StatusOK, false},
		{"event we don't act on", signedDelivery(t, path, "workflow_job", whSecret, payload), http.StatusAccepted, false},
		{"malformed", signedDelivery(t, path, "pull_request_review", whSecret, []byte(`{`)), http.StatusBadRequest, false},
		{"over GitHub's size limit", signedDelivery(t, path, "pull_request_review", whSecret, bytes.Repeat([]byte(" "), maxWebhookPayload+1)), http.StatusRequestEntityTooLarge, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			serveWebhook(rec, c.req)
			if rec.Code != c.wantCode {
				t.Errorf("status = %d, want %d (%s)", rec.Code, c.wantCode, rec.Body.String())
			}
			select {
			case d := <-queue:
				if !c.queued {
					t.Errorf("queued %+v", d)
				}
			default:
				if c.queued {
					t.Error("nothing queued")
				}
			}
		})
	}
}

// A review delivery, as GitHub sends it, from the wire to the caches.
func TestReviewDeliveryEndToEnd(t *testing.T) {
	env := newWebhookTestEnv(t)
	queue := withTestQueue(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	trackItem(t, env.db, key)

	payload := []byte(`{
	  "action": "submitted",
	  "review": {"id": 77, "user": {"login": "rev"}, "body": "ship it", "state": "approved",
	             "submitted_at": "2026-10-01T12:00:00Z", "html_url": "https://github.com/acme/widgets/pull/1#review-77"},
	  "pull_request": {"number": 1, "title": "Add widgets", "state": "open", "draft": false,
	                   "user": {"login": "author"}, "requested_reviewers": [],
	                   "head": {"sha": "head1", "ref": "feature", "repo": {"name": "widgets"}},
	                   "base": {"sha": "base-sha", "ref": "main", "repo": {"name": "widgets", "full_name": "acme/widgets", "owner": {"login": "acme"}}}},
	  "repository": {"id": 99, "name": "widgets", "full_name": "acme/widgets", "owner": {"login": "acme"}}
	}`)
	rec := httptest.NewRecorder()
	serveWebhook(rec, signedDelivery(t, config.DefaultWebhookPath, "pull_request_review", whSecret, payload))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	applyWebhookDelivery(<-queue)

	reviews := cachedReviewsOf(t, env.db, key)
	if len(reviews) != 1 || reviews[0].ID != 77 || reviews[0].State != "APPROVED" || reviews[0].User != "rev" {
		t.Fatalf("reviews = %+v, want rev's approval in the API's upper case", reviews)
	}
	meta := cachedMetadataOf(t, env.db, key)
	if approved, _ := meta["approved_by"].([]any); len(approved) != 1 || approved[0] != "rev" {
		t.Errorf("metadata approved_by = %v", meta["approved_by"])
	}
	if len(env.refetched) != 0 {
		t.Errorf("a review refetched the PR: %v", env.refetched)
	}
	if action, _ := env.db.GetLatestWorkflowAction(whOwner, whRepo, 1); action == nil || action.WorkflowName != webhookWorkflowName {
		t.Errorf("no action log entry for the delivery: %+v", action)
	}
}

func TestDeliveriesAboutUnknownPRsAreDropped(t *testing.T) {
	env := newWebhookTestEnv(t)
	review := &github.PullRequestReviewEvent{
		Action:      github.Ptr("submitted"),
		Review:      &github.PullRequestReview{ID: github.Ptr(int64(1)), User: &github.User{Login: github.Ptr("rev")}, State: github.Ptr("approved")},
		PullRequest: whPR(5, "head5"),
		Repo:        whRepository(),
	}
	applyWebhookEvent(webhookDelivery{eventType: "pull_request_review", event: review})
	if raw, _ := env.db.GetPRReviews(whOwner, whRepo, 5); raw != "" {
		t.Errorf("cached reviews for a PR nothing tracks: %s", raw)
	}

	// A PR a cycle cached but no section lists is still kept current: the
	// filters read its caches to decide whether it joins one.
	key := whKey(5)
	seedCachedPR(t, env.db, key, "head5")
	applyWebhookEvent(webhookDelivery{eventType: "pull_request_review", event: review})
	if got := cachedReviewsOf(t, env.db, key); len(got) != 1 {
		t.Errorf("reviews = %+v, want the delivery's review", got)
	}
}

func TestReviewDeliveryNeverStartsAReviewList(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	trackItem(t, env.db, key)
	env.db.UpsertPullRequest(key.Owner, key.Repo, key.Number, "head1", "", "")
	applyWebhookEvent(webhookDelivery{event: &github.PullRequestReviewEvent{
		Action:      github.Ptr("submitted"),
		Review:      &github.PullRequestReview{ID: github.Ptr(int64(1)), State: github.Ptr("approved")},
		PullRequest: whPR(1, "head1"),
		Repo:        whRepository(),
	}})
	if raw, _ := env.db.GetPRReviews(key.Owner, key.Repo, key.Number); raw != "" {
		t.Errorf("a single review became the PR's cached list: %s", raw)
	}
}

func TestReviewDismissalReplacesTheReview(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	cacheReviews(env.db, key, []*github.PullRequestReview{
		{ID: github.Ptr(int64(1)), User: &github.User{Login: github.Ptr("a")}, State: github.Ptr("COMMENTED")},
		{ID: github.Ptr(int64(2)), User: &github.User{Login: github.Ptr("me")}, State: github.Ptr("APPROVED")},
	})
	applyWebhookEvent(webhookDelivery{event: &github.PullRequestReviewEvent{
		Action:      github.Ptr("dismissed"),
		Review:      &github.PullRequestReview{ID: github.Ptr(int64(2)), User: &github.User{Login: github.Ptr("me")}, State: github.Ptr("dismissed")},
		PullRequest: whPR(1, "head1"),
		Repo:        whRepository(),
	}})
	got := cachedReviewsOf(t, env.db, key)
	if len(got) != 2 || got[1].ID != 2 || got[1].State != "DISMISSED" {
		t.Errorf("reviews = %+v, want #2 dismissed in place", got)
	}
}

func TestDeliveryForANewHeadRefetchesThePR(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	trackItem(t, env.db, key)

	applyWebhookEvent(webhookDelivery{event: &github.PullRequestEvent{
		Action: github.Ptr("synchronize"), PullRequest: whPR(1, "head2"), Repo: whRepository(),
	}})
	if !slices.Equal(env.refetched, []PRKey{key}) {
		t.Fatalf("refetched = %v, want the pushed PR", env.refetched)
	}

	// A review that finds the head moved — its push's delivery lost — fetches
	// rather than patching a review list about older code.
	applyWebhookEvent(webhookDelivery{event: &github.PullRequestReviewEvent{
		Action:      github.Ptr("submitted"),
		Review:      &github.PullRequestReview{ID: github.Ptr(int64(1)), State: github.Ptr("approved")},
		PullRequest: whPR(1, "head2"),
		Repo:        whRepository(),
	}})
	if len(env.refetched) != 2 {
		t.Errorf("refetched = %v, want a second fetch", env.refetched)
	}

	// So does a new base branch.
	env.refetched = nil
	applyWebhookEvent(webhookDelivery{event: &github.PullRequestEvent{
		Action:      github.Ptr("edited"),
		PullRequest: whPR(1, "head1"),
		Changes:     &github.EditChange{Base: &github.EditBase{Ref: &github.EditRef{From: github.Ptr("develop")}}},
		Repo:        whRepository(),
	}})
	if len(env.refetched) != 1 {
		t.Errorf("refetched = %v after the base changed", env.refetched)
	}
}

func TestPullRequestDeliveryUpdatesMetadataAndTheList(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	trackItem(t, env.db, key)

	pr := whPR(1, "head1")
	pr.Title = github.Ptr("Add better widgets")
	pr.State = github.Ptr("closed")
	pr.MergedAt = &github.Timestamp{Time: time.Now()}
	pr.Mergeable = github.Ptr(true)
	pr.RequestedReviewers = []*github.User{{Login: github.Ptr("someone")}}
	applyWebhookEvent(webhookDelivery{event: &github.PullRequestEvent{Action: github.Ptr("closed"), PullRequest: pr, Repo: whRepository()}})

	if len(env.refetched) != 0 {
		t.Errorf("closing a PR refetched it: %v", env.refetched)
	}
	meta := cachedMetadataOf(t, env.db, key)
	if meta["title"] != "Add better widgets" || meta["state"] != "closed" {
		t.Errorf("metadata = %v", meta)
	}
	if raw, _ := env.db.GetRequestedReviewers(key.Owner, key.Repo, key.Number); !strings.Contains(raw, "someone") {
		t.Errorf("requested reviewers = %s", raw)
	}
	section, _ := env.db.GetSection("", "Reviews")
	item, _ := env.db.GetItem(section.ID, "acme/widgets-1")
	if item.Status != "DONE" || item.Title != "Add better widgets" || !strings.Contains(item.Tags, "merged") {
		t.Errorf("list row = %+v", item)
	}
	if details, _ := item.GetDetails(); !slices.Equal(details, []string{"detail"}) {
		t.Errorf("details rewritten: %v", details)
	}
}

func TestReviewCommentDeliveries(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	root := &github.PullRequestComment{
		ID: github.Ptr(int64(10)), URL: github.Ptr("https://api.github.com/repos/acme/widgets/pulls/comments/10"),
		DiffHunk: github.Ptr("@@"), Path: github.Ptr("w.go"), Body: github.Ptr("why?"),
		User: &github.User{Login: github.Ptr("rev")}, CreatedAt: &github.Timestamp{Time: time.Unix(100, 0)},
	}
	j, _ := json.Marshal([]*github.PullRequestComment{root})
	env.db.UpsertPRComments(key.Owner, key.Repo, key.Number, string(j))
	env.db.UpsertPRReviewThreads(key.Owner, key.Repo, key.Number, `[{"id":"PRRT_1","comment_ids":[10]}]`)

	deliver := func(action string, c *github.PullRequestComment) {
		applyWebhookEvent(webhookDelivery{event: &github.PullRequestReviewCommentEvent{
			Action: github.Ptr(action), Comment: c, PullRequest: whPR(1, "head1"), Repo: whRepository(),
		}})
	}
	threads := func() []git_tools.ReviewThread {
		got, _ := loadCachedReviewThreads(env.db, key)
		return got
	}
	commentIDs := func() []int64 {
		got, _ := loadCachedComments(env.db, key)
		var ids []int64
		for _, c := range got {
			ids = append(ids, c.GetID())
		}
		return ids
	}

	// A reply joins its thread without a lookup.
	reply := &github.PullRequestComment{
		ID: github.Ptr(int64(11)), InReplyTo: github.Ptr(int64(10)), URL: github.Ptr("https://api.github.com/repos/acme/widgets/pulls/comments/11"),
		DiffHunk: github.Ptr("@@"), Path: github.Ptr("w.go"), Body: github.Ptr("because"),
		User: &github.User{Login: github.Ptr("author")}, CreatedAt: &github.Timestamp{Time: time.Unix(200, 0)},
	}
	deliver("created", reply)
	if got := commentIDs(); !slices.Equal(got, []int64{10, 11}) {
		t.Errorf("comments = %v", got)
	}
	if got := threads(); len(got) != 1 || !slices.Equal(got[0].CommentIDs, []int64{10, 11}) {
		t.Errorf("threads = %+v", got)
	}
	if len(env.threadFetch) != 0 {
		t.Error("a reply looked the threads up")
	}

	// An edit replaces the comment.
	edited := *reply
	edited.Body = github.Ptr("because, edited")
	deliver("edited", &edited)
	if got, _ := loadCachedComments(env.db, key); len(got) != 2 || got[1].GetBody() != "because, edited" {
		t.Errorf("edit not applied: %v", got)
	}

	// A new thread's ID is only to be had from GraphQL.
	env.fetchThreads = []git_tools.ReviewThread{{ID: "PRRT_1", CommentIDs: []int64{10, 11}}, {ID: "PRRT_2", CommentIDs: []int64{12}}}
	deliver("created", &github.PullRequestComment{
		ID: github.Ptr(int64(12)), URL: github.Ptr("https://api.github.com/repos/acme/widgets/pulls/comments/12"),
		DiffHunk: github.Ptr("@@"), Path: github.Ptr("x.go"), User: &github.User{Login: github.Ptr("rev")},
		CreatedAt: &github.Timestamp{Time: time.Unix(300, 0)},
	})
	if len(env.threadFetch) != 1 || len(threads()) != 2 {
		t.Errorf("new thread: fetched %v, threads %+v", env.threadFetch, threads())
	}

	// A deletion leaves the comments and the thread.
	deliver("deleted", reply)
	if got := commentIDs(); !slices.Equal(got, []int64{10, 12}) {
		t.Errorf("comments after delete = %v", got)
	}
	if got := threads(); !slices.Equal(got[0].CommentIDs, []int64{10}) {
		t.Errorf("threads after delete = %+v", got)
	}
}

func TestIssueCommentDeliveries(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	// A review comment sharing the conversation comment's ID must survive it.
	review := &github.PullRequestComment{ID: github.Ptr(int64(5)), URL: github.Ptr("https://api.github.com/repos/acme/widgets/pulls/comments/5"),
		DiffHunk: github.Ptr("@@"), User: &github.User{Login: github.Ptr("rev")}, CreatedAt: &github.Timestamp{Time: time.Unix(1, 0)}}
	j, _ := json.Marshal([]*github.PullRequestComment{review})
	env.db.UpsertPRComments(key.Owner, key.Repo, key.Number, string(j))

	comment := &github.IssueComment{ID: github.Ptr(int64(5)), URL: github.Ptr("https://api.github.com/repos/acme/widgets/issues/comments/5"),
		Body: github.Ptr("thanks"), User: &github.User{Login: github.Ptr("author")}, CreatedAt: &github.Timestamp{Time: time.Unix(2, 0)}}
	issue := &github.Issue{Number: github.Ptr(1), PullRequestLinks: &github.PullRequestLinks{URL: github.Ptr("x")}}
	deliver := func(action string, issue *github.Issue) {
		applyWebhookEvent(webhookDelivery{event: &github.IssueCommentEvent{Action: github.Ptr(action), Issue: issue, Comment: comment, Repo: whRepository()}})
	}

	deliver("created", issue)
	got, _ := loadCachedComments(env.db, key)
	if len(got) != 2 || !isConversationComment(got[1]) || got[1].GetBody() != "thanks" {
		t.Fatalf("comments = %v", got)
	}
	deliver("deleted", issue)
	got, _ = loadCachedComments(env.db, key)
	if len(got) != 1 || isConversationComment(got[0]) {
		t.Errorf("deleting the conversation comment took the wrong one: %v", got)
	}

	// The same event about an issue, not a PR, is not ours.
	deliver("created", &github.Issue{Number: github.Ptr(1)})
	if got, _ = loadCachedComments(env.db, key); len(got) != 1 {
		t.Errorf("an issue's comment landed on the PR: %v", got)
	}
}

func TestReviewThreadDeliveries(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	env.db.UpsertPRReviewThreads(key.Owner, key.Repo, key.Number, `[{"id":"PRRT_1","comment_ids":[10,11]}]`)
	deliver := func(action, nodeID string, ids ...int64) {
		var comments []*github.PullRequestComment
		for _, id := range ids {
			comments = append(comments, &github.PullRequestComment{ID: github.Ptr(id), Path: github.Ptr("w.go")})
		}
		applyWebhookEvent(webhookDelivery{event: &github.PullRequestReviewThreadEvent{
			Action: github.Ptr(action), Thread: &github.PullRequestThread{NodeID: github.Ptr(nodeID), Comments: comments},
			PullRequest: whPR(1, "head1"), Repo: whRepository(), Sender: &github.User{Login: github.Ptr("rev")},
		}})
	}

	deliver("resolved", "PRRT_1", 10, 11)
	got, _ := loadCachedReviewThreads(env.db, key)
	if len(got) != 1 || !got[0].IsResolved || got[0].ResolvedBy != "rev" {
		t.Fatalf("threads = %+v", got)
	}
	deliver("unresolved", "", 11) // matched by comment when the ID is missing
	got, _ = loadCachedReviewThreads(env.db, key)
	if got[0].IsResolved || got[0].ResolvedBy != "" {
		t.Errorf("threads = %+v", got)
	}
	deliver("resolved", "PRRT_9", 20)
	if got, _ = loadCachedReviewThreads(env.db, key); len(got) != 2 || got[1].ID != "PRRT_9" || !got[1].IsResolved {
		t.Errorf("unknown thread not added: %+v", got)
	}
}

func TestCheckRunDeliveries(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	app := &github.App{ID: github.Ptr(int64(15368))}
	run := func(id int64, status, conclusion string) *github.CheckRun {
		r := &github.CheckRun{ID: github.Ptr(id), Name: github.Ptr("build"), HeadSHA: github.Ptr("head1"), App: app, Status: github.Ptr(status)}
		if conclusion != "" {
			r.Conclusion = github.Ptr(conclusion)
		}
		return r
	}
	deliver := func(r *github.CheckRun, prs ...*github.PullRequest) {
		r.PullRequests = prs
		applyWebhookEvent(webhookDelivery{event: &github.CheckRunEvent{Action: github.Ptr("completed"), CheckRun: r, Repo: whRepository()}})
	}
	checks := func() []*github.CheckRun {
		_, runs, _ := loadCachedCI(env.db, key, "head1")
		return runs.CheckRuns
	}

	// Named by the run, as for a branch in the repository itself.
	deliver(run(1, "completed", "failure"), &github.PullRequest{Number: github.Ptr(1)})
	if got := checks(); len(got) != 1 || got[0].GetConclusion() != "failure" {
		t.Fatalf("check runs = %v", got)
	}
	if meta := cachedMetadataOf(t, env.db, key); !strings.HasPrefix(meta["ci_status"].(string), "failure") {
		t.Errorf("metadata ci_status = %v", meta["ci_status"])
	}

	// A re-run replaces the failed attempt — found by head SHA, as for a fork.
	deliver(run(2, "in_progress", ""))
	if got := checks(); len(got) != 1 || got[0].GetID() != 2 {
		t.Fatalf("re-run didn't replace the attempt: %v", got)
	}
	// And a late delivery about the old attempt changes nothing.
	deliver(run(1, "completed", "failure"))
	if got := checks(); len(got) != 1 || got[0].GetID() != 2 {
		t.Errorf("stale attempt came back: %v", got)
	}
	deliver(run(2, "completed", "success"))
	if meta := cachedMetadataOf(t, env.db, key); !strings.HasPrefix(meta["ci_status"].(string), "success") {
		t.Errorf("metadata ci_status = %v", meta["ci_status"])
	}

	// A commit with no CI cached isn't started from one run.
	other := run(3, "completed", "success")
	other.HeadSHA = github.Ptr("head-unknown")
	deliver(other, &github.PullRequest{Number: github.Ptr(1)})
	if raw, _ := env.db.GetCIStatus(key.Owner, key.Repo, key.Number, "head-unknown"); raw != "" {
		t.Errorf("CI started from a single run: %s", raw)
	}
}

func TestStatusDeliveries(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	deliver := func(context, state string, at int64) {
		applyWebhookEvent(webhookDelivery{event: &github.StatusEvent{
			SHA: github.Ptr("head1"), Context: github.Ptr(context), State: github.Ptr(state),
			UpdatedAt: &github.Timestamp{Time: time.Unix(at, 0)}, Repo: whRepository(),
		}})
	}
	status := func() *github.CombinedStatus {
		s, _, _ := loadCachedCI(env.db, key, "head1")
		return s
	}

	deliver("ci/jenkins", "pending", 10)
	deliver("ci/lint", "success", 10)
	if s := status(); s.GetState() != "pending" || s.GetTotalCount() != 2 {
		t.Fatalf("status = %+v", s)
	}
	deliver("ci/jenkins", "failure", 20)
	if s := status(); s.GetState() != "failure" || s.GetTotalCount() != 2 {
		t.Fatalf("status = %+v", s)
	}
	deliver("ci/jenkins", "success", 5) // older than what's cached
	if s := status(); s.GetState() != "failure" {
		t.Errorf("a stale status won: %+v", s)
	}
	deliver("ci/jenkins", "success", 30)
	if s := status(); s.GetState() != "success" {
		t.Errorf("status = %+v", s)
	}
}

func TestCIStatusInfo(t *testing.T) {
	runs := &github.ListCheckRunsResults{CheckRuns: []*github.CheckRun{
		{Name: github.Ptr("build"), Status: github.Ptr("completed"), Conclusion: github.Ptr("success")},
		{Name: github.Ptr("docs"), Status: github.Ptr("completed"), Conclusion: github.Ptr("skipped")},
	}}
	if got := ciStatusInfo(nil, runs); got.OverallStatus != "DONE" || len(got.Statuses) != 2 {
		t.Errorf("all passing = %+v", got)
	}
	runs.CheckRuns = append(runs.CheckRuns, &github.CheckRun{Name: github.Ptr("test"), Status: github.Ptr("in_progress")})
	if got := ciStatusInfo(nil, runs); got.OverallStatus != "WAITING" {
		t.Errorf("one running = %+v", got)
	}
	status := &github.CombinedStatus{Statuses: []*github.RepoStatus{{Context: github.Ptr("ci/jenkins"), State: github.Ptr("error")}}}
	if got := ciStatusInfo(status, runs); got.OverallStatus != "TODO" {
		t.Errorf("one failed = %+v", got)
	}
	if got := ciStatusInfo(nil, nil); got.OverallStatus != "DONE" || len(got.Statuses) != 0 {
		t.Errorf("no CI = %+v", got)
	}
}

func TestMergeabilityStandsUntilTheHeadOrBaseMoves(t *testing.T) {
	env := newWebhookTestEnv(t)
	ref := git_tools.PRRef{Owner: whOwner, Repo: whRepo, Number: 1}
	pr := whPR(1, "head1")
	t.Cleanup(func() { pruneWebhookMemory(time.Now().Add(time.Hour)) })

	if mergeabilityCurrent(env.db, ref, pr) {
		t.Fatal("current with nothing recorded")
	}
	env.db.RecordPRMergeability(whOwner, whRepo, 1, "head1", git_tools.MergeabilityConflicting)
	if mergeabilityCurrent(env.db, ref, pr) {
		t.Fatal("current although this process never asked")
	}
	markMergeabilityChecked(ref, time.Now())
	if !mergeabilityCurrent(env.db, ref, pr) {
		t.Fatal("expected the recorded answer to stand")
	}
	if mergeabilityCurrent(env.db, ref, whPR(1, "head2")) {
		t.Error("current although the head moved")
	}
	recordBaseMoved(whOwner, whRepo, "develop", time.Now())
	if !mergeabilityCurrent(env.db, ref, pr) {
		t.Error("a push to another branch made it stale")
	}
	applyWebhookEvent(webhookDelivery{event: &github.PushEvent{
		Ref:  github.Ptr("refs/heads/main"),
		Repo: &github.PushEventRepository{Name: github.Ptr(whRepo), FullName: github.Ptr("acme/widgets"), Owner: &github.User{Login: github.Ptr(whOwner)}},
	}})
	if mergeabilityCurrent(env.db, ref, pr) {
		t.Error("still current after a push to the PR's base")
	}
}

func TestRecordMergeabilityAsksOnlyAboutStaleAnswersInAWebhookCycle(t *testing.T) {
	env := newWebhookTestEnv(t)
	t.Cleanup(func() { pruneWebhookMemory(time.Now().Add(time.Hour)) })
	var asked []int
	old := lookupMergeability
	lookupMergeability = func(refs []git_tools.PRRef) (map[git_tools.PRRef]git_tools.Mergeability, error) {
		answers := map[git_tools.PRRef]git_tools.Mergeability{}
		for _, ref := range refs {
			asked = append(asked, ref.Number)
			answers[ref] = git_tools.Mergeability{State: git_tools.MergeabilityMergeable, HeadSHA: "head" + itoa(ref.Number)}
		}
		return answers, nil
	}
	t.Cleanup(func() { lookupMergeability = old })
	prs := []*github.PullRequest{whPR(1, "head1"), whPR(2, "head2")}

	// Polling asks about every PR.
	recordMergeability(env.db, prs)
	if !slices.Equal(asked, []int{1, 2}) && !slices.Equal(asked, []int{2, 1}) {
		t.Fatalf("asked = %v", asked)
	}

	// A webhook cycle only about the one whose head moved.
	withCycleCoverage(t, whOwner+"/"+whRepo)
	asked = nil
	recordMergeability(env.db, []*github.PullRequest{whPR(1, "head1"), whPR(2, "head2-pushed")})
	if !slices.Equal(asked, []int{2}) {
		t.Errorf("asked = %v, want only #2", asked)
	}
}

func TestInteractionStateFromCache(t *testing.T) {
	env := newWebhookTestEnv(t)
	cfg := config.C()
	cfg.GithubUsername = "me"
	config.SetC(cfg)
	key := whKey(1)
	pr := whPR(1, "head1")
	seedCachedPR(t, env.db, key, "head1")
	at := func(s int64) *github.Timestamp { return &github.Timestamp{Time: time.Unix(s, 0)} }
	comments := []*github.PullRequestComment{
		{ID: github.Ptr(int64(1)), URL: github.Ptr("https://api.github.com/repos/acme/widgets/pulls/comments/1"),
			DiffHunk: github.Ptr("@@"), User: &github.User{Login: github.Ptr("me")}, CreatedAt: at(100)},
		{ID: github.Ptr(int64(2)), InReplyTo: github.Ptr(int64(1)), URL: github.Ptr("https://api.github.com/repos/acme/widgets/pulls/comments/2"),
			DiffHunk: github.Ptr("@@"), User: &github.User{Login: github.Ptr("author")}, CreatedAt: at(200)},
	}
	j, _ := json.Marshal(comments)
	env.db.UpsertPRComments(key.Owner, key.Repo, key.Number, string(j))
	env.db.UpsertPRCommits(key.Owner, key.Repo, key.Number, `[{"sha":"c1","date":"2026-01-01T00:00:00Z","committed_at":"2026-02-01T00:00:00Z"}]`)

	source := webhookPRState{}
	if _, ok := source.InteractionState(whOwner, whRepo, "me", pr); ok {
		t.Fatal("answered outside a webhook cycle")
	}
	withCycleCoverage(t, whOwner+"/"+whRepo)
	state, ok := source.InteractionState(whOwner, whRepo, "me", pr)
	if !ok {
		t.Fatal("didn't answer for a covered, cached PR")
	}
	want := git_tools.CalculateInteractionState("me", pr, nil, comments, nil)
	want.LastCommitTime = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if !state.HasUnrespondedComments || !state.LastCommitTime.Equal(want.LastCommitTime) || !state.LastMeTime.Equal(want.LastMeTime) {
		t.Errorf("state = %+v, want %+v", state, want)
	}
	// Another login asking gets its own standing from the same caches.
	theirs, ok := source.InteractionState(whOwner, whRepo, "author", pr)
	wantTheirs := git_tools.CalculateInteractionState("author", pr, nil, comments, nil)
	if !ok || theirs.HasUnrespondedComments != wantTheirs.HasUnrespondedComments || !theirs.LastMeTime.Equal(wantTheirs.LastMeTime) ||
		theirs.LastMeTime.Equal(state.LastMeTime) {
		t.Errorf("author's state = %+v, %v; want %+v", theirs, ok, wantTheirs)
	}
	if _, ok := source.InteractionState(whOwner, whRepo, "me", whPR(1, "head2")); ok {
		t.Error("answered from caches about an older head")
	}

	info, ok := source.CIStatus(pr)
	if !ok || info.OverallStatus != "DONE" {
		t.Errorf("CIStatus = %+v, %v", info, ok)
	}
}

func TestSyncWebhookListener(t *testing.T) {
	newWebhookTestEnv(t)
	t.Cleanup(func() { syncWebhookListener(config.Config{}) })

	cfg := config.C()
	syncWebhookListener(cfg)
	since, listening := webhooksListeningSince()
	if !listening {
		t.Fatal("not listening with webhooks configured")
	}
	webhookListener.mu.Lock()
	server := webhookListener.server
	webhookListener.mu.Unlock()

	// The same config leaves the listener be.
	syncWebhookListener(cfg)
	webhookListener.mu.Lock()
	same := webhookListener.server == server
	webhookListener.mu.Unlock()
	if again, _ := webhooksListeningSince(); !same || !again.Equal(since) {
		t.Error("listener restarted for an unchanged config")
	}

	// Without a secret nothing listens.
	cfg.Webhooks.Secret = ""
	t.Setenv(config.WebhookSecretEnv, "")
	syncWebhookListener(cfg)
	if _, listening := webhooksListeningSince(); listening {
		t.Error("still listening without a secret")
	}
}

// The worker applies deliveries in order, one at a time.
func TestWebhookWorkerAppliesDeliveriesInOrder(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	queue := make(chan webhookDelivery, 3)
	for i, state := range []string{"commented", "changes_requested", "approved"} {
		queue <- webhookDelivery{event: &github.PullRequestReviewEvent{
			Action:      github.Ptr("submitted"),
			Review:      &github.PullRequestReview{ID: github.Ptr(int64(i + 1)), User: &github.User{Login: github.Ptr("rev")}, State: github.Ptr(state)},
			PullRequest: whPR(1, "head1"),
			Repo:        whRepository(),
		}}
	}
	close(queue)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for d := range queue {
			applyWebhookDelivery(d)
		}
	}()
	wg.Wait()
	got := cachedReviewsOf(t, env.db, key)
	if len(got) != 3 || got[2].State != "APPROVED" {
		t.Errorf("reviews = %+v", got)
	}
	meta := cachedMetadataOf(t, env.db, key)
	if approved, _ := meta["approved_by"].([]any); len(approved) != 1 {
		t.Errorf("approved_by = %v", meta["approved_by"])
	}
}

// A delivery that would panic is contained to that delivery.
func TestApplyWebhookDeliveryRecoversFromPanics(t *testing.T) {
	newWebhookTestEnv(t)
	old := refetchPR
	refetchPR = func(PRKey, *github.PullRequest, string) { panic("boom") }
	t.Cleanup(func() { refetchPR = old })
	env := config.C().DB
	seedCachedPR(t, env, whKey(1), "head1")
	applyWebhookDelivery(webhookDelivery{event: &github.PullRequestEvent{
		Action: github.Ptr("synchronize"), PullRequest: whPR(1, "head2"), Repo: whRepository(),
	}})
}
