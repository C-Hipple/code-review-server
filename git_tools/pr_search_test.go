package git_tools

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// stubSearch swaps in a fake search (and an empty team list, so no test ever
// reaches the real API) for the duration of a test, and records the queries it
// was asked for.
func stubSearch(t *testing.T, results map[string][]PRRef, err error) *[]string {
	t.Helper()
	oldSearch, oldTeams := searchPRRefs, myTeamRefs
	t.Cleanup(func() { searchPRRefs, myTeamRefs = oldSearch, oldTeams })
	myTeamRefs = func() []string { return nil }

	queries := []string{}
	searchPRRefs = func(query string) ([]PRRef, error) {
		queries = append(queries, query)
		if err != nil {
			return nil, err
		}
		return results[query], nil
	}
	return &queries
}

// Each test needs its own login, since the ref searches are cached globally by
// login for a couple of minutes.
func testLogin(t *testing.T) string {
	t.Helper()
	return "user-" + strings.ReplaceAll(t.Name(), "/", "-")
}

func TestReviewRequestedQueriesCoverDirectAndTeamRequests(t *testing.T) {
	queries := ReviewRequestedQueries("octocat", []string{"acme/reviewers", "acme/platform"})

	if len(queries) != 3 {
		t.Fatalf("expected one direct query plus one per team, got %v", queries)
	}
	if !strings.Contains(queries[0], "review-requested:octocat") {
		t.Errorf("first query should be the direct request search, got %q", queries[0])
	}
	if !strings.Contains(queries[1], "team-review-requested:acme/reviewers") ||
		!strings.Contains(queries[2], "team-review-requested:acme/platform") {
		t.Errorf("team queries missing: %v", queries)
	}
	for _, q := range queries {
		for _, qualifier := range []string{"is:pr", "is:open", "archived:false"} {
			if !strings.Contains(q, qualifier) {
				t.Errorf("query %q is missing %q", q, qualifier)
			}
		}
	}
}

func TestReviewRequestedQueriesWithoutTeams(t *testing.T) {
	queries := ReviewRequestedQueries("octocat", nil)
	if len(queries) != 1 {
		t.Fatalf("expected just the direct request search, got %v", queries)
	}
}

// Someone in dozens of teams would otherwise spend the whole search rate limit
// on this one workflow.
func TestReviewRequestedQueriesCapTeamSearches(t *testing.T) {
	teams := make([]string, 25)
	for i := range teams {
		teams[i] = fmt.Sprintf("acme/team-%d", i)
	}
	queries := ReviewRequestedQueries("octocat", teams)
	if len(queries) != maxTeamSearches+1 {
		t.Errorf("expected %d queries (direct + capped teams), got %d", maxTeamSearches+1, len(queries))
	}
}

func TestReviewRequestedRefsUnionsAndDedupes(t *testing.T) {
	login := testLogin(t)
	shared := PRRef{Owner: "acme", Repo: "api", Number: 7}
	direct := fmt.Sprintf("is:pr is:open archived:false review-requested:%s", login)
	stubSearch(t, map[string][]PRRef{
		direct: {shared, {Owner: "acme", Repo: "web", Number: 3}},
	}, nil)

	refs, err := ReviewRequestedRefs(login)
	if err != nil {
		t.Fatalf("ReviewRequestedRefs: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %v", refs)
	}
	if refs[0] != shared {
		t.Errorf("expected search order to be preserved, got %v", refs)
	}
}

func TestReviewRequestedRefsFailsLoudly(t *testing.T) {
	login := testLogin(t)
	stubSearch(t, nil, errors.New("rate limited"))

	if _, err := ReviewRequestedRefs(login); err == nil {
		t.Fatal("a failed search must be an error: an empty candidate list would empty the section")
	}

	// The failure must not be cached, or one bad minute would silence the
	// section for several cycles.
	stubSearch(t, map[string][]PRRef{
		fmt.Sprintf("is:pr is:open archived:false review-requested:%s", login): {{Owner: "a", Repo: "b", Number: 1}},
	}, nil)
	refs, err := ReviewRequestedRefs(login)
	if err != nil {
		t.Fatalf("retry after a failure: %v", err)
	}
	if len(refs) != 1 {
		t.Errorf("expected the retry to return results, got %v", refs)
	}
}

func TestRefSearchesAreCachedPerLogin(t *testing.T) {
	login := testLogin(t)
	queries := stubSearch(t, map[string][]PRRef{
		fmt.Sprintf("is:pr is:open archived:false review-requested:%s", login): {{Owner: "a", Repo: "b", Number: 1}},
	}, nil)

	for i := 0; i < 3; i++ {
		if _, err := ReviewRequestedRefs(login); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(*queries) != 1 {
		t.Errorf("expected the searches to be shared between calls in a cycle, ran %d", len(*queries))
	}
}

func TestMyInteractionRefsSearchesReviewsAndComments(t *testing.T) {
	login := testLogin(t)
	reviewed := fmt.Sprintf("is:pr is:open reviewed-by:%s", login)
	commented := fmt.Sprintf("is:pr is:open commenter:%s", login)
	queries := stubSearch(t, map[string][]PRRef{
		reviewed:  {{Owner: "acme", Repo: "api", Number: 1}},
		commented: {{Owner: "acme", Repo: "api", Number: 1}, {Owner: "acme", Repo: "api", Number: 2}},
	}, nil)

	refs, err := MyInteractionRefs(login)
	if err != nil {
		t.Fatalf("MyInteractionRefs: %v", err)
	}
	if len(*queries) != 2 {
		t.Errorf("expected both interaction searches, got %v", *queries)
	}
	if len(refs) != 2 {
		t.Errorf("expected the overlap to be deduped, got %v", refs)
	}
}

func TestRefSearchesRequireALogin(t *testing.T) {
	if _, err := ReviewRequestedRefs(""); err == nil {
		t.Error("expected an error without a login")
	}
	if _, err := MyInteractionRefs(""); err == nil {
		t.Error("expected an error without a login")
	}
}

func TestDedupeRefsKeepsFirstSeenOrder(t *testing.T) {
	a := PRRef{Owner: "acme", Repo: "api", Number: 1}
	b := PRRef{Owner: "acme", Repo: "api", Number: 2}
	c := PRRef{Owner: "other", Repo: "api", Number: 1}

	got := DedupeRefs([]PRRef{a, b, a, c, b})
	want := []PRRef{a, b, c}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestPRRefString(t *testing.T) {
	ref := PRRef{Owner: "acme", Repo: "api", Number: 42}
	if ref.String() != "acme/api#42" {
		t.Errorf("String() = %q", ref.String())
	}
}
