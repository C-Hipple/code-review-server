package database

import (
	"testing"
	"time"
)

func TestAIResultsRoundTrip(t *testing.T) {
	db := newTestDB(t)

	if _, ok, err := db.GetAIResult("acme", "widgets", 42, "comments-addressed"); err != nil || ok {
		t.Fatalf("expected no stored result, got ok=%v err=%v", ok, err)
	}

	before := time.Now().UTC().Add(-time.Minute)
	if err := db.UpsertAIResult("acme", "widgets", 42, "comments-addressed", `{"body":{}}`, "success", "sha-1", "digest-1"); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, ok, err := db.GetAIResult("acme", "widgets", 42, "comments-addressed")
	if err != nil || !ok {
		t.Fatalf("expected a stored result, got ok=%v err=%v", ok, err)
	}
	if got.Result != `{"body":{}}` || got.Status != "success" || got.SHA != "sha-1" || got.InputHash != "digest-1" {
		t.Errorf("unexpected row: %+v", got)
	}
	if got.UpdatedAt.Before(before) {
		t.Errorf("updated_at = %v, want a time after %v", got.UpdatedAt, before)
	}
}

func TestUpsertAIResultReplacesTheWholeRow(t *testing.T) {
	db := newTestDB(t)

	if err := db.UpsertAIResult("acme", "widgets", 42, "comments-addressed", "old", "success", "sha-1", "digest-1"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A later run is keyed to newer inputs; every column must describe it, or
	// a reader would pair the new result with the old key and call it fresh.
	if err := db.UpsertAIResult("acme", "widgets", 42, "comments-addressed", "new", "insufficient-input", "sha-2", "digest-2"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	got, _, err := db.GetAIResult("acme", "widgets", 42, "comments-addressed")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Result != "new" || got.Status != "insufficient-input" || got.SHA != "sha-2" || got.InputHash != "digest-2" {
		t.Errorf("row was not replaced: %+v", got)
	}
}

func TestGetAIResultsIsScopedToThePR(t *testing.T) {
	db := newTestDB(t)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.UpsertAIResult("acme", "widgets", 42, "comments-addressed", "a", "success", "s", "d"))
	must(db.UpsertAIResult("acme", "widgets", 42, "mermaid", "b", "error", "s", "d"))
	must(db.UpsertAIResult("acme", "widgets", 43, "comments-addressed", "c", "success", "s", "d"))
	must(db.UpsertAIResult("other", "widgets", 42, "comments-addressed", "d", "success", "s", "d"))

	results, err := db.GetAIResults("acme", "widgets", 42)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for acme/widgets#42, got %d: %+v", len(results), results)
	}
	if results["comments-addressed"].Result != "a" || results["mermaid"].Status != "error" {
		t.Errorf("unexpected results: %+v", results)
	}

	empty, err := db.GetAIResults("acme", "widgets", 99)
	if err != nil || len(empty) != 0 {
		t.Errorf("expected no results for an untouched PR, got %+v (err %v)", empty, err)
	}
}

func TestAIResultsTableLeavesPluginResultsAlone(t *testing.T) {
	// The table is additive: storing an AI result must not surface as a
	// plugin result (the two are read by different RPCs and clients).
	db := newTestDB(t)
	if err := db.UpsertAIResult("acme", "widgets", 42, "comments-addressed", "a", "success", "s", "d"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	plugins, err := db.GetPluginResults("acme", "widgets", 42)
	if err != nil || len(plugins) != 0 {
		t.Errorf("expected no plugin results, got %+v (err %v)", plugins, err)
	}
}
