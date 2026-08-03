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

	// A nil set (fail-open) unless the case says otherwise; cases exercising
	// the prefilter pass an explicit set, mirroring the filter's paths.
	inSet := func(numbers ...int) git_tools.InteractionSet {
		set := git_tools.InteractionSet{}
		for _, n := range numbers {
			key := strings.ToLower(fmt.Sprintf("%s/%s#%d", owner, repo, n))
			set[key] = true
		}
		return set
	}

	tests := []struct {
		name       string
		pr         *github.PullRequest
		state      git_tools.InteractionState
		interacted git_tools.InteractionSet
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
		{
			// Same dismissed state as the "dismissed review" case, but the
			// search set doesn't contain the PR — the prefilter's decision
			// must win, and the reason must say so.
			name:       "prefiltered despite dismissed state",
			pr:         makePR(7, "someone-else"),
			state:      git_tools.InteractionState{MyReviewDismissed: true},
			interacted: inSet(999),
			wantMatch:  false,
			wantReason: "prefiltered",
		},
		{
			name:       "in the interaction set with dismissed review",
			pr:         makePR(8, "someone-else"),
			state:      git_tools.InteractionState{MyReviewDismissed: true},
			interacted: inSet(8),
			wantMatch:  true,
			wantReason: "dismissed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := fmt.Sprintf("interaction_state:%s/%s:%d", owner, repo, tt.pr.GetNumber())
			git_tools.GlobalCache.Set(key, tt.state, time.Hour)

			match, reason := explain(tt.pr, login, tt.interacted)
			if match != tt.wantMatch {
				t.Errorf("match: expected %v, got %v (%s)", tt.wantMatch, match, reason)
			}
			if !strings.Contains(reason, tt.wantReason) {
				t.Errorf("reason: expected to contain %q, got %q", tt.wantReason, reason)
			}
		})
	}
}
