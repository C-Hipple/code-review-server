package database

import (
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	return db
}

// A SHA-only placeholder write (empty body) must not wipe a real diff that was
// already cached for the same head SHA. This is the cache-poisoning bug that
// caused diff cache misses: the workflow writes an empty body for PRs whose
// section doesn't need the diff, which previously overwrote the cached diff.
func TestUpsertPullRequest_EmptyBodyDoesNotClobberDiff(t *testing.T) {
	db := newTestDB(t)

	const (
		prNumber = 42
		repo     = "code-review-server"
		sha      = "abc123"
		diff     = "diff --git a/foo b/foo\n+bar\n"
	)

	if err := db.UpsertPullRequest(prNumber, repo, sha, "base", diff); err != nil {
		t.Fatalf("seed diff: %v", err)
	}

	// Simulate the workflow's SHA-only placeholder write for the same SHA.
	if err := db.UpsertPullRequest(prNumber, repo, sha, "base", ""); err != nil {
		t.Fatalf("placeholder write: %v", err)
	}

	got, gotSHA, err := db.GetPullRequest(prNumber, repo)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if got != diff {
		t.Fatalf("diff was clobbered: got %q, want %q", got, diff)
	}
	if gotSHA != sha {
		t.Fatalf("sha = %q, want %q", gotSHA, sha)
	}
}

// A non-empty body must still overwrite a previous body for the same SHA
// (e.g. a diff refetch), so the no-clobber guard only protects against empties.
func TestUpsertPullRequest_NonEmptyBodyOverwrites(t *testing.T) {
	db := newTestDB(t)

	const (
		prNumber = 7
		repo     = "code-review-server"
		sha      = "deadbeef"
	)

	if err := db.UpsertPullRequest(prNumber, repo, sha, "", "old"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.UpsertPullRequest(prNumber, repo, sha, "", "new"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	got, _, err := db.GetPullRequest(prNumber, repo)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if got != "new" {
		t.Fatalf("body = %q, want %q", got, "new")
	}
}

// When a PR gets new commits, a new row is written for the new head SHA. Reads
// must return the current (latest) SHA's row, not an arbitrary older one.
func TestGetPullRequest_ReturnsLatestSHA(t *testing.T) {
	db := newTestDB(t)

	const (
		prNumber = 99
		repo     = "code-review-server"
	)

	if err := db.UpsertPullRequest(prNumber, repo, "sha_old", "base_old", "old diff"); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := db.UpsertPullRequest(prNumber, repo, "sha_new", "base_new", "new diff"); err != nil {
		t.Fatalf("seed new: %v", err)
	}

	body, sha, err := db.GetPullRequest(prNumber, repo)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if sha != "sha_new" || body != "new diff" {
		t.Fatalf("got (%q, %q), want (%q, %q)", body, sha, "new diff", "sha_new")
	}

	headSHA, baseSHA, err := db.GetPullRequestSHAs(prNumber, repo)
	if err != nil {
		t.Fatalf("GetPullRequestSHAs: %v", err)
	}
	if headSHA != "sha_new" || baseSHA != "base_new" {
		t.Fatalf("SHAs = (%q, %q), want (%q, %q)", headSHA, baseSHA, "sha_new", "base_new")
	}
}

// Comment counts come straight out of the cached comment JSON: review comments
// and conversation comments alike, grouped by author, with nothing to count
// before the workflow has cached the PR's comments.
func TestGetPRCommentCountsByAuthor(t *testing.T) {
	db := newTestDB(t)

	const (
		prNumber = 12
		repo     = "code-review-server"
	)

	counts, err := db.GetPRCommentCountsByAuthor(prNumber, repo)
	if err != nil {
		t.Fatalf("GetPRCommentCountsByAuthor before caching: %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("counts = %v, want none before the comments are cached", counts)
	}

	// Two review comments on the code, a conversation comment (no path, as
	// the workflow converts issue comments) and one with no author at all.
	seed := `[
		{"id":1,"user":{"login":"bob"},"body":"nit","path":"main.go","diff_hunk":"@@ -1 +1 @@"},
		{"id":2,"user":{"login":"alice"},"body":"fixed","path":"main.go","in_reply_to_id":1},
		{"id":3,"user":{"login":"bob"},"body":"LGTM overall"},
		{"id":4,"body":"ghost"}
	]`
	if err := db.UpsertPRComments(prNumber, repo, seed); err != nil {
		t.Fatalf("seed comments: %v", err)
	}
	// Same number in another repo: must not leak into this PR's counts.
	if err := db.UpsertPRComments(prNumber, "other-repo", `[{"id":9,"user":{"login":"bob"}}]`); err != nil {
		t.Fatalf("seed other repo: %v", err)
	}

	counts, err = db.GetPRCommentCountsByAuthor(prNumber, repo)
	if err != nil {
		t.Fatalf("GetPRCommentCountsByAuthor: %v", err)
	}
	want := map[string]int{"bob": 2, "alice": 1, "": 1}
	if len(counts) != len(want) {
		t.Fatalf("counts = %v, want %v", counts, want)
	}
	for login, n := range want {
		if counts[login] != n {
			t.Errorf("counts[%q] = %d, want %d (all: %v)", login, counts[login], n, counts)
		}
	}

	// A PR cached with no comments — "[]", or "null" from marshalling a nil
	// slice — counts as none rather than as one "null" element.
	for _, empty := range []string{"[]", "null"} {
		if err := db.UpsertPRComments(prNumber, repo, empty); err != nil {
			t.Fatalf("cache %s: %v", empty, err)
		}
		counts, err = db.GetPRCommentCountsByAuthor(prNumber, repo)
		if err != nil {
			t.Fatalf("GetPRCommentCountsByAuthor(%s): %v", empty, err)
		}
		if len(counts) != 0 {
			t.Errorf("counts for %s = %v, want none", empty, counts)
		}
	}

	// A corrupt entry is reported rather than silently counted as zero.
	if err := db.UpsertPRComments(prNumber, repo, "{not json"); err != nil {
		t.Fatalf("cache corrupt entry: %v", err)
	}
	if _, err := db.GetPRCommentCountsByAuthor(prNumber, repo); err == nil {
		t.Error("GetPRCommentCountsByAuthor on malformed JSON: want an error, got nil")
	}
}
