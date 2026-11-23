package workflows

import (
	"crs/config"
	"crs/forge"
	"crs/git_tools"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeForgeKind is a kind of forge only these tests have a driver for, so a
// config can put workflows on forges that never reach GitHub.
const fakeForgeKind = "fake-forge"

// fakeForge answers the few questions these tests ask, and records them. Its
// embedded Forge is nil: anything else it is asked panics.
type fakeForge struct {
	forge.Forge
	id string

	mu               sync.Mutex
	mergeabilityAsks [][]git_tools.PRRef
}

func (f *fakeForge) ID() string   { return f.id }
func (f *fakeForge) Kind() string { return fakeForgeKind }

func (f *fakeForge) Mergeability(refs []git_tools.PRRef) (map[git_tools.PRRef]git_tools.Mergeability, error) {
	f.mu.Lock()
	f.mergeabilityAsks = append(f.mergeabilityAsks, refs)
	f.mu.Unlock()
	answers := map[git_tools.PRRef]git_tools.Mergeability{}
	for _, ref := range refs {
		// The head SHA says which forge answered.
		answers[ref] = git_tools.Mergeability{State: git_tools.MergeabilityMergeable, HeadSHA: f.id}
	}
	return answers, nil
}

func (f *fakeForge) ReviewRequestedRefs(login string) ([]git_tools.PRRef, error) {
	return []git_tools.PRRef{{Owner: "forgetest", Repo: "requested", Number: 1}, {Owner: "forgetest", Repo: "both", Number: 2}}, nil
}

func (f *fakeForge) MyInteractionRefs(login string) ([]git_tools.PRRef, error) {
	return []git_tools.PRRef{{Owner: "forgetest", Repo: "both", Number: 2}, {Owner: "forgetest", Repo: "interacted", Number: 3}}, nil
}

var fakeForges struct {
	sync.Mutex
	byID map[string]*fakeForge
}

func init() {
	forge.Register(fakeForgeKind, func(id string, _ config.ForgeSettings) (forge.Forge, error) {
		fakeForges.Lock()
		defer fakeForges.Unlock()
		if fakeForges.byID == nil {
			fakeForges.byID = map[string]*fakeForge{}
		}
		f := &fakeForge{id: id}
		fakeForges.byID[id] = f
		return f, nil
	})
}

// useForges sets a config defining the fake forges ids (and whatever else cfg
// holds) for the test.
func useForges(t *testing.T, cfg config.Config, ids ...string) {
	t.Helper()
	old := config.C()
	if cfg.Forges == nil {
		cfg.Forges = map[string]config.ForgeSettings{}
	}
	for _, id := range ids {
		cfg.Forges[id] = config.ForgeSettings{Kind: fakeForgeKind}
	}
	config.SetC(cfg)
	t.Cleanup(func() { config.SetC(old) })
}

// forgetRepos puts repos back on the built-in forge once the test is done:
// what every other test's config takes them to be on.
func forgetRepos(t *testing.T, repos ...string) {
	t.Cleanup(func() {
		for _, entry := range repos {
			owner, repo, _ := strings.Cut(entry, "/")
			forge.Remember(owner, repo, config.ForgeGitHub)
		}
	})
}

func TestMatchWorkflowsPutsEachWorkflowOnItsForge(t *testing.T) {
	useForges(t, config.Config{}, "lab")
	forgetRepos(t, "forgetest/web", "forgetest/api")
	raws := []config.RawWorkflow{
		{WorkflowType: "SyncReviewRequestsWorkflow", Name: "Web", SectionTitle: "Web", Repos: []string{"forgetest/web"}},
		{WorkflowType: "SyncReviewRequestsWorkflow", Name: "API", SectionTitle: "API", Repos: []string{"forgetest/api"}, Forge: "lab"},
		{WorkflowType: "MyReviewRequestsWorkflow", Name: "Lab reviews", SectionTitle: "Lab reviews", GithubUsername: "me", Forge: "lab"},
		{WorkflowType: "SyncReviewRequestsWorkflow", Name: "Lost", SectionTitle: "Lost", Repos: []string{"forgetest/lost"}, Forge: "nowhere"},
	}
	built := MatchWorkflows(raws, &[]string{}, "")

	got := map[string]string{}
	for _, wf := range built {
		got[wf.GetName()] = workflowForgeID(wf)
	}
	want := map[string]string{"Web": config.ForgeGitHub, "API": "lab", "Lab reviews": "lab"}
	if len(got) != len(want) {
		t.Fatalf("built %v, want %v (a workflow whose forge can't be opened is skipped)", got, want)
	}
	for name, id := range want {
		if got[name] != id {
			t.Errorf("workflow %q is on forge %q, want %q", name, got[name], id)
		}
	}

	// The listed repositories are remembered on their workflow's forge, which
	// is where the server will send what a client asks about their PRs.
	if id := forge.IDForRepo("forgetest", "api"); id != "lab" {
		t.Errorf("forgetest/api is remembered on %q, want lab", id)
	}
	if id := forge.IDForRepo("forgetest", "web"); id != config.ForgeGitHub {
		t.Errorf("forgetest/web is remembered on %q, want the default", id)
	}

	// The requirements carry the forge into the collection phase.
	for _, wf := range built {
		for _, req := range wf.GetPRRequirements() {
			if src := req.source(); src.Forge != workflowForgeID(wf) {
				t.Errorf("%s lists %s from forge %q, want its own %q", wf.GetName(), src.Repo, src.Forge, workflowForgeID(wf))
			}
		}
	}
}

func TestValidateRepoForges(t *testing.T) {
	sync := func(name, forgeID string, repos ...string) config.RawWorkflow {
		return config.RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: name, SectionTitle: name, Repos: repos, Forge: forgeID}
	}
	tests := []struct {
		name string
		cfg  config.Config
		want string // "" for no problem
	}{
		{
			name: "the default named and left out is one forge",
			cfg:  config.Config{RawWorkflows: []config.RawWorkflow{sync("a", "", "acme/web"), sync("b", "github", "Acme/Web")}},
		},
		{
			name: "one repository on two forges",
			cfg: config.Config{RawWorkflows: []config.RawWorkflow{
				sync("a", "", "acme/web"), sync("b", "lab", "acme/web"),
			}},
			want: `lists acme/web on forge "lab", but workflow 0 lists it on "github"`,
		},
		{
			name: "the root Repos inherited onto two forges",
			cfg: config.Config{Repos: []string{"acme/web"}, RawWorkflows: []config.RawWorkflow{
				sync("a", ""), sync("b", "lab"),
			}},
			want: "a repository's PRs can only come from one forge",
		},
		{
			name: "searches list no repositories",
			cfg: config.Config{Repos: []string{"acme/web"}, RawWorkflows: []config.RawWorkflow{
				sync("a", ""),
				{WorkflowType: "WaitingOnMeWorkflow", Name: "w", SectionTitle: "w", Forge: "lab"},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := ValidateRepoForges(&tt.cfg)
			if tt.want == "" {
				if len(problems) != 0 {
					t.Errorf("unexpected problems: %v", problems)
				}
				return
			}
			if len(problems) != 1 || problems[0].Field != "Forge" || problems[0].Workflow != 1 ||
				!strings.Contains(problems[0].Message, tt.want) {
				t.Errorf("want one problem on Workflows[1].Forge containing %q, got %v", tt.want, problems)
			}
		})
	}
}

func TestWorkflowsTargetingRepoKeepsToTheRepositorysForge(t *testing.T) {
	useForges(t, config.Config{}, "lab")
	forgetRepos(t, "forgetest/api")
	forge.Remember("forgetest", "api", "lab")

	onLab := SyncReviewRequestsWorkflow{Name: "lab", Forge: "lab", Repos: []string{"forgetest/api"}}
	onGitHub := SyncReviewRequestsWorkflow{Name: "github", Repos: []string{"forgetest/api"}}
	targets := WorkflowsTargetingRepo([]Workflow{onLab, onGitHub}, "forgetest", "api")
	if len(targets) != 1 || targets[0].GetName() != "lab" {
		t.Errorf("only the workflow on the repository's forge should be reprocessed, got %v", targets)
	}
}

func TestMergeabilityAsksEachForgeAboutItsOwn(t *testing.T) {
	useForges(t, config.Config{DefaultForge: "lab"}, "lab", "other")
	forgetRepos(t, "forgetest/elsewhere")
	forge.Remember("forgetest", "elsewhere", "other")

	refs := []git_tools.PRRef{
		{Owner: "forgetest", Repo: "here", Number: 1},
		{Owner: "forgetest", Repo: "elsewhere", Number: 2},
		{Owner: "forgetest", Repo: "here", Number: 3},
	}
	answers, err := mergeabilityOnForges(refs)
	if err != nil {
		t.Fatalf("mergeabilityOnForges: %v", err)
	}
	for _, ref := range refs {
		want := "lab"
		if ref.Repo == "elsewhere" {
			want = "other"
		}
		if got := answers[ref]; got.State != git_tools.MergeabilityMergeable || got.HeadSHA != want {
			t.Errorf("%s answered %+v, want an answer from %s", ref, got, want)
		}
	}

	fakeForges.Lock()
	lab, other := fakeForges.byID["lab"], fakeForges.byID["other"]
	fakeForges.Unlock()
	if len(lab.mergeabilityAsks) != 1 || !slices.Equal(lab.mergeabilityAsks[0], []git_tools.PRRef{refs[0], refs[2]}) {
		t.Errorf("lab should be asked once about its own PRs, in order; was asked %v", lab.mergeabilityAsks)
	}
	if len(other.mergeabilityAsks) != 1 || !slices.Equal(other.mergeabilityAsks[0], []git_tools.PRRef{refs[1]}) {
		t.Errorf("other should be asked once about its PR; was asked %v", other.mergeabilityAsks)
	}
}

func TestSearchCandidatesComeFromTheWorkflowsForge(t *testing.T) {
	useForges(t, config.Config{}, "lab")
	f, err := forge.For("lab")
	if err != nil {
		t.Fatalf("For(lab): %v", err)
	}
	refs, err := SearchWorkflow{Mode: ModeWaitingOnMe}.candidateRefs(f, "me")
	if err != nil {
		t.Fatalf("candidateRefs: %v", err)
	}
	if len(refs) != 3 {
		t.Errorf("want the union of the forge's two searches without repeats, got %v", refs)
	}
}
