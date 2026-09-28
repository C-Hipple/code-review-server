package server

import (
	"context"
	"crs/ai"
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

const (
	aiOwner  = "acme"
	aiRepo   = "widgets"
	aiNumber = 42
)

var aiT0 = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

func aiAt(hours float64) time.Time { return aiT0.Add(time.Duration(hours * float64(time.Hour))) }

const aiTestDiff = `diff --git a/src/greet.ts b/src/greet.ts
index 1c4e9d2..7a2b8f0 100644
--- a/src/greet.ts
+++ b/src/greet.ts
@@ -3,7 +3,8 @@
 export interface Greeting {
     name: string;
+    punctuation: string;
 }
diff --git a/src/main.ts b/src/main.ts
index 3b18e51..a1c9d2f 100644
--- a/src/main.ts
+++ b/src/main.ts
@@ -1,3 +1,4 @@
 import { formatGreeting } from './greet';
-console.log('hello');
+const message = formatGreeting({ name: 'world', punctuation: '!' });
+console.log(message);
`

// reviewComment is a REST review comment on the head side of the diff.
func reviewComment(id int64, login, path string, line int, body string, at time.Time, replyTo int64) *github.PullRequestComment {
	c := &github.PullRequestComment{
		ID:                  github.Int64(id),
		Body:                github.String(body),
		User:                &github.User{Login: github.String(login)},
		Path:                github.String(path),
		Position:            github.Int(line),
		Line:                github.Int(line),
		Side:                github.String("RIGHT"),
		CreatedAt:           &github.Timestamp{Time: at},
		HTMLURL:             github.String("https://github.com/acme/widgets/pull/42#discussion_r" + strconv.FormatInt(id, 10)),
		PullRequestReviewID: github.Int64(700),
	}
	if replyTo != 0 {
		c.InReplyTo = github.Int64(replyTo)
	}
	return c
}

// issueComment is a top-level conversation comment, as GetPRDetails caches it.
func issueComment(id int64, login, body string, at time.Time) *github.PullRequestComment {
	return &github.PullRequestComment{
		ID:        github.Int64(id),
		Body:      github.String(body),
		User:      &github.User{Login: github.String(login)},
		CreatedAt: &github.Timestamp{Time: at},
	}
}

// aiTestComments: bob's thread on greet.ts (answered, and resolved in
// aiTestThreads), carol's thread on main.ts (unresolved, and nobody has
// replied since the commit at t0+2h), and dave's conversation comment.
func aiTestComments() []*github.PullRequestComment {
	return []*github.PullRequestComment{
		reviewComment(5001, "bob", "src/greet.ts", 3, "Should punctuation have a default?", aiAt(0), 0),
		reviewComment(5002, "alice", "src/greet.ts", 3, "Callers always pass one.", aiAt(1), 5001),
		reviewComment(6001, "carol", "src/main.ts", 2, "Log the formatted message, not a constant.", aiAt(0.5), 0),
		issueComment(9001, "dave", "Please mention the new flag in the README.", aiAt(0.75)),
	}
}

func aiThreadsJSON(carolResolved bool) string {
	threads := []git_tools.ReviewThread{
		{ID: "PRRT_5001", IsResolved: true, ResolvedBy: "alice", Path: "src/greet.ts", CommentIDs: []int64{5001, 5002}},
		{ID: "PRRT_6001", IsResolved: carolResolved, Path: "src/main.ts", CommentIDs: []int64{6001}},
	}
	raw, _ := json.Marshal(threads)
	return string(raw)
}

// seedAIPR writes every cache GetPRDetails reads for acme/widgets#42, so an AI
// request for it is assembled without reaching GitHub.
func seedAIPR(t *testing.T, db *database.DB, comments []*github.PullRequestComment, threadsJSON string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("seeding the PR caches: %v", err)
		}
	}
	metadata, _ := json.Marshal(PRMetadata{Number: aiNumber, Title: "Add greeting helper", Author: "alice", BaseRef: "main", HeadRef: "alice/greeting", State: "open"})
	must(db.UpsertPRMetadataCache(aiOwner, aiRepo, aiNumber, string(metadata)))
	must(db.UpsertPullRequest(aiNumber, aiRepo, "sha-1", "base-1", aiTestDiff))
	rawComments, _ := json.Marshal(comments)
	must(db.UpsertPRComments(aiNumber, aiRepo, string(rawComments)))
	must(db.UpsertPRReviewThreads(aiNumber, aiRepo, threadsJSON))
	must(db.UpsertPRReactions(aiNumber, aiRepo, `{"comments": {}, "reviews": {}}`))
	reviews, _ := json.Marshal([]ReviewJSON{{ID: 700, User: "bob", State: "COMMENTED", Body: "A question.", SubmittedAt: aiAt(0)}})
	must(db.UpsertPRReviews(aiNumber, aiRepo, string(reviews)))
	commits, _ := json.Marshal([]CommitJSON{{SHA: "sha-1", Message: "Add greeting helper", Author: "alice", Date: aiAt(2).Format(time.RFC3339)}})
	must(db.UpsertPRCommits(aiNumber, aiRepo, string(commits)))
}

// aiTestSetup points the server at a fresh DB and CRS home with the given AI
// features configured, and a token so GetPRDetails can build its client (the
// seeded caches mean it never uses it).
func aiTestSetup(t *testing.T, features []config.AIFeature) *database.DB {
	t.Helper()
	t.Setenv("CRS_HOME", t.TempDir())
	t.Setenv("CRS_GITHUB_TOKEN", "test-token")
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, AIFeatures: features, SleepDuration: 10 * time.Minute})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	return db
}

// useAIRunner swaps the runner the RPCs and hooks use for the test.
func useAIRunner(t *testing.T, features ...ai.Feature) *ai.Runner {
	t.Helper()
	reg := ai.NewRegistry()
	for _, f := range features {
		reg.MustRegister(f)
	}
	r := ai.NewRunner(reg)
	orig := aiRunner
	aiRunner = r
	t.Cleanup(func() { aiRunner = orig })
	return r
}

func waitForAIRun(t *testing.T, feature string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for aiRunner.Running(aiOwner, aiRepo, aiNumber, feature) {
		if time.Now().After(deadline) {
			t.Fatalf("%s still running after 5s", feature)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stubFeature reports the head SHA it saw, and waits on release when set.
type stubFeature struct {
	id      string
	release chan struct{}
	calls   atomic.Int32
}

func (f *stubFeature) ID() string   { return f.id }
func (f *stubFeature) Name() string { return "Stub " + f.id }
func (f *stubFeature) Run(_ context.Context, req ai.Request) (ai.Result, error) {
	f.calls.Add(1)
	if f.release != nil {
		<-f.release
	}
	return ai.Result{
		Body:        ai.Body{BodyType: ai.BodyMarkdown, BodyContent: "report for " + req.HeadSHA},
		Annotations: []ai.Annotation{{Filename: "src/main.ts", Line: 2, Severity: "warning", Content: "look here"}},
	}, nil
}

func TestRunAIFeatureRunsInTheBackgroundWhileGetAIOutputPolls(t *testing.T) {
	db := aiTestSetup(t, []config.AIFeature{{ID: "stub", Enabled: true}})
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	f := &stubFeature{id: "stub", release: make(chan struct{})}
	useAIRunner(t, f)
	h := &RPCHandler{}

	var run RunAIFeatureReply
	if err := h.RunAIFeature(&RunAIFeatureArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Feature: "stub"}, &run); err != nil {
		t.Fatalf("RunAIFeature: %v", err)
	}
	if !run.Okay || run.Outcome != string(ai.OutcomeStarted) || run.Output == nil || run.Output.Status != ai.StatusPending {
		t.Fatalf("unexpected reply: %+v", run)
	}

	var poll GetAIOutputReply
	if err := h.GetAIOutput(&GetAIOutputArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &poll); err != nil {
		t.Fatalf("GetAIOutput: %v", err)
	}
	if poll.Output["stub"].Status != ai.StatusPending {
		t.Errorf("status while running = %q, want pending", poll.Output["stub"].Status)
	}

	var again RunAIFeatureReply
	h.RunAIFeature(&RunAIFeatureArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Feature: "stub", Force: true}, &again)
	if again.Outcome != string(ai.OutcomeAlreadyRunning) || !again.Okay {
		t.Errorf("a second request while running: %+v", again)
	}

	close(f.release)
	waitForAIRun(t, "stub")

	if err := h.GetAIOutput(&GetAIOutputArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &poll); err != nil {
		t.Fatalf("GetAIOutput: %v", err)
	}
	out := poll.Output["stub"]
	if out.Status != ai.StatusSuccess || out.Body.BodyContent != "report for sha-1" || out.Name != "Stub stub" {
		t.Errorf("unexpected output: %+v", out)
	}
	if out.Stale || out.CoversSHA != "sha-1" || out.CurrentSHA != "sha-1" || out.CoversDigest != out.CurrentDigest || out.UpdatedAt == "" {
		t.Errorf("a fresh result should cover the current inputs: %+v", out)
	}
	if len(out.Annotations) != 1 {
		t.Fatalf("annotations = %+v", out.Annotations)
	}
	if a := out.Annotations[0]; a.Source != AnnotationSourceAI || a.Feature != "stub" || a.Plugin != "" || a.Line != 2 {
		t.Errorf("an AI annotation must say so, not pass for a plugin's: %+v", a)
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("feature ran %d times, want 1", n)
	}
}

func TestAIOutputGoesStaleWhenAThreadIsResolved(t *testing.T) {
	db := aiTestSetup(t, []config.AIFeature{{ID: "stub", Enabled: true}})
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	f := &stubFeature{id: "stub"}
	useAIRunner(t, f)
	h := &RPCHandler{}
	args := &RunAIFeatureArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Feature: "stub"}

	var run RunAIFeatureReply
	h.RunAIFeature(args, &run)
	waitForAIRun(t, "stub")
	h.RunAIFeature(args, &run)
	if run.Outcome != string(ai.OutcomeUpToDate) {
		t.Fatalf("nothing changed, so the stored result should answer: %+v", run)
	}

	// Carol resolves her thread; the next workflow cycle rewrites the cache.
	if err := db.UpsertPRReviewThreads(aiNumber, aiRepo, aiThreadsJSON(true)); err != nil {
		t.Fatal(err)
	}
	var poll GetAIOutputReply
	h.GetAIOutput(&GetAIOutputArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Feature: "stub"}, &poll)
	out := poll.Output["stub"]
	if !out.Stale || out.CoversDigest == out.CurrentDigest || out.CoversSHA != out.CurrentSHA {
		t.Errorf("a resolved thread must make the result stale on the digest alone: %+v", out)
	}

	h.RunAIFeature(args, &run)
	if run.Outcome != string(ai.OutcomeStarted) {
		t.Errorf("stale inputs should start a run: %+v", run)
	}
	waitForAIRun(t, "stub")
	h.GetAIOutput(&GetAIOutputArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Feature: "stub"}, &poll)
	if poll.Output["stub"].Stale {
		t.Errorf("the rerun should cover the new inputs: %+v", poll.Output["stub"])
	}
}

func TestRunAIFeatureRefusesWhatCannotRun(t *testing.T) {
	aiTestSetup(t, []config.AIFeature{{ID: "off", Automatic: true}})
	useAIRunner(t, &stubFeature{id: "off"})
	h := &RPCHandler{}

	var unknown RunAIFeatureReply
	if err := h.RunAIFeature(&RunAIFeatureArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Feature: "nope"}, &unknown); err != nil {
		t.Fatalf("RunAIFeature: %v", err)
	}
	if unknown.Okay || unknown.Outcome != string(ai.OutcomeUnknown) || unknown.Output != nil {
		t.Errorf("unknown feature: %+v", unknown)
	}

	var disabled RunAIFeatureReply
	h.RunAIFeature(&RunAIFeatureArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Feature: "off"}, &disabled)
	if disabled.Okay || disabled.Outcome != string(ai.OutcomeDisabled) || !strings.Contains(disabled.Message, "[[AIFeatures]]") {
		t.Errorf("disabled feature: %+v", disabled)
	}
	if disabled.Output == nil || disabled.Output.Status != ai.StatusNotRun {
		t.Errorf("a disabled feature still reports its (empty) output: %+v", disabled.Output)
	}
}

func TestGetAIOutputListsEnabledFeatures(t *testing.T) {
	aiTestSetup(t, []config.AIFeature{{ID: "on", Enabled: true}})
	useAIRunner(t, &stubFeature{id: "on"}, &stubFeature{id: "off"})
	h := &RPCHandler{}

	var all GetAIOutputReply
	if err := h.GetAIOutput(&GetAIOutputArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &all); err != nil {
		t.Fatalf("GetAIOutput: %v", err)
	}
	if len(all.Output) != 1 || all.Output["on"].Status != ai.StatusNotRun || all.Output["on"].Stale {
		t.Errorf("expected only the enabled feature, not yet run: %+v", all.Output)
	}

	var one GetAIOutputReply
	if err := h.GetAIOutput(&GetAIOutputArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Feature: "off"}, &one); err != nil {
		t.Fatalf("GetAIOutput(off): %v", err)
	}
	if _, ok := one.Output["off"]; !ok {
		t.Error("asking for a feature by name returns it even when disabled")
	}

	if err := h.GetAIOutput(&GetAIOutputArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Feature: "nope"}, &one); err == nil {
		t.Error("an unknown feature should be an error")
	}
}

func TestListAIFeaturesDescribesTheBuiltIns(t *testing.T) {
	aiTestSetup(t, nil)
	var reply ListAIFeaturesReply
	if err := (&RPCHandler{}).ListAIFeatures(&ListAIFeaturesArgs{}, &reply); err != nil {
		t.Fatalf("ListAIFeatures: %v", err)
	}
	var found *ai.Info
	for i := range reply.Features {
		if reply.Features[i].ID == ai.CommentsAddressedID {
			found = &reply.Features[i]
		}
	}
	if found == nil {
		t.Fatalf("comments-addressed missing from %+v", reply.Features)
	}
	// Off by default: rollout depends on it.
	if found.Enabled || found.Automatic || found.Mode != "oneshot" || found.Name == "" {
		t.Errorf("unexpected default: %+v", *found)
	}
}

func TestPostUpdateHooksRunOnlyAutomaticAIFeatures(t *testing.T) {
	db := aiTestSetup(t, []config.AIFeature{
		{ID: "auto", Enabled: true, Automatic: true},
		{ID: "manual", Enabled: true},
		{ID: "off", Automatic: true},
	})
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	auto, manual, off := &stubFeature{id: "auto"}, &stubFeature{id: "manual"}, &stubFeature{id: "off"}
	useAIRunner(t, auto, manual, off)

	dispatchAutomaticAIFeatures(aiOwner, aiRepo, aiNumber)
	waitForAIRun(t, "auto")

	if auto.calls.Load() != 1 || manual.calls.Load() != 0 || off.calls.Load() != 0 {
		t.Errorf("calls: auto %d, manual %d, off %d", auto.calls.Load(), manual.calls.Load(), off.calls.Load())
	}
	if stored, ok, _ := db.GetAIResult(aiOwner, aiRepo, aiNumber, "auto"); !ok || stored.Status != ai.StatusSuccess {
		t.Errorf("automatic result not stored: %+v", stored)
	}
}

// recordingProvider answers every prompt with a fixed answer.
type recordingProvider struct {
	mu      sync.Mutex
	answer  string
	prompts []string
}

func (p *recordingProvider) Name() string  { return "fake" }
func (p *recordingProvider) Model() string { return "fake-model" }
func (p *recordingProvider) Generate(_ context.Context, prompt string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompts = append(p.prompts, prompt)
	return p.answer, nil
}

func TestCommentsAddressedEndToEnd(t *testing.T) {
	db := aiTestSetup(t, []config.AIFeature{{ID: ai.CommentsAddressedID, Enabled: true}})
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	// A local draft is the reviewer's own business: it must not be judged.
	draft := "Draft reply I haven't sent"
	if _, err := db.InsertLocalComment(aiOwner, aiRepo, aiNumber, "src/main.ts", 2, &draft, nil); err != nil {
		t.Fatal(err)
	}

	r := useAIRunner(t, ai.CommentsAddressed{})
	model := &recordingProvider{answer: `{"items": [
		{"id": "6001", "status": "outstanding", "rationale": "main.ts still logs a constant elsewhere."},
		{"id": "9001", "status": "addressed", "rationale": "The README now documents the flag."},
		{"id": "5001", "status": "outstanding", "rationale": "Not asked, should be ignored."}
	]}`}
	r.NewProvider = func(provider, command string) (ai.Provider, error) {
		if provider != config.AIProviderGemini || command != "" {
			t.Errorf("provider settings = %q, %q", provider, command)
		}
		return model, nil
	}
	h := &RPCHandler{}

	var run RunAIFeatureReply
	h.RunAIFeature(&RunAIFeatureArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Feature: ai.CommentsAddressedID}, &run)
	if run.Outcome != string(ai.OutcomeStarted) {
		t.Fatalf("RunAIFeature: %+v", run)
	}
	waitForAIRun(t, ai.CommentsAddressedID)

	var poll GetAIOutputReply
	if err := h.GetAIOutput(&GetAIOutputArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &poll); err != nil {
		t.Fatalf("GetAIOutput: %v", err)
	}
	out := poll.Output[ai.CommentsAddressedID]
	if out.Status != ai.StatusSuccess {
		t.Fatalf("status = %q, body:\n%s", out.Status, out.Body.BodyContent)
	}

	var report ai.CommentsReport
	if err := json.Unmarshal(out.Report, &report); err != nil {
		t.Fatalf("report is not a comments report: %v\n%s", err, out.Report)
	}
	if report.Verdict != ai.VerdictOutstanding || report.Counts != (ai.ReportCounts{Total: 3, Addressed: 2, Outstanding: 1, ByModel: 2}) {
		t.Errorf("verdict %q counts %+v", report.Verdict, report.Counts)
	}
	statuses := map[string]string{}
	for _, it := range report.Items {
		statuses[it.RootCommentID] = it.Status + "/" + it.Source
	}
	want := map[string]string{"5001": "addressed/github", "6001": "outstanding/model", "9001": "addressed/model"}
	if len(statuses) != len(want) {
		t.Errorf("items = %v, want %v", statuses, want)
	}
	for id, w := range want {
		if statuses[id] != w {
			t.Errorf("item %s = %q, want %q", id, statuses[id], w)
		}
	}

	var outstanding []ai.ReportItem
	if err := json.Unmarshal(out.Outstanding, &outstanding); err != nil || len(outstanding) != 1 ||
		outstanding[0].RootCommentID != "6001" || outstanding[0].Path != "src/main.ts" || outstanding[0].Line != 2 {
		t.Errorf("outstanding = %+v (err %v)", outstanding, err)
	}
	if len(out.Annotations) != 1 || out.Annotations[0].Filename != "src/main.ts" || out.Annotations[0].Line != 2 ||
		out.Annotations[0].Source != AnnotationSourceAI {
		t.Errorf("annotations = %+v", out.Annotations)
	}

	model.mu.Lock()
	defer model.mu.Unlock()
	if len(model.prompts) != 1 {
		t.Fatalf("expected one model call, got %d", len(model.prompts))
	}
	prompt := model.prompts[0]
	for _, want := range []string{"### Item 6001", "### Item 9001", `"Add greeting helper" by alice`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	for _, unwanted := range []string{"### Item 5001", draft} {
		if strings.Contains(prompt, unwanted) {
			t.Errorf("prompt should not contain %q", unwanted)
		}
	}
}

func TestAIDiscussionPartitionsComments(t *testing.T) {
	details := &PRDetails{
		Metadata: PRMetadata{Author: "alice"},
		Comments: []CommentJSON{
			{ID: "5001", Author: "bob", Body: "Root", Path: "src/greet.ts", Position: "3", CreatedAt: aiAt(0), ThreadID: "PRRT_1", Resolved: true, ResolvedBy: "alice"},
			{ID: "9001", Author: "dave", Body: "Conversation", CreatedAt: aiAt(0.5)},
			{ID: "7001", Author: "local", Body: "My draft", Path: "src/main.ts", Position: "2"},
			{ID: "6001", Author: "carol", Body: "Removed line", Path: "src/main.ts", Position: "1", CreatedAt: aiAt(0.25), ThreadID: "PRRT_2"},
		},
		// The reply to 5001 happens to be in the outdated list; it still joins
		// its thread, in posting order.
		OutdatedComments: []CommentJSON{
			{ID: "5002", Author: "alice", Body: "Reply", Path: "src/greet.ts", InReplyTo: 5001, CreatedAt: aiAt(1), ThreadID: "PRRT_1", Outdated: true},
			{ID: "8001", Author: "bob", Body: "Old", Path: "old.go", CreatedAt: aiAt(0.1), ThreadID: "PRRT_3", Outdated: true},
		},
		Reviews: []ReviewJSON{{ID: 700, User: "bob", State: "CHANGES_REQUESTED", SubmittedAt: aiAt(0)}},
		Commits: []CommitJSON{
			{SHA: "a", Date: aiAt(1).Format(time.RFC3339)},
			{SHA: "b", Date: aiAt(2).Format(time.RFC3339)},
			{SHA: "c", Date: "not a date"},
		},
		commentsLoaded: true,
		reviewThreads:  []git_tools.ReviewThread{},
	}
	raw := `[
		{"id": 5001, "line": 3, "side": "RIGHT"},
		{"id": 6001, "line": 1, "side": "LEFT"},
		{"id": 8001, "line": null, "original_line": 9}
	]`

	d := aiDiscussion(details, raw)

	if !d.CommentsKnown || !d.ThreadsKnown || d.PRAuthor != "alice" || !d.LatestCommitAt.Equal(aiAt(2)) {
		t.Errorf("unexpected header: known %v/%v author %q latest %v", d.CommentsKnown, d.ThreadsKnown, d.PRAuthor, d.LatestCommitAt)
	}
	if len(d.Conversation) != 1 || d.Conversation[0].ID != "9001" {
		t.Errorf("conversation = %+v", d.Conversation)
	}
	if len(d.Threads) != 3 {
		t.Fatalf("expected 3 threads (the local draft left out), got %+v", d.Threads)
	}
	byRoot := map[string]ai.Thread{}
	for _, th := range d.Threads {
		byRoot[th.RootID] = th
	}
	greet := byRoot["5001"]
	if len(greet.Comments) != 2 || greet.Comments[1].ID != "5002" || !greet.Resolved || greet.ResolvedBy != "alice" ||
		greet.ThreadID != "PRRT_1" || greet.Line != 3 || greet.Outdated {
		t.Errorf("greet thread: %+v", greet)
	}
	if removed := byRoot["6001"]; removed.Line != 0 {
		t.Errorf("a comment on a removed line has no head line: %+v", removed)
	}
	if old := byRoot["8001"]; !old.Outdated || old.Line != 0 {
		t.Errorf("an outdated thread has no head line: %+v", old)
	}
	if len(d.Reviews) != 1 || d.Reviews[0].State != "CHANGES_REQUESTED" {
		t.Errorf("reviews = %+v", d.Reviews)
	}

	details.reviewThreads = nil
	details.commentsLoaded = false
	if d := aiDiscussion(details, raw); d.ThreadsKnown || d.CommentsKnown {
		t.Error("unknown inputs must read as unknown")
	}
}

func TestGetPRDetailsRecordsWhatItKnewForAI(t *testing.T) {
	db := aiTestSetup(t, nil)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))

	details, err := GetPRDetails(aiOwner, aiRepo, aiNumber, false)
	if err != nil {
		t.Fatalf("GetPRDetails: %v", err)
	}
	if !details.commentsLoaded || len(details.reviewThreads) != 2 {
		t.Errorf("commentsLoaded %v, %d threads", details.commentsLoaded, len(details.reviewThreads))
	}

	// With the thread cache gone and GitHub's GraphQL failing, the state is
	// unknown rather than "no threads".
	gql := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer gql.Close()
	orig := git_tools.GraphQLEndpoint
	git_tools.GraphQLEndpoint = gql.URL
	t.Cleanup(func() { git_tools.GraphQLEndpoint = orig })
	if err := db.DeletePRReviewThreads(aiNumber, aiRepo); err != nil {
		t.Fatal(err)
	}

	details, err = GetPRDetails(aiOwner, aiRepo, aiNumber, false)
	if err != nil {
		t.Fatalf("GetPRDetails: %v", err)
	}
	if details.reviewThreads != nil {
		t.Errorf("thread state should be unknown, got %+v", details.reviewThreads)
	}
}

func TestValidateConfigChecksAIFeaturesAgainstTheRegistry(t *testing.T) {
	cfg := &config.Config{
		SleepDuration: 10 * time.Minute,
		AIFeatures:    []config.AIFeature{{ID: "mermaid", Enabled: true}},
	}
	found := false
	for _, p := range validateConfig(cfg) {
		if p.Field == "AIFeatures[0].ID" && strings.Contains(p.Message, "unknown AI feature") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an unknown-feature problem, got %v", validateConfig(cfg))
	}
}

func TestCleanRepoPath(t *testing.T) {
	ok := map[string]string{
		"src/main.ts":      "src/main.ts",
		" ./src/main.ts ":  "src/main.ts",
		"src/../README.md": "README.md",
	}
	for in, want := range ok {
		if got, err := cleanRepoPath(in); err != nil || got != want {
			t.Errorf("cleanRepoPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "/etc/passwd", "../secrets", "src/../../x", "..", ".", `src\..\x`} {
		if got, err := cleanRepoPath(in); err == nil {
			t.Errorf("cleanRepoPath(%q) = %q, want an error", in, got)
		}
	}
}
