package database

import (
	"testing"
	"time"
)

func TestWorkflowActionLogCRUD(t *testing.T) {
	db := newTestDB(t)

	const (
		owner = "C-hipple"
		repo  = "code-review-server"
		num   = 207
	)

	id, err := db.LogWorkflowAction(WorkflowAction{
		WorkflowName:  "review_requests",
		Action:        WorkflowActionCacheWrite,
		Owner:         owner,
		Repo:          repo,
		PRNumber:      num,
		SHA:           "abc123",
		FieldsWritten: []string{"comments", "diff", "reviews"},
	})
	if err != nil {
		t.Fatalf("LogWorkflowAction: %v", err)
	}
	if id == 0 {
		t.Fatal("LogWorkflowAction returned id 0")
	}

	latest, err := db.GetLatestWorkflowAction(owner, repo, num)
	if err != nil {
		t.Fatalf("GetLatestWorkflowAction: %v", err)
	}
	if latest == nil {
		t.Fatal("GetLatestWorkflowAction returned nothing after a write")
	}
	if latest.WorkflowName != "review_requests" || latest.SHA != "abc123" {
		t.Errorf("round-trip mismatch: %+v", latest)
	}
	if got := latest.FieldsSummary(); got != "comments, diff, reviews" {
		t.Errorf("FieldsSummary = %q, want %q", got, "comments, diff, reviews")
	}
	// created_at is written explicitly rather than left to the column default,
	// so it must survive the round trip — the cache miss report prints it.
	if latest.CreatedAt.IsZero() {
		t.Error("CreatedAt round-tripped as the zero time")
	}

	// Update replaces the row in place.
	if err := db.UpdateWorkflowAction(id, WorkflowAction{
		WorkflowName:  "review_requests",
		Action:        WorkflowActionUpdate,
		Owner:         owner,
		Repo:          repo,
		PRNumber:      num,
		SHA:           "def456",
		FieldsWritten: []string{"status", "tags"},
		Detail:        "reclassified",
	}); err != nil {
		t.Fatalf("UpdateWorkflowAction: %v", err)
	}
	latest, err = db.GetLatestWorkflowAction(owner, repo, num)
	if err != nil {
		t.Fatalf("GetLatestWorkflowAction after update: %v", err)
	}
	if latest.Action != WorkflowActionUpdate || latest.SHA != "def456" || latest.Detail != "reclassified" {
		t.Errorf("update did not take effect: %+v", latest)
	}
	if actions, _ := db.GetWorkflowActions(owner, repo, num, 0); len(actions) != 1 {
		t.Errorf("update inserted a new row: got %d rows, want 1", len(actions))
	}

	// Delete removes it.
	if err := db.DeleteWorkflowAction(id); err != nil {
		t.Fatalf("DeleteWorkflowAction: %v", err)
	}
	latest, err = db.GetLatestWorkflowAction(owner, repo, num)
	if err != nil {
		t.Fatalf("GetLatestWorkflowAction after delete: %v", err)
	}
	if latest != nil {
		t.Errorf("row survived delete: %+v", latest)
	}
}

// The per-PR index is created in the migration block rather than the schema
// block, so a fresh database has to pick it up there too.
func TestWorkflowActionLogIndexExistsOnFreshDB(t *testing.T) {
	db := newTestDB(t)

	var count int
	err := db.conn.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_workflow_action_log_pr'",
	).Scan(&count)
	if err != nil {
		t.Fatalf("querying sqlite_master: %v", err)
	}
	if count != 1 {
		t.Error("idx_workflow_action_log_pr missing from a freshly created database")
	}
}

func TestGetLatestWorkflowActionIsNewestFirst(t *testing.T) {
	db := newTestDB(t)

	const (
		owner = "C-hipple"
		repo  = "code-review-server"
		num   = 42
	)

	// Same wall-clock second for both rows: the ordering has to come from the
	// row id, not the timestamp, or a busy cycle reports an arbitrary action.
	ts := time.Now().UTC()
	for _, sha := range []string{"old", "new"} {
		if _, err := db.LogWorkflowAction(WorkflowAction{
			WorkflowName: "wf", Action: WorkflowActionCacheWrite,
			Owner: owner, Repo: repo, PRNumber: num, SHA: sha,
			FieldsWritten: []string{"diff"}, CreatedAt: ts,
		}); err != nil {
			t.Fatalf("LogWorkflowAction(%s): %v", sha, err)
		}
	}

	latest, err := db.GetLatestWorkflowAction(owner, repo, num)
	if err != nil {
		t.Fatalf("GetLatestWorkflowAction: %v", err)
	}
	if latest.SHA != "new" {
		t.Errorf("latest action SHA = %q, want %q", latest.SHA, "new")
	}
}

func TestGetLatestWorkflowActionWithField(t *testing.T) {
	db := newTestDB(t)

	const (
		owner = "C-hipple"
		repo  = "code-review-server"
		num   = 99
	)

	// An older write that included reviews, then a newer one that didn't. The
	// cache miss report needs the older row when explaining a reviews miss.
	if _, err := db.LogWorkflowAction(WorkflowAction{
		WorkflowName: "wf", Action: WorkflowActionCacheWrite,
		Owner: owner, Repo: repo, PRNumber: num, SHA: "sha1",
		FieldsWritten: []string{"diff", "reviews"},
	}); err != nil {
		t.Fatalf("seeding first action: %v", err)
	}
	if _, err := db.LogWorkflowAction(WorkflowAction{
		WorkflowName: "wf", Action: WorkflowActionCacheWrite,
		Owner: owner, Repo: repo, PRNumber: num, SHA: "sha2",
		FieldsWritten: []string{"diff"},
	}); err != nil {
		t.Fatalf("seeding second action: %v", err)
	}

	withReviews, err := db.GetLatestWorkflowActionWithField(owner, repo, num, "reviews")
	if err != nil {
		t.Fatalf("GetLatestWorkflowActionWithField(reviews): %v", err)
	}
	if withReviews == nil || withReviews.SHA != "sha1" {
		t.Errorf("reviews lookup = %+v, want the sha1 row", withReviews)
	}

	withDiff, err := db.GetLatestWorkflowActionWithField(owner, repo, num, "diff")
	if err != nil {
		t.Fatalf("GetLatestWorkflowActionWithField(diff): %v", err)
	}
	if withDiff == nil || withDiff.SHA != "sha2" {
		t.Errorf("diff lookup = %+v, want the sha2 row", withDiff)
	}

	// A field no row ever wrote must come back as "nothing", not as the newest
	// row — that distinction is the whole point of the per-field lookup.
	withCommits, err := db.GetLatestWorkflowActionWithField(owner, repo, num, "commits")
	if err != nil {
		t.Fatalf("GetLatestWorkflowActionWithField(commits): %v", err)
	}
	if withCommits != nil {
		t.Errorf("commits lookup = %+v, want nil", withCommits)
	}
}

// The field match is on whole comma-separated tokens, so a query for one field
// must not be satisfied by a longer field name that contains it.
func TestGetLatestWorkflowActionWithFieldMatchesWholeTokens(t *testing.T) {
	db := newTestDB(t)

	const (
		owner = "C-hipple"
		repo  = "code-review-server"
		num   = 5
	)

	if _, err := db.LogWorkflowAction(WorkflowAction{
		WorkflowName: "wf", Action: WorkflowActionCacheWrite,
		Owner: owner, Repo: repo, PRNumber: num,
		FieldsWritten: []string{"requested_reviewers", "ci_status"},
	}); err != nil {
		t.Fatalf("seeding action: %v", err)
	}

	for _, field := range []string{"reviews", "review", "status", "ci"} {
		got, err := db.GetLatestWorkflowActionWithField(owner, repo, num, field)
		if err != nil {
			t.Fatalf("GetLatestWorkflowActionWithField(%s): %v", field, err)
		}
		if got != nil {
			t.Errorf("%q matched a row that only wrote requested_reviewers/ci_status", field)
		}
	}

	for _, field := range []string{"requested_reviewers", "ci_status"} {
		got, err := db.GetLatestWorkflowActionWithField(owner, repo, num, field)
		if err != nil {
			t.Fatalf("GetLatestWorkflowActionWithField(%s): %v", field, err)
		}
		if got == nil {
			t.Errorf("%q did not match the row that wrote it", field)
		}
	}
}

func TestPruneWorkflowActionsBefore(t *testing.T) {
	db := newTestDB(t)

	const (
		owner = "C-hipple"
		repo  = "code-review-server"
		num   = 7
	)

	now := time.Now().UTC()
	if _, err := db.LogWorkflowAction(WorkflowAction{
		WorkflowName: "wf", Action: WorkflowActionCacheWrite,
		Owner: owner, Repo: repo, PRNumber: num, SHA: "stale",
		FieldsWritten: []string{"diff"}, CreatedAt: now.Add(-30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("seeding stale action: %v", err)
	}
	if _, err := db.LogWorkflowAction(WorkflowAction{
		WorkflowName: "wf", Action: WorkflowActionCacheWrite,
		Owner: owner, Repo: repo, PRNumber: num, SHA: "fresh",
		FieldsWritten: []string{"diff"}, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seeding fresh action: %v", err)
	}

	deleted, err := db.PruneWorkflowActionsBefore(now.Add(-7 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("PruneWorkflowActionsBefore: %v", err)
	}
	if deleted != 1 {
		t.Errorf("pruned %d rows, want 1", deleted)
	}

	remaining, err := db.GetWorkflowActions(owner, repo, num, 0)
	if err != nil {
		t.Fatalf("GetWorkflowActions: %v", err)
	}
	if len(remaining) != 1 || remaining[0].SHA != "fresh" {
		t.Errorf("prune kept the wrong rows: %+v", remaining)
	}
}

func TestDeleteWorkflowActionsForPR(t *testing.T) {
	db := newTestDB(t)

	const (
		owner = "C-hipple"
		repo  = "code-review-server"
	)

	for _, num := range []int{1, 1, 2} {
		if _, err := db.LogWorkflowAction(WorkflowAction{
			WorkflowName: "wf", Action: WorkflowActionCacheWrite,
			Owner: owner, Repo: repo, PRNumber: num, FieldsWritten: []string{"diff"},
		}); err != nil {
			t.Fatalf("seeding pr %d: %v", num, err)
		}
	}

	deleted, err := db.DeleteWorkflowActionsForPR(owner, repo, 1)
	if err != nil {
		t.Fatalf("DeleteWorkflowActionsForPR: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted %d rows, want 2", deleted)
	}
	if other, _ := db.GetWorkflowActions(owner, repo, 2, 0); len(other) != 1 {
		t.Errorf("deleting PR 1 disturbed PR 2: %d rows remain", len(other))
	}
}

// A legacy database that predates some of the WorkflowActionLog columns must be
// brought up to date by initSchema rather than left broken: CREATE TABLE IF NOT
// EXISTS is a no-op once the table is there, so the ALTERs carry the migration.
func TestWorkflowActionLogMigratesLegacyTable(t *testing.T) {
	db := newTestDB(t)

	if _, err := db.conn.Exec("DROP TABLE WorkflowActionLog"); err != nil {
		t.Fatalf("dropping table: %v", err)
	}
	if _, err := db.conn.Exec(`CREATE TABLE WorkflowActionLog (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		workflow_name TEXT NOT NULL DEFAULT '',
		owner TEXT NOT NULL DEFAULT '',
		repo TEXT NOT NULL DEFAULT '',
		pr_number INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("creating legacy table: %v", err)
	}

	if err := db.initSchema(); err != nil {
		t.Fatalf("initSchema on legacy table: %v", err)
	}

	if _, err := db.LogWorkflowAction(WorkflowAction{
		WorkflowName: "wf", Action: WorkflowActionCacheWrite,
		Owner: "C-hipple", Repo: "code-review-server", PRNumber: 1,
		SHA: "abc", FieldsWritten: []string{"diff"}, Detail: "migrated",
	}); err != nil {
		t.Fatalf("writing to migrated table: %v", err)
	}

	latest, err := db.GetLatestWorkflowAction("C-hipple", "code-review-server", 1)
	if err != nil {
		t.Fatalf("GetLatestWorkflowAction: %v", err)
	}
	if latest == nil || latest.SHA != "abc" || latest.Detail != "migrated" {
		t.Errorf("migrated table lost data: %+v", latest)
	}
	if latest.CreatedAt.IsZero() {
		t.Error("created_at missing on migrated table; LogWorkflowAction must not rely on the column default")
	}
}
