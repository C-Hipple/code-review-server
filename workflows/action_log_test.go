package workflows

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"encoding/json"
	"testing"

	"github.com/google/go-github/v74/github"
)

func TestParsePRIdentifier(t *testing.T) {
	cases := []struct {
		identifier string
		owner      string
		repo       string
		number     int
		ok         bool
	}{
		// Dashes appear in both owner and repo names, so the split has to be on
		// the last dash rather than the first.
		{"C-hipple/code-review-server-207", "C-hipple", "code-review-server", 207, true},
		{"multimediallc/chaturbate-30321", "multimediallc", "chaturbate", 30321, true},
		{"owner/repo-1", "owner", "repo", 1, true},
		// Non-PR or malformed identifiers must be rejected rather than logged
		// with half-empty coordinates.
		{"repo-207", "", "", 0, false},
		{"owner/repo", "", "", 0, false},
		{"owner/repo-abc", "", "", 0, false},
		{"owner/repo-0", "", "", 0, false},
		{"", "", "", 0, false},
	}

	for _, c := range cases {
		owner, repo, number, ok := parsePRIdentifier(c.identifier)
		if ok != c.ok || owner != c.owner || repo != c.repo || number != c.number {
			t.Errorf("parsePRIdentifier(%q) = (%q, %q, %d, %v), want (%q, %q, %d, %v)",
				c.identifier, owner, repo, number, ok, c.owner, c.repo, c.number, c.ok)
		}
	}
}

func TestLogItemActionRecordsWorkflowAndSHA(t *testing.T) {
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	change := &FileChanges{
		ChangeType:   "Update",
		Identifier:   "C-hipple/code-review-server-207",
		SectionName:  "Review Requests",
		WorkflowName: "review_requests",
		HeadSHA:      "abc123",
	}
	logItemAction(change, []string{"status", "tags"}, "")

	// The action log is keyed by the short repo name, matching every other
	// cache table, so a lookup from the cache miss report finds it.
	latest, err := db.GetLatestWorkflowAction("C-hipple", "code-review-server", 207)
	if err != nil {
		t.Fatalf("GetLatestWorkflowAction: %v", err)
	}
	if latest == nil {
		t.Fatal("no action recorded for an applied item update")
	}
	if latest.Action != "Update" || latest.WorkflowName != "review_requests" {
		t.Errorf("wrong action recorded: %+v", latest)
	}
	if latest.SHA != "abc123" {
		t.Errorf("head SHA = %q, want %q", latest.SHA, "abc123")
	}
	if latest.SectionName != "Review Requests" {
		t.Errorf("section = %q, want %q", latest.SectionName, "Review Requests")
	}
	if latest.FieldsSummary() != "status, tags" {
		t.Errorf("fields = %q, want %q", latest.FieldsSummary(), "status, tags")
	}
}

func TestLogItemActionSkipsNonPRIdentifiers(t *testing.T) {
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	logItemAction(&FileChanges{
		ChangeType:   "Update",
		Identifier:   "some-freeform-item",
		WorkflowName: "wf",
	}, []string{"status"}, "")

	actions, err := db.GetWorkflowActions("", "", 0, 0)
	if err != nil {
		t.Fatalf("GetWorkflowActions: %v", err)
	}
	if len(actions) != 0 {
		t.Errorf("unparseable identifier logged with empty coordinates: %+v", actions)
	}
}

func TestDescribeAuxRequest(t *testing.T) {
	// A cache write where nothing landed is the case worth explaining: the
	// field list alone would just be empty.
	if got := describeAuxRequest(&PRAuxData{HeadSHA: "abc"}, nil); got == "" {
		t.Error("expected a note when no cache fields were written")
	}
	if got := describeAuxRequest(&PRAuxData{}, []string{"comments"}); got == "" {
		t.Error("expected a note when the head SHA is unknown")
	}
	if got := describeAuxRequest(&PRAuxData{HeadSHA: "abc"}, []string{"comments"}); got != "" {
		t.Errorf("expected no note on a normal write, got %q", got)
	}
}

func TestPersistPRCacheDataLogsWrittenFields(t *testing.T) {
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	key := PRKey{Owner: "C-hipple", Repo: "code-review-server", Number: 207}
	auxData := &PRAuxData{HeadSHA: "abc123", Diff: "diff --git a/foo b/foo\n"}

	// Reviews come back as an empty (non-nil) slice — a PR with no reviews still
	// writes the cache row, so "reviews" must appear in the log.
	persistPRCacheData("review_requests", key, nil, auxData, nil,
		[]*github.PullRequestReview{}, nil, nil, nil)

	latest, err := db.GetLatestWorkflowAction(key.Owner, key.Repo, key.Number)
	if err != nil {
		t.Fatalf("GetLatestWorkflowAction: %v", err)
	}
	if latest == nil {
		t.Fatal("no CacheWrite action recorded")
	}
	if latest.Action != database.WorkflowActionCacheWrite {
		t.Errorf("action = %q, want %q", latest.Action, database.WorkflowActionCacheWrite)
	}
	if latest.WorkflowName != "review_requests" {
		t.Errorf("workflow = %q, want %q", latest.WorkflowName, "review_requests")
	}
	if latest.SHA != "abc123" {
		t.Errorf("sha = %q, want %q", latest.SHA, "abc123")
	}

	fields := latest.FieldsWritten
	if !contains(fields, "diff") || !contains(fields, "reviews") {
		t.Errorf("fields = %v, want diff and reviews", fields)
	}
	// Nothing was fetched for these, so claiming them would make the log lie
	// about why a later read misses.
	if contains(fields, "comments") || contains(fields, "commits") || contains(fields, "metadata") {
		t.Errorf("fields = %v, want no comments/commits/metadata", fields)
	}
	if contains(fields, "review_threads") {
		t.Errorf("fields = %v, want no review_threads when none were fetched", fields)
	}
}

func TestPersistPRCacheDataCachesReviewThreads(t *testing.T) {
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	key := PRKey{Owner: "C-hipple", Repo: "code-review-server", Number: 208}
	auxData := &PRAuxData{
		HeadSHA: "abc123",
		ReviewThreads: []git_tools.ReviewThread{
			{ID: "T1", IsResolved: true, ResolvedBy: "alice", CommentIDs: []int64{11, 12}},
		},
	}

	persistPRCacheData("review_requests", key, nil, auxData, nil, nil, nil, nil, nil)

	// The row has to be readable back through the same accessor GetPRDetails
	// uses, in the shape it expects, or the warm buys nothing.
	cached, err := db.GetPRReviewThreads(key.Number, key.Repo)
	if err != nil {
		t.Fatalf("GetPRReviewThreads: %v", err)
	}
	var threads []git_tools.ReviewThread
	if err := json.Unmarshal([]byte(cached), &threads); err != nil {
		t.Fatalf("cached review threads are not readable: %v", err)
	}
	if len(threads) != 1 || threads[0].ID != "T1" || !threads[0].IsResolved {
		t.Fatalf("cached threads = %+v, want the resolved T1 thread", threads)
	}
	if len(threads[0].CommentIDs) != 2 {
		t.Errorf("comment ids = %v, want both preserved", threads[0].CommentIDs)
	}

	latest, err := db.GetLatestWorkflowAction(key.Owner, key.Repo, key.Number)
	if err != nil {
		t.Fatalf("GetLatestWorkflowAction: %v", err)
	}
	if latest == nil || !contains(latest.FieldsWritten, "review_threads") {
		t.Errorf("fields = %v, want review_threads recorded", latest)
	}
}

// A failed or unrequested GraphQL fetch leaves ReviewThreads nil. Blanking the
// cache row in that case would turn a transient error into lost resolution
// state for everyone who opens the PR before the next successful cycle.
func TestPersistPRCacheDataKeepsReviewThreadsWhenNotFetched(t *testing.T) {
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	key := PRKey{Owner: "C-hipple", Repo: "code-review-server", Number: 209}
	if err := db.UpsertPRReviewThreads(key.Number, key.Repo, `[{"id":"T1","is_resolved":true}]`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	persistPRCacheData("review_requests", key,
		nil, &PRAuxData{HeadSHA: "abc123"}, nil, nil, nil, nil, nil)

	cached, err := db.GetPRReviewThreads(key.Number, key.Repo)
	if err != nil {
		t.Fatalf("GetPRReviewThreads: %v", err)
	}
	if cached != `[{"id":"T1","is_resolved":true}]` {
		t.Errorf("cached = %q, want the previously cached row left intact", cached)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
