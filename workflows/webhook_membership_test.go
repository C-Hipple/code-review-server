package workflows

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

// membershipEnv is a webhook test environment in which webhooks are trusted
// for acme/widgets and the given workflows are the manager's.
func membershipEnv(t *testing.T, workflows ...Workflow) *webhookTestEnv {
	t.Helper()
	env := newWebhookTestEnv(t)
	cfg := config.C()
	cfg.GithubUsername = "me"
	config.SetC(cfg)
	withCycleCoverage(t, whOwner+"/"+whRepo)
	setActiveWorkflows(workflows)
	// Registered by the listener in a running server, which is the only way
	// webhooks come to be trusted.
	git_tools.SetPRStateSource(webhookPRState{})
	t.Cleanup(func() {
		git_tools.SetPRStateSource(nil)
		setActiveWorkflows(nil)
		reevaluations.mu.Lock()
		reevaluations.pending = nil
		reevaluations.mu.Unlock()
		takeWebhookActivity()
	})
	for _, wf := range workflows {
		if _, err := env.db.GetOrCreateSection(wf.GetOrgSectionName(), 1); err != nil {
			t.Fatalf("section: %v", err)
		}
	}
	return env
}

// listIn puts PR #number in wf's section, owned by wf, as a cycle would.
func listIn(t *testing.T, db *database.DB, wf Workflow, number int) {
	t.Helper()
	section, _ := db.GetSection(wf.GetOrgSectionName())
	if _, err := db.UpsertItemWithWorkflow(section.ID, "acme/widgets-"+itoa(number), "TODO", "Add widgets", []string{"old"}, []string{whRepo}, 0, wf.GetName()); err != nil {
		t.Fatalf("list: %v", err)
	}
}

// listedIn reports whether wf's section lists PR #number on wf's behalf.
func listedIn(t *testing.T, db *database.DB, wf Workflow, number int) bool {
	t.Helper()
	section, _ := db.GetSection(wf.GetOrgSectionName())
	item, err := db.GetItem(section.ID, "acme/widgets-"+itoa(number))
	return err == nil && item != nil && slices.Contains(item.GetWorkflows(), wf.GetName())
}

func reviewRequestedWorkflow() SearchWorkflow {
	return SearchWorkflow{Name: "review-requested", SectionTitle: "Review Requested", Mode: ModeReviewRequested, Login: "me"}
}

func requestedOfMe(pr *github.PullRequest) *github.PullRequest {
	pr.RequestedReviewers = []*github.User{{Login: github.Ptr("me")}}
	return pr
}

// The case that started this: reviewing a PR takes it out of my review queue
// as soon as the review's delivery lands, not a cycle later — even when the
// delivery's PR still lists me.
func TestSubmittingAReviewTakesThePROutOfReviewRequested(t *testing.T) {
	wf := reviewRequestedWorkflow()
	env := membershipEnv(t, wf)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	listIn(t, env.db, wf, 1)

	applyWebhookEvent(webhookDelivery{event: &github.PullRequestReviewEvent{
		Action:      github.Ptr("submitted"),
		Review:      &github.PullRequestReview{ID: github.Ptr(int64(9)), User: &github.User{Login: github.Ptr("me")}, State: github.Ptr("approved")},
		PullRequest: requestedOfMe(whPR(1, "head1")),
		Repo:        whRepository(),
	}})
	drainReevaluations()

	if listedIn(t, env.db, wf, 1) {
		t.Fatal("the PR is still in Review Requested after I reviewed it")
	}
	section, _ := env.db.GetSection(wf.SectionTitle)
	if item, _ := env.db.GetItem(section.ID, "acme/widgets-1"); item != nil {
		t.Error("the row was left behind with no workflow claiming it")
	}
	if raw, _ := env.db.GetRequestedReviewers(1, whRepo); strings.Contains(raw, `"me"`) {
		t.Errorf("requested reviewers still list me: %s", raw)
	}
	if a := takeWebhookActivity(); a.SectionDrops != 1 || a.Applied != 1 {
		t.Errorf("activity = %+v", a)
	}
}

func TestAReviewRequestPutsThePRIntoReviewRequested(t *testing.T) {
	wf := reviewRequestedWorkflow()
	env := membershipEnv(t, wf)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")

	applyWebhookEvent(webhookDelivery{event: &github.PullRequestEvent{
		Action:      github.Ptr("review_requested"),
		PullRequest: requestedOfMe(whPR(1, "head1")),
		Repo:        whRepository(),
	}})
	drainReevaluations()

	if !listedIn(t, env.db, wf, 1) {
		t.Fatal("a PR requesting my review didn't join Review Requested")
	}
	section, _ := env.db.GetSection(wf.SectionTitle)
	item, _ := env.db.GetItem(section.ID, "acme/widgets-1")
	if details, _ := item.GetDetails(); !strings.Contains(strings.Join(details, "\n"), "Requested Reviewers: me") {
		t.Errorf("row details not built from the caches: %v", details)
	}
	if a := takeWebhookActivity(); a.SectionAdds != 1 {
		t.Errorf("activity = %+v", a)
	}
}

func TestTeamReviewRequests(t *testing.T) {
	t.Setenv("CRS_GITHUB_TOKEN", "test-token")
	pr := whPR(1, "head1")
	pr.RequestedTeams = []*github.Team{{Slug: github.Ptr("core")}}

	git_tools.GlobalCache.Set("my_team_refs", []string{"Acme/Core"}, time.Minute)
	if got := reviewRequestedOf(pr, "me"); got != memberYes {
		t.Errorf("my team's request: %v", got)
	}
	git_tools.GlobalCache.Set("my_team_refs", []string{"acme/other"}, time.Minute)
	if got := reviewRequestedOf(pr, "me"); got != memberNo {
		t.Errorf("another team's request: %v", got)
	}
	if got := reviewRequestedOf(whPR(1, "head1"), "me"); got != memberNo {
		t.Errorf("no request at all: %v", got)
	}

	// Without knowing my teams, a team request can't be judged.
	t.Setenv("CRS_GITHUB_TOKEN", "")
	if got := reviewRequestedOf(pr, "me"); got != memberUnknown {
		t.Errorf("unknown teams: %v", got)
	}
}

func TestSyncWorkflowFiltersDecideMembership(t *testing.T) {
	wf, err := BuildSyncReviewRequestWorkflow(&config.RawWorkflow{
		Name: "ready", SectionTitle: "Ready", Repos: []string{"acme/widgets"}, Filters: []string{"FilterNotDraft"},
	}, &[]string{})
	if err != nil {
		t.Fatal(err)
	}
	env := membershipEnv(t, wf)
	seedCachedPR(t, env.db, whKey(1), "head1")
	listIn(t, env.db, wf, 1)
	deliver := func(action string, draft bool) {
		pr := whPR(1, "head1")
		pr.Draft = github.Ptr(draft)
		applyWebhookEvent(webhookDelivery{event: &github.PullRequestEvent{Action: github.Ptr(action), PullRequest: pr, Repo: whRepository()}})
		drainReevaluations()
	}

	deliver("converted_to_draft", true)
	if listedIn(t, env.db, wf, 1) {
		t.Fatal("a draft is still in a FilterNotDraft section")
	}
	deliver("ready_for_review", false)
	if !listedIn(t, env.db, wf, 1) {
		t.Fatal("ready for review again, but not back in the section")
	}

	// A PR in a repository the workflow doesn't list never joins it.
	other := whPR(2, "head2")
	other.Base.Repo = &github.Repository{Name: github.Ptr("gadgets"), FullName: github.Ptr("acme/gadgets"), Owner: &github.User{Login: github.Ptr("acme")}}
	if got := wf.(webhookMember).webhookMembership(other); got != memberNo {
		t.Errorf("membership of another repository's PR = %v", got)
	}
}

// A check run carries no PR object; the PR is judged as last seen.
func TestDeliveriesWithoutAPRUseTheRememberedOne(t *testing.T) {
	wf, err := BuildSyncReviewRequestWorkflow(&config.RawWorkflow{
		Name: "green", SectionTitle: "Green", Repos: []string{"acme/widgets"}, Filters: []string{"FilterCIPassing"},
	}, &[]string{})
	if err != nil {
		t.Fatal(err)
	}
	env := membershipEnv(t, wf)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	listIn(t, env.db, wf, 1)

	// Nothing remembered: nothing to judge by, so the row stays.
	run := &github.CheckRun{ID: github.Ptr(int64(1)), Name: github.Ptr("build"), HeadSHA: github.Ptr("head1"),
		Status: github.Ptr("completed"), Conclusion: github.Ptr("failure"), PullRequests: []*github.PullRequest{{Number: github.Ptr(1)}}}
	applyWebhookEvent(webhookDelivery{event: &github.CheckRunEvent{Action: github.Ptr("completed"), CheckRun: run, Repo: whRepository()}})
	drainReevaluations()
	if !listedIn(t, env.db, wf, 1) {
		t.Fatal("dropped without a PR to judge by")
	}

	rememberPR(key, whPR(1, "head1"))
	applyWebhookEvent(webhookDelivery{event: &github.CheckRunEvent{Action: github.Ptr("completed"), CheckRun: run, Repo: whRepository()}})
	drainReevaluations()
	if listedIn(t, env.db, wf, 1) {
		t.Error("a PR whose CI just failed is still in a FilterCIPassing section")
	}
}

func TestRememberPRKeepsTheNewer(t *testing.T) {
	t.Cleanup(func() { pruneRememberedPRs(time.Now().Add(time.Hour)) })
	key := whKey(1)
	newer, older := whPR(1, "head2"), whPR(1, "head1")
	newer.UpdatedAt = &github.Timestamp{Time: time.Unix(200, 0)}
	older.UpdatedAt = &github.Timestamp{Time: time.Unix(100, 0)}

	rememberPR(key, newer)
	rememberPR(PRKey{Owner: "ACME", Repo: "Widgets", Number: 1}, older) // a listing taken before the delivery
	if got := rememberedPRFor(key); got.GetHead().GetSHA() != "head2" {
		t.Errorf("remembered %s, want the newer head2", got.GetHead().GetSHA())
	}
	pruneRememberedPRs(time.Now().Add(time.Minute))
	if rememberedPRFor(key) != nil {
		t.Error("prune kept a PR nothing mentioned since")
	}
}

// A cycle writing sections from a listing older than a delivery mustn't undo
// what the delivery decided, so re-evaluation waits for it.
func TestReevaluationWaitsForACycleInProgress(t *testing.T) {
	wf := reviewRequestedWorkflow()
	env := membershipEnv(t, wf)
	seedCachedPR(t, env.db, whKey(1), "head1")
	listIn(t, env.db, wf, 1)

	membershipMu.Lock()
	applyWebhookEvent(webhookDelivery{event: &github.PullRequestEvent{
		Action: github.Ptr("review_request_removed"), PullRequest: whPR(1, "head1"), Repo: whRepository(),
	}})
	done := make(chan struct{})
	go func() {
		drainReevaluations()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("re-evaluated while a cycle held the sections")
	case <-time.After(50 * time.Millisecond):
	}
	if !listedIn(t, env.db, wf, 1) {
		t.Fatal("the row changed while a cycle held the sections")
	}
	membershipMu.Unlock()
	<-done
	if listedIn(t, env.db, wf, 1) {
		t.Error("not dropped once the cycle finished")
	}
}

func TestNoReevaluationUnlessWebhooksAreTrusted(t *testing.T) {
	wf := reviewRequestedWorkflow()
	env := membershipEnv(t, wf)
	activeCoverage.Store(nil) // e.g. a full refresh is running
	seedCachedPR(t, env.db, whKey(1), "head1")
	listIn(t, env.db, wf, 1)

	applyWebhookEvent(webhookDelivery{event: &github.PullRequestEvent{
		Action: github.Ptr("review_request_removed"), PullRequest: whPR(1, "head1"), Repo: whRepository(),
	}})
	drainReevaluations()
	if !listedIn(t, env.db, wf, 1) {
		t.Error("sections changed while webhooks weren't trusted; the cycle decides then")
	}
}

func TestProjectListMembershipOnlyDrops(t *testing.T) {
	wf := ProjectListWorkflow{Name: "epic", SectionTitle: "Epic", Filters: []git_tools.PRFilter{git_tools.FilterNotDraft}}
	if got := wf.webhookMembership(whPR(1, "head1")); got != memberUnknown {
		t.Errorf("membership = %v, want unknown: only Jira knows the epic's PRs", got)
	}
	draft := whPR(1, "head1")
	draft.Draft = github.Ptr(true)
	if got := wf.webhookMembership(draft); got != memberNo {
		t.Errorf("membership = %v for a PR its filters reject", got)
	}
}
