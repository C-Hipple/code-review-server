package workflows

import (
	"crs/config"
	"strings"
	"sync"
	"testing"
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
