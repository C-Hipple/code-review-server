package server

import (
	"testing"
)

func makeItem(owner, repo string, number int, status string) ReviewItem {
	return ReviewItem{
		Owner:  owner,
		Repo:   repo,
		Number: number,
		Status: status,
	}
}

func TestFindAdjacentPR_Next(t *testing.T) {
	items := []ReviewItem{
		makeItem("org", "alpha", 1, "TODO"),
		makeItem("org", "alpha", 2, "TODO"),
		makeItem("org", "alpha", 3, "TODO"),
	}

	target, err := findAdjacentPR(items, "org", "alpha", 1, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Number != 2 {
		t.Errorf("expected PR #2, got #%d", target.Number)
	}
}

func TestFindAdjacentPR_Previous(t *testing.T) {
	items := []ReviewItem{
		makeItem("org", "alpha", 1, "TODO"),
		makeItem("org", "alpha", 2, "TODO"),
		makeItem("org", "alpha", 3, "TODO"),
	}

	target, err := findAdjacentPR(items, "org", "alpha", 3, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Number != 2 {
		t.Errorf("expected PR #2, got #%d", target.Number)
	}
}

func TestFindAdjacentPR_NotFound(t *testing.T) {
	items := []ReviewItem{
		makeItem("org", "alpha", 1, "TODO"),
	}

	_, err := findAdjacentPR(items, "org", "alpha", 999, false)
	if err == nil {
		t.Error("expected error for missing PR, got nil")
	}
}

func TestFindAdjacentPR_NoNextAtEnd(t *testing.T) {
	items := []ReviewItem{
		makeItem("org", "alpha", 1, "TODO"),
		makeItem("org", "alpha", 2, "TODO"),
	}

	_, err := findAdjacentPR(items, "org", "alpha", 2, false)
	if err == nil {
		t.Error("expected error when no next PR available")
	}
}

func TestFindAdjacentPR_NoPreviousAtStart(t *testing.T) {
	items := []ReviewItem{
		makeItem("org", "alpha", 1, "TODO"),
		makeItem("org", "alpha", 2, "TODO"),
	}

	_, err := findAdjacentPR(items, "org", "alpha", 1, true)
	if err == nil {
		t.Error("expected error when no previous PR available")
	}
}

func TestFindAdjacentPR_SingleItem(t *testing.T) {
	items := []ReviewItem{
		makeItem("org", "alpha", 1, "TODO"),
	}

	_, errNext := findAdjacentPR(items, "org", "alpha", 1, false)
	_, errPrev := findAdjacentPR(items, "org", "alpha", 1, true)
	if errNext == nil {
		t.Error("expected error for next on single-item list")
	}
	if errPrev == nil {
		t.Error("expected error for previous on single-item list")
	}
}

func TestSortReviewItems_StatusOrder(t *testing.T) {
	items := []ReviewItem{
		makeItem("org", "repo", 1, "DONE"),    // closed → 3
		makeItem("org", "repo", 2, "WAITING"), // draft → 1
		makeItem("org", "repo", 3, "TODO"),    // open → 0
	}

	sortReviewItems(items)

	expected := []int{3, 2, 1}
	for i, want := range expected {
		if items[i].Number != want {
			t.Errorf("position %d: want #%d, got #%d", i, want, items[i].Number)
		}
	}
}

func TestSortReviewItems_RepoAlpha(t *testing.T) {
	items := []ReviewItem{
		makeItem("org", "zebra", 1, "TODO"),
		makeItem("org", "alpha", 1, "TODO"),
		makeItem("org", "mango", 1, "TODO"),
	}

	sortReviewItems(items)

	expected := []string{"alpha", "mango", "zebra"}
	for i, want := range expected {
		if items[i].Repo != want {
			t.Errorf("position %d: want repo %q, got %q", i, want, items[i].Repo)
		}
	}
}

func TestSortReviewItems_NumberWithinRepo(t *testing.T) {
	items := []ReviewItem{
		makeItem("org", "repo", 10, "TODO"),
		makeItem("org", "repo", 2, "TODO"),
		makeItem("org", "repo", 5, "TODO"),
	}

	sortReviewItems(items)

	expected := []int{2, 5, 10}
	for i, want := range expected {
		if items[i].Number != want {
			t.Errorf("position %d: want #%d, got #%d", i, want, items[i].Number)
		}
	}
}

func TestSortReviewItems_MergedBeforeClosed(t *testing.T) {
	items := []ReviewItem{
		{Owner: "org", Repo: "repo", Number: 1, Status: "DONE", Tags: ""},          // closed → 3
		{Owner: "org", Repo: "repo", Number: 2, Status: "DONE", Tags: "merged,foo"}, // merged → 2
	}

	sortReviewItems(items)

	if items[0].Number != 2 {
		t.Errorf("expected merged PR (#2) before closed PR (#1), got #%d first", items[0].Number)
	}
}

func TestSortReviewItems_StatusThenRepo(t *testing.T) {
	items := []ReviewItem{
		makeItem("org", "beta", 1, "TODO"),
		makeItem("org", "alpha", 1, "WAITING"),
		makeItem("org", "gamma", 1, "TODO"),
	}

	sortReviewItems(items)

	// TODO comes before WAITING; within TODO, alpha < beta... wait, "beta" < "gamma"
	// Expected: beta(TODO), gamma(TODO), alpha(WAITING)
	wantOrder := []struct{ repo string }{{"beta"}, {"gamma"}, {"alpha"}}
	for i, want := range wantOrder {
		if items[i].Repo != want.repo {
			t.Errorf("position %d: want repo %q, got %q", i, want.repo, items[i].Repo)
		}
	}
}

func TestFindAdjacentPR_AfterSort(t *testing.T) {
	// Simulates the real usage: sort then find adjacent
	items := []ReviewItem{
		makeItem("org", "repo", 5, "WAITING"),
		makeItem("org", "repo", 1, "TODO"),
		makeItem("org", "repo", 3, "DONE"),
	}

	sortReviewItems(items) // [#1 TODO, #5 WAITING, #3 DONE]

	// From #1 (first), next should be #5
	target, err := findAdjacentPR(items, "org", "repo", 1, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Number != 5 {
		t.Errorf("expected next PR to be #5, got #%d", target.Number)
	}

	// From #5 (middle), previous should be #1
	target, err = findAdjacentPR(items, "org", "repo", 5, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Number != 1 {
		t.Errorf("expected previous PR to be #1, got #%d", target.Number)
	}
}

func TestFindAdjacentPR_DifferentOwnerSameNumber(t *testing.T) {
	// Ensure owner is part of the match key
	items := []ReviewItem{
		makeItem("org-a", "repo", 1, "TODO"),
		makeItem("org-b", "repo", 1, "TODO"),
		makeItem("org-a", "repo", 2, "TODO"),
	}

	target, err := findAdjacentPR(items, "org-a", "repo", 1, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Next after org-a/repo#1 should be org-b/repo#1 (index 1)
	if target.Owner != "org-b" || target.Number != 1 {
		t.Errorf("expected org-b/repo#1, got %s/repo#%d", target.Owner, target.Number)
	}
}
