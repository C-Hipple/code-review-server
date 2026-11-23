package forge

import (
	"crs/config"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-github/v74/github"
)

// fakeForge is a Forge that only knows who it is. The embedded interface is
// nil, so any other method panics: these tests only resolve forges.
type fakeForge struct {
	Forge
	id, kind string
}

func (f *fakeForge) ID() string   { return f.id }
func (f *fakeForge) Kind() string { return f.kind }

// useRegistry gives a test an empty registry with a fake driver for each kind
// it names, and cfg as the config, restoring both afterwards. It returns how
// many times each kind's driver opened a forge.
func useRegistry(t *testing.T, cfg config.Config, kinds ...string) map[string]int {
	t.Helper()
	oldCfg := config.C()
	registry.Lock()
	saved := struct {
		drivers map[string]Driver
		open    map[string]opened
		repos   map[string]string
		warned  map[string]bool
	}{registry.drivers, registry.open, registry.repos, registry.warned}
	registry.drivers, registry.open, registry.repos, registry.warned = nil, nil, nil, nil
	registry.Unlock()
	t.Cleanup(func() {
		config.SetC(oldCfg)
		registry.Lock()
		registry.drivers, registry.open, registry.repos, registry.warned = saved.drivers, saved.open, saved.repos, saved.warned
		registry.Unlock()
	})

	config.SetC(cfg)
	opens := map[string]int{}
	for _, kind := range kinds {
		Register(kind, func(id string, s config.ForgeSettings) (Forge, error) {
			opens[s.Kind]++
			return &fakeForge{id: id, kind: s.Kind}, nil
		})
	}
	return opens
}

func TestForResolvesTheDefaultToGitHub(t *testing.T) {
	useRegistry(t, config.Config{}, config.ForgeKindGitHub)
	f, err := For("")
	if err != nil {
		t.Fatalf("For(\"\"): %v", err)
	}
	if f.ID() != config.ForgeGitHub || f.Kind() != config.ForgeKindGitHub {
		t.Errorf("the default forge should be the built-in GitHub one, got %s/%s", f.ID(), f.Kind())
	}
	if d := Default(); d != f {
		t.Errorf("Default() = %v, want the forge For(\"\") opened", d)
	}
}

func TestForOpensEachForgeOnceUntilItsSettingsChange(t *testing.T) {
	cfg := config.Config{Forges: map[string]config.ForgeSettings{"work": {Kind: "github"}}}
	opens := useRegistry(t, cfg, config.ForgeKindGitHub)
	first, err := For("work")
	if err != nil {
		t.Fatalf("For(work): %v", err)
	}
	if first.ID() != "work" || first.Kind() != config.ForgeKindGitHub {
		t.Errorf("For(work) = %s/%s", first.ID(), first.Kind())
	}
	if again, _ := For(" work "); again != first {
		t.Error("asking again should hand back the forge already opened")
	}
	if opens["github"] != 1 {
		t.Errorf("driver opened %d forges, want 1", opens["github"])
	}

	// A reload that changes the forge's settings opens it again.
	cfg.Forges = map[string]config.ForgeSettings{"work": {Kind: "other"}}
	config.SetC(cfg)
	Register("other", func(id string, s config.ForgeSettings) (Forge, error) {
		return &fakeForge{id: id, kind: s.Kind}, nil
	})
	reopened, err := For("work")
	if err != nil {
		t.Fatalf("For(work) after the reload: %v", err)
	}
	if reopened == first || reopened.Kind() != "other" {
		t.Errorf("changed settings should open the forge again, got %s", reopened.Kind())
	}
}

func TestForReportsWhatItCannotOpen(t *testing.T) {
	cfg := config.Config{Forges: map[string]config.ForgeSettings{"lab": {Kind: "gitlab"}}}
	useRegistry(t, cfg, config.ForgeKindGitHub)
	if _, err := For("nowhere"); err == nil || !strings.Contains(err.Error(), `no forge "nowhere"`) {
		t.Errorf("an undefined forge should fail to open, got %v", err)
	}
	if _, err := For("lab"); err == nil || !strings.Contains(err.Error(), "no driver") {
		t.Errorf("a kind without a driver should fail to open, got %v", err)
	}
}

func TestDefaultFallsBackToGitHub(t *testing.T) {
	useRegistry(t, config.Config{DefaultForge: "nowhere"}, config.ForgeKindGitHub)
	if f := Default(); f.ID() != config.ForgeGitHub {
		t.Errorf("a default that can't be opened should fall back to GitHub, got %s", f.ID())
	}
}

func TestForRepoUsesTheRememberedForge(t *testing.T) {
	cfg := config.Config{Forges: map[string]config.ForgeSettings{"work": {Kind: "github"}}}
	useRegistry(t, cfg, config.ForgeKindGitHub)

	if got := IDForRepo("acme", "widgets"); got != config.ForgeGitHub {
		t.Errorf("a repository nobody remembered should be on the default forge, got %q", got)
	}
	Remember("Acme", "Widgets", "work")
	if got := ForRepo("acme", "widgets").ID(); got != "work" {
		t.Errorf("ForRepo should be case-insensitive and use the remembered forge, got %q", got)
	}
	pr := &github.PullRequest{Base: &github.PullRequestBranch{Repo: &github.Repository{
		Name: github.Ptr("widgets"), Owner: &github.User{Login: github.Ptr("ACME")},
	}}}
	if got := ForPR(pr).ID(); got != "work" {
		t.Errorf("ForPR should resolve the PR's base repository, got %q", got)
	}
	if got := ForPR(&github.PullRequest{}).ID(); got != config.ForgeGitHub {
		t.Errorf("a PR without a base repository should get the default forge, got %q", got)
	}

	// Remembering "" is the default forge; a repository that moves takes the
	// last forge remembered for it.
	Remember("acme", "widgets", "")
	if got := IDForRepo("acme", "widgets"); got != config.ForgeGitHub {
		t.Errorf("remembering the default should record it, got %q", got)
	}

	// A remembered forge the config no longer defines falls back to GitHub.
	Remember("acme", "gadgets", "gone")
	if got := ForRepo("acme", "gadgets").ID(); got != config.ForgeGitHub {
		t.Errorf("a forge that can't be opened should fall back to GitHub, got %q", got)
	}
}

func TestRegisterTwicePanics(t *testing.T) {
	useRegistry(t, config.Config{}, config.ForgeKindGitHub)
	if got := Kinds(); !slices.Equal(got, []string{config.ForgeKindGitHub}) {
		t.Errorf("Kinds() = %v", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("registering a kind twice should panic")
		}
	}()
	Register(config.ForgeKindGitHub, func(string, config.ForgeSettings) (Forge, error) { return nil, nil })
}

func TestInteractionSet(t *testing.T) {
	pr := &github.PullRequest{Number: github.Ptr(7), Base: &github.PullRequestBranch{Repo: &github.Repository{
		Name: github.Ptr("Widgets"), Owner: &github.User{Login: github.Ptr("Acme")},
	}}}
	if !InteractionSet(nil).MayHaveInteracted(pr) {
		t.Error("a nil set means unknown, which must let every PR through")
	}
	set := InteractionSet{InteractionKey("acme", "widgets", 7): true}
	if !set.MayHaveInteracted(pr) {
		t.Error("membership should ignore case")
	}
	if (InteractionSet{}).MayHaveInteracted(pr) {
		t.Error("an empty set rules every PR out")
	}
}
