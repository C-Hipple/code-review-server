package git_tools

import (
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

// fakePRStateSource answers for the PR numbers it holds and passes on the rest.
type fakePRStateSource struct {
	states map[int]InteractionState
	ci     map[int]CIStatusInfo
}

func (f fakePRStateSource) InteractionState(_, _ string, pr *github.PullRequest) (InteractionState, bool) {
	state, ok := f.states[pr.GetNumber()]
	return state, ok
}

func (f fakePRStateSource) CIStatus(pr *github.PullRequest) (CIStatusInfo, bool) {
	info, ok := f.ci[pr.GetNumber()]
	return info, ok
}

func withPRStateSource(t *testing.T, source PRStateSource) {
	t.Helper()
	SetPRStateSource(source)
	t.Cleanup(func() { SetPRStateSource(nil) })
}

func TestGetInteractionStateAsksTheRegisteredSourceFirst(t *testing.T) {
	want := InteractionState{LastMeTime: time.Unix(100, 0), MyReviewDismissed: true}
	withPRStateSource(t, fakePRStateSource{states: map[int]InteractionState{7: want}})

	// The source answers, so nothing reaches GitHub (there is no client set up
	// to reach it with).
	got := GetInteractionState("acme", "widgets", &github.PullRequest{Number: github.Ptr(7)})
	if got != want {
		t.Errorf("GetInteractionState = %+v, want the source's %+v", got, want)
	}
}

func TestCIFiltersAskTheRegisteredSourceFirst(t *testing.T) {
	withPRStateSource(t, fakePRStateSource{ci: map[int]CIStatusInfo{
		1: {OverallStatus: "DONE"},
		2: {OverallStatus: "TODO"},
		3: {OverallStatus: "WAITING"},
	}})
	pr := func(n int) *github.PullRequest {
		return &github.PullRequest{
			Number: github.Ptr(n),
			Base:   &github.PullRequestBranch{Repo: &github.Repository{Name: github.Ptr("widgets"), Owner: &github.User{Login: github.Ptr("acme")}}},
			Head:   &github.PullRequestBranch{Label: github.Ptr("acme:feature"), Repo: &github.Repository{Name: github.Ptr("widgets")}},
		}
	}
	prs := []*github.PullRequest{pr(1), pr(2), pr(3)}

	if got := FilterCIPassing(prs); len(got) != 1 || got[0].GetNumber() != 1 {
		t.Errorf("FilterCIPassing kept %v, want only #1", numbers(got))
	}
	if got := FilterCIFailing(prs); len(got) != 1 || got[0].GetNumber() != 2 {
		t.Errorf("FilterCIFailing kept %v, want only #2", numbers(got))
	}
}

func numbers(prs []*github.PullRequest) []int {
	var out []int
	for _, pr := range prs {
		out = append(out, pr.GetNumber())
	}
	return out
}
