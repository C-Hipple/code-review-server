package database

import (
	"slices"
	"testing"
)

func TestMarkPluginDeferredKeepsAResultForTheSameHead(t *testing.T) {
	db := newTestDB(t)
	state := func() (string, string) {
		t.Helper()
		sha, status, _, err := db.GetPluginResultState("owner", "repo", 1, "style")
		if err != nil {
			t.Fatal(err)
		}
		return sha, status
	}

	if sha, status := state(); sha != "" || status != "" {
		t.Fatalf("state before any write = %q/%q, want empty", sha, status)
	}

	if written, err := db.MarkPluginDeferred("owner", "repo", 1, "style", "sha-1"); err != nil || !written {
		t.Fatalf("first placeholder: written %v, err %v", written, err)
	}
	if sha, status := state(); sha != "sha-1" || status != "deferred" {
		t.Errorf("state = %q/%q, want sha-1/deferred", sha, status)
	}

	// A run someone asked for at this head is kept...
	if _, err := db.UpsertPluginResult("owner", "repo", 1, "style", "report", "success", "sha-1", "explicit"); err != nil {
		t.Fatal(err)
	}
	if written, err := db.MarkPluginDeferred("owner", "repo", 1, "style", "sha-1"); err != nil || written {
		t.Fatalf("placeholder over a result at the same head: written %v, err %v", written, err)
	}
	if sha, status := state(); sha != "sha-1" || status != "success" {
		t.Errorf("state = %q/%q, want the success kept", sha, status)
	}

	// ...while a result for an older head is replaced.
	if written, err := db.MarkPluginDeferred("owner", "repo", 1, "style", "sha-2"); err != nil || !written {
		t.Fatalf("placeholder for a new head: written %v, err %v", written, err)
	}
	results, _ := db.GetPluginResults("owner", "repo", 1)
	if got := results["style"]; got.Status != "deferred" || got.Result != "" {
		t.Errorf("result = %+v, want an empty deferred placeholder", got)
	}
}

func TestSectionsHoldingItem(t *testing.T) {
	db := newTestDB(t)
	team, err := db.GetOrCreateSection("Team", 0)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := db.GetOrCreateSection("Mine", 0)
	if err != nil {
		t.Fatal(err)
	}
	const id = "C-Hipple/code-review-server-7"
	if _, err := db.UpsertItemWithWorkflow(team.ID, id, "TODO", "t", nil, nil, 0, "team"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertItemWithWorkflow(mine.ID, id, "TODO", "t", nil, nil, 0, "mine"); err != nil {
		t.Fatal(err)
	}

	// Any spelling of the owner and repo finds it.
	got, err := db.SectionsHoldingItem("c-hipple/code-review-server-7")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Mine", "Team"}; !slices.Equal(got, want) {
		t.Errorf("sections = %v, want %v", got, want)
	}

	// An item no workflow claims any more is on its way out.
	if err := db.RemoveWorkflowFromItem(mine.ID, id, "mine"); err != nil {
		t.Fatal(err)
	}
	got, _ = db.SectionsHoldingItem(id)
	if want := []string{"Team"}; !slices.Equal(got, want) {
		t.Errorf("sections after the release = %v, want %v", got, want)
	}
	if got, _ := db.SectionsHoldingItem("C-Hipple/code-review-server-8"); len(got) != 0 {
		t.Errorf("another PR is in %v, want none", got)
	}
}

func TestUpsertPluginResultKeepsARequestedRunFromAnAutomaticOne(t *testing.T) {
	db := newTestDB(t)
	upsert := func(result, sha, callType string) bool {
		t.Helper()
		written, err := db.UpsertPluginResult("owner", "repo", 1, "validator", result, "success", sha, callType)
		if err != nil {
			t.Fatal(err)
		}
		return written
	}
	stored := func() string {
		t.Helper()
		results, _ := db.GetPluginResults("owner", "repo", 1)
		return results["validator"].Result
	}

	// An automatic run that finishes after the explicit one a section made
	// in its place at the same head doesn't replace it...
	if !upsert("started", "sha-1", "explicit") {
		t.Fatal("the first write wasn't written")
	}
	if upsert("report", "sha-1", "automatic") {
		t.Error("an automatic result replaced an explicit one at the same head")
	}
	if got := stored(); got != "started" {
		t.Errorf("result = %q, want the explicit run's kept", got)
	}
	// ...though anything else for that head does,
	if !upsert("rerun", "sha-1", "rerun") || stored() != "rerun" {
		t.Errorf("a rerun at the same head wasn't written: %q", stored())
	}
	// and an automatic result for a new head does.
	if !upsert("next", "sha-2", "automatic") || stored() != "next" {
		t.Errorf("an automatic result for a new head wasn't written: %q", stored())
	}
	if _, _, callType, _ := db.GetPluginResultState("owner", "repo", 1, "validator"); callType != "automatic" {
		t.Errorf("call type = %q, want automatic", callType)
	}
}
