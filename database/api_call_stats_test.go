package database

import (
	"testing"
	"time"
)

// insertStatsAt writes an APICallStats row with an explicit recorded_at, which
// LogAPICallStats cannot do — it leans on the column default. The lookback
// query is the thing under test, so the rows need to be spread over time.
func insertStatsAt(t *testing.T, db *DB, at time.Time, total int64, remaining, limit int) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO APICallStats (recorded_at, pr_list, total, rate_limit_remaining, rate_limit_limit, rate_limit_reset_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		at.UTC().Format(sqliteTimeLayout), total, total, remaining, limit, "",
	)
	if err != nil {
		t.Fatalf("inserting APICallStats row: %v", err)
	}
}

func TestGetAPICallStatsSinceWindowsAndOrders(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)

	// Inserted newest first, to prove the query does the ordering.
	insertStatsAt(t, db, now.Add(-30*time.Minute), 30, 4700, 5000)
	insertStatsAt(t, db, now.Add(-2*time.Hour), 20, 4800, 5000)
	insertStatsAt(t, db, now.Add(-5*time.Hour), 10, 4900, 5000) // outside a 3h window

	rows, err := db.GetAPICallStatsSince(now.Add(-3 * time.Hour))
	if err != nil {
		t.Fatalf("GetAPICallStatsSince: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows inside the 3h window, got %d", len(rows))
	}
	if rows[0].Total != 20 || rows[1].Total != 30 {
		t.Errorf("expected oldest-first ordering, got totals %d then %d", rows[0].Total, rows[1].Total)
	}
	if rows[0].RateLimitRemaining != 4800 || rows[0].RateLimitLimit != 5000 {
		t.Errorf("rate limit columns did not round-trip: %+v", rows[0])
	}
	if rows[0].RecordedAt.IsZero() {
		t.Error("recorded_at round-tripped as the zero time")
	}
	if !rows[0].RecordedAt.Before(rows[1].RecordedAt) {
		t.Errorf("timestamps out of order: %v then %v", rows[0].RecordedAt, rows[1].RecordedAt)
	}

	// A wider window reaches the row the 3h one excluded.
	all, err := db.GetAPICallStatsSince(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("GetAPICallStatsSince(24h): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected all 3 rows in a 24h window, got %d", len(all))
	}
}

func TestGetAPICallStatsSinceEmpty(t *testing.T) {
	db := newTestDB(t)
	rows, err := db.GetAPICallStatsSince(time.Now().UTC().Add(-3 * time.Hour))
	if err != nil {
		t.Fatalf("GetAPICallStatsSince on an empty table: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected no rows, got %d", len(rows))
	}
}

// LogAPICallStats is what the workflow manager actually calls, so the read path
// has to line up with the row it writes — including the derived total.
func TestGetAPICallStatsSinceReadsLoggedCycle(t *testing.T) {
	db := newTestDB(t)

	if err := db.LogAPICallStats(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 4321, 5000, "2026-08-26T12:00:00Z"); err != nil {
		t.Fatalf("LogAPICallStats: %v", err)
	}

	rows, err := db.GetAPICallStatsSince(time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetAPICallStatsSince: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected the logged cycle to come back, got %d rows", len(rows))
	}
	got := rows[0]
	if got.PRList != 1 || got.PRSpecific != 2 || got.ReviewThreads != 11 || got.TeamReviews != 12 {
		t.Errorf("per-type counters did not round-trip: %+v", got)
	}
	if want := int64(1 + 2 + 3 + 4 + 5 + 6 + 7 + 8 + 9 + 10 + 11 + 12); got.Total != want {
		t.Errorf("Total = %d, want %d", got.Total, want)
	}
	if got.RateLimitRemaining != 4321 || got.RateLimitLimit != 5000 {
		t.Errorf("rate limit did not round-trip: %+v", got)
	}
	if got.RateLimitResetAt != "2026-08-26T12:00:00Z" {
		t.Errorf("ResetAt = %q", got.RateLimitResetAt)
	}
}
