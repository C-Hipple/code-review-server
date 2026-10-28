package workflows

import (
	"crs/config"
	"testing"
	"time"
)

func TestApplyChangeRunsTheSectionsPluginsForTheItemsItWrites(t *testing.T) {
	db := warmTestDB(t)
	config.SetC(config.Config{
		DB: db,
		RawWorkflows: []config.RawWorkflow{
			{Name: "team", SectionTitle: "Team", AlwaysRunPlugins: []string{"style"}},
			{Name: "mine", SectionTitle: "Mine"},
		},
	})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	team, _ := db.GetOrCreateSection("Team", 0)
	mine, _ := db.GetOrCreateSection("Mine", 0)

	type call struct {
		owner, repo string
		number      int
	}
	calls := make(chan call, 4)
	SetSectionPluginsHook(func(owner, repo string, number int) { calls <- call{owner, repo, number} })
	t.Cleanup(func() { SetSectionPluginsHook(nil) })

	write := func(changeType string, sectionID int64, section, identifier, workflow string) {
		applyChange(SerializedFileChange{FileChange: &FileChanges{
			ChangeType:   changeType,
			Identifier:   identifier,
			Title:        "A PR",
			SectionID:    sectionID,
			SectionName:  section,
			WorkflowName: workflow,
		}}, "")
	}
	write("Addition", mine.ID, "Mine", "C-Hipple/code-review-server-6", "mine")
	write("Addition", team.ID, "Team", "C-Hipple/code-review-server-7", "team")
	write("Update", team.ID, "Team", "C-Hipple/code-review-server-7", "team")
	write("Delete", team.ID, "Team", "C-Hipple/code-review-server-7", "team")

	// Both writes into Team call it, under the cache tables' short repo name;
	// the write into Mine and the release don't.
	want := call{"C-Hipple", "code-review-server", 7}
	for i := 0; i < 2; i++ {
		select {
		case got := <-calls:
			if got != want {
				t.Errorf("hook called with %+v, want %+v", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for hook call %d", i+1)
		}
	}
	select {
	case got := <-calls:
		t.Errorf("unexpected hook call %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}
