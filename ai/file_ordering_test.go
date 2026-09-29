package ai

import (
	"context"
	"crs/llm"
	"crs/utils"
	"errors"
	"strings"
	"testing"
)

// orderingDiff is a raw three-file diff: a test file first, as GitHub might
// list it, then the code it tests.
const orderingDiff = `diff --git a/main_test.go b/main_test.go
index 1111111..2222222 100644
--- a/main_test.go
+++ b/main_test.go
@@ -1,3 +1,4 @@
 package main
+func TestGreet(t *testing.T) {}
diff --git a/main.go b/main.go
index 3333333..4444444 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+func main() { greet() }
diff --git a/helper.go b/helper.go
new file mode 100644
index 0000000..5555555
--- /dev/null
+++ b/helper.go
@@ -0,0 +1,2 @@
+package main
+func greet() {}
`

func appliedRequest(diff string, model Provider) Request {
	return Request{Owner: "acme", Repo: "widgets", Number: 42, HeadSHA: "sha-1", Diff: diff, Model: model}
}

func TestFileOrderingStoresTheModelsOrder(t *testing.T) {
	model := &scriptedProvider{t: t, answers: []string{"main.go\nhelper.go\nmain_test.go\n"}}
	res, err := FileOrdering{}.Run(context.Background(), appliedRequest(orderingDiff, model))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	report, ok := res.Report.(FileOrderingReport)
	if !ok || strings.Join(report.Files, ",") != "main.go,helper.go,main_test.go" {
		t.Fatalf("report = %#v", res.Report)
	}
	if res.Status != "" || res.Truncated {
		t.Errorf("status %q truncated %v", res.Status, res.Truncated)
	}
	if !strings.Contains(res.Body.BodyContent, "1. `main.go`") || !strings.Contains(res.Body.BodyContent, "3. `main_test.go`") {
		t.Errorf("body:\n%s", res.Body.BodyContent)
	}
	if !strings.HasPrefix(res.Log.Input, "3 file(s), ") || !strings.HasSuffix(res.Log.Input, "-byte diff") {
		t.Errorf("log input = %q", res.Log.Input)
	}
	if res.Log.Parsed != "3 ordered file paths" {
		t.Errorf("log parsed = %q", res.Log.Parsed)
	}

	// The prompt lists every file and carries the diff, and asks for nothing
	// but the order.
	prompt := model.prompts[0]
	for _, want := range []string{
		"You are ordering the files of a pull request diff",
		"Files in this diff:\n- main_test.go\n- main.go\n- helper.go\n",
		"=== FILE: helper.go ===",
		"+ func greet() {}",
		"Respond with ONLY the file paths, one per line",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "REVIEW_EASE") {
		t.Error("the ordering prompt must not ask for a rating")
	}
}

func TestFileOrderingSkipsTheModelForASingleFile(t *testing.T) {
	single := orderingDiff[strings.Index(orderingDiff, "diff --git a/helper.go"):]
	res, err := FileOrdering{}.Run(context.Background(), appliedRequest(single, noModel{t}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report := res.Report.(FileOrderingReport); strings.Join(report.Files, ",") != "helper.go" {
		t.Errorf("report = %#v", report)
	}
}

func TestFileOrderingFailures(t *testing.T) {
	t.Run("no diff", func(t *testing.T) {
		_, err := FileOrdering{}.Run(context.Background(), appliedRequest("  \n", noModel{t}))
		wantStage(t, err, StageInput)
	})
	t.Run("model unavailable", func(t *testing.T) {
		model := &scriptedProvider{t: t, err: &llm.CallError{Stage: llm.StageClientInit, Err: errors.New("GEMINI_API_KEY not set")}}
		res, err := FileOrdering{}.Run(context.Background(), appliedRequest(orderingDiff, model))
		wantStage(t, err, llm.StageClientInit)
		if !strings.HasPrefix(res.Log.Input, "3 file(s)") {
			t.Errorf("a failed run still says what it worked from: %+v", res.Log)
		}
	})
	t.Run("no paths in the answer", func(t *testing.T) {
		model := &scriptedProvider{t: t, answers: []string{"```\n```\n"}}
		res, err := FileOrdering{}.Run(context.Background(), appliedRequest(orderingDiff, model))
		wantStage(t, err, llm.StageParse)
		if res.Log.ResponseSnippet == "" || res.Log.Parsed != "0 ordered file paths" {
			t.Errorf("an unreadable answer is logged for debugging: %+v", res.Log)
		}
	})
}

func TestFileOrderingMarksATruncatedDiff(t *testing.T) {
	big := orderingDiff + "diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -1,1 +1,1 @@\n+" +
		strings.Repeat("x", maxDiffInputBytes) + "\n"
	model := &scriptedProvider{t: t, answers: []string{"main.go\n"}}
	res, err := FileOrdering{}.Run(context.Background(), appliedRequest(big, model))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Truncated || !strings.Contains(res.Log.Input, "truncated to") {
		t.Errorf("truncated %v, log input %q", res.Truncated, res.Log.Input)
	}
	// The file list still goes in whole.
	if !strings.Contains(model.prompts[0], "- big.txt\n") || len(model.prompts[0]) > maxDiffInputBytes+5000 {
		t.Errorf("prompt is %d bytes", len(model.prompts[0]))
	}
}

func TestParseFileOrdering(t *testing.T) {
	got := parseFileOrdering("REVIEW_EASE: medium\n- main.go\n\n`helper.go`\n**Review_Ease: hard**\nb/main_test.go\n")
	if strings.Join(got, ",") != "main.go,helper.go,main_test.go" {
		t.Errorf("parseFileOrdering = %q", got)
	}
}

func TestCleanFileName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"server/server.go", "server/server.go"},
		{"  server/server.go  ", "server/server.go"},
		{"- server/server.go", "server/server.go"},
		{"* server/server.go", "server/server.go"},
		{"`server/server.go`", "server/server.go"},
		{"\"server/server.go\"", "server/server.go"},
		{"a/server/server.go", "server/server.go"},
		{"b/server/server.go", "server/server.go"},
		{"1. server/server.go", "server/server.go"},
		{"12) `server/server.go`", "server/server.go"},
		{"2024/notes.md", "2024/notes.md"},
		{"3.go", "3.go"},
		{"", ""},
		{"```", ""},
	}
	for i, tc := range tests {
		if got := cleanFileName(tc.in); got != tc.want {
			t.Errorf("[%d] cleanFileName(%q) = %q, want %q", i, tc.in, got, tc.want)
		}
	}
}

func TestOrderDiffFiles(t *testing.T) {
	files := []*utils.DiffFile{
		{NewName: "server/server_test.go"},
		{NewName: "server/server.go"},
		{NewName: "config/config.go"},
		{NewName: "styles/app.css"},
	}

	// The model's reading order; basename-only and a/ prefix variants must
	// still match.
	names := []string{"server/server.go", "config.go", "a/styles/app.css", "server/server_test.go"}
	got := OrderDiffFiles(files, names)

	wantOrder := []string{
		"server/server.go",
		"config/config.go",
		"styles/app.css",
		"server/server_test.go",
	}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d files, want %d", len(got), len(wantOrder))
	}
	for i, w := range wantOrder {
		if got[i].NewName != w {
			t.Errorf("[%d] got %q, want %q", i, got[i].NewName, w)
		}
	}
}

func TestOrderDiffFilesAppendsUnmentionedFiles(t *testing.T) {
	files := []*utils.DiffFile{
		{NewName: "a.go"},
		{NewName: "b.go"},
		{NewName: "c.go"},
	}
	// The model only mentions one file; the rest keep their relative order.
	got := OrderDiffFiles(files, []string{"c.go"})

	wantOrder := []string{"c.go", "a.go", "b.go"}
	for i, w := range wantOrder {
		if got[i].NewName != w {
			t.Errorf("[%d] got %q, want %q", i, got[i].NewName, w)
		}
	}
}

func TestStoredFileOrderingReadsWhatRunStored(t *testing.T) {
	encoded, err := Result{Report: FileOrderingReport{Files: []string{"main.go", "main_test.go"}}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if got := StoredFileOrdering(encoded); strings.Join(got, ",") != "main.go,main_test.go" {
		t.Errorf("StoredFileOrdering = %q", got)
	}
	failed, _ := Result{Body: Body{BodyContent: "**File ordering failed.**"}}.Encode()
	for _, stored := range []string{"", "not json", failed} {
		if got := StoredFileOrdering(stored); got != nil {
			t.Errorf("StoredFileOrdering(%q) = %q, want nil", stored, got)
		}
	}
}
