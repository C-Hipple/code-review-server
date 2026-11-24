package server

import (
	"context"
	"crs/config"
	"crs/forge"
	"strings"
	"testing"

	"github.com/google/go-github/v74/github"
)

// labForgeKind is a kind of forge only these tests have a driver for, so an
// RPC can be seen going to the forge a repository is on without reaching
// GitHub.
const labForgeKind = "lab-forge"

// labForge answers what these tests ask of it, saying so in its answers. Its
// embedded Forge is nil: anything else it is asked panics.
type labForge struct {
	forge.Forge
	id string
}

func (f labForge) ID() string       { return f.id }
func (f labForge) Kind() string     { return labForgeKind }
func (f labForge) Configured() bool { return true }

func (f labForge) MergePR(owner, repo string, number int, method string) (*github.PullRequestMergeResult, error) {
	return &github.PullRequestMergeResult{
		Merged:  github.Ptr(true),
		SHA:     github.Ptr("merged-on-" + f.id),
		Message: github.Ptr(method),
	}, nil
}

func (f labForge) CompareStatus(ctx context.Context, owner, repo, base, head string) (string, error) {
	return "status-from-" + f.id, nil
}

func init() {
	forge.Register(labForgeKind, func(id string, _ config.ForgeSettings) (forge.Forge, error) {
		return labForge{id: id}, nil
	})
}

// onLabForge defines the forge "lab" and remembers owner/repo on it, for the
// test.
func onLabForge(t *testing.T, owner, repo string) {
	t.Helper()
	old := config.C()
	cfg := old
	cfg.Forges = map[string]config.ForgeSettings{"lab": {Kind: labForgeKind}}
	config.SetC(cfg)
	forge.Remember(owner, repo, "lab")
	t.Cleanup(func() {
		forge.Remember(owner, repo, config.ForgeGitHub)
		config.SetC(old)
	})
}

func TestMergePRGoesToTheRepositorysForge(t *testing.T) {
	onLabForge(t, "forgetest", "api")
	var reply MergePRReply
	if err := (&RPCHandler{}).MergePR(&MergePRArgs{Owner: "forgetest", Repo: "api", Number: 4}, &reply); err != nil {
		t.Fatalf("MergePR: %v", err)
	}
	if !reply.Merged || reply.SHA != "merged-on-lab" || reply.Message != "squash" {
		t.Errorf("MergePR should be answered by the repository's forge with the default method, got %+v", reply)
	}
}

func TestFilteredDiffsCompareOnTheRepositorysForge(t *testing.T) {
	onLabForge(t, "forgetest", "api")
	status, err := forgeCompare{}.Status(context.Background(), "forgetest", "api", "base", "head")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != "status-from-lab" {
		t.Errorf("the compare should run on the repository's forge, got %q", status)
	}
}

func TestErrNoTokenNamesTheForge(t *testing.T) {
	if got := errNoToken(labForge{id: "lab"}).Error(); !strings.Contains(got, `forge "lab"`) {
		t.Errorf("errNoToken = %q, want it to name the forge", got)
	}
	gh, err := forge.For(config.ForgeGitHub)
	if err != nil {
		t.Fatalf("For(github): %v", err)
	}
	if got := errNoToken(gh).Error(); got != "no GitHub token is configured" {
		t.Errorf("GitHub's message should be unchanged, got %q", got)
	}
}
