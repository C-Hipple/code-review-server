package git_tools

import (
	"crs/config"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
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
			// A pending review request outlives whatever I did before it: GitHub
			// removes the request when I submit a review, so a login still sitting
			// in RequestedReviewers means the request was (re-)made and is open.
			name: "Personally requested with a newer prior review (re-review request)",
			pr:   makePR(2, myLogin),
			state: InteractionState{
				LastMeTime:     time.Now().Add(-10 * time.Minute),
				LastOthersTime: time.Now().Add(-20 * time.Minute),
				LastCommitTime: time.Now().Add(-30 * time.Minute),
			},
			shouldInclude: true,
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
			name: "Personally requested, and I commented since (request still open)",
			pr:   makePR(5, myLogin),
			state: InteractionState{
				LastMeTime:     time.Now().Add(-5 * time.Minute),
				LastOthersTime: time.Now().Add(-10 * time.Minute),
				LastCommitTime: time.Now().Add(-20 * time.Minute),
			},
			shouldInclude: true,
		},
		{
			// Stale-review dismissal (CODEOWNERS, "dismiss stale approvals on push")
			// drops my approval without adding me back to RequestedReviewers.
			name: "Not requested, but my latest review was dismissed",
			pr:   makePR(8, "other"),
			state: InteractionState{
				LastMeTime:        time.Now().Add(-5 * time.Minute),
				LastOthersTime:    time.Now().Add(-10 * time.Minute),
				LastCommitTime:    time.Now().Add(-20 * time.Minute),
				MyReviewDismissed: true,
			},
			shouldInclude: true,
		},
		{
			name: "Not requested, my review stands, nothing outstanding",
			pr:   makePR(9, "other"),
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

// TestFilterWaitingOnMeUsernameCaseInsensitive guards against a regression where
// a casing difference between the configured GithubUsername and the login
// returned by the GitHub API caused the filter to recognize no one as "me",
// silently matching zero PRs. GitHub logins are case-insensitive.
func TestFilterWaitingOnMeUsernameCaseInsensitive(t *testing.T) {
	cfg := config.C()
	cfg.GithubUsername = "MyUser" // configured with different casing than the PR data
	config.SetC(cfg)

	owner := "owner"
	repo := "repo"
	number := 500

	pr := &github.PullRequest{
		Number: &number,
		User:   &github.User{Login: github.String("author")},
		Base: &github.PullRequestBranch{
			Repo: &github.Repository{
				Owner: &github.User{Login: &owner},
				Name:  &repo,
			},
		},
		// API returns the login in lowercase; config has "MyUser".
		RequestedReviewers: []*github.User{{Login: github.String("myuser")}},
	}

	// Fresh request: no prior interaction, so the only reason to include is the
	// requested-reviewer check, which must match case-insensitively.
	cacheKey := fmt.Sprintf("interaction_state:%s/%s:%d", owner, repo, number)
	GlobalCache.Set(cacheKey, InteractionState{}, 1*time.Hour)

	result := FilterWaitingOnMe([]*github.PullRequest{pr})
	if len(result) != 1 {
		t.Errorf("expected PR to be included despite username casing mismatch, got %d results", len(result))
	}
}

func TestCalculateInteractionState(t *testing.T) {
	myLogin := "myself"
	other := "other"
	now := time.Now()

	makeReview := func(user string, submittedAt time.Time) *github.PullRequestReview {
		return &github.PullRequestReview{
			User:        &github.User{Login: &user},
			SubmittedAt: &github.Timestamp{Time: submittedAt},
		}
	}

	makeComment := func(id int64, user string, createdAt time.Time, inReplyTo int64) *github.PullRequestComment {
		c := &github.PullRequestComment{
			ID:        &id,
			User:      &github.User{Login: &user},
			CreatedAt: &github.Timestamp{Time: createdAt},
		}
		if inReplyTo != 0 {
			c.InReplyTo = &inReplyTo
		}
		return c
	}

	makeIssueComment := func(user string, createdAt time.Time) *github.IssueComment {
		return &github.IssueComment{
			User:      &github.User{Login: &user},
			CreatedAt: &github.Timestamp{Time: createdAt},
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

// TestCalculateInteractionStateVerdictClearsThreads covers the rule that an
// explicit verdict (APPROVED / CHANGES_REQUESTED) is a holistic response to
// the PR: thread replies older than the verdict are not "waiting on me", ones
// newer than it are. Modeled on a real false positive: I comment in a thread,
// the author replies, and I respond by approving the PR rather than replying
// in the thread — without the verdict rule that PR sits in Waiting on Me
// forever.
func TestCalculateInteractionStateVerdictClearsThreads(t *testing.T) {
	myLogin := "myself"
	other := "other"
	now := time.Now()

	makeReview := func(user, state string, submittedAt time.Time) *github.PullRequestReview {
		return &github.PullRequestReview{
			User:        &github.User{Login: github.String(user)},
			State:       github.String(state),
			SubmittedAt: &github.Timestamp{Time: submittedAt},
		}
	}
	makeComment := func(id int64, user string, createdAt time.Time, inReplyTo int64) *github.PullRequestComment {
		c := &github.PullRequestComment{
			ID:        &id,
			User:      &github.User{Login: &user},
			CreatedAt: &github.Timestamp{Time: createdAt},
		}
		if inReplyTo != 0 {
			c.InReplyTo = &inReplyTo
		}
		return c
	}

	// My comment, then the author's reply.
	danglingThread := []*github.PullRequestComment{
		makeComment(1, myLogin, now.Add(-30*time.Minute), 0),
		makeComment(2, other, now.Add(-20*time.Minute), 1),
	}

	tests := []struct {
		name            string
		reviews         []*github.PullRequestReview
		reviewComments  []*github.PullRequestComment
		issueComments   []*github.IssueComment
		wantUnresponded bool
	}{
		{
			name:            "Dangling reply with no verdict",
			reviewComments:  danglingThread,
			wantUnresponded: true,
		},
		{
			name:            "Approval after the reply clears it",
			reviews:         []*github.PullRequestReview{makeReview(myLogin, "APPROVED", now.Add(-10*time.Minute))},
			reviewComments:  danglingThread,
			wantUnresponded: false,
		},
		{
			name:            "Changes-requested after the reply clears it",
			reviews:         []*github.PullRequestReview{makeReview(myLogin, "CHANGES_REQUESTED", now.Add(-10*time.Minute))},
			reviewComments:  danglingThread,
			wantUnresponded: false,
		},
		{
			name:    "Reply after my approval re-flags",
			reviews: []*github.PullRequestReview{makeReview(myLogin, "APPROVED", now.Add(-25*time.Minute))},
			// Thread reply at -20m postdates the -25m approval.
			reviewComments:  danglingThread,
			wantUnresponded: true,
		},
		{
			// COMMENTED reviews are how GitHub wraps standalone inline
			// comments; they must not act as PR-wide responses.
			name:            "COMMENTED review does not clear the reply",
			reviews:         []*github.PullRequestReview{makeReview(myLogin, "COMMENTED", now.Add(-10*time.Minute))},
			reviewComments:  danglingThread,
			wantUnresponded: true,
		},
		{
			// A dismissed approval's state is rewritten to DISMISSED, so it
			// stops clearing threads (and MyReviewDismissed flags the PR
			// through its own signal).
			name:            "Dismissed approval does not clear the reply",
			reviews:         []*github.PullRequestReview{makeReview(myLogin, "DISMISSED", now.Add(-10*time.Minute))},
			reviewComments:  danglingThread,
			wantUnresponded: true,
		},
		{
			name:            "Someone else's verdict does not clear the reply",
			reviews:         []*github.PullRequestReview{makeReview(other, "APPROVED", now.Add(-10*time.Minute))},
			reviewComments:  danglingThread,
			wantUnresponded: true,
		},
		{
			// Regression: the conversation-tab check used to compare against
			// LastOthersTime, which moves on other people's *reviews* — a
			// third reviewer approving after my last activity re-flagged any
			// PR I had ever conversation-commented on.
			name: "Another reviewer's later review does not re-flag the conversation tab",
			reviews: []*github.PullRequestReview{
				makeReview(other, "APPROVED", now.Add(-5*time.Minute)),
			},
			issueComments: []*github.IssueComment{
				{User: &github.User{Login: &myLogin}, CreatedAt: &github.Timestamp{Time: now.Add(-15 * time.Minute)}},
			},
			wantUnresponded: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := CalculateInteractionState(myLogin, &github.PullRequest{}, tt.reviews, tt.reviewComments, tt.issueComments)
			if state.HasUnrespondedComments != tt.wantUnresponded {
				t.Errorf("HasUnrespondedComments: expected %v, got %v", tt.wantUnresponded, state.HasUnrespondedComments)
			}
		})
	}
}

// TestCalculateInteractionStateReviewDismissed covers the re-review signal that
// never reaches RequestedReviewers: a dismissed review means my verdict was
// thrown away and the PR is waiting on me again, while a later review of any
// other state means I have already weighed in since.
func TestCalculateInteractionStateReviewDismissed(t *testing.T) {
	myLogin := "myself"
	now := time.Now()

	makeReview := func(user, state string, submittedAt time.Time) *github.PullRequestReview {
		return &github.PullRequestReview{
			User:        &github.User{Login: github.String(user)},
			State:       github.String(state),
			SubmittedAt: &github.Timestamp{Time: submittedAt},
		}
	}

	tests := []struct {
		name            string
		reviews         []*github.PullRequestReview
		expectDismissed bool
	}{
		{
			name:            "No reviews",
			expectDismissed: false,
		},
		{
			name:            "My approval still stands",
			reviews:         []*github.PullRequestReview{makeReview(myLogin, "APPROVED", now.Add(-time.Hour))},
			expectDismissed: false,
		},
		{
			name:            "My latest review was dismissed",
			reviews:         []*github.PullRequestReview{makeReview(myLogin, "DISMISSED", now.Add(-time.Hour))},
			expectDismissed: true,
		},
		{
			name: "Dismissed, then I reviewed again",
			reviews: []*github.PullRequestReview{
				makeReview(myLogin, "DISMISSED", now.Add(-2*time.Hour)),
				makeReview(myLogin, "APPROVED", now.Add(-time.Hour)),
			},
			expectDismissed: false,
		},
		{
			name: "Someone else's review was dismissed",
			reviews: []*github.PullRequestReview{
				makeReview("other", "DISMISSED", now.Add(-time.Hour)),
			},
			expectDismissed: false,
		},
		{
			// A COMMENTED review is what GitHub creates for a standalone inline
			// comment, so this is one drive-by remark after my approval was
			// dismissed — not a re-review. Ranking it as my latest review used
			// to clear the dismissal and drop the PR out of Waiting On Me while
			// I still owed the re-review.
			name: "Dismissed, then I left an inline comment",
			reviews: []*github.PullRequestReview{
				makeReview(myLogin, "DISMISSED", now.Add(-2*time.Hour)),
				makeReview(myLogin, "COMMENTED", now.Add(-time.Hour)),
			},
			expectDismissed: true,
		},
		{
			name: "Dismissed, then I commented, then I re-approved",
			reviews: []*github.PullRequestReview{
				makeReview(myLogin, "DISMISSED", now.Add(-3*time.Hour)),
				makeReview(myLogin, "COMMENTED", now.Add(-2*time.Hour)),
				makeReview(myLogin, "APPROVED", now.Add(-time.Hour)),
			},
			expectDismissed: false,
		},
		{
			// The dismissal is the newer verdict here: I requested changes, the
			// author pushed, and that dismissed the review.
			name: "Requested changes, then a later approval of mine was dismissed",
			reviews: []*github.PullRequestReview{
				makeReview(myLogin, "CHANGES_REQUESTED", now.Add(-3*time.Hour)),
				makeReview(myLogin, "DISMISSED", now.Add(-time.Hour)),
			},
			expectDismissed: true,
		},
		{
			name: "Only a COMMENTED review of mine",
			reviews: []*github.PullRequestReview{
				makeReview(myLogin, "COMMENTED", now.Add(-time.Hour)),
			},
			expectDismissed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := CalculateInteractionState(myLogin, &github.PullRequest{}, tt.reviews, nil, nil)
			if state.MyReviewDismissed != tt.expectDismissed {
				t.Errorf("MyReviewDismissed: expected %v, got %v", tt.expectDismissed, state.MyReviewDismissed)
			}
		})
	}
}

// TestLatestCommitTime guards the "last push" timestamp against list orderings
// where the newest commit is not the final element — force-pushes and rebases
// both produce them, and an under-reported push time makes a PR look answered.
func TestLatestCommitTime(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	commit := func(committed time.Time) *github.RepositoryCommit {
		return &github.RepositoryCommit{
			Commit: &github.Commit{
				Committer: &github.CommitAuthor{Date: &github.Timestamp{Time: committed}},
			},
		}
	}

	tests := []struct {
		name     string
		commits  []*github.RepositoryCommit
		expected time.Time
	}{
		{name: "No commits", expected: time.Time{}},
		{
			name:     "Newest commit is last",
			commits:  []*github.RepositoryCommit{commit(now.Add(-2 * time.Hour)), commit(now.Add(-time.Hour))},
			expected: now.Add(-time.Hour),
		},
		{
			name:     "Newest commit is not last",
			commits:  []*github.RepositoryCommit{commit(now.Add(-time.Hour)), commit(now.Add(-2 * time.Hour))},
			expected: now.Add(-time.Hour),
		},
		{
			name:     "Nil entries are skipped",
			commits:  []*github.RepositoryCommit{nil, {}, {Commit: &github.Commit{}}, commit(now.Add(-time.Hour))},
			expected: now.Add(-time.Hour),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := latestCommitTime(tt.commits)
			if !got.Equal(tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, got)
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

	makePR := func(number int, reviewers ...string) *github.PullRequest {
		requestedReviewers := []*github.User{}
		for _, r := range reviewers {
			requestedReviewers = append(requestedReviewers, &github.User{Login: github.String(r)})
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
			// Same timestamps as the first case, but the author re-requested my
			// review — the PR is waiting on me, not on them.
			name: "I acted last but my review was re-requested",
			pr:   makePR(105, "myself"),
			state: InteractionState{
				LastMeTime:     time.Now().Add(-5 * time.Minute),
				LastOthersTime: time.Now().Add(-10 * time.Minute),
				LastCommitTime: time.Now().Add(-20 * time.Minute),
			},
			shouldInclude: false,
		},
		{
			name: "I acted last but my review was dismissed",
			pr:   makePR(106),
			state: InteractionState{
				LastMeTime:        time.Now().Add(-5 * time.Minute),
				LastOthersTime:    time.Now().Add(-10 * time.Minute),
				LastCommitTime:    time.Now().Add(-20 * time.Minute),
				MyReviewDismissed: true,
			},
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
