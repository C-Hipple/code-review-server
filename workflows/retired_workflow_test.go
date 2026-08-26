package workflows

import (
	"crs/config"
	"crs/database"
	"testing"
	"time"
)

// retiredTestDB returns a database with a config wired to it, since the prune
// helpers reach the DB and the workflow list through config.C().
func retiredTestDB(t *testing.T, workflows ...config.RawWorkflow) *database.DB {
	t.Helper()
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db, RawWorkflows: workflows})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	return db
}

func seedOwnedItem(t *testing.T, db *database.DB, sectionName, identifier string, ttl int64, owners ...string) *database.Section {
	t.Helper()
	section, err := db.GetOrCreateSection(sectionName, 0)
	if err != nil {
		t.Fatalf("GetOrCreateSection(%q): %v", sectionName, err)
	}
	for _, owner := range owners {
		if _, err := db.UpsertItemWithWorkflow(section.ID, identifier, "TODO", "a pr",
			[]string{"detail"}, []string{"tag"}, ttl, owner); err != nil {
			t.Fatalf("UpsertItemWithWorkflow(%q): %v", owner, err)
		}
	}
	return section
}

func itemExists(t *testing.T, db *database.DB, sectionID int64, identifier string) bool {
	t.Helper()
	item, err := db.GetItem(sectionID, identifier)
	return err == nil && item != nil
}

func sectionExists(t *testing.T, db *database.DB, name string) bool {
	t.Helper()
	section, err := db.GetSection(name)
	return err == nil && section != nil
}

// The reported bug: a workflow and its section are deleted from the TOML, and
// its reviews stay in the dashboard indefinitely. Nothing else can evict them —
// the TTL sweep only runs inside the owning workflow's own section pass, and the
// orphan prune only deletes rows whose ownership list is empty.
func TestRetiredWorkflowItemsAreEvicted(t *testing.T) {
	live := config.RawWorkflow{Name: "live_workflow", SectionTitle: "Live Section"}
	db := retiredTestDB(t, live)

	expired := time.Now().Add(-48 * time.Hour).Unix()
	retiredSection := seedOwnedItem(t, db, "Retired Section", "o/r-1", expired, "retired_workflow")
	liveSection := seedOwnedItem(t, db, live.SectionTitle, "o/r-2", expired, live.Name)

	releaseStaleWorkflowOwnership()
	pruneOrphanedItems()
	pruneRetiredSections()

	if itemExists(t, db, retiredSection.ID, "o/r-1") {
		t.Error("item owned only by a workflow removed from the config survived the cycle")
	}
	if sectionExists(t, db, "Retired Section") {
		t.Error("section left behind by a removed workflow was not pruned")
	}
	if !itemExists(t, db, liveSection.ID, "o/r-2") {
		t.Error("item owned by a configured workflow was evicted")
	}
	if !sectionExists(t, db, live.SectionTitle) {
		t.Error("section of a configured workflow was pruned")
	}
}

// A dead owner also pins items in sections that are still live: once the
// surviving workflow releases its claim the list still reads ["retired"], which
// is non-empty, so the orphan prune skips the row forever.
func TestRetiredCoOwnerDoesNotPinItemInLiveSection(t *testing.T) {
	live := config.RawWorkflow{Name: "live_workflow", SectionTitle: "Live Section"}
	db := retiredTestDB(t, live)

	section := seedOwnedItem(t, db, live.SectionTitle, "o/r-1", 0, "retired_workflow", live.Name)
	// The live workflow drops the PR this cycle: its filters stopped matching.
	if err := db.RemoveWorkflowFromItem(section.ID, "o/r-1", live.Name); err != nil {
		t.Fatalf("RemoveWorkflowFromItem: %v", err)
	}

	releaseStaleWorkflowOwnership()
	pruneOrphanedItems()

	if itemExists(t, db, section.ID, "o/r-1") {
		t.Error("item dropped by its live workflow was pinned by a retired co-owner")
	}
}

// Ownership is only valid for the section the workflow currently writes into,
// so re-pointing SectionTitle at a new section clears out the old one.
func TestRepointedWorkflowReleasesItsOldSection(t *testing.T) {
	moved := config.RawWorkflow{Name: "wf", SectionTitle: "New Section"}
	db := retiredTestDB(t, moved)

	oldSection := seedOwnedItem(t, db, "Old Section", "o/r-1", 0, moved.Name)
	newSection := seedOwnedItem(t, db, moved.SectionTitle, "o/r-1", 0, moved.Name)

	releaseStaleWorkflowOwnership()
	pruneOrphanedItems()
	pruneRetiredSections()

	if itemExists(t, db, oldSection.ID, "o/r-1") {
		t.Error("item stayed in the section the workflow no longer writes into")
	}
	if !itemExists(t, db, newSection.ID, "o/r-1") {
		t.Error("item was evicted from the section the workflow now writes into")
	}
}

// A workflow present in the TOML but failing to build is skipped by
// MatchWorkflows, and must not be mistaken for one that was deleted: the sweep
// reads the raw config so a typo doesn't cost the dashboard its items.
func TestUnbuildableWorkflowKeepsItsItems(t *testing.T) {
	broken := config.RawWorkflow{Name: "broken_workflow", SectionTitle: "Broken Section", WorkflowType: "NoSuchType"}
	db := retiredTestDB(t, broken)

	section := seedOwnedItem(t, db, broken.SectionTitle, "o/r-1", 0, broken.Name)

	if built := MatchWorkflows(config.C().RawWorkflows, &[]string{}, ""); len(built) != 0 {
		t.Fatalf("expected the broken workflow to be skipped by MatchWorkflows, got %d", len(built))
	}

	releaseStaleWorkflowOwnership()
	pruneOrphanedItems()
	pruneRetiredSections()

	if !itemExists(t, db, section.ID, "o/r-1") {
		t.Error("items of a misconfigured but still-configured workflow were evicted")
	}
}

// A config that loads with no workflows at all must not be read as "everything
// was retired" — that would empty the whole dashboard in one cycle.
func TestEmptyConfigReleasesNothing(t *testing.T) {
	db := retiredTestDB(t)

	section := seedOwnedItem(t, db, "Some Section", "o/r-1", 0, "some_workflow")

	releaseStaleWorkflowOwnership()
	pruneOrphanedItems()
	pruneRetiredSections()

	if !itemExists(t, db, section.ID, "o/r-1") {
		t.Error("items were evicted when no workflows were configured")
	}
	if !sectionExists(t, db, "Some Section") {
		t.Error("sections were pruned when no workflows were configured")
	}
}

// An empty section a workflow still writes into is kept: GetOrCreateSection
// would recreate it on the next cycle anyway.
func TestEmptyConfiguredSectionIsKept(t *testing.T) {
	live := config.RawWorkflow{Name: "live_workflow", SectionTitle: "Live Section"}
	db := retiredTestDB(t, live)

	if _, err := db.GetOrCreateSection(live.SectionTitle, 0); err != nil {
		t.Fatalf("GetOrCreateSection: %v", err)
	}
	if _, err := db.GetOrCreateSection("Retired Section", 0); err != nil {
		t.Fatalf("GetOrCreateSection: %v", err)
	}

	pruneRetiredSections()

	if !sectionExists(t, db, live.SectionTitle) {
		t.Error("empty section of a configured workflow was pruned")
	}
	if sectionExists(t, db, "Retired Section") {
		t.Error("empty retired section was not pruned")
	}
}
