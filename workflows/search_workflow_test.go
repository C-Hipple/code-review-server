package workflows

import (
	"crs/config"
	"crs/git_tools"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

// TestDefaultConfigWorkflowsAreBuildable is the guard on the promise that the
// server runs with no config file: the built-in defaults must survive both
// validation registries and actually build into workflows.
func TestDefaultConfigWorkflowsAreBuildable(t *testing.T) {
	cfg, err := config.DefaultConfig()
	if err != nil {
		t.Fatalf("config.DefaultConfig: %v", err)
	}

	if problems := ValidateWorkflows(cfg.RawWorkflows, cfg.Repos); len(problems) > 0 {
		t.Errorf("the default workflows must validate cleanly, got %v", problems)
	}

	built := MatchWorkflows(cfg.RawWorkflows, &cfg.Repos, cfg.JiraDomain)
	if len(built) != 2 {
		t.Fatalf("expected both default workflows to build, got %d", len(built))
	}

	modes := map[SearchMode]string{}
	for _, wf := range built {
		search, ok := wf.(SearchWorkflow)
		if !ok {
			t.Fatalf("workflow %q is a %T, want a SearchWorkflow", wf.GetName(), wf)
		}
		// Search-driven workflows do their own fetching, so the manager must
		// not try to collect PRs for them.
		if reqs := search.GetPRRequirements(); reqs != nil {
			t.Errorf("workflow %q should declare no PR requirements, got %v", search.Name, reqs)
		}
		modes[search.Mode] = search.SectionTitle
	}
	if modes[ModeWaitingOnMe] != "Waiting On Me" {
		t.Errorf("waiting-on-me section = %q", modes[ModeWaitingOnMe])
	}
	if modes[ModeReviewRequested] != "Review Requested" {
		t.Errorf("review-requested section = %q", modes[ModeReviewRequested])
	}
}

func TestBuildSearchWorkflow(t *testing.T) {
	raw := config.RawWorkflow{
		Name:           "waiting",
		SectionTitle:   "Waiting On Me",
		GithubUsername: "  octocat  ",
		Filters:        []string{"FilterNotDraft"},
		IncludeDiff:    true,
	}

	built, err := BuildSearchWorkflow(&raw, ModeWaitingOnMe)
	if err != nil {
		t.Fatalf("BuildSearchWorkflow: %v", err)
	}
	wf := built.(SearchWorkflow)

	if wf.Login != "octocat" {
		t.Errorf("Login = %q, want the trimmed configured login", wf.Login)
	}
	if wf.Mode != ModeWaitingOnMe {
		t.Errorf("Mode = %q", wf.Mode)
	}
	if len(wf.Filters) != 1 {
		t.Errorf("expected the configured filter to be built, got %d", len(wf.Filters))
	}
	// The section display needs this data for the PRs that survive filtering.
	want := AuxDataRequirement{Comments: true, Reviews: true, Commits: true, Diff: true}
	if wf.PostFilterAux != want {
		t.Errorf("PostFilterAux = %+v, want %+v", wf.PostFilterAux, want)
	}
}

// A workflow with no identity can't search for anything. It must say so rather
// than run: an empty result would read as "the section is empty now" and delete
// everything in it.
func TestSearchWorkflowRunRequiresALogin(t *testing.T) {
	wf := SearchWorkflow{Name: "waiting", SectionTitle: "Waiting On Me", Mode: ModeWaitingOnMe}

	var wg sync.WaitGroup
	_, err := wf.Run(nil, make(chan FileChanges, 1), &wg)
	if err == nil {
		t.Fatal("expected an error when no login is available")
	}
	if !strings.Contains(err.Error(), "GithubUsername") {
		t.Errorf("the error should point at how to fix it, got %q", err)
	}
}

func TestSearchWorkflowRunRejectsUnknownMode(t *testing.T) {
	wf := SearchWorkflow{Name: "odd", SectionTitle: "Odd", Mode: "nonsense", Login: "octocat"}

	var wg sync.WaitGroup
	_, err := wf.Run(nil, make(chan FileChanges, 1), &wg)
	if err == nil || !strings.Contains(err.Error(), "unknown search mode") {
		t.Errorf("expected an unknown-mode error, got %v", err)
	}
}

// The waiting-on-me mode owns its filter: it is applied first, ahead of
// whatever the config asked for.
func TestSearchWorkflowFiltersPrependTheModeFilter(t *testing.T) {
	waiting := SearchWorkflow{Mode: ModeWaitingOnMe}
	if got := len(waiting.filters("octocat")); got != 1 {
		t.Errorf("waiting-on-me should contribute its own filter, got %d filters", got)
	}

	requested := SearchWorkflow{Mode: ModeReviewRequested}
	if got := len(requested.filters("octocat")); got != 0 {
		t.Errorf("review-requested is already the search; it should add no filter, got %d", got)
	}
}

// A config file's search workflow is the server's own and follows the review
// requests made of the token's teams as well as of its login, whoever that
// login is; a hosted server's user follows only the requests made of them by
// name (see git_tools' TestReviewRequestedRefsSearchTheTokensTeamsForTheServerOnly
// for the searches each runs). The two are cached under different keys, which
// is what this tells apart without reaching GitHub.
func TestSearchWorkflowsSearchTheTokensTeamsForTheServerOnly(t *testing.T) {
	login := "octocat-" + strings.ToLower(t.Name())
	withTeams := []git_tools.PRRef{{Owner: "acme", Repo: "widgets", Number: 1}, {Owner: "acme", Repo: "widgets", Number: 2}}
	byName := []git_tools.PRRef{{Owner: "acme", Repo: "widgets", Number: 1}}
	git_tools.GlobalCache.Set("review_requested:"+login, withTeams, time.Minute)
	git_tools.GlobalCache.Set("review_requested_by_name:"+login, byName, time.Minute)
	t.Cleanup(func() {
		git_tools.GlobalCache.Set("review_requested:"+login, nil, -1)
		git_tools.GlobalCache.Set("review_requested_by_name:"+login, nil, -1)
	})

	server := SearchWorkflow{Name: "requested", Mode: ModeReviewRequested, Login: login}
	user := server
	user.User = login
	if !server.searchesTokenTeams() || user.searchesTokenTeams() {
		t.Fatalf("searchesTokenTeams: server %v, user %v", server.searchesTokenTeams(), user.searchesTokenTeams())
	}
	if refs, err := server.candidateRefs(login); err != nil || len(refs) != 2 {
		t.Errorf("the server's workflow found %v, %v; want the search with the token's teams", refs, err)
	}
	if refs, err := user.candidateRefs(login); err != nil || len(refs) != 1 {
		t.Errorf("a user's workflow found %v, %v; want the by-name search", refs, err)
	}

	// A delivery's re-check asks what the search asks: a request made of one
	// of the token's teams counts for the server's dashboard only.
	t.Setenv("CRS_GITHUB_TOKEN", "test-token")
	git_tools.GlobalCache.Set("my_team_refs", []string{"acme/core"}, time.Minute)
	t.Cleanup(func() { git_tools.GlobalCache.Set("my_team_refs", nil, -1) })
	pr := whPR(1, "head1")
	pr.RequestedTeams = []*github.Team{{Slug: github.Ptr("core")}}
	if got := server.webhookMembership(pr); got != memberYes {
		t.Errorf("the server's workflow judged a request of the token's team %v", got)
	}
	if got := user.webhookMembership(pr); got != memberNo {
		t.Errorf("a user's workflow judged a request of the token's team %v; it isn't theirs", got)
	}
}
