package database

import (
	"testing"
	"time"
)

func itemCreatedAt(t *testing.T, db *DB, sectionID int64, identifier string) time.Time {
	t.Helper()
	items, err := db.GetItemsBySection(sectionID)
	if err != nil {
		t.Fatalf("GetItemsBySection: %v", err)
	}
	for _, it := range items {
		if it.Identifier == identifier {
			return it.CreatedAt
		}
	}
	t.Fatalf("item %s not found", identifier)
	return time.Time{}
}

// created_at is when the item entered its section: stamped on insert, kept by
// every later upsert, and not read back as "now".
func TestItemCreatedAtIsWhenAddedToSection(t *testing.T) {
	db := newTestDB(t)
	section, err := db.GetOrCreateSection("Review Requested", 0)
	if err != nil {
		t.Fatalf("GetOrCreateSection: %v", err)
	}
	const id = "C-hipple/code-review-server-1"
	if _, err := db.UpsertItemWithWorkflow(section.ID, id, "", "t", nil, nil, 0, "wf"); err != nil {
		t.Fatalf("UpsertItemWithWorkflow: %v", err)
	}
	// Pretend it was added a day ago, then upsert again as a later cycle would.
	dayAgo := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	if _, err := db.conn.Exec("UPDATE items SET created_at = ? WHERE identifier = ?", dayAgo.Format(sqliteTimeLayout), id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertItemWithWorkflow(section.ID, id, "", "t2", nil, nil, 0, "wf"); err != nil {
		t.Fatalf("UpsertItemWithWorkflow: %v", err)
	}
	if got := itemCreatedAt(t, db, section.ID, id); !got.Equal(dayAgo) {
		t.Errorf("created_at = %v after an update, want %v", got, dayAgo)
	}
}

// Rows written before created_at was recorded are backfilled from the earliest
// action-log row for the PR in that section.
func TestItemCreatedAtBackfill(t *testing.T) {
	db := newTestDB(t)
	section, err := db.GetOrCreateSection("Review Requested", 0)
	if err != nil {
		t.Fatalf("GetOrCreateSection: %v", err)
	}
	const id = "C-hipple/code-review-server-7"
	if _, err := db.UpsertItemWithWorkflow(section.ID, id, "", "t", nil, nil, 0, "wf"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec("UPDATE items SET created_at = NULL"); err != nil {
		t.Fatal(err)
	}
	threeDaysAgo := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	for _, a := range []WorkflowAction{
		{Action: WorkflowActionCacheWrite, Owner: "C-hipple", Repo: "code-review-server", PRNumber: 7, CreatedAt: threeDaysAgo.Add(-time.Hour)},
		{Action: WorkflowActionAddition, Owner: "C-hipple", Repo: "code-review-server", PRNumber: 7, SectionName: "Review Requested", CreatedAt: threeDaysAgo},
		{Action: WorkflowActionUpdate, Owner: "C-hipple", Repo: "code-review-server", PRNumber: 7, SectionName: "Review Requested", CreatedAt: threeDaysAgo.Add(time.Hour)},
	} {
		if _, err := db.LogWorkflowAction(a); err != nil {
			t.Fatal(err)
		}
	}

	db.backfillItemCreatedAt(true)

	if got := itemCreatedAt(t, db, section.ID, id); !got.Equal(threeDaysAgo) {
		t.Errorf("backfilled created_at = %v, want %v", got, threeDaysAgo)
	}
}
