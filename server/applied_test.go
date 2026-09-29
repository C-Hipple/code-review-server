package server

import (
	"context"
	"crs/ai"
	"crs/config"
	"crs/database"
	"crs/utils"
	"strings"
	"sync"
	"testing"
)

// storeAIResult stores result as a run of feature would have, for the PR at
// sha.
func storeAIResult(t *testing.T, db *database.DB, owner, repo string, number int, feature, status, sha string, result ai.Result) {
	t.Helper()
	encoded, err := result.Encode()
	if err != nil {
		t.Fatalf("encoding the %s result: %v", feature, err)
	}
	if err := db.UpsertAIResult(owner, repo, number, feature, encoded, status, sha, ai.CodeOnlyDigest); err != nil {
		t.Fatalf("storing the %s result: %v", feature, err)
	}
}

// forgetHookDispatches clears the per-SHA hook debounce, so hooks an earlier
// test dispatched for the same PR don't swallow this test's.
func forgetHookDispatches() {
	recentHookDispatches.Range(func(k, _ any) bool {
		recentHookDispatches.Delete(k)
		return true
	})
}

// appliedStub is an applied feature that records the trigger of each run.
type appliedStub struct {
	stubFeature
	mu       sync.Mutex
	triggers []ai.Trigger
}

func (f *appliedStub) Applied() bool { return true }
func (f *appliedStub) Run(ctx context.Context, req ai.Request) (ai.Result, error) {
	f.mu.Lock()
	f.triggers = append(f.triggers, req.Trigger)
	f.mu.Unlock()
	return f.stubFeature.Run(ctx, req)
}

func (f *appliedStub) ranFor() []ai.Trigger {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ai.Trigger(nil), f.triggers...)
}

func names(files []*utils.DiffFile) string {
	var out []string
	for _, f := range files {
		out = append(out, f.NewName)
	}
	return strings.Join(out, ",")
}

// diffLists reports whether the rendered diff shows first's file before
// second's.
func diffLists(diff, first, second string) bool {
	i, j := strings.Index(diff, "diff --git a/"+first), strings.Index(diff, "diff --git a/"+second)
	return i >= 0 && j >= 0 && i < j
}

func orderingTestFiles() []*utils.DiffFile {
	return []*utils.DiffFile{{NewName: "a_test.go"}, {NewName: "main.go"}, {NewName: "helper.go"}}
}

func TestOrderDiffFilesDefaultsToTestsLast(t *testing.T) {
	// With file-ordering off, the order is the default sort, whatever is
	// stored.
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	storeAIResult(t, db, aiOwner, aiRepo, 7, ai.FileOrderingID, ai.StatusSuccess, "sha-abc",
		ai.Result{Report: ai.FileOrderingReport{Files: []string{"helper.go", "main.go", "a_test.go"}}})

	if got := names(orderDiffFiles(orderingTestFiles(), aiOwner, aiRepo, 7, "sha-abc")); got != "main.go,helper.go,a_test.go" {
		t.Errorf("orderDiffFiles = %s, want the tests-last sort", got)
	}
}

func TestOrderDiffFilesAppliesTheStoredOrder(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, AIFeatures: []config.AIFeature{{ID: ai.FileOrderingID, Enabled: true}}})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	order := ai.Result{Report: ai.FileOrderingReport{Files: []string{"helper.go", "main.go", "a_test.go"}}}

	// Nothing stored yet: the default sort.
	if got := names(orderDiffFiles(orderingTestFiles(), aiOwner, aiRepo, 7, "sha-abc")); got != "main.go,helper.go,a_test.go" {
		t.Errorf("with nothing stored: %s", got)
	}

	storeAIResult(t, db, aiOwner, aiRepo, 7, ai.FileOrderingID, ai.StatusSuccess, "sha-abc", order)
	if got := names(orderDiffFiles(orderingTestFiles(), aiOwner, aiRepo, 7, "sha-abc")); got != "helper.go,main.go,a_test.go" {
		t.Errorf("with an order stored for the head: %s", got)
	}

	// An order computed for another revision doesn't apply to this one.
	if got := names(orderDiffFiles(orderingTestFiles(), aiOwner, aiRepo, 7, "sha-new")); got != "main.go,helper.go,a_test.go" {
		t.Errorf("with an order for another head: %s", got)
	}

	// Nor does a failed run's result.
	storeAIResult(t, db, aiOwner, aiRepo, 7, ai.FileOrderingID, ai.StatusError, "sha-abc",
		ai.Result{Body: ai.Body{BodyContent: "**File ordering failed.**"}})
	if got := names(orderDiffFiles(orderingTestFiles(), aiOwner, aiRepo, 7, "sha-abc")); got != "main.go,helper.go,a_test.go" {
		t.Errorf("with a failed run stored: %s", got)
	}

	// A single file needs no order.
	single := []*utils.DiffFile{{NewName: "only_test.go"}}
	if got := names(orderDiffFiles(single, aiOwner, aiRepo, 7, "sha-abc")); got != "only_test.go" {
		t.Errorf("a single file: %s", got)
	}
}

func TestOrderDiffFilesKeepsAnOrderFromBeforeTheMove(t *testing.T) {
	// An order computed before file-ordering was an AI feature still applies
	// to the revision it was computed for.
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, AIFeatures: []config.AIFeature{{ID: ai.FileOrderingID, Enabled: true}}})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	if err := db.UpsertDiffFileOrdering(7, aiRepo, "sha-abc", `["helper.go","main.go","a_test.go"]`); err != nil {
		t.Fatal(err)
	}

	if got := names(orderDiffFiles(orderingTestFiles(), aiOwner, aiRepo, 7, "sha-abc")); got != "helper.go,main.go,a_test.go" {
		t.Errorf("orderDiffFiles = %s, want the legacy order", got)
	}
	if got := names(orderDiffFiles(orderingTestFiles(), aiOwner, aiRepo, 7, "sha-new")); got != "main.go,helper.go,a_test.go" {
		t.Errorf("a legacy order for another head applied: %s", got)
	}
}

func TestOrderDiffFilesNeverStartsARun(t *testing.T) {
	// Rendering is on the critical path of opening a review: it reads what a
	// run stored, and asks for nothing.
	db := aiTestSetup(t, []config.AIFeature{{ID: ai.FileOrderingID, Enabled: true, Automatic: true}})
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	r := useAIRunner(t, ai.FileOrdering{})
	r.NewProvider = func(config.AIProviderChoice) (ai.Provider, error) {
		t.Error("rendering must not build a provider")
		return nil, nil
	}

	details, err := GetPRDetails(aiOwner, aiRepo, aiNumber, false)
	if err != nil {
		t.Fatalf("GetPRDetails: %v", err)
	}
	if !diffLists(details.Diff, "src/greet.ts", "src/main.ts") {
		t.Errorf("expected the default order:\n%s", details.Diff)
	}
	if aiRunner.Running(aiOwner, aiRepo, aiNumber, ai.FileOrderingID) {
		t.Error("rendering started a file-ordering run")
	}
}

func TestReviewEaseShowsTheLatestRating(t *testing.T) {
	db := setupTestDB(t)
	enabled := config.Config{DB: db, AIFeatures: []config.AIFeature{{ID: ai.ReviewEaseID, Enabled: true}}}
	config.SetC(enabled)
	t.Cleanup(func() { config.SetC(config.Config{}) })

	if got := reviewEase(aiOwner, aiRepo, aiNumber); got != "" {
		t.Errorf("nothing rated yet: %q", got)
	}

	// A rating from before review-ease was an AI feature still shows...
	if err := db.UpsertReviewEase(aiNumber, aiRepo, "sha-0", "hard"); err != nil {
		t.Fatal(err)
	}
	if got := reviewEase(aiOwner, aiRepo, aiNumber); got != "hard" {
		t.Errorf("legacy rating: %q", got)
	}
	// ...including after a run that failed...
	storeAIResult(t, db, aiOwner, aiRepo, aiNumber, ai.ReviewEaseID, ai.StatusError, "sha-1",
		ai.Result{Body: ai.Body{BodyContent: "**Review ease failed.**"}})
	if got := reviewEase(aiOwner, aiRepo, aiNumber); got != "hard" {
		t.Errorf("legacy rating after a failed run: %q", got)
	}
	// ...until a run rates the PR, whichever revision it rated.
	storeAIResult(t, db, aiOwner, aiRepo, aiNumber, ai.ReviewEaseID, ai.StatusSuccess, "sha-1",
		ai.Result{Report: ai.ReviewEaseReport{Rating: "easy"}})
	if got := reviewEase(aiOwner, aiRepo, aiNumber); got != "easy" {
		t.Errorf("stored rating: %q", got)
	}

	// Off means no rating, stored or not.
	config.SetC(config.Config{DB: db})
	if got := reviewEase(aiOwner, aiRepo, aiNumber); got != "" {
		t.Errorf("review-ease is off, but the PR shows %q", got)
	}
}

func TestOpeningAPRRequestsTheAppliedFeatures(t *testing.T) {
	// Enabled but not automatic: the applied feature runs because a client
	// opened the PR; the report feature waits to be asked.
	db := aiTestSetup(t, []config.AIFeature{
		{ID: "applied", Enabled: true},
		{ID: "report", Enabled: true},
		{ID: "applied-off"},
	})
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	forgetHookDispatches()
	applied, off := &appliedStub{stubFeature: stubFeature{id: "applied"}}, &appliedStub{stubFeature: stubFeature{id: "applied-off"}}
	report := &stubFeature{id: "report"}
	useAIRunner(t, applied, report, off)

	var reply GetPRReply
	if err := (&RPCHandler{}).GetPR(&GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &reply); err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	waitForAIRun(t, "applied")

	if got := applied.ranFor(); len(got) != 1 || got[0] != ai.TriggerExplicit {
		t.Errorf("the applied feature ran for %v, want one explicit run", got)
	}
	if report.calls.Load() != 0 || off.calls.Load() != 0 {
		t.Errorf("report ran %d times, disabled applied feature %d times", report.calls.Load(), off.calls.Load())
	}
	if stored, ok, _ := db.GetAIResult(aiOwner, aiRepo, aiNumber, "applied"); !ok || stored.Status != ai.StatusSuccess {
		t.Errorf("the applied result wasn't stored: %+v", stored)
	}

	// Covered now: opening the PR again costs no run.
	if err := (&RPCHandler{}).GetPR(&GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &reply); err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	waitForAIRun(t, "applied")
	if n := applied.calls.Load(); n != 1 {
		t.Errorf("the applied feature ran %d times, want 1", n)
	}
}

func TestOpeningAPRBeatsTheAutomaticQueue(t *testing.T) {
	// For an automatic applied feature, the client's request claims the run
	// first, so it doesn't wait behind the automatic-run cap.
	db := aiTestSetup(t, []config.AIFeature{{ID: "applied", Enabled: true, Automatic: true}})
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	applied := &appliedStub{stubFeature: stubFeature{id: "applied"}}
	useAIRunner(t, applied)

	requestAppliedAIFeatures(aiOwner, aiRepo, aiNumber)
	dispatchAutomaticAIFeatures(aiOwner, aiRepo, aiNumber)
	waitForAIRun(t, "applied")

	if got := applied.ranFor(); len(got) != 1 || got[0] != ai.TriggerExplicit {
		t.Errorf("ran for %v, want one explicit run", got)
	}
}

func TestWarmingAPRDoesNotRequestAppliedFeatures(t *testing.T) {
	// A workflow warming a PR runs only what config makes automatic.
	db := aiTestSetup(t, []config.AIFeature{{ID: "applied", Enabled: true}})
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	forgetHookDispatches()
	applied := &appliedStub{stubFeature: stubFeature{id: "applied"}}
	useAIRunner(t, applied)

	WarmPRAnalysis(aiOwner, aiRepo, aiNumber)

	// A request would have claimed its run before WarmPRAnalysis returned.
	if aiRunner.Running(aiOwner, aiRepo, aiNumber, "applied") || applied.calls.Load() != 0 {
		t.Error("warming the PR requested a feature that isn't automatic")
	}
	if _, ok, _ := db.GetAIResult(aiOwner, aiRepo, aiNumber, "applied"); ok {
		t.Error("warming the PR stored a result for a feature that isn't automatic")
	}
}

func TestFileOrderingEndToEnd(t *testing.T) {
	db := aiTestSetup(t, []config.AIFeature{{ID: ai.FileOrderingID, Enabled: true}})
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	forgetHookDispatches()
	r := useAIRunner(t, ai.FileOrdering{})
	model := &recordingProvider{answer: "src/main.ts\nsrc/greet.ts\n"}
	r.NewProvider = func(config.AIProviderChoice) (ai.Provider, error) { return model, nil }
	h := &RPCHandler{}
	args := &GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}

	// The first open renders the default order and asks for the real one.
	var first GetPRReply
	if err := h.GetPR(args, &first); err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if !diffLists(first.Diff, "src/greet.ts", "src/main.ts") {
		t.Errorf("the first open should show the default order:\n%s", first.Diff)
	}
	waitForAIRun(t, ai.FileOrderingID)

	// From the next render on, the diff reads in the model's order.
	var second GetPRReply
	if err := h.GetPR(args, &second); err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if !diffLists(second.Diff, "src/main.ts", "src/greet.ts") {
		t.Errorf("the stored order wasn't applied:\n%s", second.Diff)
	}
	waitForAIRun(t, ai.FileOrderingID)

	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.prompts) != 1 {
		t.Fatalf("expected one model call for the revision, got %d", len(model.prompts))
	}
	if !strings.Contains(model.prompts[0], "Files in this diff:\n- src/greet.ts\n- src/main.ts\n") {
		t.Errorf("the prompt should list the diff's files:\n%s", model.prompts[0])
	}
}

func TestReviewEaseEndToEnd(t *testing.T) {
	db := aiTestSetup(t, []config.AIFeature{{ID: ai.ReviewEaseID, Enabled: true}})
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	forgetHookDispatches()
	section, err := db.GetOrCreateSection("Review Requested", 0)
	if err != nil {
		t.Fatal(err)
	}
	details := []string{"42", "Repo: acme/widgets", "https://github.com/acme/widgets/pull/42"}
	if _, err := db.UpsertItem(section.ID, "42", "TODO", "Add greeting helper", details, []string{"widgets"}, 0); err != nil {
		t.Fatal(err)
	}
	r := useAIRunner(t, ai.ReviewEase{})
	r.NewProvider = func(config.AIProviderChoice) (ai.Provider, error) {
		return &recordingProvider{answer: "REVIEW_EASE: easy\n"}, nil
	}
	h := &RPCHandler{}
	args := &GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}

	var first GetPRReply
	if err := h.GetPR(args, &first); err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if first.Metadata.ReviewEase != "" {
		t.Errorf("rated before any run: %q", first.Metadata.ReviewEase)
	}
	waitForAIRun(t, ai.ReviewEaseID)

	var second GetPRReply
	if err := h.GetPR(args, &second); err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	if second.Metadata.ReviewEase != "easy" {
		t.Errorf("metadata review_ease = %q, want easy", second.Metadata.ReviewEase)
	}
	items, err := NewOrgRenderer(db).GetAllReviewItems()
	if err != nil || len(items) != 1 || items[0].ReviewEase != "easy" {
		t.Errorf("the review list should carry the rating: %+v (err %v)", items, err)
	}

	// After a push, the PR keeps its rating until the new head is rated.
	if err := db.UpsertPullRequest(aiNumber, aiRepo, "sha-2", "base-1", aiTestDiff); err != nil {
		t.Fatal(err)
	}
	if got := reviewEase(aiOwner, aiRepo, aiNumber); got != "easy" {
		t.Errorf("after a push: %q", got)
	}
}

func TestLegacyFlagRunsItsFeatureAutomatically(t *testing.T) {
	// A config from before review-ease was an AI feature has no [[AIFeatures]]
	// entry for it, only the flag: warming a PR must still run it.
	parsed, err := config.ParseConfigForTest([]byte("ExperimentalLLMReviewEase = true\n"))
	if err != nil {
		t.Fatal(err)
	}
	db := aiTestSetup(t, nil)
	parsed.DB = db
	config.SetC(*parsed)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	r := useAIRunner(t, ai.ReviewEase{})
	var choice config.AIProviderChoice
	r.NewProvider = func(c config.AIProviderChoice) (ai.Provider, error) {
		choice = c
		return &recordingProvider{answer: "REVIEW_EASE: hard\n"}, nil
	}

	dispatchAutomaticAIFeatures(aiOwner, aiRepo, aiNumber)
	waitForAIRun(t, ai.ReviewEaseID)

	if got := reviewEase(aiOwner, aiRepo, aiNumber); got != "hard" {
		t.Errorf("review ease = %q, want hard", got)
	}
	if choice.Provider != config.AIProviderGemini {
		t.Errorf("the flag runs on %q, want gemini", choice.Provider)
	}
}
