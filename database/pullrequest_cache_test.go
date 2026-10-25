package database

import (
	"path/filepath"
	"testing"
)

// testOwner owns the repositories the PR cache tests cache PRs of.
const testOwner = "C-Hipple"

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

	if err := db.UpsertPullRequest(testOwner, repo, prNumber, sha, "base", diff); err != nil {
		t.Fatalf("seed diff: %v", err)
	}

	// Simulate the workflow's SHA-only placeholder write for the same SHA.
	if err := db.UpsertPullRequest(testOwner, repo, prNumber, sha, "base", ""); err != nil {
		t.Fatalf("placeholder write: %v", err)
	}

	got, gotSHA, err := db.GetPullRequest(testOwner, repo, prNumber)
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

	if err := db.UpsertPullRequest(testOwner, repo, prNumber, sha, "", "old"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.UpsertPullRequest(testOwner, repo, prNumber, sha, "", "new"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	got, _, err := db.GetPullRequest(testOwner, repo, prNumber)
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

	if err := db.UpsertPullRequest(testOwner, repo, prNumber, "sha_old", "base_old", "old diff"); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := db.UpsertPullRequest(testOwner, repo, prNumber, "sha_new", "base_new", "new diff"); err != nil {
		t.Fatalf("seed new: %v", err)
	}

	body, sha, err := db.GetPullRequest(testOwner, repo, prNumber)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if sha != "sha_new" || body != "new diff" {
		t.Fatalf("got (%q, %q), want (%q, %q)", body, sha, "new diff", "sha_new")
	}

	headSHA, baseSHA, err := db.GetPullRequestSHAs(testOwner, repo, prNumber)
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

	counts, err := db.GetPRCommentCountsByAuthor(testOwner, repo, prNumber)
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
	if err := db.UpsertPRComments(testOwner, repo, prNumber, seed); err != nil {
		t.Fatalf("seed comments: %v", err)
	}
	// Same number in another repo: must not leak into this PR's counts.
	if err := db.UpsertPRComments(testOwner, "other-repo", prNumber, `[{"id":9,"user":{"login":"bob"}}]`); err != nil {
		t.Fatalf("seed other repo: %v", err)
	}

	counts, err = db.GetPRCommentCountsByAuthor(testOwner, repo, prNumber)
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
		if err := db.UpsertPRComments(testOwner, repo, prNumber, empty); err != nil {
			t.Fatalf("cache %s: %v", empty, err)
		}
		counts, err = db.GetPRCommentCountsByAuthor(testOwner, repo, prNumber)
		if err != nil {
			t.Fatalf("GetPRCommentCountsByAuthor(%s): %v", empty, err)
		}
		if len(counts) != 0 {
			t.Errorf("counts for %s = %v, want none", empty, counts)
		}
	}

	// A corrupt entry is reported rather than silently counted as zero.
	if err := db.UpsertPRComments(testOwner, repo, prNumber, "{not json"); err != nil {
		t.Fatalf("cache corrupt entry: %v", err)
	}
	if _, err := db.GetPRCommentCountsByAuthor(testOwner, repo, prNumber); err == nil {
		t.Error("GetPRCommentCountsByAuthor on malformed JSON: want an error, got nil")
	}
}

// GitHub answers "unknown" while it rechecks a PR's mergeability, which it does
// after every push and every move of the base branch. An unknown for the head
// already recorded must keep the answer recorded for it, or a conflict would
// vanish from the review list each time anyone merged elsewhere; an unknown for
// a new head must not, since that answer was about other code.
func TestRecordPRMergeabilityKeepsTheAnswerForTheSameHead(t *testing.T) {
	db := newTestDB(t)

	const (
		prNumber = 12
		repo     = "code-review-server"
	)
	check := func(step, wantState, wantSHA string) {
		t.Helper()
		state, sha, err := db.GetPRMergeability(testOwner, repo, prNumber)
		if err != nil {
			t.Fatalf("%s: GetPRMergeability: %v", step, err)
		}
		if state != wantState || sha != wantSHA {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", step, state, sha, wantState, wantSHA)
		}
	}
	record := func(sha, state string) {
		t.Helper()
		if err := db.RecordPRMergeability(testOwner, repo, prNumber, sha, state); err != nil {
			t.Fatalf("RecordPRMergeability(%q, %q): %v", sha, state, err)
		}
	}

	check("nothing recorded", "", "")

	record("sha1", "conflicting")
	check("first answer", "conflicting", "sha1")

	// The base branch moved; GitHub is rechecking the same head.
	record("sha1", "unknown")
	check("unknown for the same head", "conflicting", "sha1")

	// A real answer always replaces the recorded one.
	record("sha1", "mergeable")
	check("new answer for the same head", "mergeable", "sha1")

	// A push: GitHub hasn't checked the new head yet.
	record("sha2", "unknown")
	check("unknown for a new head", "unknown", "sha2")

	record("sha2", "conflicting")
	check("answer for the new head", "conflicting", "sha2")

	// Same number in another repo is another PR.
	if err := db.RecordPRMergeability(testOwner, "other-repo", prNumber, "sha9", "mergeable"); err != nil {
		t.Fatalf("record other repo: %v", err)
	}
	check("after another repo's PR", "conflicting", "sha2")
}

// Two owners' repositories can share a name, and the PR caches keep their PRs
// of the same number apart: neither one's diff, head or comments is served for
// the other's, however the writes interleave.
func TestPRCachesKeepOwnersApart(t *testing.T) {
	db := newTestDB(t)
	upsert := func(owner, sha, body string) {
		t.Helper()
		if err := db.UpsertPullRequest(owner, "api", 5, sha, "base", body); err != nil {
			t.Fatal(err)
		}
	}
	// orgA's PR, orgB's of the same name and number, then orgA's again, as
	// the next workflow cycle caches it.
	upsert("orgA", "real-sha", "real diff")
	upsert("orgB", "other-sha", "other diff")
	upsert("orgA", "real-sha", "real diff")
	for owner, want := range map[string][2]string{"orgA": {"real diff", "real-sha"}, "orgB": {"other diff", "other-sha"}} {
		body, sha, err := db.GetPullRequest(owner, "api", 5)
		if err != nil || body != want[0] || sha != want[1] {
			t.Errorf("GetPullRequest(%s) = %q, %q, %v; want %q, %q", owner, body, sha, err, want[0], want[1])
		}
		if head, _, _ := db.GetPullRequestSHAs(owner, "api", 5); head != want[1] {
			t.Errorf("GetPullRequestSHAs(%s) head = %q, want %q", owner, head, want[1])
		}
	}

	for owner, comments := range map[string]string{"orgA": `[{"id":1}]`, "orgB": `[{"id":2}]`} {
		if err := db.UpsertPRComments(owner, "api", 5, comments); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := db.GetPRComments("orgA", "api", 5); got != `[{"id":1}]` {
		t.Errorf("orgA's comments = %s", got)
	}
	if err := db.RecordPRMergeability("orgB", "api", 5, "other-sha", "conflicting"); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := db.GetPRMergeability("orgA", "api", 5); state != "" {
		t.Errorf("orgA's PR has orgB's mergeability %q", state)
	}
}

// Before the PR caches were keyed by owner, a row named only the repository.
// The migration files each under its PR's owner where the database knows it
// from the caches that were always keyed by owner, and drops the rest — a row
// of a repository name two owners share included: whose it is cannot be
// told, and fetching it again beats serving one owner's PR for another's.
func TestPRCacheOwnerMigration(t *testing.T) {
	path := writePreUserDB(t,
		`CREATE TABLE PullRequests (pr_number INTEGER NOT NULL, repo TEXT NOT NULL, latest_sha TEXT NOT NULL,
			body TEXT NOT NULL, base_sha TEXT DEFAULT '', UNIQUE(pr_number, repo, latest_sha))`,
		`CREATE TABLE PRComments (pr_number INTEGER NOT NULL, repo TEXT NOT NULL, comments_json TEXT NOT NULL,
			UNIQUE(pr_number, repo))`,
		`CREATE TABLE PRMergeability (pr_number INTEGER NOT NULL, repo TEXT NOT NULL, head_sha TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT '', UNIQUE(pr_number, repo))`,
		`CREATE TABLE PRMetadataCache (pr_number INTEGER NOT NULL, repo TEXT NOT NULL, owner TEXT NOT NULL,
			metadata_json TEXT NOT NULL, cached_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, UNIQUE(pr_number, repo, owner))`,
		`INSERT INTO PRMetadataCache (pr_number, repo, owner, metadata_json) VALUES
			(7, 'widgets', 'acme', '{}'), (5, 'api', 'orgA', '{}'), (5, 'api', 'orgB', '{}')`,
		`INSERT INTO PullRequests (pr_number, repo, latest_sha, body, base_sha) VALUES
			(7, 'widgets', 'sha7', 'diff7', NULL), (5, 'api', 'sha5', 'diff5', ''), (9, 'nobody', 'sha9', 'diff9', '')`,
		`INSERT INTO PRComments VALUES (7, 'widgets', '[1]'), (5, 'api', '[2]')`,
		`INSERT INTO PRMergeability VALUES (7, 'widgets', 'sha7', 'conflicting')`,
	)
	db := openDB(t, path)

	if body, sha, _ := db.GetPullRequest("acme", "widgets", 7); body != "diff7" || sha != "sha7" {
		t.Errorf("acme/widgets#7 = %q at %q, want its diff kept", body, sha)
	}
	if comments, _ := db.GetPRComments("acme", "widgets", 7); comments != "[1]" {
		t.Errorf("acme/widgets#7 comments = %q", comments)
	}
	if state, sha, _ := db.GetPRMergeability("acme", "widgets", 7); state != "conflicting" || sha != "sha7" {
		t.Errorf("acme/widgets#7 mergeability = %q at %q", state, sha)
	}
	for _, table := range []string{"PullRequests", "PRComments"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s holds %d rows after the migration, want only acme/widgets#7's", table, n)
		}
	}

	// And every PR cache is the table a fresh database gets.
	fresh := newTestDB(t)
	for _, table := range prCacheTables {
		if got, want := tableShape(t, db, table.name), tableShape(t, fresh, table.name); got != want {
			t.Errorf("%s after migration:\n%s\nwant (fresh):\n%s", table.name, got, want)
		}
	}
}
