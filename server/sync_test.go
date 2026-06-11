package server

import (
	"encoding/json"
	"testing"

	"github.com/google/go-github/v48/github"
)

// commentsJSONWithIDs builds a PRComments cache entry containing comments with
// the given IDs, matching the format UpsertPRComments stores.
func commentsJSONWithIDs(t *testing.T, ids ...int64) string {
	t.Helper()
	comments := []*github.PullRequestComment{}
	for _, id := range ids {
		comments = append(comments, &github.PullRequestComment{
			ID:   github.Int64(id),
			Body: github.String("comment body"),
		})
	}
	raw, err := json.Marshal(comments)
	if err != nil {
		t.Fatalf("failed to marshal comments: %v", err)
	}
	return string(raw)
}

func TestSyncDetectedChanges(t *testing.T) {
	tests := []struct {
		name            string
		oldSHA          string
		newSHA          string
		oldCommentsJSON string
		newCommentsJSON string
		expected        bool
	}{
		{
			name:     "no changes and no comments",
			oldSHA:   "abc123",
			newSHA:   "abc123",
			expected: false,
		},
		{
			name:     "new head SHA",
			oldSHA:   "abc123",
			newSHA:   "def456",
			expected: true,
		},
		{
			name:     "no previous cache counts as updated",
			oldSHA:   "",
			newSHA:   "abc123",
			expected: true,
		},
		{
			name:            "new comment",
			oldSHA:          "abc123",
			newSHA:          "abc123",
			oldCommentsJSON: commentsJSONWithIDs(t, 1, 2),
			newCommentsJSON: commentsJSONWithIDs(t, 1, 2, 3),
			expected:        true,
		},
		{
			name:            "first comment on PR with empty cache entry",
			oldSHA:          "abc123",
			newSHA:          "abc123",
			oldCommentsJSON: "",
			newCommentsJSON: commentsJSONWithIDs(t, 1),
			expected:        true,
		},
		{
			name:            "comment deleted is not an update",
			oldSHA:          "abc123",
			newSHA:          "abc123",
			oldCommentsJSON: commentsJSONWithIDs(t, 1, 2),
			newCommentsJSON: commentsJSONWithIDs(t, 1),
			expected:        false,
		},
		{
			name:            "unchanged comments",
			oldSHA:          "abc123",
			newSHA:          "abc123",
			oldCommentsJSON: commentsJSONWithIDs(t, 1, 2),
			newCommentsJSON: commentsJSONWithIDs(t, 1, 2),
			expected:        false,
		},
		{
			name:            "new SHA and new comments",
			oldSHA:          "abc123",
			newSHA:          "def456",
			oldCommentsJSON: commentsJSONWithIDs(t, 1),
			newCommentsJSON: commentsJSONWithIDs(t, 1, 2),
			expected:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := syncDetectedChanges(tt.oldSHA, tt.newSHA, tt.oldCommentsJSON, tt.newCommentsJSON)
			if got != tt.expected {
				t.Errorf("syncDetectedChanges(%q, %q, ...) = %v, want %v", tt.oldSHA, tt.newSHA, got, tt.expected)
			}
		})
	}
}

func TestCachedCommentIDs(t *testing.T) {
	if got := cachedCommentIDs(""); len(got) != 0 {
		t.Errorf("expected empty set for empty JSON, got %v", got)
	}
	if got := cachedCommentIDs("not json"); len(got) != 0 {
		t.Errorf("expected empty set for malformed JSON, got %v", got)
	}

	ids := cachedCommentIDs(commentsJSONWithIDs(t, 10, 20))
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d", len(ids))
	}
	for _, want := range []int64{10, 20} {
		if _, ok := ids[want]; !ok {
			t.Errorf("expected ID %d in set %v", want, ids)
		}
	}
}
