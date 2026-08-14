package workflows

import (
	"crs/config"
	"slices"
	"sync"
	"testing"
	"time"
)

// collectForcedDispatches registers a PR-updated hook that records the PRs it is
// asked to pre-compute the deferred plugins for, and returns a function that
// waits for want of them.
func collectForcedDispatches(t *testing.T, want int) func() []PRKey {
	t.Helper()
	var mu sync.Mutex
	var forced []PRKey
	done := make(chan struct{}, want+8)

	SetPRUpdatedHook(func(owner, repo string, number int, opts PRUpdateOptions) {
		if !opts.ForceOnDemandPlugins {
			return
		}
		mu.Lock()
		forced = append(forced, PRKey{Owner: owner, Repo: repo, Number: number})
		mu.Unlock()
		done <- struct{}{}
	})
	t.Cleanup(func() { SetPRUpdatedHook(nil) })

	wait := func() []PRKey {
		t.Helper()
		for i := 0; i < want; i++ {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting for dispatch %d of %d", i+1, want)
			}
		}
		// Give any unexpected extra dispatch a moment to show up, so a test
		// asserting on the absence of one isn't just winning a race.
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(forced)
	}
	return wait
}

// applyChangesForTest runs changes through ApplyChanges the way a cycle does.
func applyChangesForTest(t *testing.T, changes []FileChanges) {
	t.Helper()
	serialized := make(chan SerializedFileChange)
	var wg sync.WaitGroup
	applied := make(chan struct{})
	go func() {
		defer close(applied)
		ApplyChanges(serialized, &wg)
	}()
	for i := range changes {
		wg.Add(1)
		serialized <- SerializedFileChange{FileChange: &changes[i]}
	}
	close(serialized)
	wg.Wait()
	<-applied
}

func TestApplyChangesDispatchesForcedOnDemandPRs(t *testing.T) {
	db := warmTestDB(t)
	section, err := db.GetOrCreateSection("Waiting On Me", 10)
	if err != nil {
		t.Fatalf("GetOrCreateSection: %v", err)
	}
	other, err := db.GetOrCreateSection("Everything Else", 90)
	if err != nil {
		t.Fatalf("GetOrCreateSection: %v", err)
	}

	config.SetC(config.Config{
		DB: db,
		RawWorkflows: []config.RawWorkflow{
			{Name: "waiting", SectionTitle: "Waiting On Me", ForceRunOnDemand: true},
			{Name: "everything", SectionTitle: "Everything Else"},
		},
	})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	wait := collectForcedDispatches(t, 2)

	applyChangesForTest(t, []FileChanges{
		{
			ChangeType: "Addition", Identifier: "C-hipple/code-review-server-1",
			SectionID: section.ID, SectionName: "Waiting On Me", WorkflowName: "waiting",
		},
		{
			ChangeType: "Update", Identifier: "C-hipple/code-review-server-2",
			SectionID: section.ID, SectionName: "Waiting On Me", WorkflowName: "waiting",
		},
		// Same PR through a second workflow feeding the same section: one
		// dispatch, not two.
		{
			ChangeType: "Update", Identifier: "C-hipple/code-review-server-2",
			SectionID: section.ID, SectionName: "Waiting On Me", WorkflowName: "other-workflow",
		},
		// A section that didn't ask for it is left alone.
		{
			ChangeType: "Addition", Identifier: "C-hipple/code-review-server-3",
			SectionID: other.ID, SectionName: "Everything Else", WorkflowName: "everything",
		},
	})

	forced := wait()
	want := []PRKey{
		{Owner: "C-hipple", Repo: "code-review-server", Number: 1},
		{Owner: "C-hipple", Repo: "code-review-server", Number: 2},
	}
	if len(forced) != len(want) {
		t.Fatalf("dispatched %+v, want exactly %+v", forced, want)
	}
	for _, key := range want {
		if !slices.Contains(forced, key) {
			t.Errorf("no dispatch for %+v; got %+v", key, forced)
		}
	}
}

func TestApplyChangesSkipsForcedDispatchOnDelete(t *testing.T) {
	// Releasing ownership is not "added or updated"; there is nothing to
	// pre-compute for a PR on its way out of the section.
	db := warmTestDB(t)
	section, err := db.GetOrCreateSection("Waiting On Me", 10)
	if err != nil {
		t.Fatalf("GetOrCreateSection: %v", err)
	}

	config.SetC(config.Config{
		DB: db,
		RawWorkflows: []config.RawWorkflow{
			{Name: "waiting", SectionTitle: "Waiting On Me", ForceRunOnDemand: true},
		},
	})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	var mu sync.Mutex
	dispatched := 0
	SetPRUpdatedHook(func(owner, repo string, number int, opts PRUpdateOptions) {
		mu.Lock()
		dispatched++
		mu.Unlock()
	})
	t.Cleanup(func() { SetPRUpdatedHook(nil) })

	applyChangesForTest(t, []FileChanges{{
		ChangeType: "Delete", Identifier: "C-hipple/code-review-server-1",
		SectionID: section.ID, SectionName: "Waiting On Me", WorkflowName: "waiting",
	}})
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if dispatched != 0 {
		t.Errorf("got %d dispatches for a Delete, want 0", dispatched)
	}
}
