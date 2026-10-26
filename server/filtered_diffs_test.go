package server

import (
	"context"
	"crs/config"
	"crs/database"
	"crs/filtereddiff"
	"crs/git_tools"
	"crs/utils"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

const fdOwner, fdRepo, fdNumber = "acme", "widgets", 77

var fdT0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func fdAt(minutes int) time.Time { return fdT0.Add(time.Duration(minutes) * time.Minute) }

const fdDiff = `diff --git a/greet.go b/greet.go
index 1111111..2222222 100644
--- a/greet.go
+++ b/greet.go
@@ -1,3 +1,4 @@
 package greet

+// Hello says hello.
 func Hello() {}
`

// fakeCompare stands in for GitHub's compare endpoint.
type fakeCompare struct {
	diff    string
	diffErr error
	status  string
	// release, when set, holds each Diff call until it is closed.
	release     chan struct{}
	diffCalls   atomic.Int32
	statusCalls atomic.Int32
	mu          sync.Mutex
	bases       []string
}

func (f *fakeCompare) Diff(_ context.Context, owner, repo, base, head string) (string, error) {
	f.diffCalls.Add(1)
	f.mu.Lock()
	f.bases = append(f.bases, base)
	f.mu.Unlock()
	if f.release != nil {
		<-f.release
	}
	return f.diff, f.diffErr
}

func (f *fakeCompare) Status(_ context.Context, owner, repo, base, head string) (string, error) {
	f.statusCalls.Add(1)
	return f.status, nil
}

func useFakeCompare(t *testing.T, f *fakeCompare) {
	t.Helper()
	orig := filteredDiffSource
	filteredDiffSource = f
	t.Cleanup(func() { filteredDiffSource = orig })
}

// fdSetup seeds a PR whose head is c4, with commits c1..c4 and the given
// reviews and comments, reviewed as "me".
func fdSetup(t *testing.T, reviews []ReviewJSON, comments []*github.PullRequestComment) *database.DB {
	t.Helper()
	t.Setenv("CRS_HOME", t.TempDir())
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, GithubUsername: "me"})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.UpsertPullRequest(fdNumber, fdRepo, "c4", "base", "full diff"))
	var commits []CommitJSON
	for i, sha := range []string{"c1", "c2", "c3", "c4"} {
		commits = append(commits, CommitJSON{SHA: sha, Date: fdAt(10 * i).Format(time.RFC3339)})
	}
	raw, _ := json.Marshal(commits)
	must(db.UpsertPRCommits(fdNumber, fdRepo, string(raw)))
	if reviews == nil {
		reviews = []ReviewJSON{}
	}
	raw, _ = json.Marshal(reviews)
	must(db.UpsertPRReviews(fdNumber, fdRepo, string(raw)))
	if comments != nil {
		raw, _ = json.Marshal(comments)
		must(db.UpsertPRComments(fdNumber, fdRepo, string(raw)))
	}
	return db
}

func myReview(at time.Time, commit string) ReviewJSON {
	return ReviewJSON{ID: 1, User: "me", State: "APPROVED", SubmittedAt: at, CommitID: commit}
}

func getFD(t *testing.T, filter string) filtereddiff.Result {
	t.Helper()
	res, err := GetFilteredDiff(fdOwner, fdRepo, fdNumber, filter)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestFilteredDiffSinceLastReviewIsFetchedOnceThenServedFromTheDB(t *testing.T) {
	fdSetup(t, []ReviewJSON{myReview(fdAt(12), "c2")}, nil)
	fake := &fakeCompare{diff: fdDiff}
	useFakeCompare(t, fake)

	res := getFD(t, filtereddiff.SinceLastReview)
	if res.Status != filtereddiff.StatusReady || res.BaseSHA != "c2" || res.HeadSHA != "c4" {
		t.Fatalf("result = %+v, want ready from c2 to c4", res)
	}
	if !strings.Contains(res.Diff, "Hello says hello.") || !strings.Contains(res.Diff, "+++ b/greet.go") {
		t.Fatalf("diff = %q, want the compare's diff rendered", res.Diff)
	}
	if len(res.Commits) != 2 || res.Commits[0] != "c3" || res.Commits[1] != "c4" {
		t.Fatalf("commits = %v, want [c3 c4]", res.Commits)
	}
	if res.Baseline == nil || res.Baseline.Kind != filtereddiff.KindReview || res.ComputedAt.IsZero() {
		t.Fatalf("baseline = %+v, computed_at = %v", res.Baseline, res.ComputedAt)
	}
	// The ancestry is known from the cached commits, so GitHub is only asked
	// for the diff.
	if fake.statusCalls.Load() != 0 || fake.diffCalls.Load() != 1 {
		t.Fatalf("status calls = %d, diff calls = %d, want 0 and 1", fake.statusCalls.Load(), fake.diffCalls.Load())
	}

	again := getFD(t, filtereddiff.SinceLastReview)
	if again.Diff != res.Diff || fake.diffCalls.Load() != 1 {
		t.Fatalf("second read made %d diff calls in all, want it served from the DB", fake.diffCalls.Load())
	}

	// The interaction filter lands on the same review, so it shares the diff.
	if got := getFD(t, filtereddiff.SinceLastInteraction); got.BaseSHA != "c2" || got.Diff != res.Diff || fake.diffCalls.Load() != 1 {
		t.Fatalf("since-last-interaction = %+v after %d diff calls, want the shared diff", got, fake.diffCalls.Load())
	}
}

func TestFilteredDiffWithoutAReviewHasNoBaselineAndNoFetch(t *testing.T) {
	fdSetup(t, []ReviewJSON{{ID: 1, User: "someone", State: "APPROVED", SubmittedAt: fdAt(12), CommitID: "c2"}}, nil)
	fake := &fakeCompare{diff: fdDiff}
	useFakeCompare(t, fake)

	res := getFD(t, filtereddiff.SinceLastReview)
	if res.Status != filtereddiff.StatusNoBaseline || res.Message == "" || res.Diff != "" || res.Commits == nil {
		t.Fatalf("result = %+v, want no-baseline with a message and an empty commit list", res)
	}
	if fake.diffCalls.Load()+fake.statusCalls.Load() != 0 {
		t.Fatal("a PR with no baseline reached GitHub")
	}
}

func TestFilteredDiffAtTheReviewedHeadIsEmptyWithoutAFetch(t *testing.T) {
	fdSetup(t, []ReviewJSON{myReview(fdAt(40), "c4")}, nil)
	fake := &fakeCompare{diff: fdDiff}
	useFakeCompare(t, fake)

	res := getFD(t, filtereddiff.SinceLastReview)
	if res.Status != filtereddiff.StatusReady || res.Diff != "" || len(res.Commits) != 0 {
		t.Fatalf("result = %+v, want ready and empty", res)
	}
	if fake.diffCalls.Load() != 0 {
		t.Fatal("nothing was pushed since the review, yet GitHub was asked for a diff")
	}
}

func TestFilteredDiffFromACommitTheHistoryLost(t *testing.T) {
	t.Run("rewritten by a force-push", func(t *testing.T) {
		fdSetup(t, []ReviewJSON{myReview(fdAt(12), "gone")}, nil)
		fake := &fakeCompare{diff: fdDiff, status: git_tools.CompareDiverged}
		useFakeCompare(t, fake)

		res := getFD(t, filtereddiff.SinceLastReview)
		if res.Status != filtereddiff.StatusRebased || res.Diff != "" || !strings.Contains(res.Message, "gone") {
			t.Fatalf("result = %+v, want rebased with a message naming the commit", res)
		}
		if fake.statusCalls.Load() != 1 || fake.diffCalls.Load() != 0 {
			t.Fatalf("status calls = %d, diff calls = %d, want 1 and 0", fake.statusCalls.Load(), fake.diffCalls.Load())
		}
		// Stored like any other answer.
		getFD(t, filtereddiff.SinceLastReview)
		if fake.statusCalls.Load() != 1 {
			t.Fatal("a rebased answer was asked for again instead of served from the DB")
		}
	})

	t.Run("only missing from a truncated commit list", func(t *testing.T) {
		fdSetup(t, []ReviewJSON{myReview(fdAt(12), "c0")}, nil)
		fake := &fakeCompare{diff: fdDiff, status: git_tools.CompareAhead}
		useFakeCompare(t, fake)

		res := getFD(t, filtereddiff.SinceLastReview)
		if res.Status != filtereddiff.StatusReady || res.Diff == "" {
			t.Fatalf("result = %+v, want the diff GitHub confirmed is sound", res)
		}
	})

	t.Run("collected since", func(t *testing.T) {
		fdSetup(t, []ReviewJSON{myReview(fdAt(12), "c2")}, nil)
		fake := &fakeCompare{diffErr: git_tools.ErrNoCommonHistory}
		useFakeCompare(t, fake)

		if res := getFD(t, filtereddiff.SinceLastReview); res.Status != filtereddiff.StatusRebased {
			t.Fatalf("result = %+v, want rebased", res)
		}
	})
}

func TestFilteredDiffFailureIsServedUntilItIsRetried(t *testing.T) {
	db := fdSetup(t, []ReviewJSON{myReview(fdAt(12), "c2")}, nil)
	fake := &fakeCompare{diffErr: errors.New("GitHub is down")}
	useFakeCompare(t, fake)

	res := getFD(t, filtereddiff.SinceLastReview)
	if res.Status != filtereddiff.StatusError || !strings.Contains(res.Message, "GitHub is down") {
		t.Fatalf("result = %+v, want the failure", res)
	}
	getFD(t, filtereddiff.SinceLastReview)
	if fake.diffCalls.Load() != 1 {
		t.Fatalf("a fresh failure was retried at once (%d calls)", fake.diffCalls.Load())
	}

	// Once the failure is old enough, the next read tries again.
	if _, err := db.Exec(`UPDATE PRFilteredDiffs SET updated_at = ?`, time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	fake.diffErr = nil
	fake.diff = fdDiff
	if res := getFD(t, filtereddiff.SinceLastReview); res.Status != filtereddiff.StatusReady || fake.diffCalls.Load() != 2 {
		t.Fatalf("result = %+v after %d calls, want a retry that succeeded", res, fake.diffCalls.Load())
	}
}

func TestFilteredDiffSinceLastInteractionStartsAtMyLatestComment(t *testing.T) {
	comments := []*github.PullRequestComment{
		// A conversation comment names no commit: the head when it was
		// posted (c3, committed at +20m) stands in.
		{ID: github.Ptr(int64(9)), User: &github.User{Login: github.Ptr("me")}, CreatedAt: &github.Timestamp{Time: fdAt(25)}},
		{ID: github.Ptr(int64(10)), User: &github.User{Login: github.Ptr("someone")}, CreatedAt: &github.Timestamp{Time: fdAt(35)}, OriginalCommitID: github.Ptr("c4")},
	}
	fdSetup(t, []ReviewJSON{myReview(fdAt(12), "c2")}, comments)
	fake := &fakeCompare{diff: fdDiff}
	useFakeCompare(t, fake)

	res := getFD(t, filtereddiff.SinceLastInteraction)
	if res.BaseSHA != "c3" || res.Baseline == nil || !res.Baseline.Inferred || res.Baseline.Kind != filtereddiff.KindComment {
		t.Fatalf("result = %+v (baseline %+v), want c3 inferred from my comment", res, res.Baseline)
	}
	if review := getFD(t, filtereddiff.SinceLastReview); review.BaseSHA != "c2" {
		t.Fatalf("since-last-review = %+v, want it unmoved by the comment", review)
	}
	if fake.diffCalls.Load() != 2 {
		t.Fatalf("diff calls = %d, want one per baseline", fake.diffCalls.Load())
	}
}

func TestFilteredDiffConcurrentRequestsShareOneFetch(t *testing.T) {
	fdSetup(t, []ReviewJSON{myReview(fdAt(12), "c2")}, nil)
	fake := &fakeCompare{diff: fdDiff, release: make(chan struct{})}
	useFakeCompare(t, fake)

	var wg sync.WaitGroup
	results := make([]filtereddiff.Result, 3)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = GetFilteredDiff(fdOwner, fdRepo, fdNumber, filtereddiff.SinceLastReview)
		}(i)
	}
	// Let every caller reach the flight before the fetch finishes.
	deadline := time.Now().Add(5 * time.Second)
	for fake.diffCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(fake.release)
	wg.Wait()

	if fake.diffCalls.Load() != 1 {
		t.Fatalf("diff calls = %d, want the concurrent requests to share one", fake.diffCalls.Load())
	}
	for i, r := range results {
		if r.Status != filtereddiff.StatusReady || r.Diff == "" {
			t.Fatalf("result %d = %+v", i, r)
		}
	}
}

func TestWarmFilteredDiffsStoresEveryFilter(t *testing.T) {
	comments := []*github.PullRequestComment{
		{ID: github.Ptr(int64(9)), User: &github.User{Login: github.Ptr("me")}, CreatedAt: &github.Timestamp{Time: fdAt(22)}, OriginalCommitID: github.Ptr("c3")},
	}
	db := fdSetup(t, []ReviewJSON{myReview(fdAt(12), "c2")}, comments)
	fake := &fakeCompare{diff: fdDiff}
	useFakeCompare(t, fake)

	warmFilteredDiffs(fdOwner, fdRepo, fdNumber)

	for _, base := range []string{"c2", "c3"} {
		if row, ok, err := db.GetFilteredDiff(fdOwner, fdRepo, fdNumber, base, "c4"); err != nil || !ok || row.Status != filtereddiff.StatusReady {
			t.Fatalf("warm left no ready diff from %s: %+v ok=%v err=%v", base, row, ok, err)
		}
	}
	// A client switching now is a pure cache read.
	getFD(t, filtereddiff.SinceLastReview)
	getFD(t, filtereddiff.SinceLastInteraction)
	if fake.diffCalls.Load() != 2 {
		t.Fatalf("diff calls = %d, want only the warm's two", fake.diffCalls.Load())
	}
}

func TestFilteredDiffRPCs(t *testing.T) {
	fdSetup(t, []ReviewJSON{myReview(fdAt(12), "c2")}, nil)
	useFakeCompare(t, &fakeCompare{diff: fdDiff})
	h := &RPCHandler{}

	var list ListDiffFiltersReply
	if err := h.ListDiffFilters(&ListDiffFiltersArgs{}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Filters) < 2 || list.Filters[0].ID != filtereddiff.SinceLastReview || list.Filters[0].Name == "" {
		t.Fatalf("filters = %+v", list.Filters)
	}

	var reply GetFilteredDiffReply
	if err := h.GetFilteredDiff(&GetFilteredDiffArgs{Owner: fdOwner, Repo: fdRepo, Number: fdNumber, Filter: filtereddiff.SinceLastReview}, &reply); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reply)
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"filter", "status", "head_sha", "base_sha", "baseline", "diff", "commits", "message", "computed_at"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("reply is missing %q: %s", key, raw)
		}
	}
	if wire["status"] != filtereddiff.StatusReady {
		t.Fatalf("status = %v", wire["status"])
	}

	if err := h.GetFilteredDiff(&GetFilteredDiffArgs{Owner: fdOwner, Repo: fdRepo, Number: fdNumber, Filter: "since-forever"}, &reply); err == nil {
		t.Fatal("an unknown filter should be an RPC error")
	}
}

func TestFilteredDiffBeforeThePRIsCached(t *testing.T) {
	t.Setenv("CRS_HOME", t.TempDir())
	config.SetC(config.Config{DB: setupTestDB(t), GithubUsername: "me"})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	useFakeCompare(t, &fakeCompare{})

	res := getFD(t, filtereddiff.SinceLastReview)
	if res.Status != filtereddiff.StatusError || res.Message == "" {
		t.Fatalf("result = %+v, want an error explaining the PR isn't cached", res)
	}
}

func TestAIRequestCarriesTheFilteredDiffs(t *testing.T) {
	db := aiTestSetup(t, nil)
	seedAIPR(t, db, []*github.PullRequestComment{}, aiThreadsJSON(false))
	cfg := config.C()
	cfg.GithubUsername = "bob"
	config.SetC(cfg)
	useFakeCompare(t, &fakeCompare{diff: fdDiff})

	req, err := buildAIRequest(aiOwner, aiRepo, aiNumber)
	if err != nil {
		t.Fatal(err)
	}
	if req.FilteredDiff == nil {
		t.Fatal("the AI request has no filtered diff accessor")
	}
	// bob's review names no commit and predates the PR's only cached one, so
	// the accessor answers that there is nothing to start from.
	res, err := req.FilteredDiff(context.Background(), filtereddiff.SinceLastReview)
	if err != nil {
		t.Fatal(err)
	}
	if res.Filter != filtereddiff.SinceLastReview || res.Status != filtereddiff.StatusNoBaseline {
		t.Fatalf("result = %+v, want the server's answer for the PR", res)
	}
}

// fdFullDiff is a PR's full diff: greet.go changed in two places, and
// notes.md added.
const fdFullDiff = `diff --git a/greet.go b/greet.go
index 1111111..3333333 100644
--- a/greet.go
+++ b/greet.go
@@ -1,4 +1,5 @@
 package greet
 
+// Hello says hello.
 func Hello() {}
 
@@ -10,3 +11,4 @@ func Hello() {}
 func Bye() {
+	println("bye")
 }
 
diff --git a/notes.md b/notes.md
new file mode 100644
--- /dev/null
+++ b/notes.md
@@ -0,0 +1,2 @@
+# Notes
+Second line.
`

// fdSinceDiff is the diff since the reviewer's baseline: the comment line was
// reworded, a line the baseline had added was taken out again (so the full
// diff doesn't show it), Bye gained its println, and a file the full diff
// doesn't touch at all came and went.
const fdSinceDiff = `diff --git a/greet.go b/greet.go
index 2222222..3333333 100644
--- a/greet.go
+++ b/greet.go
@@ -1,6 +1,5 @@
 package greet
 
-// Hello greets.
-// TODO: remove
+// Hello says hello.
 func Hello() {}
 
@@ -11,4 +10,5 @@ func Hello() {}
 // Bye says bye.
 func Bye() {
+	println("bye")
 }
 
diff --git a/other.go b/other.go
index 4444444..5555555 100644
--- a/other.go
+++ b/other.go
@@ -1 +1 @@
-package old
+package other
`

func TestDiffPositionsMapsHeadLinesOntoTheFullDiff(t *testing.T) {
	full, err := utils.Parse(fdFullDiff)
	if err != nil {
		t.Fatal(err)
	}
	since, err := utils.Parse(fdSinceDiff)
	if err != nil {
		t.Fatal(err)
	}
	got := diffPositions(full, since)

	// In fdSinceDiff's greet.go: rows 1-2 are context (head lines 1, 2), rows
	// 3-4 the removed lines, row 5 the new comment (head 3), rows 6-7 context
	// (head 4, 5); row 8 is the second hunk header, then row 9 context (head
	// 10), row 10 Bye (head 11), row 11 the println (head 12), rows 12-13
	// context (head 13, 14). In the full diff, head lines 1-5 sit at rows 1-5,
	// the second hunk header at 6, and head lines 11-14 at rows 7-10.
	want := map[int]int{0: 0, 1: 1, 2: 2, 5: 3, 6: 4, 7: 5, 10: 7, 11: 8, 12: 9, 13: 10}
	if !mapsEqual(got["greet.go"], want) {
		t.Fatalf("greet.go positions = %v, want %v", got["greet.go"], want)
	}
	// Head line 10 is outside the full diff's hunks; removed rows and hunk
	// headers map nowhere.
	for _, row := range []int{3, 4, 8, 9} {
		if _, ok := got["greet.go"][row]; ok {
			t.Errorf("row %d mapped to %d, want it left out", row, got["greet.go"][row])
		}
	}
	if _, ok := got["other.go"]; ok {
		t.Error("a file the full diff doesn't touch got positions")
	}
	if len(diffPositions(nil, since)) != 0 || len(diffPositions(full, nil)) != 0 {
		t.Error("a missing diff should map nothing")
	}
}

func mapsEqual(a, b map[int]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func TestFilteredDiffCarriesItsPositionsOntoTheFullDiff(t *testing.T) {
	db := fdSetup(t, []ReviewJSON{myReview(fdAt(12), "c2")}, nil)
	if err := db.UpsertPullRequest(fdNumber, fdRepo, "c4", "base", fdFullDiff); err != nil {
		t.Fatal(err)
	}
	useFakeCompare(t, &fakeCompare{diff: fdSinceDiff})

	res := getFD(t, filtereddiff.SinceLastReview)
	if res.Status != filtereddiff.StatusReady {
		t.Fatalf("result = %+v", res)
	}
	if res.Positions["greet.go"][5] != 3 || res.Positions["greet.go"][11] != 8 {
		t.Fatalf("positions = %v, want greet.go rows 5 and 11 at 3 and 8", res.Positions)
	}

	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), `"positions":{"greet.go":{`) {
		t.Fatalf("positions should serialize per file with string keys: %s", raw)
	}

	// Nothing to show, nothing to map: still an object, never null.
	if err := db.UpsertPRReviews(fdNumber, fdRepo, "[]"); err != nil {
		t.Fatal(err)
	}
	none := getFD(t, filtereddiff.SinceLastReview)
	raw, _ = json.Marshal(none)
	if none.Positions == nil || !strings.Contains(string(raw), `"positions":{}`) {
		t.Fatalf("positions without a diff = %s, want {}", raw)
	}
}
