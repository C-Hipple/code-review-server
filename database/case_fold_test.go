package database

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// A PR's owner and repository reach the caches spelled more than one way — a
// cycle uses the config's spelling, a webhook delivery and a client GitHub's —
// and every spelling reads and writes the same rows.
func TestPRKeysIgnoreCase(t *testing.T) {
	db := newTestDB(t)
	const n = 7
	writer, readers := [2]string{"Acme", "Widgets"}, [][2]string{{"acme", "widgets"}, {"ACME", "wIdGeTs"}}

	for _, c := range []struct {
		table string
		write func(owner, repo string) error
		read  func(owner, repo string) (string, error)
	}{
		{"PullRequests", func(o, r string) error { return db.UpsertPullRequest(o, r, n, "sha", "base", "diff") },
			func(o, r string) (string, error) { body, _, err := db.GetPullRequest(o, r, n); return body, err }},
		{"PRComments", func(o, r string) error { return db.UpsertPRComments(o, r, n, `[{"id":1}]`) },
			func(o, r string) (string, error) { return db.GetPRComments(o, r, n) }},
		{"RequestedReviewers", func(o, r string) error { return db.UpsertRequestedReviewers(o, r, n, `["me"]`) },
			func(o, r string) (string, error) { return db.GetRequestedReviewers(o, r, n) }},
		{"PRReviews", func(o, r string) error { return db.UpsertPRReviews(o, r, n, `[{"id":2}]`) },
			func(o, r string) (string, error) { return db.GetPRReviews(o, r, n) }},
		{"PRCommits", func(o, r string) error { return db.UpsertPRCommits(o, r, n, `[{"sha":"c"}]`) },
			func(o, r string) (string, error) { return db.GetPRCommits(o, r, n) }},
		{"PRReviewThreads", func(o, r string) error { return db.UpsertPRReviewThreads(o, r, n, `[{"id":"T"}]`) },
			func(o, r string) (string, error) { return db.GetPRReviewThreads(o, r, n) }},
		{"PRReactions", func(o, r string) error { return db.UpsertPRReactions(o, r, n, `{"1":[]}`) },
			func(o, r string) (string, error) { return db.GetPRReactions(o, r, n) }},
		{"PRTeamReviews", func(o, r string) error { return db.UpsertPRTeamReviews(o, r, n, `[]`) },
			func(o, r string) (string, error) { return db.GetPRTeamReviews(o, r, n) }},
		{"PRMergeability", func(o, r string) error { return db.RecordPRMergeability(o, r, n, "sha", "conflicting") },
			func(o, r string) (string, error) { state, _, err := db.GetPRMergeability(o, r, n); return state, err }},
		{"CIStatus", func(o, r string) error { return db.UpsertCIStatus(o, r, n, "sha", `{"state":"success"}`) },
			func(o, r string) (string, error) { return db.GetCIStatus(o, r, n, "sha") }},
		{"PRMetadataCache", func(o, r string) error { return db.UpsertPRMetadataCache(o, r, n, `{"number":7}`) },
			func(o, r string) (string, error) { return db.GetPRMetadataCache(o, r, n) }},
		{"PluginResults", func(o, r string) error { return db.UpsertPluginResult(o, r, n, "lint", "ok", "success", "sha") },
			func(o, r string) (string, error) { sha, err := db.GetPluginResultSHA(o, r, n, "lint"); return sha, err }},
		{"AIResults", func(o, r string) error { return db.UpsertAIResult(o, r, n, "ease", "easy", "success", "sha", "h") },
			func(o, r string) (string, error) { res, _, err := db.GetAIResult(o, r, n, "ease"); return res.SHA, err }},
		{"Worktrees", func(o, r string) error { return db.AddWorktree(n, r, o, "/tmp/wt", "feature") },
			func(o, r string) (string, error) { return db.GetWorktree(n, r, o) }},
		{"WorkflowActionLog", func(o, r string) error {
			_, err := db.LogWorkflowAction(WorkflowAction{WorkflowName: "wf", Action: WorkflowActionCacheWrite, Owner: o, Repo: r, PRNumber: n, SHA: "sha"})
			return err
		}, func(o, r string) (string, error) {
			action, err := db.GetLatestWorkflowAction(o, r, n)
			if action == nil {
				return "", err
			}
			return action.SHA, err
		}},
	} {
		if err := c.write(writer[0], writer[1]); err != nil {
			t.Fatalf("%s: write: %v", c.table, err)
		}
		want, err := c.read(writer[0], writer[1])
		if err != nil || want == "" {
			t.Fatalf("%s: reading back what was written: %q, %v", c.table, want, err)
		}
		for _, r := range readers {
			if got, err := c.read(r[0], r[1]); err != nil || got != want {
				t.Errorf("%s read as %s/%s = %q, %v; want %q", c.table, r[0], r[1], got, err, want)
			}
		}
		// Writing under another spelling is the same row, not a second one.
		if err := c.write(readers[0][0], readers[0][1]); err != nil {
			t.Fatalf("%s: second write: %v", c.table, err)
		}
		var rows, unfolded int
		if err := db.QueryRow("SELECT COUNT(*), COUNT(CASE WHEN owner != lower(owner) OR repo != lower(repo) THEN 1 END) FROM "+c.table).Scan(&rows, &unfolded); err != nil {
			t.Fatal(err)
		}
		if want := 1; c.table == "WorkflowActionLog" {
			want = 2 // a log, one row per action
			if rows != want {
				t.Errorf("%s holds %d rows, want %d", c.table, rows, want)
			}
		} else if rows != want {
			t.Errorf("%s holds %d rows after writes under two spellings, want one", c.table, rows)
		}
		if unfolded != 0 {
			t.Errorf("%s stored %d keys unfolded", c.table, unfolded)
		}
	}

	// Lookups by head SHA fold too: a commit status names the repository as
	// GitHub spells it.
	if got, _ := db.FindPRNumbersBySHA("ACME", "widgets", "sha"); len(got) != 1 || got[0] != n {
		t.Errorf("FindPRNumbersBySHA = %v", got)
	}
	if head, _, _ := db.GetPullRequestSHAs("acme", "WIDGETS", n); head != "sha" {
		t.Errorf("GetPullRequestSHAs head = %q", head)
	}
	if counts, _ := db.GetPRCommentCountsByAuthor("acme", "widgets", n); counts[""] != 1 {
		t.Errorf("GetPRCommentCountsByAuthor = %v", counts)
	}
	if results, _ := db.GetPluginResults("aCmE", "widgets", n); results["lint"].Result != "ok" {
		t.Errorf("GetPluginResults = %v", results)
	}
	// Deletes find the rows whatever the spelling.
	if err := db.DeletePRComments("ACME", "WIDGETS", n); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetPRComments("Acme", "Widgets", n); got != "" {
		t.Errorf("comments survived a delete under another spelling: %q", got)
	}
	if deleted, err := db.DeleteWorkflowActionsForPR("ACME", "Widgets", n); err != nil || deleted != 2 {
		t.Errorf("DeleteWorkflowActionsForPR = %d, %v; want 2", deleted, err)
	}
}

// reopen closes db and opens its file again, running the migrations.
func reopen(t *testing.T, db *DB, path string) *DB {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return openDB(t, path)
}

// A database written before keys were folded holds a PR under each spelling
// it was written with. Opening it keeps one row per PR — the newest, or for
// a diff the one that holds it — under the folded key, and leaves every other
// row alone.
func TestFoldPRKeyCaseMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unfolded.db")
	db := openDB(t, path)
	// Raw writes, as the accessors before folding made them: the cycle under
	// the config's "acme", a client or a delivery under GitHub's "Acme".
	for _, stmt := range []string{
		// PR 1's head moved: the cycle cached head1, then a client read head2
		// under GitHub's spelling, later.
		`INSERT INTO PullRequests (owner, repo, pr_number, latest_sha, base_sha, body) VALUES ('acme', 'widgets', 1, 'head1', '', 'diff1')`,
		`INSERT INTO PullRequests (owner, repo, pr_number, latest_sha, base_sha, body) VALUES ('Acme', 'Widgets', 1, 'head2', '', 'diff2')`,
		// PR 2's head is cached under both spellings, but only one holds its
		// diff: the other is a SHA-only placeholder written after it.
		`INSERT INTO PullRequests (owner, repo, pr_number, latest_sha, base_sha, body) VALUES ('Acme', 'widgets', 2, 'head3', '', 'diff3')`,
		`INSERT INTO PullRequests (owner, repo, pr_number, latest_sha, base_sha, body) VALUES ('acme', 'widgets', 2, 'head3', '', '')`,
		// One row per PR otherwise: the one inserted last wins.
		`INSERT INTO PRComments (owner, repo, pr_number, comments_json) VALUES ('acme', 'widgets', 1, 'older')`,
		`INSERT INTO PRComments (owner, repo, pr_number, comments_json) VALUES ('Acme', 'widgets', 1, 'newer')`,
		`INSERT INTO CIStatus (owner, repo, pr_number, sha, status_json) VALUES ('Acme', 'widgets', 1, 'head2', 'ci-newer-sha')`,
		`INSERT INTO CIStatus (owner, repo, pr_number, sha, status_json) VALUES ('acme', 'widgets', 1, 'head1', 'ci-older-sha')`,
		// Where a table keeps a timestamp, the latest write wins.
		`INSERT INTO PRMetadataCache (owner, repo, pr_number, metadata_json, cached_at) VALUES ('acme', 'widgets', 1, 'fresh', '2026-10-25 12:00:00')`,
		`INSERT INTO PRMetadataCache (owner, repo, pr_number, metadata_json, cached_at) VALUES ('Acme', 'widgets', 1, 'stale', '2026-10-01 12:00:00')`,
		`INSERT INTO PluginResults (owner, repo, pr_number, plugin_name, result, updated_at) VALUES ('Acme', 'widgets', 1, 'lint', 'fresh', '2026-10-25 12:00:00')`,
		`INSERT INTO PluginResults (owner, repo, pr_number, plugin_name, result, updated_at) VALUES ('acme', 'widgets', 1, 'lint', 'stale', '2026-10-01 12:00:00')`,
		`INSERT INTO PluginResults (owner, repo, pr_number, plugin_name, result, updated_at) VALUES ('acme', 'widgets', 1, 'summary', 'only', '2026-10-01 12:00:00')`,
		`INSERT INTO AIResults (owner, repo, pr_number, feature_name, result, updated_at) VALUES ('acme', 'widgets', 1, 'review-ease', 'stale', '2026-10-01 12:00:00')`,
		`INSERT INTO AIResults (owner, repo, pr_number, feature_name, result, updated_at) VALUES ('ACME', 'WIDGETS', 1, 'review-ease', 'fresh', '2026-10-25 12:00:00')`,
		// Another owner's PR of the same name and number is another PR.
		`INSERT INTO PRComments (owner, repo, pr_number, comments_json) VALUES ('Forks', 'widgets', 1, 'theirs')`,
		`INSERT INTO Worktrees (owner, repo, pr_number, path, branch, created_at) VALUES ('acme', 'widgets', 1, '/wt/old', 'b', '2026-10-01 12:00:00')`,
		`INSERT INTO Worktrees (owner, repo, pr_number, path, branch, created_at) VALUES ('Acme', 'widgets', 1, '/wt/new', 'b', '2026-10-25 12:00:00')`,
		// The action log has nothing to merge, only spellings to fold.
		`INSERT INTO WorkflowActionLog (workflow_name, owner, repo, pr_number, sha, created_at) VALUES ('wf', 'Acme', 'Widgets', 1, 'head2', '2026-10-25 12:00:00')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seeding %q: %v", stmt, err)
		}
	}
	db = reopen(t, db, path)

	if body, sha, _ := db.GetPullRequest("acme", "widgets", 1); body != "diff2" || sha != "head2" {
		t.Errorf("PR 1 = %q at %q, want the later head's diff2 at head2", body, sha)
	}
	if body, _, _ := db.GetPullRequest("Acme", "widgets", 2); body != "diff3" {
		t.Errorf("PR 2's diff = %q; the placeholder replaced it", body)
	}
	for _, c := range []struct {
		name string
		read func() (string, error)
		want string
	}{
		{"comments", func() (string, error) { return db.GetPRComments("acme", "widgets", 1) }, "newer"},
		{"CI at head1", func() (string, error) { return db.GetCIStatus("acme", "widgets", 1, "head1") }, "ci-older-sha"},
		{"CI at head2", func() (string, error) { return db.GetCIStatus("acme", "widgets", 1, "head2") }, "ci-newer-sha"},
		{"metadata", func() (string, error) { return db.GetPRMetadataCache("Acme", "widgets", 1) }, "fresh"},
		{"another owner's comments", func() (string, error) { return db.GetPRComments("forks", "widgets", 1) }, "theirs"},
		{"plugin result", func() (string, error) {
			results, err := db.GetPluginResults("acme", "widgets", 1)
			return results["lint"].Result + "," + results["summary"].Result, err
		}, "fresh,only"},
		{"AI result", func() (string, error) {
			res, _, err := db.GetAIResult("acme", "widgets", 1, "review-ease")
			return res.Result, err
		}, "fresh"},
		{"worktree", func() (string, error) { return db.GetWorktree(1, "widgets", "acme") }, "/wt/new"},
		{"action log", func() (string, error) {
			action, err := db.GetLatestWorkflowAction("acme", "widgets", 1)
			if action == nil {
				return "", err
			}
			return action.Owner + "/" + action.Repo + "@" + action.SHA, err
		}, "acme/widgets@head2"},
	} {
		if got, err := c.read(); err != nil || got != c.want {
			t.Errorf("%s = %q, %v; want %q", c.name, got, err, c.want)
		}
	}

	counts := map[string]int{}
	for _, table := range foldedTables {
		var rows, unfolded int
		if err := db.QueryRow("SELECT COUNT(*), COUNT(CASE WHEN owner != lower(owner) OR repo != lower(repo) THEN 1 END) FROM "+table.name).Scan(&rows, &unfolded); err != nil {
			t.Fatal(err)
		}
		if unfolded != 0 {
			t.Errorf("%s kept %d unfolded keys", table.name, unfolded)
		}
		counts[table.name] = rows
	}
	for table, want := range map[string]int{
		"PullRequests": 3, "PRComments": 2, "CIStatus": 2, "PRMetadataCache": 1,
		"PluginResults": 2, "AIResults": 1, "Worktrees": 1, "WorkflowActionLog": 1,
	} {
		if counts[table] != want {
			t.Errorf("%s holds %d rows, want %d", table, counts[table], want)
		}
	}

	// Opening it again changes nothing.
	before := strings.Join(dumpTable(t, db, "PullRequests"), "\n")
	db = reopen(t, db, path)
	if after := strings.Join(dumpTable(t, db, "PullRequests"), "\n"); after != before {
		t.Errorf("a second open changed PullRequests:\n%s\nwas:\n%s", after, before)
	}
}

// dumpTable renders a table's keys in rowid order.
func dumpTable(t *testing.T, db *DB, table string) []string {
	t.Helper()
	rows, err := db.Query("SELECT rowid, owner, repo, pr_number FROM " + table + " ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var rowid int64
		var owner, repo string
		var number int
		if err := rows.Scan(&rowid, &owner, &repo, &number); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d: %s/%s#%d", rowid, owner, repo, number))
	}
	return out
}

// Migrating a cache from before it was keyed by owner files each row under
// the one owner the database knows its PR by. Spellings of one owner are one
// owner: the PR is not dropped as though two owners shared it.
func TestPRCacheOwnerMigrationFoldsCase(t *testing.T) {
	path := writePreUserDB(t,
		`CREATE TABLE PRComments (pr_number INTEGER NOT NULL, repo TEXT NOT NULL, comments_json TEXT NOT NULL,
			UNIQUE(pr_number, repo))`,
		`CREATE TABLE PRMetadataCache (pr_number INTEGER NOT NULL, repo TEXT NOT NULL, owner TEXT NOT NULL,
			metadata_json TEXT NOT NULL, cached_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, UNIQUE(pr_number, repo, owner))`,
		`INSERT INTO PRMetadataCache (pr_number, repo, owner, metadata_json) VALUES
			(5, 'Widgets', 'Acme', '{}'), (5, 'widgets', 'acme', '{}'),
			(6, 'api', 'orgA', '{}'), (6, 'API', 'orgB', '{}')`,
		`INSERT INTO PRComments VALUES (5, 'WIDGETS', '[5]'), (6, 'api', '[6]')`,
	)
	db := openDB(t, path)

	if comments, _ := db.GetPRComments("Acme", "Widgets", 5); comments != "[5]" {
		t.Errorf("acme/widgets#5 comments = %q, want them kept under the one owner", comments)
	}
	// Two owners really do share api#6, whatever the case of its name.
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM PRComments").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("PRComments holds %d rows, want only acme/widgets#5's", n)
	}
}
