package workflows

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

// A server workflow and two users' workflows that collide on everything but
// their owner: the same name, the same section title, the same repo.
var sharedWorkflow = config.RawWorkflow{
	WorkflowType: "SyncReviewRequestsWorkflow",
	Name:         "review_requests",
	SectionTitle: "Needs Review",
	Repos:        []string{"acme/widgets"},
}

// usersTestDB opens a database and makes cfg, wired to it, the running config.
func usersTestDB(t *testing.T, cfg config.Config) *database.DB {
	t.Helper()
	db := warmTestDB(t)
	cfg.DB = db
	config.SetC(cfg)
	t.Cleanup(func() { config.SetC(config.Config{}) })
	return db
}

// registerUser stores a user last seen at lastSeen with workflows, the way a
// registration does: each workflow as the JSON of its RawWorkflow.
func registerUser(t *testing.T, db *database.DB, u database.User, workflows ...config.RawWorkflow) {
	t.Helper()
	if u.TokenHash == "" {
		u.TokenHash = "hash-of-" + u.Login
	}
	if err := db.CreateUser(u); err != nil {
		t.Fatalf("CreateUser(%s): %v", u.Login, err)
	}
	rows := make([]database.UserWorkflow, 0, len(workflows))
	for i, wf := range workflows {
		encoded, err := json.Marshal(wf)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		rows = append(rows, database.UserWorkflow{Position: i, Name: wf.Name, JSON: string(encoded)})
	}
	if err := db.ReplaceUserWorkflows(u.Login, rows); err != nil {
		t.Fatalf("ReplaceUserWorkflows(%s): %v", u.Login, err)
	}
}

// widgetsPRs builds open acme/widgets PRs and puts aux data for each in the
// store, so writing them into a section never reaches GitHub. Each reports
// itself mergeable, which keeps the mergeability lookup out of it too.
func widgetsPRs(t *testing.T, numbers ...int) []*github.PullRequest {
	t.Helper()
	store := NewAuxDataStore()
	prs := make([]*github.PullRequest, 0, len(numbers))
	for _, n := range numbers {
		pr := reprocessTestPR("acme", "widgets", n)
		pr.Mergeable = github.Ptr(true)
		prs = append(prs, pr)
		store.Set(PRKey{Owner: "acme", Repo: "widgets", Number: n}, &PRAuxData{
			Comments: []*github.PullRequestComment{},
			CIStatus: &git_tools.CIStatusInfo{},
			Reviews:  []*github.PullRequestReview{},
			HeadSHA:  "head-sha",
		})
	}
	SetCurrentAuxDataStore(store)
	t.Cleanup(func() { SetCurrentAuxDataStore(nil) })
	return prs
}

// workflowRun is one workflow and the PRs a cycle fetched for it.
type workflowRun struct {
	wf  Workflow
	prs []*github.PullRequest
}

// runCycle runs each workflow over its PRs through the channel, listener and
// apply path RunOnce uses, then the end-of-cycle housekeeping a cycle ends
// with.
func runCycle(t *testing.T, runs ...workflowRun) {
	t.Helper()
	changes := make(chan FileChanges)
	var wg sync.WaitGroup
	wg.Add(1)
	go ListenChanges(changes, &wg)
	for _, run := range runs {
		if _, err := run.wf.Run(run.prs, changes, &wg); err != nil {
			t.Fatalf("Run(%s for %q): %v", run.wf.GetName(), run.wf.GetUser(), err)
		}
	}
	close(changes)
	wg.Done()
	wg.Wait()
	finishCycle()
}

// byOwner indexes a run set by ownership key.
func byOwner(t *testing.T, wfs []Workflow) map[string]Workflow {
	t.Helper()
	out := map[string]Workflow{}
	for _, wf := range wfs {
		key := ownershipKey(wf)
		if _, dup := out[key]; dup {
			t.Fatalf("two workflows claim items under %q", key)
		}
		out[key] = wf
	}
	return out
}

// dashboard is user's "Needs Review" as identifier -> the keys claiming it,
// or nil when the section does not exist.
func dashboard(t *testing.T, db *database.DB, user string) map[string][]string {
	t.Helper()
	section, err := db.GetSection(user, sharedWorkflow.SectionTitle)
	if err != nil {
		return nil
	}
	items, err := db.GetItemsBySection(section.ID)
	if err != nil {
		t.Fatalf("GetItemsBySection: %v", err)
	}
	out := map[string][]string{}
	for _, item := range items {
		out[item.Identifier] = item.GetWorkflows()
	}
	return out
}

func checkDashboard(t *testing.T, db *database.DB, user string, want ...int) {
	t.Helper()
	got := dashboard(t, db, user)
	if got == nil {
		t.Fatalf("%q has no %q section", user, sharedWorkflow.SectionTitle)
	}
	key := config.OwnershipKey(user, sharedWorkflow.Name)
	if len(got) != len(want) {
		t.Errorf("%q's dashboard = %v, want PRs %v", user, got, want)
	}
	for _, n := range want {
		id := fmt.Sprintf("acme/widgets-%d", n)
		if owners := got[id]; len(owners) != 1 || owners[0] != key {
			t.Errorf("%q's %s claimed by %v, want [%q]", user, id, owners, key)
		}
	}
}

// Two users who name a workflow and its section the same — as each other and as
// the server — get a dashboard each, and full cycles of runs, releases and
// prunes never let one touch another's.
func TestUsersSharingAWorkflowNameKeepTheirOwnDashboards(t *testing.T) {
	db := usersTestDB(t, config.Config{RawWorkflows: []config.RawWorkflow{sharedWorkflow}})
	now := time.Now()
	registerUser(t, db, database.User{Login: "alice", LastSeen: now}, sharedWorkflow)
	registerUser(t, db, database.User{Login: "bob", LastSeen: now}, sharedWorkflow)

	run := byOwner(t, RunSet(config.C(), now))
	server := run[config.OwnershipKey("", sharedWorkflow.Name)]
	alice := run[config.OwnershipKey("alice", sharedWorkflow.Name)]
	bob := run[config.OwnershipKey("bob", sharedWorkflow.Name)]
	if len(run) != 3 || server == nil || alice == nil || bob == nil {
		t.Fatalf("run set = %v, want the server's, alice's and bob's", run)
	}

	both := widgetsPRs(t, 7, 8)
	runCycle(t, workflowRun{server, both}, workflowRun{alice, both}, workflowRun{bob, both})
	checkDashboard(t, db, "", 7, 8)
	checkDashboard(t, db, "alice", 7, 8)
	checkDashboard(t, db, "bob", 7, 8)

	// Alice's workflow stops matching #8. Her release of it — a Delete under
	// her key, deduplicated alongside bob's and the server's Updates for the
	// same PR — must leave theirs alone, and so must the prune that follows.
	runCycle(t, workflowRun{server, both}, workflowRun{alice, both[:1]}, workflowRun{bob, both})
	checkDashboard(t, db, "", 7, 8)
	checkDashboard(t, db, "alice", 7)
	checkDashboard(t, db, "bob", 7, 8)
}

func TestDeduplicateKeepsEachUsersChange(t *testing.T) {
	change := func(changeType, user string) SerializedFileChange {
		return SerializedFileChange{FileChange: &FileChanges{
			ChangeType: changeType, Identifier: "acme/widgets-7", SectionName: "Needs Review",
			WorkflowName: config.OwnershipKey(user, "review_requests"),
		}}
	}
	got := deduplicateChanges([]SerializedFileChange{
		change("Update", "alice"),
		change("Delete", "alice"),
		change("Delete", "bob"),
	})
	kinds := map[string]string{}
	for _, c := range got {
		kinds[c.FileChange.WorkflowName] = c.FileChange.ChangeType
	}
	want := map[string]string{
		config.OwnershipKey("alice", "review_requests"): "Update",
		config.OwnershipKey("bob", "review_requests"):   "Delete",
	}
	if len(kinds) != len(want) || kinds[config.OwnershipKey("alice", "review_requests")] != "Update" ||
		kinds[config.OwnershipKey("bob", "review_requests")] != "Delete" {
		t.Errorf("deduplicated to %v, want %v", kinds, want)
	}
}

// Deleting a user takes their workflows out of the owned set, and that alone
// clears their dashboard out on the next cycle: their items are released and
// pruned and their emptied section deleted, leaving everyone else's alone.
func TestDeletedUsersDashboardIsClearedOut(t *testing.T) {
	db := usersTestDB(t, config.Config{RawWorkflows: []config.RawWorkflow{sharedWorkflow}})
	now := time.Now()
	registerUser(t, db, database.User{Login: "alice", LastSeen: now}, sharedWorkflow)
	registerUser(t, db, database.User{Login: "bob", LastSeen: now}, sharedWorkflow)
	run := byOwner(t, RunSet(config.C(), now))
	prs := widgetsPRs(t, 7)
	runs := []workflowRun{}
	for _, wf := range run {
		runs = append(runs, workflowRun{wf, prs})
	}
	runCycle(t, runs...)
	checkDashboard(t, db, "alice", 7)

	if err := db.DeleteUser("alice"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	finishCycle()

	if got := dashboard(t, db, "alice"); got != nil {
		t.Errorf("alice's section survived her deletion, holding %v", got)
	}
	checkDashboard(t, db, "", 7)
	checkDashboard(t, db, "bob", 7)
}

// An idle user's workflows are not run, but they still own what they wrote:
// the user comes back to the dashboard they left, not an empty one.
func TestIdleUserIsSkippedButKeepsTheirDashboard(t *testing.T) {
	db := usersTestDB(t, config.Config{RawWorkflows: []config.RawWorkflow{sharedWorkflow}, UserActiveHours: 24})
	now := time.Now()
	registerUser(t, db, database.User{Login: "alice", LastSeen: now.Add(-48 * time.Hour)}, sharedWorkflow)
	registerUser(t, db, database.User{Login: "bob", LastSeen: now.Add(-time.Hour)}, sharedWorkflow)

	// Alice's dashboard as her last active cycle left it.
	prs := widgetsPRs(t, 7)
	aliceRaw := AsUserWorkflow("alice", sharedWorkflow)
	aliceWorkflows := buildDashboard([]config.RawWorkflow{aliceRaw}, nil, "", nil)
	runCycle(t, workflowRun{aliceWorkflows[0], prs})
	checkDashboard(t, db, "alice", 7)

	run := byOwner(t, RunSet(config.C(), now))
	if _, ok := run[config.OwnershipKey("alice", sharedWorkflow.Name)]; ok {
		t.Error("an idle user's workflow is in the run set")
	}
	bob, ok := run[config.OwnershipKey("bob", sharedWorkflow.Name)]
	if !ok || len(run) != 2 {
		t.Fatalf("run set = %v, want the server's and bob's", run)
	}

	runCycle(t, workflowRun{run[sharedWorkflow.Name], prs}, workflowRun{bob, prs})
	checkDashboard(t, db, "alice", 7)
	checkDashboard(t, db, "bob", 7)

	cfg := config.C()
	cfg.UserActiveHours = 0
	if _, ok := byOwner(t, RunSet(cfg, now))[config.OwnershipKey("alice", sharedWorkflow.Name)]; !ok {
		t.Error("UserActiveHours = 0 should run every user's workflows")
	}
}

// The retention sweep runs ahead of release and prune, so the user it deletes
// is gone from the owned set by the time they run: their drafts, feedback and
// whole dashboard go in one cycle. Registering again with the same workflows
// brings the dashboard back on the next.
func TestRetentionSweepClearsAUserOutInOneCycle(t *testing.T) {
	db := usersTestDB(t, config.Config{RawWorkflows: []config.RawWorkflow{sharedWorkflow}, UserRetentionDays: 7})
	now := time.Now()
	registerUser(t, db, database.User{Login: "alice", LastSeen: now.Add(-8 * 24 * time.Hour)}, sharedWorkflow)
	registerUser(t, db, database.User{Login: "bob", LastSeen: now.Add(-6 * 24 * time.Hour)}, sharedWorkflow)
	draft := "nit: rename this"
	if _, err := db.InsertLocalComment("alice", "acme", "widgets", 7, "main.go", 3, &draft, nil); err != nil {
		t.Fatalf("InsertLocalComment: %v", err)
	}
	if err := db.InsertFeedback("alice", "acme", "widgets", 7, &draft); err != nil {
		t.Fatalf("InsertFeedback: %v", err)
	}

	prs := widgetsPRs(t, 7)
	runs := []workflowRun{}
	for _, wf := range RunSet(config.C(), now) {
		runs = append(runs, workflowRun{wf, prs})
	}
	// The cycle that runs her workflows one last time also sweeps her.
	runCycle(t, runs...)

	if _, err := db.GetUser("alice"); !errors.Is(err, database.ErrNotFound) {
		t.Errorf("GetUser(alice) = %v, want ErrNotFound after the sweep", err)
	}
	if drafts, _ := db.GetLocalCommentsForPR("alice", "acme", "widgets", 7); len(drafts) != 0 {
		t.Errorf("alice's drafts survived the sweep: %v", drafts)
	}
	if feedback, _ := db.GetFeedback("alice", "acme", "widgets", 7); feedback != "" {
		t.Errorf("alice's feedback survived the sweep: %q", feedback)
	}
	if got := dashboard(t, db, "alice"); got != nil {
		t.Errorf("alice's section survived the sweep, holding %v", got)
	}
	checkDashboard(t, db, "bob", 7)
	checkDashboard(t, db, "", 7)

	registerUser(t, db, database.User{Login: "alice", LastSeen: time.Now()}, sharedWorkflow)
	run := byOwner(t, RunSet(config.C(), time.Now()))
	alice, ok := run[config.OwnershipKey("alice", sharedWorkflow.Name)]
	if !ok {
		t.Fatal("a re-registered user's workflow is missing from the run set")
	}
	runCycle(t, workflowRun{alice, prs}, workflowRun{run[config.OwnershipKey("bob", sharedWorkflow.Name)], prs},
		workflowRun{run[sharedWorkflow.Name], prs})
	checkDashboard(t, db, "alice", 7)
}

// The sweep hands the users it deleted, with the GitHub credentials they
// held, to the registered hook, so the server can revoke them.
func TestRetentionSweepHandsDeletedUsersToTheHook(t *testing.T) {
	db := usersTestDB(t, config.Config{UserRetentionDays: 7})
	now := time.Now()
	registerUser(t, db, database.User{Login: "alice", LastSeen: now.Add(-8 * 24 * time.Hour)})
	registerUser(t, db, database.User{Login: "bob", LastSeen: now.Add(-8 * 24 * time.Hour)})
	registerUser(t, db, database.User{Login: "carol", LastSeen: now})
	if err := db.SetUserGitHubCredential("alice", "hash-of-alice", "v1:sealed"); err != nil {
		t.Fatal(err)
	}

	var got []database.DeletedUser
	SetUsersDeletedHook(func(deleted []database.DeletedUser) { got = append(got, deleted...) })
	t.Cleanup(func() { SetUsersDeletedHook(nil) })

	sweepIdleUsers(config.C(), now)
	want := []database.DeletedUser{{Login: "alice", GitHubCredential: "v1:sealed"}, {Login: "bob"}}
	if !slices.Equal(got, want) {
		t.Errorf("the hook was handed %+v, want %+v", got, want)
	}

	// A sweep that deletes nobody does not call it.
	got = nil
	sweepIdleUsers(config.C(), now)
	if got != nil {
		t.Errorf("a sweep that deleted nobody handed the hook %+v", got)
	}
}

func TestUserWorkflowsAreBuiltForTheirUser(t *testing.T) {
	db := usersTestDB(t, config.Config{
		Repos:                []string{"server/repo"},
		GithubUsername:       "server-owner",
		DesktopNotifications: true,
		SectionPriority:      map[string]int{"Needs Review": 9},
		RawWorkflows:         []config.RawWorkflow{sharedWorkflow},
	})
	now := time.Now()
	notify := true
	fallsBack := config.RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: "mine", SectionTitle: "Mine",
		Filters: []string{"FilterMyPRs"}, DesktopNotifications: &notify}
	search := config.RawWorkflow{WorkflowType: "WaitingOnMeWorkflow", Name: "waiting", SectionTitle: "Waiting"}
	someoneElse := config.RawWorkflow{WorkflowType: "WaitingOnMeWorkflow", Name: "theirs", SectionTitle: "Theirs",
		GithubUsername: "carol"}
	registerUser(t, db, database.User{Login: "alice", LastSeen: now, Repos: []string{"alice/repo"},
		SectionPriority: map[string]int{"Needs Review": 3}}, sharedWorkflow, fallsBack, search, someoneElse)
	registerUser(t, db, database.User{Login: "bob", LastSeen: now}, fallsBack)

	run := byOwner(t, RunSet(config.C(), now))
	if len(run) != 6 {
		t.Fatalf("run set has %d workflows, want 6: %v", len(run), run)
	}

	// An identity filter builds without a GithubUsername of its own: the
	// login stands in for it, not the server's root-level one.
	aliceMine := run[config.OwnershipKey("alice", "mine")].(SyncReviewRequestsWorkflow)
	if got := aliceMine.Repos; !slices.Equal(got, []string{"alice/repo"}) {
		t.Errorf("alice's fallback repos = %v, want her own", got)
	}
	bobMine := run[config.OwnershipKey("bob", "mine")].(SyncReviewRequestsWorkflow)
	if got := bobMine.Repos; !slices.Equal(got, []string{"server/repo"}) {
		t.Errorf("bob's fallback repos = %v, want the server's", got)
	}
	if got := run[config.OwnershipKey("alice", "waiting")].(SearchWorkflow).Login; got != "alice" {
		t.Errorf("alice's search workflow is about %q, want alice", got)
	}
	if got := run[config.OwnershipKey("alice", "theirs")].(SearchWorkflow).Login; got != "carol" {
		t.Errorf("a workflow naming its own GithubUsername is about %q, want carol", got)
	}

	// A user's workflow never notifies — not by inheriting the server's
	// setting, and not by asking to.
	if aliceMine.ShouldNotify() || bobMine.ShouldNotify() {
		t.Error("a user's workflow would send a desktop notification on the server's machine")
	}
	if waiting := run[config.OwnershipKey("alice", "waiting")].(SearchWorkflow); waiting.ShouldNotify() {
		t.Error("a user's search workflow inherited the server's DesktopNotifications")
	}
	if !run[sharedWorkflow.Name].(SyncReviewRequestsWorkflow).ShouldNotify() {
		t.Error("the server's own workflow should still inherit DesktopNotifications")
	}

	// Each section lands on its owner's dashboard at its owner's priority.
	ms := NewManagerService(RunSet(config.C(), now), true, time.Minute)
	ms.Initialize()
	for user, want := range map[string]int{"": 9, "alice": 3} {
		section, err := db.GetSection(user, "Needs Review")
		if err != nil {
			t.Fatalf("GetSection(%q): %v", user, err)
		}
		if section.Priority != want {
			t.Errorf("%q's Needs Review sorts at %d, want %d", user, section.Priority, want)
		}
	}
}

// A stored workflow that will not decode is not run. It keeps its items when
// the decode got far enough to say where it writes, as a config workflow that
// fails to build does; when it did not, nothing is released or pruned at all
// rather than taking the user's dashboard down with it.
func TestUndecodableUserWorkflow(t *testing.T) {
	db := usersTestDB(t, config.Config{RawWorkflows: []config.RawWorkflow{sharedWorkflow}})
	now := time.Now()
	registerUser(t, db, database.User{Login: "alice", LastSeen: now})
	// Repos is a list; a string fails the decode but leaves SectionTitle read.
	if err := db.ReplaceUserWorkflows("alice", []database.UserWorkflow{{
		Name: sharedWorkflow.Name,
		JSON: `{"WorkflowType":"SyncReviewRequestsWorkflow","Name":"review_requests","SectionTitle":"Needs Review","Repos":"acme/widgets"}`,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := byOwner(t, RunSet(config.C(), now))[config.OwnershipKey("alice", sharedWorkflow.Name)]; ok {
		t.Error("a workflow that failed to decode is in the run set")
	}
	owned, ok := ownedSections(config.C())
	if !ok {
		t.Fatal("a partly decoded workflow should still say what it owns")
	}
	if got := owned[config.OwnershipKey("alice", sharedWorkflow.Name)]; got != (database.SectionRef{User: "alice", Name: "Needs Review"}) {
		t.Errorf("owned section = %+v, want alice's Needs Review", got)
	}

	if err := db.ReplaceUserWorkflows("alice", []database.UserWorkflow{{Name: "broken", JSON: `{"Name":`}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ownedSections(config.C()); ok {
		t.Error("a workflow whose section cannot be read should stop release and prune")
	}
}

// Submitting a review reprocesses the PR against the run set, so every active
// dashboard that lists it — the submitter's included — moves together, each
// under its own key.
func TestReprocessUpdatesEveryActiveDashboard(t *testing.T) {
	db := usersTestDB(t, config.Config{RawWorkflows: []config.RawWorkflow{sharedWorkflow}})
	now := time.Now()
	registerUser(t, db, database.User{Login: "alice", LastSeen: now}, sharedWorkflow)
	registerUser(t, db, database.User{Login: "bob", LastSeen: now}, sharedWorkflow)
	pr := widgetsPRs(t, 7)[0]

	targets := WorkflowsTargetingRepo(RunSet(config.C(), now), "acme", "widgets")
	if len(targets) != 3 {
		t.Fatalf("reprocess targets %d workflows, want the server's, alice's and bob's", len(targets))
	}
	applyReprocessedPR(db, targets, pr)

	checkDashboard(t, db, "", 7)
	checkDashboard(t, db, "alice", 7)
	checkDashboard(t, db, "bob", 7)
}

// Without the users the owned set is unknown, and nothing may be released.
func TestOwnedSectionsUnknownWithoutUsers(t *testing.T) {
	db := usersTestDB(t, config.Config{RawWorkflows: []config.RawWorkflow{sharedWorkflow}})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := ownedSections(config.C()); ok {
		t.Error("ownedSections claimed to know the owned set without being able to load the users")
	}
	// The run set falls back to the config's own workflows.
	if run := RunSet(config.C(), time.Now()); len(run) != 1 || run[0].GetUser() != "" {
		t.Errorf("run set without users = %v, want the config's workflow alone", run)
	}
}
