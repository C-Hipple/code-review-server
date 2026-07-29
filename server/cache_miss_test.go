package server

import (
	"crs/config"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readCacheMissLog runs writeCacheMissLog against a temporary CRS_HOME and
// returns whatever it wrote.
func readCacheMissLog(t *testing.T, state cacheMissState) string {
	t.Helper()
	crsHome := t.TempDir()
	t.Setenv("CRS_HOME", crsHome)

	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	writeCacheMissLog(state)

	body, err := os.ReadFile(filepath.Join(crsHome, "cache_miss.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("reading cache miss log: %v", err)
	}
	return string(body)
}

func TestWriteCacheMissLogSeparatesForcedRefreshes(t *testing.T) {
	// A SyncPR-driven refresh bypasses the caches on purpose. It must not be
	// filed under "Cache Miss Report", or grepping the log for real misses
	// drowns in entries the user asked for.
	forced := readCacheMissLog(t, cacheMissState{
		owner:        "multimediallc",
		repo:         "chaturbate",
		number:       30321,
		skipCache:    true,
		missedFields: []string{"metadata", "diff", "comments", "commits"},
		metadataHit:  true,
		diffHit:      true,
	})

	if strings.Contains(forced, "=== Cache Miss Report ===") {
		t.Errorf("forced refresh filed as a cache miss:\n%s", forced)
	}
	if !strings.Contains(forced, "=== Forced Refresh Report ===") {
		t.Errorf("missing forced refresh header:\n%s", forced)
	}
	if !strings.Contains(forced, "Fetched:  metadata, diff, comments, commits") {
		t.Errorf("forced refresh should report refetched fields:\n%s", forced)
	}
	// The probed state is reported as-is rather than blanket MISS.
	if !strings.Contains(forced, "metadata  (PRMetadataCache):  HIT") {
		t.Errorf("forced refresh should report the real pre-request cache state:\n%s", forced)
	}
	if !strings.Contains(forced, "reviews   (PRReviews):        MISS") {
		t.Errorf("forced refresh should report reviews as genuinely absent:\n%s", forced)
	}
}

func TestWriteCacheMissLogReportsFetchError(t *testing.T) {
	// GetPRDetails aborts on a GitHub error before it reaches the diff/comments
	// blocks, so the field list alone understates what happened.
	body := readCacheMissLog(t, cacheMissState{
		owner:        "multimediallc",
		repo:         "chaturbate",
		number:       30321,
		missedFields: []string{"metadata"},
		fetchErr:     errors.New("GET https://api.github.com/...: 403 API rate limit exceeded"),
	})

	if !strings.Contains(body, "Error:    GET https://api.github.com/...: 403 API rate limit exceeded") {
		t.Errorf("fetch error missing from report:\n%s", body)
	}
	if !strings.Contains(body, "request aborted here") {
		t.Errorf("report should explain that later fields were never reached:\n%s", body)
	}
}

func TestWriteCacheMissLogSkipsCleanRequests(t *testing.T) {
	if body := readCacheMissLog(t, cacheMissState{
		owner:  "multimediallc",
		repo:   "chaturbate",
		number: 30306,
	}); body != "" {
		t.Errorf("nothing missed, but a report was written:\n%s", body)
	}
}

func TestGetPRReviewsCachedEmptyIsNotNil(t *testing.T) {
	const (
		repo  = "chaturbate"
		owner = "multimediallc"
	)
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	// "null" is what rows written before reviews were cached as "[]" hold. Both
	// spellings must come back as an empty, non-nil slice so callers can tell
	// "this PR has no reviews" from "reviews were never loaded".
	for _, cached := range []string{"null", "[]"} {
		if err := db.UpsertPRReviews(30306, repo, cached); err != nil {
			t.Fatalf("seeding %q: %v", cached, err)
		}
		reviews, err := GetPRReviews(owner, repo, 30306, false)
		if err != nil {
			t.Fatalf("GetPRReviews with cached %q: %v", cached, err)
		}
		if reviews == nil {
			t.Errorf("cached %q returned a nil slice; a zero-review PR would be re-fetched from GitHub", cached)
		}
		if len(reviews) != 0 {
			t.Errorf("cached %q returned %d reviews, want 0", cached, len(reviews))
		}
	}
}
