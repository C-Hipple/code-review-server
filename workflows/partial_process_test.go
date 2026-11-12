package workflows

import (
	"sync"
	"testing"
	"time"

	"crs/config"
	"crs/database"
	"crs/git_tools"

	"github.com/google/go-github/v74/github"
)

// processHoldingPR8 runs process over PR 7 for a section where the workflow
// already holds PR 8, and returns the changes it sent.
func processHoldingPR8(t *testing.T, process func(string, []*github.PullRequest, chan FileChanges, *database.DB, *database.Section, *sync.WaitGroup, bool, bool) RunResult) []FileChanges {
	t.Helper()
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	seedReprocessAuxData(t, "acme", "widgets", 7)
	fakeMergeabilityLookup(t, answerAll(git_tools.MergeabilityMergeable))

	section, err := db.GetOrCreateSection("Project", 0)
	if err != nil {
		t.Fatalf("GetOrCreateSection: %v", err)
	}
	held := PRToOrgBridge{PR: mergeabilityTestPR(8, "open", nil)}.Identifier()
	ttl := time.Now().Add(time.Hour).Unix()
	if _, err := db.UpsertItemWithWorkflow(section.ID, held, "TODO", "held", nil, nil, ttl, "project"); err != nil {
		t.Fatalf("UpsertItemWithWorkflow: %v", err)
	}

	changes := make(chan FileChanges, 8)
	var wg sync.WaitGroup
	process("project", []*github.PullRequest{mergeabilityTestPR(7, "open", nil)}, changes, db, section, &wg, false, false)
	close(changes)
	var out []FileChanges
	for fc := range changes {
		out = append(out, fc)
	}
	return out
}

func deletes(changes []FileChanges) []string {
	var ids []string
	for _, fc := range changes {
		if fc.ChangeType == "Delete" {
			ids = append(ids, fc.Identifier)
		}
	}
	return ids
}

func TestProcessPRsDBReleasesHeldPRsItWasNotGiven(t *testing.T) {
	if got := deletes(processHoldingPR8(t, ProcessPRsDB)); len(got) != 1 {
		t.Errorf("deleted %v, want the held PR released", got)
	}
}

// A project list whose Jira lookup failed partway doesn't know whether a held
// PR left the epic, so it keeps it rather than dropping it and adding it back
// (with a notification) on the next cycle.
func TestProcessPartialPRsDBKeepsHeldPRs(t *testing.T) {
	changes := processHoldingPR8(t, ProcessPartialPRsDB)
	if got := deletes(changes); len(got) != 0 {
		t.Errorf("deleted %v, want the held PR kept", got)
	}
	added := 0
	for _, fc := range changes {
		if fc.ChangeType == "Addition" {
			added++
		}
	}
	if added != 1 {
		t.Errorf("added %d, want the PR it was given added", added)
	}
}
