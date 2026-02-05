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
	cfg := config.C()
	cfg.GithubUsername = myLogin
	config.SetC(cfg)

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
		{
			name: "Personally requested, never acted (fresh request)",
			pr:   makePR(6, myLogin),
			state: InteractionState{
				LastCommitTime: time.Now().Add(-30 * time.Minute),
			},
			shouldInclude: true,
		},
		{
			name: "Personally requested and has unresponded comments",
			pr:   makePR(7, myLogin),
			state: InteractionState{
				LastMeTime:             time.Now().Add(-10 * time.Minute),
				LastOthersTime:         time.Now().Add(-20 * time.Minute),
				LastCommitTime:         time.Now().Add(-30 * time.Minute),
				HasUnrespondedComments: true,
			},
			shouldInclude: true,
		},
		{
			name:          "Nil PR should be ignored",
			pr:            nil,
			shouldInclude: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.pr != nil {
				// Prime cache
				cacheKey := fmt.Sprintf("interaction_state:%s/%s:%d", owner, repo, *tt.pr.Number)
				GlobalCache.Set(cacheKey, tt.state, 1*time.Hour)
			}

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
			pr:   &github.PullRequest{},
			reviews: []*github.PullRequestReview{
				makeReview(myLogin, now.Add(-30 * time.Minute)),
				makeReview(other, now.Add(-45 * time.Minute)),
			},
			expectedState: InteractionState{
				LastMeTime:     now.Add(-30 * time.Minute),
				LastOthersTime: now.Add(-45 * time.Minute),
			},
		},
		{
			name: "Threaded review comments - reply by other",
			pr:   &github.PullRequest{},
			reviewComments: []*github.PullRequestComment{
				makeComment(1, myLogin, now.Add(-20 * time.Minute), 0),
				makeComment(2, other, now.Add(-10 * time.Minute), 1),
			},
			expectedState: InteractionState{
				LastMeTime:             now.Add(-20 * time.Minute),
				LastOthersTime:         now.Add(-10 * time.Minute),
				HasUnrespondedComments: true,
			},
		},
		{
			name: "Threaded review comments - replied back by me",
			pr:   &github.PullRequest{},
			reviewComments: []*github.PullRequestComment{
				makeComment(1, myLogin, now.Add(-20 * time.Minute), 0),
				makeComment(2, other, now.Add(-10 * time.Minute), 1),
				makeComment(3, myLogin, now.Add(-5 * time.Minute), 1),
			},
			expectedState: InteractionState{
				LastMeTime:             now.Add(-5 * time.Minute),
				LastOthersTime:         now.Add(-10 * time.Minute),
				HasUnrespondedComments: false,
			},
		},
		{
			name: "Issue comments - reply by other",
			pr:   &github.PullRequest{},
			issueComments: []*github.IssueComment{
				makeIssueComment(myLogin, now.Add(-20 * time.Minute)),
				makeIssueComment(other, now.Add(-10 * time.Minute)),
			},
			expectedState: InteractionState{
				LastMeTime:             now.Add(-20 * time.Minute),
				LastOthersTime:         now.Add(-10 * time.Minute),
				HasUnrespondedComments: true,
			},
		},
		{
			name: "Issue comments - I replied last",
			pr:   &github.PullRequest{},
			issueComments: []*github.IssueComment{
				makeIssueComment(other, now.Add(-20 * time.Minute)),
				makeIssueComment(myLogin, now.Add(-10 * time.Minute)),
			},
			expectedState: InteractionState{
				LastMeTime:             now.Add(-10 * time.Minute),
				LastOthersTime:         now.Add(-20 * time.Minute),
				HasUnrespondedComments: false,
			},
		},
		{
			name: "Issue comments - only others, I never participated",
			pr:   &github.PullRequest{},
			issueComments: []*github.IssueComment{
				makeIssueComment(other, now.Add(-10 * time.Minute)),
			},
			expectedState: InteractionState{
				LastOthersTime:         now.Add(-10 * time.Minute),
				HasUnrespondedComments: false,
			},
		},
		{
			name: "Review comment thread where I did not participate",
			pr:   &github.PullRequest{},
			reviewComments: []*github.PullRequestComment{
				makeComment(10, other, now.Add(-20 * time.Minute), 0),
				makeComment(11, "third", now.Add(-10 * time.Minute), 10),
			},
			expectedState: InteractionState{
				LastOthersTime:         now.Add(-10 * time.Minute),
				HasUnrespondedComments: false,
			},
		},
		{
			name: "Multiple review comment threads - one resolved, one not",
			pr:   &github.PullRequest{},
			reviewComments: []*github.PullRequestComment{
				makeComment(20, myLogin, now.Add(-30 * time.Minute), 0),
				makeComment(21, other, now.Add(-25 * time.Minute), 20),
				makeComment(22, myLogin, now.Add(-20 * time.Minute), 20),
				makeComment(23, myLogin, now.Add(-15 * time.Minute), 0),
				makeComment(24, other, now.Add(-10 * time.Minute), 23),
			},
			expectedState: InteractionState{
				LastMeTime:             now.Add(-15 * time.Minute),
				LastOthersTime:         now.Add(-10 * time.Minute),
				HasUnrespondedComments: true,
			},
		},
		{
			name: "Mixed sources - all resolved",
			pr:   &github.PullRequest{},
			reviews: []*github.PullRequestReview{
				makeReview(other, now.Add(-40 * time.Minute)),
				makeReview(myLogin, now.Add(-35 * time.Minute)),
			},
			reviewComments: []*github.PullRequestComment{
				makeComment(30, myLogin, now.Add(-30 * time.Minute), 0),
				makeComment(31, other, now.Add(-25 * time.Minute), 30),
				makeComment(32, myLogin, now.Add(-20 * time.Minute), 30),
			},
			issueComments: []*github.IssueComment{
				makeIssueComment(other, now.Add(-15 * time.Minute)),
				makeIssueComment(myLogin, now.Add(-10 * time.Minute)),
			},
			expectedState: InteractionState{
				LastMeTime:             now.Add(-10 * time.Minute),
				LastOthersTime:         now.Add(-15 * time.Minute),
				HasUnrespondedComments: false,
			},
		},
		{
			name: "Mixed sources - issue comment unresolved",
			pr:   &github.PullRequest{},
			reviews: []*github.PullRequestReview{
				makeReview(myLogin, now.Add(-40 * time.Minute)),
			},
			reviewComments: []*github.PullRequestComment{
				makeComment(40, myLogin, now.Add(-30 * time.Minute), 0),
				makeComment(41, other, now.Add(-25 * time.Minute), 40),
				makeComment(42, myLogin, now.Add(-20 * time.Minute), 40),
			},
			issueComments: []*github.IssueComment{
				makeIssueComment(myLogin, now.Add(-15 * time.Minute)),
				makeIssueComment(other, now.Add(-10 * time.Minute)),
			},
			expectedState: InteractionState{
				LastMeTime:             now.Add(-15 * time.Minute),
				LastOthersTime:         now.Add(-10 * time.Minute),
				HasUnrespondedComments: true,
			},
		},
		{
			name: "Nil checks (should not panic)",
			pr:   nil,
			reviews: []*github.PullRequestReview{
				nil,
				{User: nil},
				{User: &github.User{Login: nil}},
			},
			reviewComments: []*github.PullRequestComment{
				nil,
				{User: nil},
			},
			issueComments: []*github.IssueComment{
				nil,
			},
			expectedState: InteractionState{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := CalculateInteractionState(myLogin, tt.pr, tt.reviews, tt.reviewComments, tt.issueComments)

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

func TestFilterWaitingOnAuthor(t *testing.T) {
	cfg := config.C()
	cfg.GithubUsername = "myself"
	config.SetC(cfg)

	owner := "owner"
	repo := "repo"

	makePR := func(number int) *github.PullRequest {
		return &github.PullRequest{
			Number: &number,
			User:   &github.User{Login: github.String("author")},
			Base: &github.PullRequestBranch{
				Repo: &github.Repository{
					Owner: &github.User{Login: &owner},
					Name:  &repo,
				},
			},
		}
	}

	tests := []struct {
		name          string
		pr            *github.PullRequest
		state         InteractionState
		shouldInclude bool
	}{
		{
			name: "I acted after others and after commit",
			pr:   makePR(100),
			state: InteractionState{
				LastMeTime:     time.Now().Add(-5 * time.Minute),
				LastOthersTime: time.Now().Add(-10 * time.Minute),
				LastCommitTime: time.Now().Add(-20 * time.Minute),
			},
			shouldInclude: true,
		},
		{
			name: "Others acted after me",
			pr:   makePR(101),
			state: InteractionState{
				LastMeTime:     time.Now().Add(-20 * time.Minute),
				LastOthersTime: time.Now().Add(-5 * time.Minute),
				LastCommitTime: time.Now().Add(-10 * time.Minute),
			},
			shouldInclude: false,
		},
		{
			name: "New commit after my action",
			pr:   makePR(102),
			state: InteractionState{
				LastMeTime:     time.Now().Add(-10 * time.Minute),
				LastOthersTime: time.Now().Add(-20 * time.Minute),
				LastCommitTime: time.Now().Add(-5 * time.Minute),
			},
			shouldInclude: false,
		},
		{
			name: "Never acted (LastMeTime zero)",
			pr:   makePR(103),
			state: InteractionState{
				LastOthersTime: time.Now().Add(-10 * time.Minute),
				LastCommitTime: time.Now().Add(-5 * time.Minute),
			},
			shouldInclude: false,
		},
		{
			name:          "All times zero",
			pr:            makePR(104),
			state:         InteractionState{},
			shouldInclude: false,
		},
		{
			name:          "Nil PR ignored",
			pr:            nil,
			shouldInclude: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.pr != nil {
				cacheKey := fmt.Sprintf("interaction_state:%s/%s:%d", owner, repo, *tt.pr.Number)
				GlobalCache.Set(cacheKey, tt.state, 1*time.Hour)
			}

			result := FilterWaitingOnAuthor([]*github.PullRequest{tt.pr})
			included := len(result) > 0

			if included != tt.shouldInclude {
				t.Errorf("expected included=%v, got %v", tt.shouldInclude, included)
			}
		})
	}
}
