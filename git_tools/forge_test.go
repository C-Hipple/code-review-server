package git_tools

import (
	"crs/config"
	"crs/forge"
	"slices"
	"testing"
)

// Validation accepts the kinds config.ForgeKinds lists; each must have a
// driver, or a config that validates would still leave its workflows unbuilt.
func TestEveryForgeKindHasADriver(t *testing.T) {
	registered := forge.Kinds()
	for _, kind := range config.ForgeKinds {
		if !slices.Contains(registered, kind) {
			t.Errorf("config.ForgeKinds lists %q, but no driver is registered for it", kind)
		}
	}
}

func TestGitHubDriverOpensEveryGitHubForge(t *testing.T) {
	old := config.C()
	t.Cleanup(func() { config.SetC(old) })
	config.SetC(config.Config{Forges: map[string]config.ForgeSettings{"work": {Kind: config.ForgeKindGitHub}}})

	for _, id := range []string{"", config.ForgeGitHub, "work"} {
		f, err := forge.For(id)
		if err != nil {
			t.Fatalf("For(%q): %v", id, err)
		}
		want := id
		if want == "" {
			want = config.ForgeGitHub
		}
		if f.ID() != want || f.Kind() != config.ForgeKindGitHub {
			t.Errorf("For(%q) = %s of kind %s", id, f.ID(), f.Kind())
		}
		if got := f.PRHeadRef(12); got != "refs/pull/12/head" {
			t.Errorf("PRHeadRef(12) = %q, want GitHub's pull ref", got)
		}
	}
}
