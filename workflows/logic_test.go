package workflows

import (
	"testing"

	"github.com/google/go-github/v48/github"
)

func TestPRToOrgBridge_RepoAndIdentifier(t *testing.T) {
	number := 123
	owner := "owner"
	repoName := "repo"
	fullName := "owner/repo"
	headRepoName := "head-repo"

	pr := &github.PullRequest{
		Number: &number,
		Base: &github.PullRequestBranch{
			Repo: &github.Repository{
				FullName: &fullName,
				Owner: &github.User{
					Login: &owner,
				},
				Name: &repoName,
			},
		},
		Head: &github.PullRequestBranch{
			Repo: &github.Repository{
				Name: &headRepoName,
			},
		},
	}

	bridge := PRToOrgBridge{
		PR: pr,
	}

	expectedRepo := "owner/repo"
	expectedID := "123"
	expectedIdentifier := "owner/repo-123"

	if repo := bridge.Repo(); repo != expectedRepo {
		t.Errorf("Repo() = %v, want %v", repo, expectedRepo)
	}

	if id := bridge.ID(); id != expectedID {
		t.Errorf("ID() = %v, want %v", id, expectedID)
	}

	if identifier := bridge.Identifier(); identifier != expectedIdentifier {
		t.Errorf("Identifier() = %v, want %v", identifier, expectedIdentifier)
	}
}
