package workflows

import (
	"crs/database"
	"path/filepath"
	"testing"

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
