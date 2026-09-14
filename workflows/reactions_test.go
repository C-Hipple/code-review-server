package workflows

import (
	"crs/git_tools"
	"encoding/json"
	"testing"
)

func TestPersistPRCacheDataStoresReactions(t *testing.T) {
	cfg := teamReviewTestConfig(t)
	key := PRKey{Owner: "acme", Repo: "widgets", Number: 21}

	auxData := &PRAuxData{
		HeadSHA: "abc123",
		Reactions: &git_tools.PRReactions{
			Comments: map[string][]git_tools.Reaction{
				"11": {{Content: "+1", Emoji: "👍", Users: []string{"erin"}, Count: 1}},
			},
			Reviews: map[string][]git_tools.Reaction{},
		},
	}
	persistPRCacheData("test-workflow", key, nil, auxData, nil, nil, nil, nil, nil)

	raw, err := cfg.DB.GetPRReactions(key.Number, key.Repo)
	if err != nil {
		t.Fatalf("GetPRReactions: %v", err)
	}
	var stored git_tools.PRReactions
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("stored row is not decodable: %v (%q)", err, raw)
	}
	if got := stored.ForComment("11"); len(got) != 1 || got[0].Users[0] != "erin" {
		t.Errorf("stored = %+v, want erin's thumbs-up on comment 11", stored)
	}
}

// A cycle that did not fetch reactions must leave whatever a previous cycle
// stored alone, rather than writing an empty row over it.
func TestPersistPRCacheDataLeavesReactionsAloneWhenNotFetched(t *testing.T) {
	cfg := teamReviewTestConfig(t)
	key := PRKey{Owner: "acme", Repo: "widgets", Number: 22}

	if err := cfg.DB.UpsertPRReactions(key.Number, key.Repo,
		`{"comments":{"11":[{"content":"+1","emoji":"👍","users":["erin"],"count":1}]},"reviews":{}}`); err != nil {
		t.Fatalf("seeding the cache: %v", err)
	}

	persistPRCacheData("test-workflow", key, nil, &PRAuxData{HeadSHA: "abc123"}, nil, nil, nil, nil, nil)

	raw, err := cfg.DB.GetPRReactions(key.Number, key.Repo)
	if err != nil {
		t.Fatalf("GetPRReactions: %v", err)
	}
	var stored git_tools.PRReactions
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("stored row is not decodable: %v (%q)", err, raw)
	}
	if len(stored.ForComment("11")) != 1 {
		t.Errorf("stored = %+v, want the previously cached reaction to survive", stored)
	}
}

// Asking a cycle for a PR's comments asks it for the reactions on them: they
// change without the head SHA moving, so the push-triggered warm alone would
// leave them stale.
func TestCommentsRequirementImpliesReactions(t *testing.T) {
	got := applyAuxImplications(AuxDataRequirement{Comments: true})
	if !got.Reactions {
		t.Errorf("got %+v, want a comments fetch to bring the reactions with it", got)
	}
	// Nothing else is dragged along by it.
	if got != (AuxDataRequirement{Comments: true, Reactions: true}) {
		t.Errorf("got %+v, want only Comments and Reactions", got)
	}
	// And a request that wants no comments is left alone.
	if quiet := applyAuxImplications(AuxDataRequirement{Diff: true}); quiet.Reactions {
		t.Errorf("got %+v, want no reactions fetch for a diff-only request", quiet)
	}
}
