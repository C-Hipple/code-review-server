package workflows

import (
	"crs/config"
	"crs/git_tools"
	"encoding/json"
	"testing"

	"github.com/google/go-github/v74/github"
)

// teamReviewTestConfig points the workflow layer at a scratch DB and gives it an
// identity, with no GitHub token: every lookup that would call GitHub (team
// membership, the user's own teams, the request history) reports "unknown"
// instead, which is exactly the path a cached PR takes during a cycle.
func teamReviewTestConfig(t *testing.T) config.Config {
	t.Helper()
	t.Setenv("CRS_GITHUB_TOKEN", "")
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db, GithubUsername: "erin"})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	return config.C()
}

func teamRequest(name, slug, org string) *github.Team {
	return &github.Team{
		Name:         github.String(name),
		Slug:         github.String(slug),
		Organization: &github.Organization{Login: github.String(org)},
	}
}

func TestResolvePRTeamReviewsUnionsCachedAndPendingTeams(t *testing.T) {
	cfg := teamReviewTestConfig(t)
	key := PRKey{Owner: "acme", Repo: "widgets", Number: 12}

	// A previous cycle recorded two teams; one of them has since been cleared
	// by GitHub because a member reviewed, so it is no longer pending.
	cached := []git_tools.TeamReviewStatus{
		{Name: "Platform", Slug: "platform", Org: "acme", Status: git_tools.TeamReviewPending},
		{Name: "Security", Slug: "security", Org: "acme", Status: git_tools.TeamReviewPending},
	}
	seed, err := json.Marshal(cached)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := cfg.DB.UpsertPRTeamReviews(key.Number, key.Repo, string(seed)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	pr := &github.PullRequest{
		RequestedTeams: []*github.Team{
			teamRequest("Platform", "platform", "acme"),
			// Requested since the last cycle: no history call needed to see it.
			teamRequest("Docs", "docs", "acme"),
		},
	}

	statuses := resolvePRTeamReviews(&apiCallCounter{}, key, pr, nil)

	got := map[string]string{}
	for _, status := range statuses {
		got[status.Name] = status.Status
	}
	want := map[string]string{
		"Platform": git_tools.TeamReviewPending,
		"Docs":     git_tools.TeamReviewPending,
		// Carried over from the cache and no longer awaiting review: without a
		// readable member list we can say it was reviewed, not by whom.
		"Security": git_tools.TeamReviewReviewed,
	}
	if len(got) != len(want) {
		t.Fatalf("statuses = %+v, want %v", statuses, want)
	}
	for name, status := range want {
		if got[name] != status {
			t.Errorf("%s = %q, want %q", name, got[name], status)
		}
	}
}

func TestResolvePRTeamReviewsKeepsPersonalRequestAfterItIsCleared(t *testing.T) {
	cfg := teamReviewTestConfig(t)
	key := PRKey{Owner: "acme", Repo: "widgets", Number: 13}

	seed, err := json.Marshal([]git_tools.TeamReviewStatus{
		{Name: "erin", Status: git_tools.TeamReviewPending, Mine: true, Personal: true},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := cfg.DB.UpsertPRTeamReviews(key.Number, key.Repo, string(seed)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// GitHub drops the reviewer from requested_reviewers once they review, so
	// the entry only survives because the cache remembered it.
	statuses := resolvePRTeamReviews(&apiCallCounter{}, key, &github.PullRequest{}, []*github.PullRequestReview{
		{User: &github.User{Login: github.String("erin")}, State: github.String("APPROVED")},
	})

	if len(statuses) != 1 {
		t.Fatalf("statuses = %+v, want the personal entry", statuses)
	}
	if !statuses[0].Personal || statuses[0].Status != git_tools.TeamReviewApproved {
		t.Errorf("personal entry = %+v, want an approved personal entry", statuses[0])
	}
}

func TestResolvePRTeamReviewsKeepsAnEmptyAnswerEmpty(t *testing.T) {
	cfg := teamReviewTestConfig(t)
	key := PRKey{Owner: "acme", Repo: "widgets", Number: 14}

	// A previous cycle already established this PR needs no team review. The
	// answer must come back as an empty non-nil slice: persisting "[]" is what
	// stops every later cycle asking GitHub for the history all over again.
	if err := cfg.DB.UpsertPRTeamReviews(key.Number, key.Repo, "[]"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	statuses := resolvePRTeamReviews(&apiCallCounter{}, key, &github.PullRequest{}, nil)
	if statuses == nil || len(statuses) != 0 {
		t.Fatalf("statuses = %+v, want an empty non-nil slice", statuses)
	}
}

func TestResolvePRTeamReviewsLeavesNothingCachedWhenTheHistoryFails(t *testing.T) {
	teamReviewTestConfig(t)
	key := PRKey{Owner: "acme", Repo: "widgets", Number: 17}

	// No token, so the first-sight history call fails. Storing the teams still
	// listed on the PR would look like a complete answer and stop the retry,
	// leaving out every team whose review GitHub had already cleared.
	pr := &github.PullRequest{RequestedTeams: []*github.Team{teamRequest("Platform", "platform", "acme")}}
	if statuses := resolvePRTeamReviews(&apiCallCounter{}, key, pr, nil); statuses != nil {
		t.Fatalf("statuses = %+v, want nil so the next cycle tries again", statuses)
	}
}

func TestResolvePRTeamReviewsSkipsPRsItCannotRead(t *testing.T) {
	teamReviewTestConfig(t)
	key := PRKey{Owner: "acme", Repo: "widgets", Number: 15}

	if statuses := resolvePRTeamReviews(&apiCallCounter{}, key, nil, nil); statuses != nil {
		t.Fatalf("statuses = %+v, want nil so the cached row is left alone", statuses)
	}
}

func TestPersistPRCacheDataStoresTeamReviews(t *testing.T) {
	cfg := teamReviewTestConfig(t)
	key := PRKey{Owner: "acme", Repo: "widgets", Number: 16}

	auxData := &PRAuxData{
		HeadSHA: "abc123",
		TeamReviews: []git_tools.TeamReviewStatus{
			{Name: "Platform", Slug: "platform", Org: "acme", Status: git_tools.TeamReviewApproved, Mine: true},
		},
	}
	persistPRCacheData("test-workflow", key, nil, auxData, nil, nil, nil, nil, nil)

	raw, err := cfg.DB.GetPRTeamReviews(key.Number, key.Repo)
	if err != nil {
		t.Fatalf("GetPRTeamReviews: %v", err)
	}
	var stored []git_tools.TeamReviewStatus
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("stored row is not decodable: %v (%q)", err, raw)
	}
	if len(stored) != 1 || stored[0].Slug != "platform" || !stored[0].Mine {
		t.Errorf("stored = %+v, want the resolved platform entry", stored)
	}
}
