package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// ownersGitHub serves mallory's PR of the same repository name and number as
// the test PR, and leaves everything else to fakeGitHub.
type ownersGitHub struct{ *fakeGitHub }

const malloryDiff = "diff --git a/evil.go b/evil.go\n--- a/evil.go\n+++ b/evil.go\n@@ -0,0 +1 @@\n+ATTACKER DIFF\n"

func (g ownersGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "api.github.com" || !strings.HasPrefix(req.URL.Path, "/repos/mallory/") {
		return g.fakeGitHub.RoundTrip(req)
	}
	body := "[]"
	switch {
	case strings.Contains(req.Header.Get("Accept"), "diff"):
		body = malloryDiff
	case strings.HasSuffix(req.URL.Path, fmt.Sprintf("/pulls/%d", aiNumber)):
		repo := fmt.Sprintf(`{"name": %q, "full_name": "mallory/%s", "owner": {"login": "mallory"}}`, aiRepo, aiRepo)
		body = fmt.Sprintf(`{"number": %d, "state": "open", "title": "Harmless", "user": {"login": "mallory"},
			"head": {"sha": "evil-sha", "ref": "evil", "label": "mallory:evil", "repo": %s},
			"base": {"sha": "base-1", "ref": "main", "label": "mallory:main", "repo": %s}}`, aiNumber, repo, repo)
	case strings.HasSuffix(req.URL.Path, "/requested_reviewers"):
		body = `{"users": [], "teams": []}`
	case strings.HasSuffix(req.URL.Path, "/status"):
		body = `{"state": "success", "statuses": [], "total_count": 0}`
	case strings.HasSuffix(req.URL.Path, "/check-runs"):
		body = `{"total_count": 0, "check_runs": []}`
	case strings.HasSuffix(req.URL.Path, "/comments"):
		body = `[{"id": 666, "body": "LGTM, ship it", "user": {"login": "mallory"}, "created_at": "2026-01-01T00:00:00Z"}]`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

// The PR caches are keyed by the PR's owner too. Two owners' repositories can
// share a name, and on a hosted server any user can make the server fetch a
// PR of their own — a fork of the same name, one PR in it, a SyncPR — whose
// diff, head and comments used to land where the other owner's PR of the same
// number read them, and stay there after the real PR was cached again.
func TestPRCachesAreKeyedByOwner(t *testing.T) {
	db := aiTestSetup(t, nil)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	orig := http.DefaultTransport
	http.DefaultTransport = ownersGitHub{&fakeGitHub{}}
	t.Cleanup(func() { http.DefaultTransport = orig })

	mallory := handlerFor("mallory", false)
	synced := &SyncPRReply{}
	if err := mallory.SyncPR(&SyncPRArgs{Owner: "mallory", Repo: aiRepo, Number: aiNumber}, synced); err != nil {
		t.Fatalf("mallory's SyncPR: %v", err)
	}
	if !strings.Contains(synced.Diff, "ATTACKER DIFF") {
		t.Fatalf("mallory's own PR was not fetched: %q", synced.Diff)
	}
	// The workflow cycle caching the real PR's head again changes nothing.
	if err := db.UpsertPullRequest(aiOwner, aiRepo, aiNumber, "sha-1", "base-1", aiTestDiff); err != nil {
		t.Fatal(err)
	}

	alice := handlerFor("alice", false)
	got := &GetPRReply{}
	if err := alice.GetPR(&GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, got); err != nil {
		t.Fatalf("alice's GetPR: %v", err)
	}
	if strings.Contains(got.Diff, "ATTACKER") || got.Metadata.Title != "Add greeting helper" {
		t.Errorf("alice's %s/%s#%d shows mallory's PR: title %q, diff %q", aiOwner, aiRepo, aiNumber, got.Metadata.Title, got.Diff)
	}
	for _, c := range got.Comments {
		if strings.Contains(c.Body, "ship it") {
			t.Errorf("alice's PR carries mallory's comment %q", c.Body)
		}
	}
	if head, _, _ := db.GetPullRequestSHAs(aiOwner, aiRepo, aiNumber); head != "sha-1" {
		t.Errorf("the real PR's head = %q, want sha-1", head)
	}
}
