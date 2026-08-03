package server

import (
	"crs/config"
	"crs/database"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readCacheMissLog runs writeCacheMissLog against a temporary CRS_HOME and
// returns whatever it wrote. seed, when non-nil, populates the database before
// the report is generated.
func readCacheMissLog(t *testing.T, state cacheMissState, seed ...func(*database.DB)) string {
	t.Helper()
	crsHome := t.TempDir()
	t.Setenv("CRS_HOME", crsHome)

	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	for _, fn := range seed {
		fn(db)
	}

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

func TestWriteCacheMissLogIncludesLatestWorkflowAction(t *testing.T) {
	// The miss itself only says the data was absent. The last workflow action
	// says who last wrote this PR, when, and from which SHA — without it there's
	// no way to tell a workflow that never ran from one that ran and skipped the
	// field.
	body := readCacheMissLog(t, cacheMissState{
		owner:        "multimediallc",
		repo:         "chaturbate",
		number:       30321,
		missedFields: []string{"reviews", "commits"},
		diffHit:      true,
	}, func(db *database.DB) {
		if _, err := db.LogWorkflowAction(database.WorkflowAction{
			WorkflowName:  "review_requests",
			Action:        database.WorkflowActionCacheWrite,
			Owner:         "multimediallc",
			Repo:          "chaturbate",
			PRNumber:      30321,
			SHA:           "deadbeef",
			FieldsWritten: []string{"comments", "diff"},
			CreatedAt:     time.Now().Add(-90 * time.Second),
		}); err != nil {
			t.Fatalf("seeding workflow action: %v", err)
		}
	})

	if !strings.Contains(body, "Last Workflow Action:") {
		t.Fatalf("workflow action section missing:\n%s", body)
	}
	if !strings.Contains(body, "Workflow:        review_requests") {
		t.Errorf("workflow name missing from report:\n%s", body)
	}
	if !strings.Contains(body, "Head SHA:        deadbeef") {
		t.Errorf("head SHA missing from report:\n%s", body)
	}
	if !strings.Contains(body, "Fields written:  comments, diff") {
		t.Errorf("written field list missing from report:\n%s", body)
	}
	// Neither missed field was ever written, which is the actionable finding.
	if !strings.Contains(body, "reviews:  never written by a workflow") {
		t.Errorf("per-field history missing for reviews:\n%s", body)
	}
	if !strings.Contains(body, "commits:  never written by a workflow") {
		t.Errorf("per-field history missing for commits:\n%s", body)
	}
}

func TestWriteCacheMissLogReportsStaleFieldWrite(t *testing.T) {
	// A field written by an earlier cycle but missing now points at eviction or
	// a SHA change rather than a workflow that never covers the field, so the
	// report has to name the last write instead of saying "never".
	body := readCacheMissLog(t, cacheMissState{
		owner:        "multimediallc",
		repo:         "chaturbate",
		number:       30321,
		missedFields: []string{"reviews"},
	}, func(db *database.DB) {
		if _, err := db.LogWorkflowAction(database.WorkflowAction{
			WorkflowName:  "review_requests",
			Action:        database.WorkflowActionCacheWrite,
			Owner:         "multimediallc",
			Repo:          "chaturbate",
			PRNumber:      30321,
			SHA:           "oldsha",
			FieldsWritten: []string{"reviews"},
			CreatedAt:     time.Now().Add(-3 * time.Hour),
		}); err != nil {
			t.Fatalf("seeding workflow action: %v", err)
		}
	})

	if !strings.Contains(body, "reviews:  3h 0m ago by review_requests (sha oldsha)") {
		t.Errorf("stale write not reported:\n%s", body)
	}
}

func TestWriteCacheMissLogWithoutWorkflowActions(t *testing.T) {
	// A PR the workflows have never touched must say so plainly rather than
	// printing an empty section.
	body := readCacheMissLog(t, cacheMissState{
		owner:        "multimediallc",
		repo:         "chaturbate",
		number:       30321,
		missedFields: []string{"metadata"},
	})

	if !strings.Contains(body, "(none recorded") {
		t.Errorf("missing 'no actions recorded' note:\n%s", body)
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
