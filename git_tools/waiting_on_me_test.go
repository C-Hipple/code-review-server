package git_tools

import (
	"crs/config"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-github/v48/github"
)

func TestFilterWaitingOnMe(t *testing.T) {
	myLogin := "myself"
	config.C.GithubUsername = myLogin

	owner := "owner"
	repo := "repo"

	makePR := func(number int, reviewers ...string) *github.PullRequest {
		requestedReviewers := []*github.User{}
		for _, r := range reviewers {
			login := r
			requestedReviewers = append(requestedReviewers, &github.User{Login: &login})
		}
		return &github.PullRequest{
			Number: &number,
			User:   &github.User{Login: github.String("author")},
			Base: &github.PullRequestBranch{
				Repo: &github.Repository{
					Owner: &github.User{Login: &owner},
					Name:  &repo,
				},
			},
			RequestedReviewers: requestedReviewers,
		}
	}

	tests := []struct {
		name          string
		pr            *github.PullRequest
		state         InteractionState
		shouldInclude bool
	}{
		{
			name: "Personally requested, no action since push",
			pr:   makePR(1, myLogin),
			state: InteractionState{
				LastMeTime:     time.Now().Add(-2 * time.Hour),
				LastOthersTime: time.Now().Add(-1 * time.Hour),
				LastCommitTime: time.Now().Add(-30 * time.Minute),
			},
			shouldInclude: true,
		},
		{
			name: "Personally requested, I acted after push",
			pr:   makePR(2, myLogin),
			state: InteractionState{
				LastMeTime:     time.Now().Add(-10 * time.Minute),
				LastOthersTime: time.Now().Add(-20 * time.Minute),
				LastCommitTime: time.Now().Add(-30 * time.Minute),
			},
			shouldInclude: false,
		},
		{
			name: "Not personally requested, but unresponded comments",
			pr:   makePR(3, "other"),
			state: InteractionState{
				HasUnrespondedComments: true,
			},
			shouldInclude: true,
		},
		{
			name: "Not personally requested, no unresponded comments",
			pr:   makePR(4, "other"),
			state: InteractionState{
				HasUnrespondedComments: false,
			},
			shouldInclude: false,
		},
		{
			name: "Personally requested, but I acted after others",
			pr:   makePR(5, myLogin),
			state: InteractionState{
				LastMeTime:     time.Now().Add(-5 * time.Minute),
				LastOthersTime: time.Now().Add(-10 * time.Minute),
				LastCommitTime: time.Now().Add(-20 * time.Minute),
			},
			shouldInclude: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Prime cache
			cacheKey := fmt.Sprintf("interaction_state:%s/%s:%d", owner, repo, *tt.pr.Number)
			GlobalCache.Set(cacheKey, tt.state, 1*time.Hour)

			result := FilterWaitingOnMe([]*github.PullRequest{tt.pr})
			included := len(result) > 0

			if included != tt.shouldInclude {
				t.Errorf("expected included=%v, got %v", tt.shouldInclude, included)
			}
		})
	}
}

func TestCalculateInteractionState(t *testing.T) {
	myLogin := "myself"
	other := "other"
	now := time.Now()

	makePR := func(updatedAt time.Time) *github.PullRequest {
		return &github.PullRequest{
			UpdatedAt: &updatedAt,
		}
	}

	makeReview := func(user string, submittedAt time.Time) *github.PullRequestReview {
		return &github.PullRequestReview{
			User:        &github.User{Login: &user},
			SubmittedAt: &submittedAt,
		}
	}

	makeComment := func(id int64, user string, createdAt time.Time, inReplyTo int64) *github.PullRequestComment {
		c := &github.PullRequestComment{
			ID:        &id,
			User:      &github.User{Login: &user},
			CreatedAt: &createdAt,
		}
		if inReplyTo != 0 {
			c.InReplyTo = &inReplyTo
		}
		return c
	}

	makeIssueComment := func(user string, createdAt time.Time) *github.IssueComment {
		return &github.IssueComment{
			User:      &github.User{Login: &user},
			CreatedAt: &createdAt,
		}
	}

	tests := []struct {
		name           string
		pr             *github.PullRequest
		reviews        []*github.PullRequestReview
		reviewComments []*github.PullRequestComment
		issueComments  []*github.IssueComment
		expectedState  InteractionState
	}{
		{
			name: "Basic times calculation",
			pr:   makePR(now.Add(-1 * time.Hour)),
			reviews: []*github.PullRequestReview{
				makeReview(myLogin, now.Add(-30 * time.Minute)),
				makeReview(other, now.Add(-45 * time.Minute)),
			},
			expectedState: InteractionState{
				LastMeTime:     now.Add(-30 * time.Minute),
				LastOthersTime: now.Add(-45 * time.Minute),
				LastCommitTime: now.Add(-1 * time.Hour),
			},
		},
		{
			name: "Threaded review comments - reply by other",
			pr:   makePR(now),
			reviewComments: []*github.PullRequestComment{
				makeComment(1, myLogin, now.Add(-20 * time.Minute), 0),
				makeComment(2, other, now.Add(-10 * time.Minute), 1),
			},
			expectedState: InteractionState{
				LastMeTime:             now.Add(-20 * time.Minute),
				LastOthersTime:         now.Add(-10 * time.Minute),
				LastCommitTime:         now,
				HasUnrespondedComments: true,
			},
		},
		{
			name: "Threaded review comments - replied back by me",
			pr:   makePR(now),
			reviewComments: []*github.PullRequestComment{
				makeComment(1, myLogin, now.Add(-20 * time.Minute), 0),
				makeComment(2, other, now.Add(-10 * time.Minute), 1),
				makeComment(3, myLogin, now.Add(-5 * time.Minute), 1),
			},
			expectedState: InteractionState{
				LastMeTime:             now.Add(-5 * time.Minute),
				LastOthersTime:         now.Add(-10 * time.Minute),
				LastCommitTime:         now,
				HasUnrespondedComments: false,
			},
		},
		{
			name: "Issue comments - reply by other",
			pr:   makePR(now),
			issueComments: []*github.IssueComment{
				makeIssueComment(myLogin, now.Add(-20 * time.Minute)),
				makeIssueComment(other, now.Add(-10 * time.Minute)),
			},
			expectedState: InteractionState{
				LastMeTime:             now.Add(-20 * time.Minute),
				LastOthersTime:         now.Add(-10 * time.Minute),
				LastCommitTime:         now,
				HasUnrespondedComments: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := CalculateInteractionState(myLogin, tt.pr, tt.reviews, tt.reviewComments, tt.issueComments)

			// Simple check for times (within a second due to rounding in GitHub if any, though here it's direct)
			if !state.LastMeTime.Equal(tt.expectedState.LastMeTime) {
				t.Errorf("LastMeTime: expected %v, got %v", tt.expectedState.LastMeTime, state.LastMeTime)
			}
			if !state.LastOthersTime.Equal(tt.expectedState.LastOthersTime) {
				t.Errorf("LastOthersTime: expected %v, got %v", tt.expectedState.LastOthersTime, state.LastOthersTime)
			}
			if state.HasUnrespondedComments != tt.expectedState.HasUnrespondedComments {
				t.Errorf("HasUnrespondedComments: expected %v, got %v", tt.expectedState.HasUnrespondedComments, state.HasUnrespondedComments)
			}
		})
	}
}
