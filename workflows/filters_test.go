package workflows

import (
	"crs/config"
	"testing"
	"time"

	"github.com/google/go-github/v48/github"
)

func TestLatestUserReviewIsDismissed(t *testing.T) {
	me := "myself"
	other := "someone-else"

	t0 := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	mkReview := func(login, state string, at time.Time) *github.PullRequestReview {
		l, s := login, state
		t := at
		return &github.PullRequestReview{
			User:        &github.User{Login: &l},
			State:       &s,
			SubmittedAt: &t,
		}
	}

	tests := []struct {
		name    string
		reviews []*github.PullRequestReview
		want    bool
	}{
		{
			name:    "no reviews",
			reviews: nil,
			want:    false,
		},
		{
			name: "user has only an APPROVED review",
			reviews: []*github.PullRequestReview{
				mkReview(me, "APPROVED", t0),
			},
			want: false,
		},
		{
			name: "user's only review is DISMISSED",
			reviews: []*github.PullRequestReview{
				mkReview(me, "DISMISSED", t0),
			},
			want: true,
		},
		{
			name: "user APPROVED then was DISMISSED (latest is DISMISSED)",
			reviews: []*github.PullRequestReview{
				mkReview(me, "APPROVED", t0),
				mkReview(me, "DISMISSED", t0.Add(time.Hour)),
			},
			want: true,
		},
		{
			name: "user DISMISSED then APPROVED again (latest is APPROVED)",
			reviews: []*github.PullRequestReview{
				mkReview(me, "DISMISSED", t0),
				mkReview(me, "APPROVED", t0.Add(time.Hour)),
			},
			want: false,
		},
		{
			name: "only other user has a DISMISSED review",
			reviews: []*github.PullRequestReview{
				mkReview(other, "DISMISSED", t0),
			},
			want: false,
		},
		{
			name: "case-insensitive login match",
			reviews: []*github.PullRequestReview{
				mkReview("MYSELF", "DISMISSED", t0),
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := latestUserReviewIsDismissed(tt.reviews, me)
			if got != tt.want {
				t.Errorf("latestUserReviewIsDismissed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterMyReviewRequested(t *testing.T) {
	me := "myself"
	cfg := config.C()
	cfg.GithubUsername = me
	config.SetC(cfg)

	owner := "owner"
	repo := "repo"

	mkPR := func(number int, reviewers ...string) *github.PullRequest {
		n := number
		o, r := owner, repo
		users := []*github.User{}
		for _, login := range reviewers {
			l := login
			users = append(users, &github.User{Login: &l})
		}
		return &github.PullRequest{
			Number: &n,
			Base: &github.PullRequestBranch{
				Repo: &github.Repository{
					Owner: &github.User{Login: &o},
					Name:  &r,
				},
			},
			RequestedReviewers: users,
		}
	}

	t0 := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	mkReview := func(login, state string, at time.Time) *github.PullRequestReview {
		l, s := login, state
		ts := at
		return &github.PullRequestReview{
			User:        &github.User{Login: &l},
			State:       &s,
			SubmittedAt: &ts,
		}
	}

	// PR 1: user is in RequestedReviewers (normal pending request).
	pr1 := mkPR(1, me)
	// PR 2: user not in RequestedReviewers, but their latest review was DISMISSED.
	pr2 := mkPR(2, "someone-else")
	// PR 3: user not in RequestedReviewers and latest review is APPROVED → exclude.
	pr3 := mkPR(3, "someone-else")
	// PR 4: user has no history at all → exclude.
	pr4 := mkPR(4, "someone-else")

	store := NewAuxDataStore()
	store.Set(PRKey{Owner: owner, Repo: repo, Number: 2}, &PRAuxData{
		Reviews: []*github.PullRequestReview{
			mkReview(me, "APPROVED", t0),
			mkReview(me, "DISMISSED", t0.Add(time.Hour)),
		},
	})
	store.Set(PRKey{Owner: owner, Repo: repo, Number: 3}, &PRAuxData{
		Reviews: []*github.PullRequestReview{
			mkReview(me, "DISMISSED", t0),
			mkReview(me, "APPROVED", t0.Add(time.Hour)),
		},
	})
	store.Set(PRKey{Owner: owner, Repo: repo, Number: 4}, &PRAuxData{})
	SetCurrentAuxDataStore(store)
	defer SetCurrentAuxDataStore(nil)

	got := filterMyReviewRequested([]*github.PullRequest{pr1, pr2, pr3, pr4})

	wantNumbers := map[int]bool{1: true, 2: true}
	if len(got) != len(wantNumbers) {
		t.Fatalf("filterMyReviewRequested returned %d PRs, want %d", len(got), len(wantNumbers))
	}
	for _, pr := range got {
		if !wantNumbers[*pr.Number] {
			t.Errorf("filterMyReviewRequested unexpectedly included PR #%d", *pr.Number)
		}
	}
}

func TestFilterMyReviewRequestedNoAuxStore(t *testing.T) {
	me := "myself"
	cfg := config.C()
	cfg.GithubUsername = me
	config.SetC(cfg)

	SetCurrentAuxDataStore(nil)

	owner, repo := "owner", "repo"
	n1, n2 := 1, 2
	pr1 := &github.PullRequest{
		Number: &n1,
		Base: &github.PullRequestBranch{
			Repo: &github.Repository{
				Owner: &github.User{Login: &owner},
				Name:  &repo,
			},
		},
		RequestedReviewers: []*github.User{{Login: &me}},
	}
	otherLogin := "other"
	pr2 := &github.PullRequest{
		Number: &n2,
		Base: &github.PullRequestBranch{
			Repo: &github.Repository{
				Owner: &github.User{Login: &owner},
				Name:  &repo,
			},
		},
		RequestedReviewers: []*github.User{{Login: &otherLogin}},
	}

	got := filterMyReviewRequested([]*github.PullRequest{pr1, pr2})
	if len(got) != 1 || *got[0].Number != 1 {
		t.Errorf("expected only PR #1 with no aux store, got %d PRs", len(got))
	}
}
