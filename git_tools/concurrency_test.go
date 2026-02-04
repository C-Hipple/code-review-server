package git_tools

import (
	"crs/config"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-github/v48/github"
)

func TestFilterConcurrencyDeadlock(t *testing.T) {
	// Ensure config is set up
	cfg := config.C()
	cfg.GithubUsername = "testuser"
	config.SetC(cfg)

	// Create a large number of PRs to stress the unbuffered channel
	numPRs := 100
	prs := make([]*github.PullRequest, numPRs)
	owner := "owner"
	repo := "repo"

	for i := 0; i < numPRs; i++ {
		num := i + 1
		prs[i] = &github.PullRequest{
			Number: &num,
			Base: &github.PullRequestBranch{
				Repo: &github.Repository{
					Owner: &github.User{Login: &owner},
					Name:  &repo,
				},
			},
			User: &github.User{Login: github.String("author")},
		}

		// Mock interaction state in cache so it doesn't try to call GitHub API
		cacheKey := fmt.Sprintf("interaction_state:%s/%s:%d", owner, repo, num)
		GlobalCache.Set(cacheKey, InteractionState{HasUnrespondedComments: false}, 1*time.Hour)
	}

	// Run FilterWaitingOnMe
	// If there's a deadlock or leak with unbuffered channels, this might hang or time out
	done := make(chan bool)
	go func() {
		FilterWaitingOnMe(prs)
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("FilterWaitingOnMe timed out - potential deadlock with unbuffered channel")
	}

	// Run FilterWaitingOnAuthor
	go func() {
		FilterWaitingOnAuthor(prs)
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("FilterWaitingOnAuthor timed out - potential deadlock with unbuffered channel")
	}
}
