package ai

import "testing"

func TestInputsDigest(t *testing.T) {
	const (
		comments = `[{"id": 1, "body": "Rename this.", "updated_at": "2026-09-01T10:00:00Z"}, {"id": 2, "body": "Done."}]`
		reviews  = `[{"id": 700, "user": "bob", "state": "COMMENTED"}]`
		threads  = `[{"id": "PRRT_1", "is_resolved": false, "is_outdated": false, "comment_ids": [1, 2]}]`
	)
	base := InputsDigest(comments, reviews, threads)
	if len(base) != 16 {
		t.Errorf("digest %q should be 16 hex characters", base)
	}
	if again := InputsDigest(comments, reviews, threads); again != base {
		t.Error("the digest is not deterministic")
	}

	same := map[string][3]string{
		"comment order": {`[{"id": 2, "body": "Done."}, {"id": 1, "body": "Rename this."}]`, reviews, threads},
		"fields outside the inputs": {
			`[{"id": 1, "body": "Rename this.", "updated_at": "2027-01-01T00:00:00Z"}, {"id": 2, "body": "Done.", "reactions": {"+1": 3}}]`,
			`[{"id": 700, "user": "bob", "state": "COMMENTED", "body": "edited summary"}]`,
			`[{"id": "PRRT_1", "is_resolved": false, "is_outdated": false, "resolved_by": "", "path": "a.go"}]`,
		},
	}
	for name, in := range same {
		if got := InputsDigest(in[0], in[1], in[2]); got != base {
			t.Errorf("%s changed the digest", name)
		}
	}

	changed := map[string][3]string{
		"an edited comment":     {`[{"id": 1, "body": "Rename this, please."}, {"id": 2, "body": "Done."}]`, reviews, threads},
		"a new comment":         {`[{"id": 1, "body": "Rename this."}, {"id": 2, "body": "Done."}, {"id": 3, "body": "Also..."}]`, reviews, threads},
		"a review state change": {comments, `[{"id": 700, "user": "bob", "state": "DISMISSED"}]`, threads},
		"a new review":          {comments, `[{"id": 700, "state": "COMMENTED"}, {"id": 701, "state": "APPROVED"}]`, threads},
		"a resolved thread":     {comments, reviews, `[{"id": "PRRT_1", "is_resolved": true, "is_outdated": false}]`},
		"an outdated thread":    {comments, reviews, `[{"id": "PRRT_1", "is_resolved": false, "is_outdated": true}]`},
		"threads not cached":    {comments, reviews, ""},
		"comments not cached":   {"", reviews, threads},
		"unreadable threads":    {comments, reviews, `{not json`},
	}
	for name, in := range changed {
		if got := InputsDigest(in[0], in[1], in[2]); got == base {
			t.Errorf("%s did not change the digest", name)
		}
	}

	if InputsDigest("", "", "") == InputsDigest("[]", "[]", "[]") {
		t.Error("an absent cache entry must hash differently from an empty one")
	}
	if InputsDigest(comments, reviews, `{not json`) == InputsDigest(comments, reviews, `{also not json`) {
		t.Error("unreadable entries should still hash by content")
	}
}
