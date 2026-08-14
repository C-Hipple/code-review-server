package git_tools

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

// Tests for the team half of an open review request.
//
// Most organizations route reviews through CODEOWNERS, which requests a *team*
// rather than a person, and GitHub never copies those into RequestedReviewers.
// FilterWaitingOnMe read only RequestedReviewers, so for anyone whose review
// requests arrive that way the section was empty no matter how many PRs were
// waiting — while WaitingOnMeWorkflow was already spending search calls finding
// exactly those PRs (see ReviewRequestedQueries) and the workflow's own
// description promised "an open review request (yours or your team's)".

const teamTestOwner = "acme"

func makeTeamPR(t *testing.T, number int, teams ...*github.Team) *github.PullRequest {
	t.Helper()
	owner, repo := teamTestOwner, "repo"
	return &github.PullRequest{
		Number: github.Int(number),
		User:   &github.User{Login: github.String("author")},
		Base: &github.PullRequestBranch{
			Repo: &github.Repository{
				Owner: &github.User{Login: &owner},
				Name:  &repo,
			},
		},
		RequestedTeams: teams,
	}
}

func team(slug string) *github.Team {
	return &github.Team{Slug: github.String(slug)}
}

// teamInOrg builds the fuller payload GitHub sends when a team carries its own
// organization, which must take precedence over the repository's owner.
func teamInOrg(org, slug string) *github.Team {
	return &github.Team{
		Slug:         github.String(slug),
		Organization: &github.Organization{Login: github.String(org)},
	}
}

func TestTeamReviewRequestedFrom(t *testing.T) {
	tests := []struct {
		name     string
		pr       *github.PullRequest
		teamRefs []string
		want     bool
	}{
		{
			name:     "requested team is mine",
			pr:       makeTeamPR(t, 1, team("payments")),
			teamRefs: []string{"acme/payments"},
			want:     true,
		},
		{
			name:     "one of several requested teams is mine",
			pr:       makeTeamPR(t, 2, team("frontend"), team("payments")),
			teamRefs: []string{"acme/payments"},
			want:     true,
		},
		{
			name:     "requested team is not mine",
			pr:       makeTeamPR(t, 3, team("frontend")),
			teamRefs: []string{"acme/payments"},
			want:     false,
		},
		{
			// The reason the org half is matched at all. Team slugs are only
			// unique within an organization, and "backend" or "platform"
			// exists in plenty of them; matching the bare slug would fill the
			// section with PRs from orgs whose teams I am not in.
			name:     "same slug in another organization does not match",
			pr:       makeTeamPR(t, 4, team("payments")),
			teamRefs: []string{"other-org/payments"},
			want:     false,
		},
		{
			// GitHub omits the organization from a PR's requested_teams, so the
			// repository's owner stands in for it — but when the payload does
			// carry one, it is the authority.
			name:     "team's own organization wins over the repo owner",
			pr:       makeTeamPR(t, 5, teamInOrg("other-org", "payments")),
			teamRefs: []string{"other-org/payments"},
			want:     true,
		},
		{
			name:     "team's own organization rules out a repo-owner match",
			pr:       makeTeamPR(t, 6, teamInOrg("other-org", "payments")),
			teamRefs: []string{"acme/payments"},
			want:     false,
		},
		{
			// Organization names and team slugs are both case-insensitive, and
			// the two sides come from different API responses.
			name:     "matching is case-insensitive on both halves",
			pr:       makeTeamPR(t, 7, team("Payments")),
			teamRefs: []string{"ACME/payments"},
			want:     true,
		},
		{
			name:     "no requested teams",
			pr:       makeTeamPR(t, 8),
			teamRefs: []string{"acme/payments"},
			want:     false,
		},
		{
			// A token that cannot read org membership yields no teams, which
			// must degrade to "no team requests match" rather than to a match.
			name:     "no teams of mine known",
			pr:       makeTeamPR(t, 9, team("payments")),
			teamRefs: nil,
			want:     false,
		},
		{
			name:     "nil entries are skipped",
			pr:       makeTeamPR(t, 10, nil, team("payments")),
			teamRefs: []string{"acme/payments"},
			want:     true,
		},
		{
			name:     "incomplete PR data does not match",
			pr:       &github.PullRequest{Number: github.Int(11), RequestedTeams: []*github.Team{team("payments")}},
			teamRefs: []string{"acme/payments"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := teamReviewRequestedFrom(tt.pr, tt.teamRefs); got != tt.want {
				t.Errorf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

// TestFilterWaitingOnMeTeamRequest is the end-to-end version: a team request has
// to survive the interaction prefilter, which excludes any PR I have never
// reviewed or commented on — and a fresh CODEOWNERS request is exactly that.
func TestFilterWaitingOnMeTeamRequest(t *testing.T) {
	login := "team-filter-user"

	restore := stubTeamRefs(t, "acme/payments")
	defer restore()

	// Empty set: the searches ran and found no interaction of mine anywhere, so
	// nothing here can match by way of a dismissal or a dangling reply.
	GlobalCache.Set(interactionSetCacheKey(login), InteractionSet{}, 1*time.Hour)

	mine := makeTeamPR(t, 20, team("payments"))
	notMine := makeTeamPR(t, 21, team("frontend"))
	otherOrg := makeTeamPR(t, 22, teamInOrg("other-org", "payments"))

	// Seeded so that reaching the interaction state at all is visible as a
	// wrong answer rather than an accidentally right one.
	for _, n := range []int{20, 21, 22} {
		GlobalCache.Set(fmt.Sprintf("interaction_state:%s/%s:%d", teamTestOwner, "repo", n), InteractionState{}, 1*time.Hour)
	}

	matched := map[int]bool{}
	for _, pr := range MakeWaitingOnMeFilter(login)([]*github.PullRequest{mine, notMine, otherOrg}) {
		matched[pr.GetNumber()] = true
	}

	if !matched[20] {
		t.Errorf("a PR requesting review from my team should be waiting on me")
	}
	if matched[21] {
		t.Errorf("a PR requesting review from a team I am not in should not match")
	}
	if matched[22] {
		t.Errorf("a same-named team in another organization should not match")
	}
}

// TestFilterWaitingOnAuthorExcludesTeamRequest pins the other side of the same
// rule: the two sections must not both claim a PR. An open request on my team
// puts the ball in my court, so the PR is not waiting on its author even though
// I acted last.
func TestFilterWaitingOnAuthorExcludesTeamRequest(t *testing.T) {
	login := "team-author-user"

	restore := stubTeamRefs(t, "acme/payments")
	defer restore()

	pr := makeTeamPR(t, 30, team("payments"))
	GlobalCache.Set(interactionSetCacheKey(login), InteractionSet{
		interactionKey(teamTestOwner, "repo", 30): true,
	}, 1*time.Hour)
	GlobalCache.Set(fmt.Sprintf("interaction_state:%s/%s:%d", teamTestOwner, "repo", 30), InteractionState{
		LastMeTime:     time.Now().Add(-5 * time.Minute),
		LastOthersTime: time.Now().Add(-10 * time.Minute),
		LastCommitTime: time.Now().Add(-20 * time.Minute),
	}, 1*time.Hour)

	if got := MakeWaitingOnAuthorFilter(login)([]*github.PullRequest{pr}); len(got) != 0 {
		t.Errorf("a PR with an open request on my team is waiting on me, not on its author")
	}
}

// TestIdentityFiltersSkipPRsWithoutNumbers guards a crash, not a filter
// decision. Every branch past the guard reads *pr.Number — including the
// slog.Debug call, whose arguments are evaluated whatever the log level — and
// these run inside goroutines, where a nil dereference takes down the server
// rather than just the filter pass.
func TestIdentityFiltersSkipPRsWithoutNumbers(t *testing.T) {
	login := "nil-number-user"
	owner, repo := teamTestOwner, "repo"

	numberless := &github.PullRequest{
		User: &github.User{Login: github.String("author")},
		Base: &github.PullRequestBranch{Repo: &github.Repository{
			Owner: &github.User{Login: &owner}, Name: &repo,
		}},
		RequestedReviewers: []*github.User{{Login: &login}},
	}
	ok := makeTeamPR(t, 40)
	ok.RequestedReviewers = []*github.User{{Login: &login}}

	// A panic in a filter goroutine cannot be recovered from here; it fails the
	// run outright, which is the point.
	if got := MakeWaitingOnMeFilter(login)([]*github.PullRequest{numberless, ok}); len(got) != 1 {
		t.Errorf("expected only the PR with a number, got %d", len(got))
	}
	if got := MakeWaitingOnAuthorFilter(login)([]*github.PullRequest{numberless, ok}); len(got) != 0 {
		t.Errorf("expected no PRs waiting on the author, got %d", len(got))
	}
}

// stubTeamRefs points the filters' team lookup at a fixed list for one test.
func stubTeamRefs(t *testing.T, refs ...string) func() {
	t.Helper()
	orig := myTeamRefs
	myTeamRefs = func() []string { return refs }
	return func() { myTeamRefs = orig }
}
