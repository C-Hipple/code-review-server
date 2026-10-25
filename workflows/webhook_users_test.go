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

// Webhook deliveries on a hosted server: one delivery serves every dashboard
// the cycles do. The server's own and two registered users' — alice and bob,
// both active — share workflow names and section titles, and so does an idle
// user's, carol's, which the cycles skip.

// The workflows every dashboard here has, under the same names and sections:
// a review queue, and the acme/widgets PRs whose CI passes.
var (
	requestedRaw = config.RawWorkflow{WorkflowType: "MyReviewRequestsWorkflow", Name: "requested", SectionTitle: "Review Requested"}
	greenRaw     = config.RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: "green", SectionTitle: "Green",
		Repos: []string{"acme/widgets"}, Filters: []string{"FilterCIPassing"}}
)

// hostedDashboards are the logins whose dashboards the tests look at: "" is
// the server's own, whose config workflows are about "me", the token's
// account.
var hostedDashboards = []string{"", "alice", "bob", "carol"}

type hostedWebhookEnv struct {
	*webhookTestEnv
}

// newHostedWebhookEnv is a hosted server with webhooks trusted for
// acme/widgets, the dashboards above registered, and the run set of a cycle
// that has just begun — alice's, bob's and the server's workflows, not
// carol's — as the workflows deliveries follow. Every dashboard has its
// sections, carol's from the cycles she was last active for.
func newHostedWebhookEnv(t *testing.T) *hostedWebhookEnv {
	t.Helper()
	env := &hostedWebhookEnv{newWebhookTestEnv(t)}
	cfg := config.C()
	cfg.GithubUsername = "me"
	cfg.UserActiveHours = 24
	server, serverGreen := requestedRaw, greenRaw
	server.GithubUsername, serverGreen.GithubUsername = "me", "me"
	cfg.RawWorkflows = []config.RawWorkflow{server, serverGreen}
	config.SetC(cfg)

	now := time.Now()
	registerUser(t, env.db, database.User{Login: "alice", LastSeen: now}, requestedRaw, greenRaw)
	registerUser(t, env.db, database.User{Login: "bob", LastSeen: now}, requestedRaw, greenRaw)
	registerUser(t, env.db, database.User{Login: "carol", LastSeen: now.Add(-72 * time.Hour)}, requestedRaw, greenRaw)

	run := RunSet(config.C(), now)
	if got := len(run); got != 6 {
		t.Fatalf("run set has %d workflows, want the server's, alice's and bob's two each", got)
	}
	for _, user := range hostedDashboards {
		for _, raw := range []config.RawWorkflow{requestedRaw, greenRaw} {
			if _, err := env.db.GetOrCreateSection(user, raw.SectionTitle, 1); err != nil {
				t.Fatal(err)
			}
		}
	}
	setActiveWorkflows(run)
	withCycleCoverage(t, whOwner+"/"+whRepo)
	git_tools.SetPRStateSource(webhookPRState{})
	t.Cleanup(func() {
		git_tools.SetPRStateSource(nil)
		setActiveWorkflows(nil)
		reevaluations.mu.Lock()
		reevaluations.pending = nil
		reevaluations.mu.Unlock()
		takeWebhookActivity()
	})
	return env
}

// row is PR #number's row in user's section of raw's, or nil.
func (env *hostedWebhookEnv) row(t *testing.T, user string, raw config.RawWorkflow, number int) *database.Item {
	t.Helper()
	section, err := env.db.GetSection(user, raw.SectionTitle)
	if err != nil {
		t.Fatalf("%q has no %q section: %v", user, raw.SectionTitle, err)
	}
	item, _ := env.db.GetItem(section.ID, "acme/widgets-"+itoa(number))
	return item
}

// list puts PR #number in user's section of raw's, claimed by user's
// workflow, as that user's cycle would.
func (env *hostedWebhookEnv) list(t *testing.T, user string, raw config.RawWorkflow, number int) {
	t.Helper()
	section, err := env.db.GetSection(user, raw.SectionTitle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.UpsertItemWithWorkflow(section.ID, "acme/widgets-"+itoa(number), "TODO", "Add widgets",
		[]string{"from " + user + "'s cycle"}, []string{whRepo}, 0, config.OwnershipKey(user, raw.Name)); err != nil {
		t.Fatal(err)
	}
}

// listed reports, for each dashboard, whether its workflow raw claims PR
// #number.
func (env *hostedWebhookEnv) listed(t *testing.T, raw config.RawWorkflow, number int) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, user := range hostedDashboards {
		item := env.row(t, user, raw, number)
		out[user] = item != nil && slices.Contains(item.GetWorkflows(), config.OwnershipKey(user, raw.Name))
	}
	return out
}

func (env *hostedWebhookEnv) checkListed(t *testing.T, what string, raw config.RawWorkflow, number int, want map[string]bool) {
	t.Helper()
	got := env.listed(t, raw, number)
	for _, user := range hostedDashboards {
		if got[user] != want[user] {
			name := user
			if name == "" {
				name = "the server"
			}
			t.Errorf("%s: %s's %q lists the PR = %v, want %v", what, name, raw.SectionTitle, got[user], want[user])
		}
	}
}

func requestedOf(pr *github.PullRequest, logins ...string) *github.PullRequest {
	pr.RequestedReviewers = nil
	for _, login := range logins {
		pr.RequestedReviewers = append(pr.RequestedReviewers, &github.User{Login: github.Ptr(login)})
	}
	return pr
}

func reviewSubmittedBy(login string, pr *github.PullRequest) webhookDelivery {
	return webhookDelivery{event: &github.PullRequestReviewEvent{
		Action:      github.Ptr("submitted"),
		Review:      &github.PullRequestReview{ID: github.Ptr(int64(9)), User: &github.User{Login: github.Ptr(login)}, State: github.Ptr("approved")},
		PullRequest: pr,
		Repo:        whRepository(),
	}}
}

// alice reviews with her own linked account, so GitHub names her as the
// reviewer and clears her request alone: the PR leaves her review queue
// between cycles, and stays in bob's, the server's and idle carol's.
func TestAUsersReviewTakesThePROutOfTheirQueueOnly(t *testing.T) {
	env := newHostedWebhookEnv(t)
	seedCachedPR(t, env.db, whKey(1), "head1")
	for _, user := range hostedDashboards {
		env.list(t, user, requestedRaw, 1)
	}

	applyWebhookEvent(reviewSubmittedBy("alice", requestedOf(whPR(1, "head1"), "alice", "bob", "me", "carol")))
	drainReevaluations()

	env.checkListed(t, "after alice's review", requestedRaw, 1, map[string]bool{"": true, "bob": true, "carol": true})
	if raw, _ := env.db.GetRequestedReviewers(whOwner, whRepo, 1); strings.Contains(raw, `"alice"`) || !strings.Contains(raw, `"bob"`) {
		t.Errorf("requested reviewers = %s, want bob's request and not alice's", raw)
	}
	// The rows still listed are rewritten from the caches — the active
	// dashboards' — and carol's is left as her last cycle wrote it.
	for _, user := range []string{"", "bob"} {
		if details, _ := env.row(t, user, requestedRaw, 1).GetDetails(); slices.Contains(details, "from "+user+"'s cycle") {
			t.Errorf("%q's row wasn't rewritten: %v", user, details)
		}
	}
	if details, _ := env.row(t, "carol", requestedRaw, 1).GetDetails(); !slices.Equal(details, []string{"from carol's cycle"}) {
		t.Errorf("idle carol's row was rewritten: %v", details)
	}
}

// A review requested of bob joins bob's queue only; the server's joins when
// the request is of its own login, and idle carol's waits for her next cycle.
func TestAReviewRequestJoinsTheRequestedUsersQueueOnly(t *testing.T) {
	env := newHostedWebhookEnv(t)
	seedCachedPR(t, env.db, whKey(1), "head1")
	deliver := func(logins ...string) {
		applyWebhookEvent(webhookDelivery{event: &github.PullRequestEvent{
			Action: github.Ptr("review_requested"), PullRequest: requestedOf(whPR(1, "head1"), logins...), Repo: whRepository(),
		}})
		drainReevaluations()
	}

	deliver("bob", "carol")
	env.checkListed(t, "requested of bob and carol", requestedRaw, 1, map[string]bool{"bob": true})
	deliver("bob", "carol", "me")
	env.checkListed(t, "requested of the server's login too", requestedRaw, 1, map[string]bool{"bob": true, "": true})
	// The PR's CI passes, so each active dashboard's Green section takes it
	// too: carol's, whose workflows the cycles skip, waits.
	env.checkListed(t, "a passing PR in acme/widgets", greenRaw, 1, map[string]bool{"": true, "alice": true, "bob": true})
}

// The token can list only its own teams, so a request of one of them is the
// server's own dashboard's business, as in its cycles' searches: no user's
// queue takes it.
func TestATeamReviewRequestJoinsOnlyTheServersQueue(t *testing.T) {
	env := newHostedWebhookEnv(t)
	t.Setenv("CRS_GITHUB_TOKEN", "test-token")
	git_tools.GlobalCache.Set("my_team_refs", []string{"acme/core"}, time.Minute)
	t.Cleanup(func() { git_tools.GlobalCache.Set("my_team_refs", nil, -1) })
	seedCachedPR(t, env.db, whKey(1), "head1")

	pr := whPR(1, "head1")
	pr.RequestedTeams = []*github.Team{{Slug: github.Ptr("core"), Name: github.Ptr("Core")}}
	applyWebhookEvent(webhookDelivery{event: &github.PullRequestEvent{Action: github.Ptr("review_requested"), PullRequest: pr, Repo: whRepository()}})
	drainReevaluations()

	env.checkListed(t, "requested of the token's team", requestedRaw, 1, map[string]bool{"": true})
}

// A CI result is the PR's, so it re-checks every active dashboard's
// CI-filtered section and rewrites each row it keeps; carol's is left to her
// next cycle.
func TestACIResultReChecksEveryActiveDashboard(t *testing.T) {
	env := newHostedWebhookEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	for _, user := range hostedDashboards {
		env.list(t, user, greenRaw, 1)
	}
	// The PR as the cycle's listing showed it; a status delivery carries none.
	rememberPR(key, whPR(1, "head1"))
	deliver := func(state string, at int64) {
		applyWebhookEvent(webhookDelivery{event: &github.StatusEvent{
			SHA: github.Ptr("head1"), Context: github.Ptr("ci/lint"), State: github.Ptr(state),
			UpdatedAt: &github.Timestamp{Time: time.Unix(at, 0)}, Repo: whRepository(),
		}})
		drainReevaluations()
	}

	deliver("success", 10)
	env.checkListed(t, "CI passed", greenRaw, 1, map[string]bool{"": true, "alice": true, "bob": true, "carol": true})
	for _, user := range []string{"", "alice", "bob"} {
		if details, _ := env.row(t, user, greenRaw, 1).GetDetails(); !strings.Contains(strings.Join(details, "\n"), "ci/lint") {
			t.Errorf("%q's row doesn't show the new CI line: %v", user, details)
		}
	}
	if details, _ := env.row(t, "carol", greenRaw, 1).GetDetails(); !slices.Equal(details, []string{"from carol's cycle"}) {
		t.Errorf("idle carol's row was rewritten: %v", details)
	}

	deliver("failure", 20)
	env.checkListed(t, "CI failed", greenRaw, 1, map[string]bool{"carol": true})
	if a := takeWebhookActivity(); a.SectionDrops != 3 {
		t.Errorf("activity = %+v, want a drop from each active dashboard", a)
	}
}

// A PR's status, title and tags are its own, the same on every dashboard: a
// delivery rewrites every row listing it, an idle user's included, without
// touching which sections list it.
func TestAPRDeliveryRewritesEveryDashboardsRowSummary(t *testing.T) {
	env := newHostedWebhookEnv(t)
	seedCachedPR(t, env.db, whKey(1), "head1")
	for _, user := range hostedDashboards {
		env.list(t, user, requestedRaw, 1)
	}

	renamed := requestedOf(whPR(1, "head1"), "me", "alice", "bob", "carol")
	renamed.Title = github.Ptr("Renamed widgets")
	renamed.Draft = github.Ptr(true)
	applyWebhookEvent(webhookDelivery{event: &github.PullRequestEvent{Action: github.Ptr("edited"), PullRequest: renamed, Repo: whRepository()}})
	drainReevaluations()

	for _, user := range hostedDashboards {
		item := env.row(t, user, requestedRaw, 1)
		if item == nil || !strings.Contains(item.Title, "Renamed widgets") || item.Status != "WAITING" || !strings.Contains(item.Tags, "draft") {
			t.Errorf("%q's row = %+v, want the new title, status and tags", user, item)
		}
	}
	env.checkListed(t, "after the rename", requestedRaw, 1, map[string]bool{"": true, "alice": true, "bob": true, "carol": true})
}

// A user deleted between cycles — by an administrator, or by the retention
// sweep that ends one — is still in the run set the last cycle began with.
// Deliveries no longer follow their workflows: nothing is added to the
// sections they leave behind, and once the cycle's prune has deleted those,
// nothing creates them again. Likewise a workflow its user removed.
func TestDeliveriesLeaveDeletedUsersAndRemovedWorkflowsAlone(t *testing.T) {
	env := newHostedWebhookEnv(t)
	seedCachedPR(t, env.db, whKey(1), "head1")
	requestAll := func() {
		applyWebhookEvent(webhookDelivery{event: &github.PullRequestEvent{
			Action: github.Ptr("review_requested"), PullRequest: requestedOf(whPR(1, "head1"), "me", "alice", "bob"), Repo: whRepository(),
		}})
		drainReevaluations()
	}

	if err := env.db.DeleteUser("alice"); err != nil {
		t.Fatal(err)
	}
	// bob keeps only his CI section.
	encoded := `{"WorkflowType":"SyncReviewRequestsWorkflow","Name":"green","SectionTitle":"Green","Repos":["acme/widgets"],"Filters":["FilterCIPassing"]}`
	if err := env.db.ReplaceUserWorkflows("bob", []database.UserWorkflow{{Position: 0, Name: "green", JSON: encoded}}); err != nil {
		t.Fatal(err)
	}

	requestAll()
	env.checkListed(t, "alice deleted, bob's queue removed", requestedRaw, 1, map[string]bool{"": true})

	// The cycle's end releases and prunes what nobody owns any more: alice's
	// sections and bob's old queue go.
	finishCycle()
	if _, err := env.db.GetSection("alice", requestedRaw.SectionTitle); err == nil {
		t.Fatal("alice's emptied section survived the prune")
	}
	requestAll()
	for _, user := range []string{"alice", "bob"} {
		if section, err := env.db.GetSection(user, requestedRaw.SectionTitle); err == nil {
			t.Errorf("a delivery re-created %q's %q section: %+v", user, requestedRaw.SectionTitle, section)
		}
	}
	if section, err := env.db.GetSection("bob", greenRaw.SectionTitle); err != nil || section == nil {
		t.Errorf("bob's remaining section went with the prune: %v", err)
	}
}
