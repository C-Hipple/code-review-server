package database

import "testing"

func TestFilteredDiffRoundTrip(t *testing.T) {
	db := newTestDB(t)

	if _, ok, err := db.GetFilteredDiff("acme", "widgets", 42, "base-1", "head-1"); err != nil || ok {
		t.Fatalf("expected nothing stored, got ok=%v err=%v", ok, err)
	}

	want := FilteredDiff{BaseSHA: "base-1", HeadSHA: "head-1", Status: "ready", Diff: "diff --git a/x b/x\n"}
	if err := db.UpsertFilteredDiff("acme", "widgets", 42, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.GetFilteredDiff("acme", "widgets", 42, "base-1", "head-1")
	if err != nil || !ok {
		t.Fatalf("expected the stored diff, got ok=%v err=%v", ok, err)
	}
	if got.Status != want.Status || got.Diff != want.Diff || got.UpdatedAt.IsZero() {
		t.Fatalf("got %+v, want %+v with an updated_at", got, want)
	}

	// Another PR, or another pair, is a different row.
	if _, ok, _ := db.GetFilteredDiff("acme", "widgets", 43, "base-1", "head-1"); ok {
		t.Fatal("a diff stored for one PR answered for another")
	}
	if _, ok, _ := db.GetFilteredDiff("acme", "widgets", 42, "base-2", "head-1"); ok {
		t.Fatal("a diff stored for one baseline answered for another")
	}
}

func TestUpsertFilteredDiffDropsRowsForOlderHeads(t *testing.T) {
	db := newTestDB(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	must(db.UpsertFilteredDiff("acme", "widgets", 42, FilteredDiff{BaseSHA: "b1", HeadSHA: "h1", Status: "ready", Diff: "one"}))
	must(db.UpsertFilteredDiff("acme", "widgets", 42, FilteredDiff{BaseSHA: "b2", HeadSHA: "h1", Status: "rebased", Message: "gone"}))
	must(db.UpsertFilteredDiff("acme", "widgets", 7, FilteredDiff{BaseSHA: "b1", HeadSHA: "h1", Status: "ready"}))

	// Both baselines at the same head stand side by side.
	for _, base := range []string{"b1", "b2"} {
		if _, ok, _ := db.GetFilteredDiff("acme", "widgets", 42, base, "h1"); !ok {
			t.Fatalf("diff from %s at h1 went missing", base)
		}
	}

	// A push moves the head; the old head's diffs can never be served again.
	must(db.UpsertFilteredDiff("acme", "widgets", 42, FilteredDiff{BaseSHA: "b1", HeadSHA: "h2", Status: "ready", Diff: "two"}))
	for _, base := range []string{"b1", "b2"} {
		if _, ok, _ := db.GetFilteredDiff("acme", "widgets", 42, base, "h1"); ok {
			t.Fatalf("diff from %s at the old head h1 survived the new head", base)
		}
	}
	if got, ok, _ := db.GetFilteredDiff("acme", "widgets", 42, "b1", "h2"); !ok || got.Diff != "two" {
		t.Fatalf("diff at the new head = %+v (ok=%v)", got, ok)
	}
	if _, ok, _ := db.GetFilteredDiff("acme", "widgets", 7, "b1", "h1"); !ok {
		t.Fatal("pruning one PR's old heads touched another PR")
	}

	// Rewriting a pair replaces every column.
	must(db.UpsertFilteredDiff("acme", "widgets", 42, FilteredDiff{BaseSHA: "b1", HeadSHA: "h2", Status: "error", Message: "boom"}))
	if got, _, _ := db.GetFilteredDiff("acme", "widgets", 42, "b1", "h2"); got.Status != "error" || got.Diff != "" || got.Message != "boom" {
		t.Fatalf("row was not replaced: %+v", got)
	}
}
