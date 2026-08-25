package server

import (
	"encoding/json"
	"testing"

	"github.com/google/go-github/v74/github"
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

// reviewsJSONWith builds a PRReviews cache entry from id/state pairs, matching
// the format UpsertPRReviews stores.
func reviewsJSONWith(t *testing.T, idStates map[int64]string) string {
	t.Helper()
	reviews := []ReviewJSON{}
	for id, state := range idStates {
		reviews = append(reviews, ReviewJSON{ID: id, State: state, User: "reviewer"})
	}
	raw, err := json.Marshal(reviews)
	if err != nil {
		t.Fatalf("failed to marshal reviews: %v", err)
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
		oldReviewsJSON  string
		newReviewsJSON  string
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
		{
			// The reported bug: an approval carries no body and no inline
			// comment, so nothing but the reviews cache changes.
			name:           "bare approval with no comment and no push",
			oldSHA:         "abc123",
			newSHA:         "abc123",
			oldReviewsJSON: reviewsJSONWith(t, map[int64]string{1: "COMMENTED"}),
			newReviewsJSON: reviewsJSONWith(t, map[int64]string{1: "COMMENTED", 2: "APPROVED"}),
			expected:       true,
		},
		{
			name:           "first review on PR with empty cache entry",
			oldSHA:         "abc123",
			newSHA:         "abc123",
			oldReviewsJSON: "",
			newReviewsJSON: reviewsJSONWith(t, map[int64]string{1: "APPROVED"}),
			expected:       true,
		},
		{
			// A dismissal keeps the review's ID and rewrites its state, and it
			// puts the PR back on the reviewer's plate.
			name:           "existing review dismissed in place",
			oldSHA:         "abc123",
			newSHA:         "abc123",
			oldReviewsJSON: reviewsJSONWith(t, map[int64]string{1: "APPROVED"}),
			newReviewsJSON: reviewsJSONWith(t, map[int64]string{1: "DISMISSED"}),
			expected:       true,
		},
		{
			name:           "unchanged reviews",
			oldSHA:         "abc123",
			newSHA:         "abc123",
			oldReviewsJSON: reviewsJSONWith(t, map[int64]string{1: "APPROVED", 2: "COMMENTED"}),
			newReviewsJSON: reviewsJSONWith(t, map[int64]string{1: "APPROVED", 2: "COMMENTED"}),
			expected:       false,
		},
		{
			name:           "review deleted is not an update",
			oldSHA:         "abc123",
			newSHA:         "abc123",
			oldReviewsJSON: reviewsJSONWith(t, map[int64]string{1: "APPROVED", 2: "COMMENTED"}),
			newReviewsJSON: reviewsJSONWith(t, map[int64]string{1: "APPROVED"}),
			expected:       false,
		},
		{
			name:            "comments and reviews both unchanged",
			oldSHA:          "abc123",
			newSHA:          "abc123",
			oldCommentsJSON: commentsJSONWithIDs(t, 1, 2),
			newCommentsJSON: commentsJSONWithIDs(t, 1, 2),
			oldReviewsJSON:  reviewsJSONWith(t, map[int64]string{1: "APPROVED"}),
			newReviewsJSON:  reviewsJSONWith(t, map[int64]string{1: "APPROVED"}),
			expected:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := syncSnapshot{sha: tt.oldSHA, commentsJSON: tt.oldCommentsJSON, reviewsJSON: tt.oldReviewsJSON}
			after := syncSnapshot{sha: tt.newSHA, commentsJSON: tt.newCommentsJSON, reviewsJSON: tt.newReviewsJSON}
			got := syncDetectedChanges(before, after)
			if got != tt.expected {
				t.Errorf("syncDetectedChanges(%q -> %q, ...) = %v, want %v", tt.oldSHA, tt.newSHA, got, tt.expected)
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
