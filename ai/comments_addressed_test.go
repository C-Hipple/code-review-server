package ai

import (
	"context"
	"crs/config"
	"crs/llm"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// scriptedProvider answers each Generate call with the next scripted answer
// and records the prompts it was sent.
type scriptedProvider struct {
	t       *testing.T
	answers []string
	err     error
	prompts []string
}

func (p *scriptedProvider) Name() string  { return "fake" }
func (p *scriptedProvider) Model() string { return "fake-model" }
func (p *scriptedProvider) Generate(_ context.Context, prompt string) (string, error) {
	p.prompts = append(p.prompts, prompt)
	if p.err != nil {
		return "", p.err
	}
	if len(p.answers) == 0 {
		p.t.Fatalf("unexpected model call #%d", len(p.prompts))
	}
	answer := p.answers[0]
	p.answers = p.answers[1:]
	return answer, nil
}

// noModel fails the test if the model is contacted at all.
type noModel struct{ t *testing.T }

func (noModel) Name() string  { return "none" }
func (noModel) Model() string { return "none" }
func (m noModel) Generate(context.Context, string) (string, error) {
	m.t.Error("the model was called on a path the deterministic rules settle")
	return "", errors.New("unexpected call")
}

var t0 = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

func at(hours int) time.Time { return t0.Add(time.Duration(hours) * time.Hour) }

func cmt(id, author, body string, when time.Time) Comment {
	return Comment{ID: id, Author: author, Body: body, CreatedAt: when, HTMLURL: "https://github.com/acme/widgets/pull/42#discussion_r" + id}
}

// thread builds a thread GitHub reported state for, on src/greet.ts:3.
func thread(resolved bool, comments ...Comment) Thread {
	return Thread{
		RootID:   comments[0].ID,
		ThreadID: "PRRT_" + comments[0].ID,
		Resolved: resolved,
		Path:     "src/greet.ts",
		Line:     3,
		Comments: comments,
	}
}

const sampleDiff = `diff --git a/src/greet.ts b/src/greet.ts
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
-console.log('hello');
+console.log(message);
`

func request(d Discussion, model Provider) Request {
	return Request{
		Owner:        "acme",
		Repo:         "widgets",
		Number:       42,
		HeadSHA:      "sha-1",
		Digest:       "digest-1",
		Diff:         sampleDiff,
		MetadataJSON: `{"title": "Add greeting helper", "author": "alice"}`,
		Discussion:   d,
		Mode:         config.AIModeOneShot,
		Model:        model,
	}
}

// discussion is a known-state discussion with the latest commit at t0+2h.
func discussion(threads ...Thread) Discussion {
	return Discussion{
		PRAuthor:       "alice",
		CommentsKnown:  true,
		ThreadsKnown:   true,
		Threads:        threads,
		LatestCommitAt: at(2),
	}
}

func run(t *testing.T, req Request) (Result, CommentsReport) {
	t.Helper()
	res, err := CommentsAddressed{}.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	report, ok := res.Report.(CommentsReport)
	if !ok {
		t.Fatalf("Report is %T, want CommentsReport", res.Report)
	}
	return res, report
}

func itemByRoot(t *testing.T, report CommentsReport, root string) ReportItem {
	t.Helper()
	for _, it := range report.Items {
		if it.RootCommentID == root {
			return it
		}
	}
	t.Fatalf("no item for root comment %s in %+v", root, report.Items)
	return ReportItem{}
}

func verdictJSON(entries ...string) string {
	return `{"items": [` + strings.Join(entries, ", ") + `]}`
}

func entry(id, status, rationale string) string {
	return fmt.Sprintf(`{"id": %q, "status": %q, "rationale": %q}`, id, status, rationale)
}

func TestResolvedThreadIsAddressedWithoutTheModel(t *testing.T) {
	d := discussion(func() Thread {
		th := thread(true, cmt("5001", "bob", "Should punctuation have a default?", at(0)))
		th.ResolvedBy = "alice"
		return th
	}())
	res, report := run(t, request(d, noModel{t}))

	it := itemByRoot(t, report, "5001")
	if it.Status != ItemAddressed || it.Source != SourceGitHub || it.Rationale != "Resolved on GitHub by alice." {
		t.Errorf("unexpected item: %+v", it)
	}
	if it.ThreadID == nil || *it.ThreadID != "PRRT_5001" {
		t.Errorf("thread_id = %v, want PRRT_5001", it.ThreadID)
	}
	if report.Verdict != VerdictAllAddressed || res.Status != StatusSuccess {
		t.Errorf("verdict %q status %q, want all-addressed/success", report.Verdict, res.Status)
	}
	if report.Model.Consulted || report.Model.Asked != 0 {
		t.Errorf("the model should not be involved: %+v", report.Model)
	}
}

func TestResolvedFlagWinsOverTheModel(t *testing.T) {
	// The model is only asked about the stale thread, but answers for the
	// resolved one too. Its opinion there must not flip the status.
	d := discussion(
		thread(true, cmt("5001", "bob", "Rename this.", at(0))),
		thread(false, cmt("6001", "carol", "Handle the empty name.", at(1))),
	)
	model := &scriptedProvider{t: t, answers: []string{verdictJSON(
		entry("5001", "outstanding", "The rename is not in the diff."),
		entry("6001", "addressed", "greet.ts now checks for an empty name."),
	)}}
	_, report := run(t, request(d, model))

	resolved := itemByRoot(t, report, "5001")
	if resolved.Status != ItemAddressed || resolved.Source != SourceGitHub {
		t.Errorf("resolved thread flipped: %+v", resolved)
	}
	if !strings.Contains(resolved.ModelNote, "outstanding") {
		t.Errorf("expected the model's opinion kept as a note, got %q", resolved.ModelNote)
	}

	stale := itemByRoot(t, report, "6001")
	if stale.Status != ItemAddressed || stale.Source != SourceModel {
		t.Errorf("the stale thread should take the model's verdict: %+v", stale)
	}

	if len(model.prompts) != 1 {
		t.Fatalf("expected one model call, got %d", len(model.prompts))
	}
	if strings.Contains(model.prompts[0], "### Item 5001") {
		t.Error("the resolved thread must not be sent to the model")
	}
	if !strings.Contains(model.prompts[0], "### Item 6001") {
		t.Error("the stale thread must be sent to the model")
	}
	if report.Verdict != VerdictAllAddressed || report.Counts.ByModel != 1 {
		t.Errorf("verdict %q by_model %d", report.Verdict, report.Counts.ByModel)
	}
	if !strings.Contains(report.Summary, "by the model's judgment") {
		t.Errorf("summary should say the model decided part of it: %q", report.Summary)
	}
}

func TestReplyAfterTheLatestCommitIsOutstanding(t *testing.T) {
	d := discussion(thread(false,
		cmt("5001", "bob", "Should punctuation have a default?", at(0)),
		cmt("5002", "alice", "Pushed a fix, have a look.", at(3)), // after the commit at t0+2h
	))
	res, report := run(t, request(d, noModel{t}))

	it := itemByRoot(t, report, "5001")
	if it.Status != ItemOutstanding || it.Source != SourceGitHub {
		t.Errorf("unexpected item: %+v", it)
	}
	if !strings.Contains(it.Rationale, "alice (the author) replied after the latest commit") {
		t.Errorf("rationale should name who spoke last: %q", it.Rationale)
	}
	if it.Replies != 1 || it.LastAuthor != "alice" {
		t.Errorf("replies %d last author %q", it.Replies, it.LastAuthor)
	}
	if report.Verdict != VerdictOutstanding {
		t.Errorf("verdict = %q, want outstanding", report.Verdict)
	}

	outstanding, ok := res.Outstanding.([]ReportItem)
	if !ok || len(outstanding) != 1 || outstanding[0].RootCommentID != "5001" {
		t.Errorf("outstanding list = %+v", res.Outstanding)
	}
}

func TestStaleUnresolvedThreadIsLeftToTheModel(t *testing.T) {
	d := discussion(thread(false, cmt("5001", "bob", "Should punctuation have a default?", at(0))))
	model := &scriptedProvider{t: t, answers: []string{"```json\n" +
		verdictJSON(entry("5001", "outstanding", "Punctuation is still required with no default.")) + "\n```"}}
	res, report := run(t, request(d, model))

	it := itemByRoot(t, report, "5001")
	if it.Status != ItemOutstanding || it.Source != SourceModel ||
		it.Rationale != "Punctuation is still required with no default." {
		t.Errorf("unexpected item: %+v", it)
	}
	if !report.Model.Consulted || report.Model.Asked != 1 || report.Model.Provider != "fake" {
		t.Errorf("model use not recorded: %+v", report.Model)
	}
	// The prompt carries the thread and the diff it is judged against.
	prompt := model.prompts[0]
	for _, want := range []string{"Add greeting helper", "bob (2026-09-01 10:00 UTC): Should punctuation have a default?", "+    punctuation: string;"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if len(res.Annotations) != 1 || res.Annotations[0].Severity != "warning" || res.Annotations[0].Line != 3 {
		t.Errorf("expected a warning annotation on src/greet.ts:3, got %+v", res.Annotations)
	}
}

func TestUnknownCommitTimingIsLeftToTheModel(t *testing.T) {
	d := discussion(thread(false, cmt("5001", "bob", "Rename this.", at(5))))
	d.LatestCommitAt = time.Time{}
	model := &scriptedProvider{t: t, answers: []string{verdictJSON(entry("5001", "unclear", "Can't see a rename."))}}
	_, report := run(t, request(d, model))

	if it := itemByRoot(t, report, "5001"); it.Status != ItemUnclear || it.Source != SourceModel {
		t.Errorf("unexpected item: %+v", it)
	}
}

func TestMissingThreadInfoDegradesToUnclearAndInsufficientInput(t *testing.T) {
	// GitHub's thread state never arrived: nothing may be called resolved,
	// and the model is not asked to guess at state only GitHub has.
	th := thread(false, cmt("5001", "bob", "Rename this.", at(0)))
	th.ThreadID = ""
	d := discussion(th)
	d.ThreadsKnown = false
	res, report := run(t, request(d, noModel{t}))

	it := itemByRoot(t, report, "5001")
	if it.Status != ItemUnclear || it.Source != SourceGitHub || it.ThreadID != nil {
		t.Errorf("unexpected item: %+v", it)
	}
	if res.Status != StatusInsufficientInput || report.Verdict != VerdictInsufficientInput {
		t.Errorf("status %q verdict %q, want insufficient-input", res.Status, report.Verdict)
	}
	if len(report.Missing) == 0 || !strings.Contains(res.Body.BodyContent, "Not enough input") {
		t.Errorf("the report should say what is missing: %+v\n%s", report.Missing, res.Body.BodyContent)
	}
	if len(res.Log.Warnings) == 0 {
		t.Error("the call log should list the missing input")
	}

	// A conversation comment the model could otherwise judge doesn't buy a
	// model call either: no verdict can stand while the thread state is missing.
	d.Conversation = []Comment{cmt("9001", "carol", "Please add a test.", at(0))}
	_, report = run(t, request(d, noModel{t}))
	if report.Model.Consulted || !strings.Contains(report.Model.Note, "not consulted") {
		t.Errorf("model use = %+v", report.Model)
	}
}

func TestThreadWithoutStateStaysUnclearWhenOthersAreKnown(t *testing.T) {
	// The thread cache predates this thread: its state is unknown, the rest
	// are fine, so the verdict can't be "all addressed" — but it isn't
	// insufficient input either.
	fresh := thread(false, cmt("7001", "bob", "One more thing.", at(3)))
	fresh.ThreadID = ""
	d := discussion(thread(true, cmt("5001", "bob", "Rename this.", at(0))), fresh)
	res, report := run(t, request(d, noModel{t}))

	if it := itemByRoot(t, report, "7001"); it.Status != ItemUnclear || it.Source != SourceGitHub {
		t.Errorf("unexpected item: %+v", it)
	}
	if res.Status != StatusSuccess || report.Verdict != VerdictUnclear {
		t.Errorf("status %q verdict %q, want success/unclear", res.Status, report.Verdict)
	}
}

func TestEmptyCommentCacheIsInsufficientInput(t *testing.T) {
	d := Discussion{PRAuthor: "alice", ThreadsKnown: true}
	res, report := run(t, request(d, noModel{t}))

	if res.Status != StatusInsufficientInput || report.Verdict != VerdictInsufficientInput {
		t.Errorf("status %q verdict %q, want insufficient-input", res.Status, report.Verdict)
	}
	if report.Counts.Total != 0 || len(report.Items) != 0 {
		t.Errorf("expected no items, got %+v", report.Items)
	}
	if !strings.Contains(report.Summary, "no comments are cached") {
		t.Errorf("summary should name the missing cache: %q", report.Summary)
	}
}

func TestNoCommentsIsItsOwnVerdict(t *testing.T) {
	res, report := run(t, request(discussion(), noModel{t}))
	if res.Status != StatusSuccess || report.Verdict != VerdictNoComments {
		t.Errorf("status %q verdict %q, want success/no-comments", res.Status, report.Verdict)
	}
}

func TestActiveChangeRequestKeepsTheVerdictOutstanding(t *testing.T) {
	d := discussion(thread(true, cmt("5001", "bob", "Rename this.", at(0))))
	d.Reviews = []Review{
		{ID: 1, User: "bob", State: "CHANGES_REQUESTED", Body: "Please add tests.", SubmittedAt: at(0)},
		{ID: 2, User: "bob", State: "COMMENTED", Body: "Also a nit.", SubmittedAt: at(1)},
		{ID: 3, User: "carol", State: "CHANGES_REQUESTED", SubmittedAt: at(0)},
		{ID: 4, User: "carol", State: "APPROVED", SubmittedAt: at(3)},
		{ID: 5, User: "alice", State: "CHANGES_REQUESTED", SubmittedAt: at(0)}, // the author can't block
	}
	res, report := run(t, request(d, noModel{t}))

	if len(report.ChangeRequests) != 1 || report.ChangeRequests[0].Reviewer != "bob" ||
		report.ChangeRequests[0].ReviewID != 1 || report.ChangeRequests[0].Excerpt != "Please add tests." {
		t.Fatalf("unexpected change requests: %+v", report.ChangeRequests)
	}
	if report.Verdict != VerdictOutstanding {
		t.Errorf("verdict = %q: every thread is resolved, but bob still blocks", report.Verdict)
	}
	if report.Summary != "All 1 item(s) are addressed, but bob still requests changes." {
		t.Errorf("summary = %q", report.Summary)
	}
	if !strings.Contains(res.Body.BodyContent, "### Changes requested (1)") {
		t.Errorf("body should list the change request:\n%s", res.Body.BodyContent)
	}
}

func TestTruncatedDiffDiscardsModelVerdicts(t *testing.T) {
	big := "diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n@@ -1,1 +1,20000 @@\n" +
		strings.Repeat("+// a very long generated file line\n", 5000)
	th := thread(false, cmt("5001", "bob", "Fix the loop in big.go.", at(0)))
	th.Path = "big.go"
	d := discussion(th)
	d.Conversation = []Comment{cmt("9001", "carol", "Please also update the docs.", at(1))}

	req := request(d, &scriptedProvider{t: t, answers: []string{verdictJSON(
		entry("5001", "addressed", "The loop was fixed."),
		entry("9001", "addressed", "The docs were updated."),
	)}})
	req.Diff = big
	res, report := run(t, req)

	if !report.Truncated || !res.Truncated {
		t.Error("the report should be marked truncated")
	}
	for _, root := range []string{"5001", "9001"} {
		it := itemByRoot(t, report, root)
		if it.Status != ItemUnclear || it.Source != SourceModel || !strings.Contains(it.Rationale, "isn't trusted") {
			t.Errorf("item %s: a verdict on cut evidence must degrade to unclear: %+v", root, it)
		}
	}
	if report.Verdict == VerdictAllAddressed {
		t.Error("truncated input produced an all-addressed verdict")
	}
}

func TestModelOutstandingOnFullEvidenceStands(t *testing.T) {
	// Only a verdict that needed the cut part is distrusted: a file that went
	// in whole still counts, even when another file was cut.
	other := "diff --git a/other.go b/other.go\n--- a/other.go\n+++ b/other.go\n@@ -1,1 +1,20000 @@\n" +
		strings.Repeat("+// filler line for the diff budget\n", 5000)
	d := discussion(thread(false, cmt("5001", "bob", "Should punctuation have a default?", at(0))))
	req := request(d, &scriptedProvider{t: t, answers: []string{verdictJSON(entry("5001", "addressed", "A default was added."))}})
	req.Diff = sampleDiff + other
	_, report := run(t, req)

	if !report.Truncated {
		t.Fatal("expected the other file to be cut")
	}
	if it := itemByRoot(t, report, "5001"); it.Status != ItemAddressed || it.Source != SourceModel {
		t.Errorf("a verdict on a file that went in whole should stand: %+v", it)
	}
}

func TestModelFailureLeavesItemsUnclear(t *testing.T) {
	d := discussion(thread(false, cmt("5001", "bob", "Rename this.", at(0))))
	model := &scriptedProvider{t: t, err: &llm.CallError{Stage: llm.StageClientInit, Err: errors.New("GEMINI_API_KEY not set")}}
	res, report := run(t, request(d, model))

	if it := itemByRoot(t, report, "5001"); it.Status != ItemUnclear || it.Source != SourceGitHub {
		t.Errorf("unexpected item: %+v", it)
	}
	if res.Status != StatusSuccess || report.Verdict != VerdictUnclear {
		t.Errorf("status %q verdict %q: a missing model degrades, it doesn't fail", res.Status, report.Verdict)
	}
	if report.Model.Consulted || !strings.Contains(report.Model.Note, "GEMINI_API_KEY not set") {
		t.Errorf("model note should say why it wasn't consulted: %+v", report.Model)
	}
	if len(res.Log.Warnings) != 1 || !strings.Contains(res.Log.Warnings[0], "model unavailable") {
		t.Errorf("warnings = %v", res.Log.Warnings)
	}
}

func TestModelFailureNoteIsBriefButTheLogIsNot(t *testing.T) {
	// Gemini's error carries its whole JSON response body.
	body := "{\n  \"error\": {\n    \"code\": 400,\n    \"message\": \"API key not valid.\"" + strings.Repeat(",\n    \"x\": 1", 100) + "\n  }\n}"
	d := discussion(thread(false, cmt("5001", "bob", "Rename this.", at(0))))
	model := &scriptedProvider{t: t, err: &llm.CallError{Stage: llm.StageHTTPStatus,
		Err: fmt.Errorf("Gemini API request failed with status 400: %s", body)}}
	res, report := run(t, request(d, model))

	note := report.Model.Note
	if strings.Contains(note, "\n") || len([]rune(note)) > 400 || !strings.Contains(note, "status 400") {
		t.Errorf("the note should be one short line naming the failure: %q", note)
	}
	if !strings.Contains(res.Log.Warnings[0], `"x": 1`) {
		t.Error("the call log should keep the whole error")
	}
}

func TestUnreadableModelAnswerLeavesItemsUnclear(t *testing.T) {
	d := discussion(thread(false, cmt("5001", "bob", "Rename this.", at(0))))
	res, report := run(t, request(d, &scriptedProvider{t: t, answers: []string{"Looks fine to me!"}}))

	if it := itemByRoot(t, report, "5001"); it.Status != ItemUnclear || it.Source != SourceGitHub {
		t.Errorf("unexpected item: %+v", it)
	}
	if res.Log.ResponseSnippet != "Looks fine to me!" {
		t.Errorf("the call log should show what the model said: %q", res.Log.ResponseSnippet)
	}
}

func TestItemsTheModelSkippedStayUnclear(t *testing.T) {
	d := discussion(
		thread(false, cmt("5001", "bob", "Rename this.", at(0))),
		thread(false, cmt("6001", "bob", "And this.", at(0))),
	)
	res, report := run(t, request(d, &scriptedProvider{t: t, answers: []string{verdictJSON(
		entry("5001", "addressed", "Renamed."),
		entry("424242", "addressed", "Not an item."),
	)}}))

	if it := itemByRoot(t, report, "6001"); it.Status != ItemUnclear || !strings.Contains(it.Rationale, "no verdict") {
		t.Errorf("unexpected item: %+v", it)
	}
	joined := strings.Join(res.Log.Warnings, "\n")
	if !strings.Contains(joined, "no verdict for 1 of 2") || !strings.Contains(joined, "wasn't asked about") {
		t.Errorf("warnings = %v", res.Log.Warnings)
	}
}

func TestConversationFromTheAuthorAndBotsIsContextOnly(t *testing.T) {
	d := discussion()
	d.Conversation = []Comment{
		cmt("9001", "carol", "Please add a test for the CLI.", at(0)),
		cmt("9002", "ci-bot[bot]", "Deploy preview ready.", at(1)),
		cmt("9003", "alice", "Added one in main_test.ts.", at(3)),
	}
	model := &scriptedProvider{t: t, answers: []string{verdictJSON(entry("9001", "addressed", "The author added a test."))}}
	_, report := run(t, request(d, model))

	if len(report.Items) != 1 {
		t.Fatalf("only carol's comment is something to address, got %+v", report.Items)
	}
	it := report.Items[0]
	if it.Kind != KindConversation || it.ThreadID != nil || it.Status != ItemAddressed || it.Source != SourceModel {
		t.Errorf("unexpected item: %+v", it)
	}
	prompt := model.prompts[0]
	if !strings.Contains(prompt, "Added one in main_test.ts.") {
		t.Error("the author's reply should reach the model as context")
	}
	if strings.Contains(prompt, "Deploy preview ready.") {
		t.Error("bot chatter should be left out of the prompt")
	}
}

func TestAnnotationsOnlyForAnchoredOpenThreads(t *testing.T) {
	outdated := thread(false, cmt("7001", "bob", "Old line.", at(3)))
	outdated.Outdated = true
	outdated.Line = 0
	d := discussion(
		thread(false, cmt("5001", "bob", "Open.", at(3))),    // outstanding, anchored
		thread(true, cmt("6001", "bob", "Resolved.", at(0))), // addressed
		outdated, // outstanding, but nowhere in the diff to hang it
	)
	res, report := run(t, request(d, noModel{t}))

	if len(res.Annotations) != 1 || res.Annotations[0].Filename != "src/greet.ts" || res.Annotations[0].Severity != "warning" {
		t.Errorf("annotations = %+v", res.Annotations)
	}
	// The outdated thread still appears in the outstanding list.
	outstanding := res.Outstanding.([]ReportItem)
	if len(outstanding) != 2 {
		t.Errorf("both open threads belong in the outstanding list, got %+v", outstanding)
	}
	if report.Counts != (ReportCounts{Total: 3, Addressed: 1, Outstanding: 2}) {
		t.Errorf("counts = %+v", report.Counts)
	}
}

func TestTooManyItemsAreCappedAndMarkedTruncated(t *testing.T) {
	var threads []Thread
	var entries []string
	for i := 0; i < maxModelItems+5; i++ {
		id := fmt.Sprintf("%d", 5000+i)
		threads = append(threads, thread(false, cmt(id, "bob", "Nit "+id, at(0))))
		entries = append(entries, entry(id, "addressed", "Done."))
	}
	_, report := run(t, request(discussion(threads...), &scriptedProvider{t: t, answers: []string{verdictJSON(entries...)}}))

	if report.Model.Asked != maxModelItems || !report.Truncated {
		t.Errorf("asked %d truncated %v", report.Model.Asked, report.Truncated)
	}
	notSent := 0
	for _, it := range report.Items {
		if strings.Contains(it.Rationale, "not sent to the model") {
			notSent++
			if it.Status != ItemUnclear {
				t.Errorf("an item that wasn't sent took a verdict: %+v", it)
			}
		}
	}
	if notSent != 5 {
		t.Errorf("expected 5 items over the cap, got %d", notSent)
	}
}

func TestAgentModeCanReadFilesCutFromThePrompt(t *testing.T) {
	big := "diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n@@ -1,1 +1,20000 @@\n" +
		strings.Repeat("+// a very long generated file line\n", 5000)
	th := thread(false, cmt("5001", "bob", "Fix the loop in big.go.", at(0)))
	th.Path = "big.go"

	model := &scriptedProvider{t: t, answers: []string{
		`TOOL_CALL {"name": "read_file", "arguments": {"path": "big.go"}}`,
		verdictJSON(entry("5001", "addressed", "The loop now terminates.")),
	}}
	req := request(discussion(th), model)
	req.Diff = big
	req.Mode = config.AIModeAgent
	req.Agent = NewProviderAgent(model)
	var read []string
	req.ReadFile = func(_ context.Context, path string) (string, error) {
		read = append(read, path)
		return "package big\n\nfunc loop() {}\n", nil
	}
	_, report := run(t, req)

	if len(read) != 1 || read[0] != "big.go" {
		t.Errorf("ReadFile calls = %v", read)
	}
	if it := itemByRoot(t, report, "5001"); it.Status != ItemAddressed || it.Source != SourceModel {
		t.Errorf("a file the agent read counts as seen: %+v", it)
	}
	if report.Model.ToolCalls != 1 || report.Model.Turns != 2 || report.Model.Mode != config.AIModeAgent {
		t.Errorf("model use = %+v", report.Model)
	}
	if !strings.Contains(model.prompts[1], "[read_file result]\npackage big") {
		t.Errorf("the second turn should carry the tool result:\n%s", model.prompts[1])
	}
}

func TestReportJSONShape(t *testing.T) {
	d := discussion(thread(false, cmt("5001", "bob", "Open.", at(3))))
	d.Conversation = []Comment{cmt("9001", "carol", "Question?", at(3))}
	res, _ := run(t, request(d, &scriptedProvider{t: t, answers: []string{verdictJSON(entry("9001", "addressed", "Answered."))}}))

	encoded, err := res.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var doc struct {
		Body   Body `json:"body"`
		Report struct {
			Verdict string           `json:"verdict"`
			Items   []map[string]any `json:"items"`
		} `json:"report"`
		Outstanding []map[string]any `json:"outstanding"`
		Truncated   bool             `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(encoded), &doc); err != nil {
		t.Fatalf("stored result is not JSON: %v", err)
	}
	if doc.Body.BodyType != BodyMarkdown || doc.Report.Verdict != VerdictOutstanding || len(doc.Outstanding) != 1 {
		t.Errorf("unexpected document: %+v", doc)
	}
	for _, it := range doc.Report.Items {
		for _, key := range []string{"root_comment_id", "thread_id", "status", "source", "rationale"} {
			if _, ok := it[key]; !ok {
				t.Errorf("item missing %q: %v", key, it)
			}
		}
		if it["kind"] == KindConversation && it["thread_id"] != nil {
			t.Errorf("a conversation item has no thread: %v", it)
		}
	}
}

func TestParseVerdicts(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    []modelVerdict
		wantErr bool
	}{
		{
			name: "plain object",
			text: verdictJSON(entry("5001", "addressed", "Done.")),
			want: []modelVerdict{{"5001", "addressed", "Done."}},
		},
		{
			name: "code fence and prose with braces after",
			text: "Here you go:\n```json\n" + verdictJSON(entry("5001", "Outstanding", "Not yet.")) + "\n```\nHope that helps {really}.",
			want: []modelVerdict{{"5001", "outstanding", "Not yet."}},
		},
		{
			name: "bare array with numeric and prefixed ids",
			text: `[{"id": 5001, "status": "addressed"}, {"id": "Item #6001", "status": "done"}]`,
			want: []modelVerdict{{"5001", "addressed", ""}, {"6001", "unclear", ""}},
		},
		{
			name:    "no JSON",
			text:    "All comments look addressed.",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVerdicts(tt.text)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVerdicts: %v", err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSplitDiffFiles(t *testing.T) {
	diff := sampleDiff + `diff --git a/old.txt b/old.txt
deleted file mode 100644
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-gone
+++ b/not-a-header.txt
`
	files := splitDiffFiles(diff)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.path)
	}
	if strings.Join(paths, ",") != "src/greet.ts,src/main.ts,old.txt" {
		t.Errorf("paths = %v", paths)
	}
	var joined strings.Builder
	for _, f := range files {
		joined.WriteString(f.text)
	}
	if joined.String() != diff {
		t.Error("the sections should add back up to the diff")
	}
}

func TestBudgetDiffPrefersTheAskedFiles(t *testing.T) {
	filler := "diff --git a/aaa.go b/aaa.go\n--- a/aaa.go\n+++ b/aaa.go\n@@ -1 +1 @@\n" + strings.Repeat("+xxxxxxxxx\n", 400)
	diff := filler + sampleDiff
	asked := []*workItem{{item: ReportItem{Kind: KindThread, Path: "src/main.ts"}}}

	text, readable, cut := budgetDiff(diff, asked, 2500)
	if !readable["src/main.ts"] {
		t.Error("the asked file should go in first, whole")
	}
	if !cut || readable[wholeDiffKey] {
		t.Error("the filler cannot fit too, so the diff is cut")
	}
	if !strings.Contains(text, "console.log(message);") {
		t.Errorf("prompt diff missing the asked file:\n%s", text)
	}
}

func TestCutConversationContextDiscardsConversationVerdicts(t *testing.T) {
	// The reply that settles carol's request is at the end of a conversation
	// too long for the prompt: a verdict made without it can't stand.
	d := discussion()
	d.Conversation = []Comment{cmt("9001", "carol", "Please add a CLI test.", at(0))}
	for i := 0; i < 30; i++ {
		d.Conversation = append(d.Conversation, cmt(fmt.Sprintf("%d", 9100+i), "dave", strings.Repeat("chatter ", 250), at(1)))
	}
	d.Conversation = append(d.Conversation, cmt("9999", "alice", "Added it in main_test.ts.", at(2)))

	model := &scriptedProvider{t: t, answers: []string{verdictJSON(entry("9001", "outstanding", "No test mentioned."))}}
	_, report := run(t, request(d, model))

	if !strings.Contains(model.prompts[0], "the rest of the conversation was omitted") ||
		strings.Contains(model.prompts[0], "Added it in main_test.ts.") {
		t.Fatal("expected the tail of the conversation to be cut from the prompt")
	}
	if it := itemByRoot(t, report, "9001"); it.Status != ItemUnclear || !report.Truncated {
		t.Errorf("a verdict made without the whole conversation must degrade to unclear: %+v", it)
	}
}

func TestExcerptDropsBotMarkup(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "badge image in a link",
			body: `<a href="#"><img alt="P1" src="https://greptile-static-assets.s3.amazonaws.com/badges/p1.svg?v=7" align="top"></a> **Missing null check**` +
				"\n\nThe lookup can return nil.",
			want: "[P1] **Missing null check** The lookup can return nil.",
		},
		{
			name: "inline svg",
			body: `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 16 16"><title>logic</title>` +
				`<path d="M8 0a8 8 0 1 0 0 16A8 8 0 0 0 8 0z"/></svg> **logic:** Off by one.`,
			want: "**logic:** Off by one.",
		},
		{
			name: "image with no alt text",
			body: `<img src="https://example.com/icon.svg"> Rename this.`,
			want: "Rename this.",
		},
		{
			name: "html comments and footers",
			body: "Nit: rename.<!-- greptile_comment -->\n<sub>Reply to this to let me know.</sub>",
			want: "Nit: rename. Reply to this to let me know.",
		},
		{
			name: "block tags break words apart",
			body: "<details><summary>Prompt</summary>Fix it<br>now</details>",
			want: "Prompt Fix it now",
		},
		{
			name: "tags with attributes, self-closing tags and open details",
			body: `<details open><summary>Why</summary><p align="left">Nil map<br/>write</p></details>`,
			want: "Why Nil map write",
		},
		{
			name: "comparisons are not tags",
			body: "Fails when a<b and c>d.",
			want: "Fails when a<b and c>d.",
		},
		{
			name: "entities are decoded",
			body: "Use a &lt;div&gt; &amp; keep it",
			want: "Use a <div> & keep it",
		},
		{
			name: "code and generics survive",
			body: "Return `Vec<String>` and keep `<br>` in the template; Pair<A, B> and <T> too.",
			want: "Return `Vec<String>` and keep `<br>` in the template; Pair<A, B> and <T> too.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := excerpt(tt.body); got != tt.want {
				t.Errorf("excerpt(%q)\n got %q\nwant %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestBotMarkupDoesNotEatThePromptBudget(t *testing.T) {
	// An inline SVG longer than a comment's prompt budget is not evidence; the
	// comment's text still fits, so the model's verdict on it stands.
	svg := `<svg viewBox="0 0 16 16">` + strings.Repeat(`<path d="M0 0h16v16H0z"/>`, 200) + `</svg>`
	d := discussion(thread(false, cmt("5001", "greptile-apps[bot]", svg+" Punctuation needs a default.", at(0))))
	model := &scriptedProvider{t: t, answers: []string{verdictJSON(entry("5001", "addressed", "A default was added."))}}
	_, report := run(t, request(d, model))

	it := itemByRoot(t, report, "5001")
	if it.Status != ItemAddressed || it.Source != SourceModel || report.Truncated {
		t.Errorf("the model's verdict should stand on the comment's text: %+v (truncated %v)", it, report.Truncated)
	}
	if it.Excerpt != "Punctuation needs a default." {
		t.Errorf("excerpt = %q", it.Excerpt)
	}
	if strings.Contains(model.prompts[0], "<svg") || !strings.Contains(model.prompts[0], "Punctuation needs a default.") {
		t.Errorf("the prompt should carry the text without the SVG:\n%s", model.prompts[0])
	}
}

func TestCodeContextOnlyOnThreadsNotAddressed(t *testing.T) {
	longHunk := "@@ -1,12 +1,12 @@\n" + strings.Join([]string{
		" line 1", " line 2", " line 3", "-line 4", "+line 4b", " line 5", " line 6", " line 7", " line 8", "+line 9",
	}, "\n")
	open := thread(false,
		cmt("5001", "bob", "Line 9 needs a guard.", at(0)),
		cmt("5002", "bob", "Still missing.", at(3)), // after the latest commit: outstanding
	)
	open.DiffHunk = longHunk
	short := thread(false,
		cmt("6001", "carol", "Is this right?", at(0)),
		cmt("6002", "alice", "Looking.", at(3)),
	)
	short.Path = "src/main.ts"
	short.DiffHunk = "@@ -1,3 +1,4 @@\n-console.log('hello');\n+console.log(message);\n"
	resolved := thread(true, cmt("7001", "dave", "Rename.", at(0)))
	resolved.Path = "src/zz.ts"
	resolved.DiffHunk = "@@ -1 +1 @@\n+x"
	d := discussion(open, short, resolved)
	d.Conversation = []Comment{cmt("9001", "carol", "Question?", at(3))}
	res, report := run(t, request(d, &scriptedProvider{t: t, answers: []string{verdictJSON(entry("9001", "outstanding", "Unanswered."))}}))

	got := itemByRoot(t, report, "5001").CodeContext
	want := strings.Join([]string{" line 3", "-line 4", "+line 4b", " line 5", " line 6", " line 7", " line 8", "+line 9"}, "\n")
	if got != want {
		t.Errorf("a long hunk keeps its last %d lines and loses its header:\n got %q\nwant %q", codeContextLines, got, want)
	}
	if got := itemByRoot(t, report, "6001").CodeContext; got != "@@ -1,3 +1,4 @@\n-console.log('hello');\n+console.log(message);" {
		t.Errorf("a short hunk is kept whole: %q", got)
	}
	if got := itemByRoot(t, report, "7001").CodeContext; got != "" {
		t.Errorf("an addressed thread carries no code context: %q", got)
	}
	if got := itemByRoot(t, report, "9001").CodeContext; got != "" {
		t.Errorf("a conversation comment has no code: %q", got)
	}

	outstanding := res.Outstanding.([]ReportItem)
	if len(outstanding) == 0 || outstanding[0].CodeContext == "" {
		t.Errorf("the served outstanding list should carry the code context: %+v", outstanding)
	}
	body := res.Body.BodyContent
	if !strings.Contains(body, "\n  ```diff\n   line 3\n  -line 4\n") {
		t.Errorf("the markdown should show the context inside the item:\n%s", body)
	}
	if strings.Contains(body, "+x") {
		t.Errorf("the addressed thread's code should not be in the markdown:\n%s", body)
	}
}

func TestCodeContextClipsLongLinesAndFencesBackticks(t *testing.T) {
	long := "+" + strings.Repeat("x", 500)
	if got := codeContext(long); utf8.RuneCountInString(got) != maxCodeLineRunes+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("a long line should be clipped to %d runes plus an ellipsis, got %d", maxCodeLineRunes, utf8.RuneCountInString(got))
	}
	if got := codeContext("\n \n"); got != "" {
		t.Errorf("a blank hunk has no context: %q", got)
	}
	if got := codeFenceFor("+x := \"```\""); got != "````" {
		t.Errorf("fence = %q, want one backtick longer than the code's", got)
	}
	if got := codeFenceFor("+x"); got != "```" {
		t.Errorf("fence = %q, want ```", got)
	}
}
