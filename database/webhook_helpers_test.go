package database

import (
	"slices"
	"testing"
)

func TestHasItemMatchesAcrossCase(t *testing.T) {
	db := newTestDB(t)
	// A user's section counts: the caches HasItem decides about are shared.
	section, err := db.GetOrCreateSection("alice", "Reviews", 1)
	if err != nil {
		t.Fatalf("section: %v", err)
	}
	if _, err := db.UpsertItemWithWorkflow(section.ID, "C-Hipple/code-review-server-7", "TODO", "t", nil, nil, 0, "alice\x1fwf"); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	for _, identifier := range []string{"C-Hipple/code-review-server-7", "c-hipple/Code-Review-Server-7"} {
		if ok, err := db.HasItem(identifier); err != nil || !ok {
			t.Errorf("HasItem(%q) = %v, %v; want true", identifier, ok, err)
		}
	}
	if ok, _ := db.HasItem("C-Hipple/code-review-server-8"); ok {
		t.Error("HasItem matched a PR no section lists")
	}
}

func TestUpdateItemSummaryRewritesEverySectionsRow(t *testing.T) {
	db := newTestDB(t)
	const identifier = "acme/widgets-12"
	var ids []int64
	for _, name := range []string{"Mine", "Team"} {
		section, err := db.GetOrCreateSection("", name, 1)
		if err != nil {
			t.Fatalf("section: %v", err)
		}
		ids = append(ids, section.ID)
		if _, err := db.UpsertItemWithWorkflow(section.ID, identifier, "TODO", "Old title", []string{"detail"}, []string{"widgets"}, 0, "wf-"+name); err != nil {
			t.Fatalf("seed item: %v", err)
		}
	}
	// Another user's row for the same PR, in a section of the same name: the
	// status, title and tags are the PR's, the same on every dashboard.
	theirs, err := db.GetOrCreateSection("alice", "Mine", 1)
	if err != nil {
		t.Fatalf("section: %v", err)
	}
	if _, err := db.UpsertItemWithWorkflow(theirs.ID, identifier, "TODO", "Old title", []string{"detail"}, []string{"widgets"}, 0, "alice\x1fwf-Mine"); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	n, err := db.UpdateItemSummary(identifier, "DONE", "New title", []string{"widgets", "merged"})
	if err != nil || n != 3 {
		t.Fatalf("UpdateItemSummary = %d, %v; want 3 rows", n, err)
	}
	ids = append(ids, theirs.ID)
	for _, id := range ids {
		item, err := db.GetItem(id, identifier)
		if err != nil {
			t.Fatalf("GetItem: %v", err)
		}
		if item.Status != "DONE" || item.Title != "New title" {
			t.Errorf("row not updated: %+v", item)
		}
		if tags, _ := item.GetTags(); !slices.Equal(tags, []string{"widgets", "merged"}) {
			t.Errorf("tags = %v", tags)
		}
		// Details and ownership are the workflows' to write.
		if details, _ := item.GetDetails(); !slices.Equal(details, []string{"detail"}) {
			t.Errorf("details changed: %v", details)
		}
		if len(item.GetWorkflows()) != 1 {
			t.Errorf("ownership changed: %v", item.GetWorkflows())
		}
	}
}

func TestFindPRNumbersBySHA(t *testing.T) {
	db := newTestDB(t)
	seed := func(owner string, number int, repo, sha string) {
		t.Helper()
		if err := db.UpsertPullRequest(owner, repo, number, sha, "", ""); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	seed("acme", 1, "widgets", "aaa")
	seed("acme", 1, "widgets", "bbb")  // pushed to since
	seed("acme", 2, "widgets", "aaa")  // a second PR on the same commit
	seed("acme", 3, "gadgets", "aaa")  // same commit, other repository
	seed("forks", 4, "widgets", "aaa") // same commit and name, other owner

	got, err := db.FindPRNumbersBySHA("acme", "widgets", "aaa")
	if err != nil {
		t.Fatalf("FindPRNumbersBySHA: %v", err)
	}
	if !slices.Equal(got, []int{1, 2}) {
		t.Errorf("FindPRNumbersBySHA = %v, want [1 2]", got)
	}
	if got, _ := db.FindPRNumbersBySHA("acme", "widgets", "ccc"); len(got) != 0 {
		t.Errorf("unknown SHA matched %v", got)
	}
}
