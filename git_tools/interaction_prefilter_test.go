package git_tools

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

// TestMain cuts the whole package off from the live GitHub API: unit tests must
// never reach it (CRS_GITHUB_TOKEN is usually set in dev shells, so a live call
// would otherwise succeed silently).
//
// The interaction searches fail, which fails open — a nil set — preserving the
// pre-prefilter behavior the older filter tests were written against. Tests
// that exercise the prefilter itself seed the GlobalCache entry for their login
// instead. The team lookup returns nothing, so team review requests match only
// where a test says which teams it is standing in for.
func TestMain(m *testing.M) {
	fetchInteractionSet = func(login string) (InteractionSet, error) {
		return nil, errors.New("interaction search disabled in tests")
	}
	myTeamRefs = func() []string { return nil }
	os.Exit(m.Run())
}

func TestMayHaveInteracted(t *testing.T) {
	makePR := func(owner, repo string, number int) *github.PullRequest {
		return &github.PullRequest{
			Number: &number,
			Base: &github.PullRequestBranch{
				Repo: &github.Repository{
					Owner: &github.User{Login: &owner},
					Name:  &repo,
				},
			},
		}
	}

	set := InteractionSet{
		interactionKey("owner", "repo", 7): true,
	}

	tests := []struct {
		name     string
		set      InteractionSet
		pr       *github.PullRequest
		expected bool
	}{
		{
			name:     "Nil set fails open",
			set:      nil,
			pr:       makePR("owner", "repo", 1),
			expected: true,
		},
		{
			name:     "Empty set excludes a complete PR",
			set:      InteractionSet{},
			pr:       makePR("owner", "repo", 1),
			expected: false,
		},
		{
			name:     "Member PR matches",
			set:      set,
			pr:       makePR("owner", "repo", 7),
			expected: true,
		},
		{
			// GitHub logins and repo names are case-insensitive; a casing
			// difference between search results and PR data must not drop a
			// PR out of the set.
			name:     "Membership is case-insensitive",
			set:      set,
			pr:       makePR("Owner", "Repo", 7),
			expected: true,
		},
		{
			name:     "Non-member PR does not match",
			set:      set,
			pr:       makePR("owner", "repo", 8),
			expected: false,
		},
		{
			name:     "Nil PR fails open",
			set:      set,
			pr:       nil,
			expected: true,
		},
		{
			name:     "Incomplete PR data fails open",
			set:      set,
			pr:       &github.PullRequest{Number: github.Int(1)},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.set.MayHaveInteracted(tt.pr); got != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestSplitRepositoryAPIURL(t *testing.T) {
	tests := []struct {
		url         string
		owner, repo string
		ok          bool
	}{
		{"https://api.github.com/repos/multimediallc/chaturbate", "multimediallc", "chaturbate", true},
		{"https://api.github.com/repos/owner/repo/extra", "", "", false},
		{"https://api.github.com/repos/owner", "", "", false},
		{"https://example.com/no/marker", "", "", false},
		{"", "", "", false},
	}

	for _, tt := range tests {
		owner, repo, ok := splitRepositoryAPIURL(tt.url)
		if owner != tt.owner || repo != tt.repo || ok != tt.ok {
			t.Errorf("splitRepositoryAPIURL(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.url, owner, repo, ok, tt.owner, tt.repo, tt.ok)
		}
	}
}

// TestSearchMyOpenInteractionsFailOpen pins the failure policy: a failed
// search returns nil (fail open) and the failure itself is cached so a broken
// search API isn't hammered by every subsequent filter call.
func TestSearchMyOpenInteractionsFailOpen(t *testing.T) {
	calls := 0
	orig := fetchInteractionSet
	fetchInteractionSet = func(login string) (InteractionSet, error) {
		calls++
		return nil, errors.New("boom")
	}
	defer func() { fetchInteractionSet = orig }()

	login := "fail-open-user"
	if set := SearchMyOpenInteractions(login); set != nil {
		t.Errorf("expected nil set on search failure, got %v", set)
	}
	if set := SearchMyOpenInteractions(login); set != nil {
		t.Errorf("expected nil set from cached failure, got %v", set)
	}
	if calls != 1 {
		t.Errorf("expected the failure to be cached after 1 fetch, got %d fetches", calls)
	}

	if set := SearchMyOpenInteractions(""); set != nil {
		t.Errorf("expected nil set for empty login, got %v", set)
	}
}

// TestFilterWaitingOnMePrefilter covers the two API-free decision paths and
// their interplay with the interaction set:
//   - an open review request matches without consulting interaction state,
//   - a PR outside the set is excluded without consulting interaction state,
//   - a PR inside the set still gets the full state-based decision.
//
// The seeded states are chosen so that taking the wrong path yields the wrong
// answer: the requested PR's state says "nothing outstanding" and the
// prefiltered PR's state says "dismissed", so consulting state where the
// short-circuit belongs (or vice versa) fails the test.
func TestFilterWaitingOnMePrefilter(t *testing.T) {
	login := "prefilter-me"
	owner := "owner"
	repo := "repo"

	makePR := func(number int, reviewers ...string) *github.PullRequest {
		requested := []*github.User{}
		for _, r := range reviewers {
			requested = append(requested, &github.User{Login: github.String(r)})
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
			RequestedReviewers: requested,
		}
	}

	seedState := func(number int, state InteractionState) {
		cacheKey := fmt.Sprintf("interaction_state:%s/%s:%d", owner, repo, number)
		GlobalCache.Set(cacheKey, state, 1*time.Hour)
	}

	// Only PR 202 has interactions according to the (fake) searches.
	GlobalCache.Set(interactionSetCacheKey(login), InteractionSet{
		interactionKey(owner, repo, 202): true,
	}, 1*time.Hour)

	// Requested: must match via the request alone. State says otherwise.
	requestedPR := makePR(200, login)
	seedState(200, InteractionState{})

	// Dismissed review, but not in the interaction set: the prefilter must
	// exclude it before the (seeded) state could include it.
	prefilteredPR := makePR(201)
	seedState(201, InteractionState{MyReviewDismissed: true})

	// Dismissed review and in the interaction set: full decision still works.
	dismissedPR := makePR(202)
	seedState(202, InteractionState{MyReviewDismissed: true})

	result := MakeWaitingOnMeFilter(login)([]*github.PullRequest{requestedPR, prefilteredPR, dismissedPR})

	matched := map[int]bool{}
	for _, pr := range result {
		matched[*pr.Number] = true
	}
	if !matched[200] {
		t.Errorf("requested PR 200 should match without interaction state")
	}
	if matched[201] {
		t.Errorf("PR 201 is outside the interaction set and should be prefiltered out")
	}
	if !matched[202] {
		t.Errorf("PR 202 is in the interaction set with a dismissed review and should match")
	}
}

// TestFilterWaitingOnAuthorPrefilter mirrors TestFilterWaitingOnMePrefilter
// for the author-side filter: PRs I never interacted with can't be waiting on
// their author *because of me*, and an open request on me disqualifies a PR
// without consulting state.
func TestFilterWaitingOnAuthorPrefilter(t *testing.T) {
	login := "prefilter-author"
	owner := "owner"
	repo := "repo"

	makePR := func(number int, reviewers ...string) *github.PullRequest {
		requested := []*github.User{}
		for _, r := range reviewers {
			requested = append(requested, &github.User{Login: github.String(r)})
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
			RequestedReviewers: requested,
		}
	}

	actedLast := InteractionState{
		LastMeTime:     time.Now().Add(-5 * time.Minute),
		LastOthersTime: time.Now().Add(-10 * time.Minute),
		LastCommitTime: time.Now().Add(-20 * time.Minute),
	}
	seedState := func(number int, state InteractionState) {
		cacheKey := fmt.Sprintf("interaction_state:%s/%s:%d", owner, repo, number)
		GlobalCache.Set(cacheKey, state, 1*time.Hour)
	}

	GlobalCache.Set(interactionSetCacheKey(login), InteractionSet{
		interactionKey(owner, repo, 301): true,
		interactionKey(owner, repo, 302): true,
	}, 1*time.Hour)

	// Outside the set: excluded even though the seeded state says I acted last.
	prefilteredPR := makePR(300)
	seedState(300, actedLast)

	// In the set and I acted last: included.
	waitingPR := makePR(301)
	seedState(301, actedLast)

	// Re-requested: the open request disqualifies it regardless of state.
	requestedPR := makePR(302, login)
	seedState(302, actedLast)

	result := MakeWaitingOnAuthorFilter(login)([]*github.PullRequest{prefilteredPR, waitingPR, requestedPR})

	matched := map[int]bool{}
	for _, pr := range result {
		matched[*pr.Number] = true
	}
	if matched[300] {
		t.Errorf("PR 300 is outside the interaction set and should be prefiltered out")
	}
	if !matched[301] {
		t.Errorf("PR 301 is in the interaction set with me having acted last and should match")
	}
	if matched[302] {
		t.Errorf("PR 302 has an open request on me and should not be waiting on its author")
	}
}
