package main

import (
	"crs/git_tools"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

// TestExplain checks that the reasons this tool prints line up with the
// conditions FilterWaitingOnMe actually applies. The interaction state is
// primed in the cache so no API calls happen.
func TestExplain(t *testing.T) {
	const login = "myself"
	owner, repo := "owner", "repo"

	makePR := func(number int, reviewers ...string) *github.PullRequest {
		requested := []*github.User{}
		for _, r := range reviewers {
			requested = append(requested, &github.User{Login: github.String(r)})
		}
		return &github.PullRequest{
			Number: github.Int(number),
			User:   &github.User{Login: github.String("author")},
			Base: &github.PullRequestBranch{Repo: &github.Repository{
				Owner: &github.User{Login: &owner}, Name: &repo,
			}},
			RequestedReviewers: requested,
		}
	}

	tests := []struct {
		name       string
		pr         *github.PullRequest
		state      git_tools.InteractionState
		wantMatch  bool
		wantReason string
	}{
		{
			name:       "requested",
			pr:         makePR(1, login),
			wantMatch:  true,
			wantReason: "review requested from you",
		},
		{
			name:       "requested with different casing",
			pr:         makePR(2, strings.ToUpper(login)),
			wantMatch:  true,
			wantReason: "review requested from you",
		},
		{
			name:       "dismissed review",
			pr:         makePR(3, "someone-else"),
			state:      git_tools.InteractionState{MyReviewDismissed: true},
			wantMatch:  true,
			wantReason: "dismissed",
		},
		{
			name:       "unresponded comments",
			pr:         makePR(4, "someone-else"),
			state:      git_tools.InteractionState{HasUnrespondedComments: true},
			wantMatch:  true,
			wantReason: "unresponded comments",
		},
		{
			name:       "nothing outstanding",
			pr:         makePR(5, "someone-else"),
			wantMatch:  false,
			wantReason: "no open request",
		},
		{
			name:       "incomplete PR",
			pr:         &github.PullRequest{Number: github.Int(6)},
			wantMatch:  false,
			wantReason: "incomplete PR data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := fmt.Sprintf("interaction_state:%s/%s:%d", owner, repo, tt.pr.GetNumber())
			git_tools.GlobalCache.Set(key, tt.state, time.Hour)

			match, reason := explain(tt.pr, login)
			if match != tt.wantMatch {
				t.Errorf("match: expected %v, got %v (%s)", tt.wantMatch, match, reason)
			}
			if !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason: expected to contain %q, got %q", tt.wantReason, reason)
			}
		})
	}
}
