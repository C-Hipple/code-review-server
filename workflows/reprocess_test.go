package workflows

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"testing"

	"github.com/google/go-github/v74/github"
)

// reprocessTestPR builds an open, non-draft PR with every field Details()
// dereferences, so section writes during a reprocess don't panic.
func reprocessTestPR(owner, repo string, number int) *github.PullRequest {
	return &github.PullRequest{
		Number: github.Int(number),
		State:  github.String("open"),
		Draft:  github.Bool(false),
		Title:  github.String("Add a thing"),
		Body:   github.String("body"),
		User:   &github.User{Login: github.String("author")},
		Base: &github.PullRequestBranch{
			Repo: &github.Repository{
				FullName: github.String(owner + "/" + repo),
				Name:     github.String(repo),
				Owner:    &github.User{Login: github.String(owner)},
			},
			SHA: github.String("base-sha"),
		},
		Head: &github.PullRequestBranch{
			Label: github.String("author:feature"),
			SHA:   github.String("head-sha"),
			Repo:  &github.Repository{Name: github.String(repo)},
		},
	}
}

// seedReprocessAuxData puts empty-but-present aux data in the global store so
// Details() takes its cached branches instead of calling GitHub.
func seedReprocessAuxData(t *testing.T, owner, repo string, number int) {
	t.Helper()
	store := NewAuxDataStore()
	store.Set(PRKey{Owner: owner, Repo: repo, Number: number}, &PRAuxData{
		Comments: []*github.PullRequestComment{},
		CIStatus: &git_tools.CIStatusInfo{},
		Reviews:  []*github.PullRequestReview{},
		HeadSHA:  "head-sha",
	})
	SetCurrentAuxDataStore(store)
	t.Cleanup(func() { SetCurrentAuxDataStore(nil) })
}

func matchAllFilter(prs []*github.PullRequest) []*github.PullRequest {
	return prs
}

func matchNoneFilter([]*github.PullRequest) []*github.PullRequest {
	return []*github.PullRequest{}
}

func seedItem(t *testing.T, db *database.DB, sectionName, identifier, workflowName string) *database.Section {
	t.Helper()
	section, err := db.GetOrCreateSection(sectionName, 0)
	if err != nil {
		t.Fatalf("GetOrCreateSection(%q): %v", sectionName, err)
	}
	if _, err := db.UpsertItemWithWorkflow(section.ID, identifier, "TODO", "Add a thing",
		[]string{"detail"}, []string{"tag"}, 0, workflowName); err != nil {
		t.Fatalf("UpsertItemWithWorkflow: %v", err)
	}
	return section
}

func itemInSection(t *testing.T, db *database.DB, sectionName, identifier string) *database.Item {
	t.Helper()
	section, err := db.GetSection(sectionName)
	if err != nil {
		return nil
	}
	item, err := db.GetItem(section.ID, identifier)
	if err != nil {
		return nil
	}
	return item
}

// A review makes the "needs review" filters stop matching and can make another
// section's filters start matching. Both have to land in the same pass: the PR
// leaves the section it no longer belongs to, and joins the one it now does —
// even though no workflow held it there before.
func TestApplyReprocessedPRMovesPRBetweenSections(t *testing.T) {
	const (
		owner      = "C-hipple"
		repo       = "code-review-server"
		number     = 211
		identifier = owner + "/" + repo + "-211"
	)
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	seedReprocessAuxData(t, owner, repo, number)

	needsReview := SyncReviewRequestsWorkflow{
		Name:         "needs_review",
		Repos:        []string{owner + "/" + repo},
		Filters:      []git_tools.PRFilter{matchNoneFilter},
		SectionTitle: "Needs Review",
	}
	waitingOnAuthor := SyncReviewRequestsWorkflow{
		Name:         "waiting_on_author",
		Repos:        []string{owner + "/" + repo},
		Filters:      []git_tools.PRFilter{matchAllFilter},
		SectionTitle: "Waiting on Author",
	}

	// Before the review: the PR sits in Needs Review only.
	seedItem(t, db, needsReview.SectionTitle, identifier, needsReview.Name)

	applyReprocessedPR(db, []Workflow{needsReview, waitingOnAuthor}, reprocessTestPR(owner, repo, number))

	if item := itemInSection(t, db, needsReview.SectionTitle, identifier); item != nil {
		t.Errorf("PR still in %q after its filters stopped matching (workflows: %v)",
			needsReview.SectionTitle, item.GetWorkflows())
	}
	item := itemInSection(t, db, waitingOnAuthor.SectionTitle, identifier)
	if item == nil {
		t.Fatalf("PR was not added to %q by the workflow whose filters now match", waitingOnAuthor.SectionTitle)
	}
	if got := item.GetWorkflows(); len(got) != 1 || got[0] != waitingOnAuthor.Name {
		t.Errorf("item workflows = %v, want [%s]", got, waitingOnAuthor.Name)
	}
}

// Dropping out of one workflow must not evict the row while another workflow
// still claims it — ownership is per workflow, and the prune only removes rows
// with no owners left.
func TestApplyReprocessedPRKeepsItemClaimedByAnotherWorkflow(t *testing.T) {
	const (
		owner      = "C-hipple"
		repo       = "code-review-server"
		number     = 211
		identifier = owner + "/" + repo + "-211"
		section    = "Shared Section"
	)
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	seedReprocessAuxData(t, owner, repo, number)

	dropping := SyncReviewRequestsWorkflow{
		Name:         "dropping",
		Repos:        []string{owner + "/" + repo},
		Filters:      []git_tools.PRFilter{matchNoneFilter},
		SectionTitle: section,
	}
	seedItem(t, db, section, identifier, dropping.Name)
	seedItem(t, db, section, identifier, "other_workflow")

	applyReprocessedPR(db, []Workflow{dropping}, reprocessTestPR(owner, repo, number))

	item := itemInSection(t, db, section, identifier)
	if item == nil {
		t.Fatal("item was pruned even though another workflow still claims it")
	}
	if got := item.GetWorkflows(); len(got) != 1 || got[0] != "other_workflow" {
		t.Errorf("item workflows = %v, want [other_workflow]", got)
	}
}

// A workflow whose PRs come from Jira can narrow its section on a reprocess but
// must not adopt a PR the epic never listed.
func TestApplyReprocessedPRLeavesProjectListAdditionsToTheCycle(t *testing.T) {
	const (
		owner      = "C-hipple"
		repo       = "code-review-server"
		number     = 211
		identifier = owner + "/" + repo + "-211"
	)
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	seedReprocessAuxData(t, owner, repo, number)

	project := ProjectListWorkflow{
		Name:         "project",
		Repos:        []string{owner + "/" + repo},
		Filters:      []git_tools.PRFilter{matchAllFilter},
		SectionTitle: "Project",
		JiraEpic:     "ABC-1",
	}

	applyReprocessedPR(db, []Workflow{project}, reprocessTestPR(owner, repo, number))

	if item := itemInSection(t, db, project.SectionTitle, identifier); item != nil {
		t.Fatal("project list section adopted a PR that the Jira epic never listed")
	}

	// Once the epic has put it there, a reprocess keeps it while the filters
	// still match and drops it when they stop.
	seedItem(t, db, project.SectionTitle, identifier, project.Name)
	applyReprocessedPR(db, []Workflow{project}, reprocessTestPR(owner, repo, number))
	if itemInSection(t, db, project.SectionTitle, identifier) == nil {
		t.Fatal("project list dropped a PR that still passes its filters")
	}

	project.Filters = []git_tools.PRFilter{matchNoneFilter}
	applyReprocessedPR(db, []Workflow{project}, reprocessTestPR(owner, repo, number))
	if itemInSection(t, db, project.SectionTitle, identifier) != nil {
		t.Fatal("project list kept a PR that no longer passes its filters")
	}
}

func TestWorkflowsTargetingRepo(t *testing.T) {
	sync := SyncReviewRequestsWorkflow{
		Name:  "sync",
		Repos: []string{"C-hipple/code-review-server", "multimediallc/chaturbate"},
	}
	// The deprecated single-repo shape stores a bare repo name plus a separate
	// owner; it targets the same repo as the "owner/repo" form.
	legacy := SyncReviewRequestsWorkflow{
		Name:  "legacy",
		Owner: "C-hipple",
		Repos: []string{"code-review-server"},
	}
	project := ProjectListWorkflow{
		Name:  "project",
		Repos: []string{"multimediallc/chaturbate"},
	}
	all := []Workflow{sync, legacy, project}

	cases := []struct {
		owner, repo string
		want        []string
	}{
		// Repo names are compared case-insensitively: the owner casing a user
		// types into the client rarely matches the config exactly.
		{"C-hipple", "code-review-server", []string{"sync", "legacy"}},
		{"c-hipple", "Code-Review-Server", []string{"sync", "legacy"}},
		{"multimediallc", "chaturbate", []string{"sync", "project"}},
		{"C-hipple", "gtdbot", nil},
	}

	for _, c := range cases {
		got := []string{}
		for _, wf := range WorkflowsTargetingRepo(all, c.owner, c.repo) {
			got = append(got, wf.GetName())
		}
		if len(got) != len(c.want) {
			t.Errorf("WorkflowsTargetingRepo(%s/%s) = %v, want %v", c.owner, c.repo, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("WorkflowsTargetingRepo(%s/%s) = %v, want %v", c.owner, c.repo, got, c.want)
				break
			}
		}
	}
}

func TestSyncWorkflowMatchesPRState(t *testing.T) {
	pr := reprocessTestPR("C-hipple", "code-review-server", 211)
	closed := reprocessTestPR("C-hipple", "code-review-server", 212)
	closed.State = github.String("closed")

	open := SyncReviewRequestsWorkflow{Filters: []git_tools.PRFilter{matchAllFilter}}
	if !open.MatchesPR(pr, false) {
		t.Error("open workflow should match an open PR that passes its filters")
	}
	if open.MatchesPR(closed, true) {
		t.Error("open workflow should not match a closed PR, even one it already owns")
	}

	all := SyncReviewRequestsWorkflow{PRState: "all", Filters: []git_tools.PRFilter{matchAllFilter}}
	if !all.MatchesPR(closed, false) {
		t.Error(`PRState "all" should match a closed PR`)
	}

	filtered := SyncReviewRequestsWorkflow{Filters: []git_tools.PRFilter{matchNoneFilter}}
	if filtered.MatchesPR(pr, true) {
		t.Error("a PR its filters reject should not match, even one the workflow already owns")
	}
}

func TestReprocessAuxRequirementsUnionsWorkflowNeeds(t *testing.T) {
	const owner, repo = "C-hipple", "code-review-server"
	ci := SyncReviewRequestsWorkflow{
		Name:       "ci",
		Repos:      []string{owner + "/" + repo},
		AuxDataReq: AuxDataRequirement{CIStatus: true},
	}
	interaction := SyncReviewRequestsWorkflow{
		Name:          "interaction",
		Repos:         []string{owner + "/" + repo},
		PostFilterAux: AuxDataRequirement{Commits: true},
		IncludeDiff:   true,
	}
	// A workflow on some other repo contributes nothing.
	elsewhere := SyncReviewRequestsWorkflow{
		Name:       "elsewhere",
		Repos:      []string{"multimediallc/chaturbate"},
		AuxDataReq: AuxDataRequirement{Comments: true},
	}

	got := reprocessAuxRequirements([]Workflow{ci, interaction, elsewhere}, owner, repo)
	want := AuxDataRequirement{Comments: true, Reviews: true, Teams: true, CIStatus: true, Diff: true, Commits: true}
	if got != want {
		t.Errorf("reprocessAuxRequirements = %+v, want %+v", got, want)
	}
}
