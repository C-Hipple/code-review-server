package workflows

import (
	"crs/database"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

func warmTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	return db
}

func prWithHeadSHA(sha string) *github.PullRequest {
	return &github.PullRequest{Head: &github.PullRequestBranch{SHA: github.String(sha)}}
}

func TestPRNeedsCacheWarm(t *testing.T) {
	const (
		prNumber = 100
		repo     = "code-review-server"
	)
	key := PRKey{Owner: "owner", Repo: repo, Number: prNumber}

	t.Run("brand new PR warms", func(t *testing.T) {
		db := warmTestDB(t)
		if !prNeedsCacheWarm(db, key, prWithHeadSHA("sha1")) {
			t.Fatal("expected warm for a PR with nothing cached")
		}
	})

	t.Run("head SHA changed warms", func(t *testing.T) {
		db := warmTestDB(t)
		if err := db.UpsertPullRequest(prNumber, repo, "sha_old", "", "cached diff"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if !prNeedsCacheWarm(db, key, prWithHeadSHA("sha_new")) {
			t.Fatal("expected warm when head SHA changed")
		}
	})

	t.Run("placeholder row (empty diff) warms", func(t *testing.T) {
		db := warmTestDB(t)
		if err := db.UpsertPullRequest(prNumber, repo, "sha1", "", ""); err != nil {
			t.Fatalf("seed placeholder: %v", err)
		}
		if !prNeedsCacheWarm(db, key, prWithHeadSHA("sha1")) {
			t.Fatal("expected warm when only a SHA-only placeholder is cached")
		}
	})

	t.Run("unchanged PR with cached diff does not warm", func(t *testing.T) {
		db := warmTestDB(t)
		if err := db.UpsertPullRequest(prNumber, repo, "sha1", "", "cached diff"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if prNeedsCacheWarm(db, key, prWithHeadSHA("sha1")) {
			t.Fatal("did not expect warm when current-SHA diff is already cached")
		}
	})

	t.Run("nil head SHA does not warm", func(t *testing.T) {
		db := warmTestDB(t)
		if prNeedsCacheWarm(db, key, &github.PullRequest{}) {
			t.Fatal("did not expect warm for a PR with no head SHA")
		}
	})
}

func TestApplyCacheWarmRequirements(t *testing.T) {
	const (
		prNumber = 100
		repo     = "code-review-server"
	)
	key := PRKey{Owner: "owner", Repo: repo, Number: prNumber}

	t.Run("warms every field GetPRDetails reads", func(t *testing.T) {
		db := warmTestDB(t)
		prObjects := map[PRKey]*github.PullRequest{key: prWithHeadSHA("sha1")}
		reqs := map[PRKey]AuxDataRequirement{}

		applyCacheWarmRequirements(db, prObjects, reqs)

		got := reqs[key]
		// Reviews and comments especially: a draft or closed PR gets no other
		// chance at a PRReviews row, and that shows up as a "reviews" miss.
		// ReviewThreads has no filter that would ever request it, so the warm is
		// its only chance to land before someone opens the review.
		want := AuxDataRequirement{
			Diff: true, Commits: true, Reviews: true, Comments: true, ReviewThreads: true,
		}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("preserves requirements the filters already asked for", func(t *testing.T) {
		db := warmTestDB(t)
		prObjects := map[PRKey]*github.PullRequest{key: prWithHeadSHA("sha1")}
		reqs := map[PRKey]AuxDataRequirement{key: {CIStatus: true}}

		applyCacheWarmRequirements(db, prObjects, reqs)

		if !reqs[key].CIStatus {
			t.Error("cache warm cleared a requirement set by the workflow's filters")
		}
	})

	t.Run("leaves an unchanged PR alone", func(t *testing.T) {
		db := warmTestDB(t)
		if err := db.UpsertPullRequest(prNumber, repo, "sha1", "", "cached diff"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		prObjects := map[PRKey]*github.PullRequest{key: prWithHeadSHA("sha1")}
		reqs := map[PRKey]AuxDataRequirement{}

		applyCacheWarmRequirements(db, prObjects, reqs)

		if got := reqs[key]; got != (AuxDataRequirement{}) {
			t.Errorf("unchanged PR should not be warmed, got %+v", got)
		}
	})

	t.Run("reports only the warmed PRs", func(t *testing.T) {
		// The returned keys drive the post-update hooks, so an unchanged PR
		// appearing here would rerun plugins for a revision already handled.
		db := warmTestDB(t)
		unchanged := PRKey{Owner: "owner", Repo: repo, Number: 200}
		if err := db.UpsertPullRequest(unchanged.Number, repo, "sha1", "", "cached diff"); err != nil {
			t.Fatalf("seed: %v", err)
		}
		prObjects := map[PRKey]*github.PullRequest{
			key:       prWithHeadSHA("sha1"),
			unchanged: prWithHeadSHA("sha1"),
		}

		warmed := applyCacheWarmRequirements(db, prObjects, map[PRKey]AuxDataRequirement{})

		if len(warmed) != 1 || warmed[0] != key {
			t.Errorf("got warmed %+v, want only %+v", warmed, key)
		}
	})
}

func TestNotifyPRsUpdated(t *testing.T) {
	key := PRKey{Owner: "owner", Repo: "code-review-server", Number: 100}

	t.Run("fires the hook once per warmed PR", func(t *testing.T) {
		var mu sync.Mutex
		var got []PRKey
		done := make(chan struct{}, 2)

		SetPRUpdatedHook(func(owner, repo string, number int) {
			mu.Lock()
			got = append(got, PRKey{Owner: owner, Repo: repo, Number: number})
			mu.Unlock()
			done <- struct{}{}
		})
		t.Cleanup(func() { SetPRUpdatedHook(nil) })

		other := PRKey{Owner: "owner", Repo: "code-review-server", Number: 200}
		notifyPRsUpdated([]PRKey{key, other})

		// The hooks run in their own goroutines; wait for both.
		for i := 0; i < 2; i++ {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for the PR-updated hooks to fire")
			}
		}

		mu.Lock()
		defer mu.Unlock()
		if len(got) != 2 {
			t.Fatalf("got %d hook calls, want 2", len(got))
		}
		if !slices.Contains(got, key) || !slices.Contains(got, other) {
			t.Errorf("hook called with %+v, want both %+v and %+v", got, key, other)
		}
	})

	t.Run("no hook registered is a no-op", func(t *testing.T) {
		SetPRUpdatedHook(nil)
		notifyPRsUpdated([]PRKey{key})
	})
}
