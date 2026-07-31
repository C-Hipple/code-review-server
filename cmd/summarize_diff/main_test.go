package main

import (
	"bytes"
	"crs/server"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const sampleDiff = `diff --git a/server/plugins.go b/server/plugins.go
index 1111111..2222222 100644
--- a/server/plugins.go
+++ b/server/plugins.go
@@ -10,4 +10,5 @@ func executePlugin() {
 	ctx := context.Background()
-	old := run(ctx)
+	fresh := run(ctx)
+	log(fresh)
 	return
@@ -30,2 +31,3 @@
 	tail()
+	extra()
diff --git a/README.md b/README.md
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/README.md
@@ -0,0 +1,2 @@
+# Title
+body
diff --git a/old.txt b/old.txt
deleted file mode 100644
index 4444444..0000000
--- a/old.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-gone
-also gone
`

// canonical rewrites numbered diff lines as "<mark><line>:<content>" so the
// expectations below stay readable without hard-coding column widths. Headers
// pass through untouched.
func canonical(rendered string) []string {
	lines := []string{}
	for _, line := range strings.Split(rendered, "\n") {
		head, content, ok := strings.Cut(line, " | ")
		if !ok {
			lines = append(lines, line)
			continue
		}
		lines = append(lines, fmt.Sprintf("%c%s:%s", head[0], strings.TrimSpace(head[1:]), content))
	}
	return lines
}

func TestNumberedDiff(t *testing.T) {
	rendered, files := numberedDiff(sampleDiff)

	wantFiles := []string{"server/plugins.go", "README.md", "old.txt"}
	if !reflect.DeepEqual(files, wantFiles) {
		t.Errorf("files = %v, want %v", files, wantFiles)
	}

	// Numbers count the head side: unchanged and added lines carry the line
	// they will occupy once the PR merges, removed lines carry none.
	want := []string{
		"=== FILE: server/plugins.go ===",
		"@@ -10,4 +10,5 @@ func executePlugin() {",
		" 10:\tctx := context.Background()",
		"-:\told := run(ctx)",
		"+11:\tfresh := run(ctx)",
		"+12:\tlog(fresh)",
		" 13:\treturn",
		"@@ -30,2 +31,3 @@",
		" 31:\ttail()",
		"+32:\textra()",
		"",
		"=== FILE: README.md ===",
		"@@ -0,0 +1,2 @@",
		"+1:# Title",
		"+2:body",
		"",
		"=== FILE: old.txt ===",
		"@@ -1,2 +0,0 @@",
		"-:gone",
		"-:also gone",
	}
	if got := canonical(rendered); !reflect.DeepEqual(got, want) {
		t.Errorf("numbered diff mismatch\ngot:\n%s\nwant:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestNumberedDiffSkipsUnannotatableLines(t *testing.T) {
	// A binary file has no lines to anchor to, and the no-newline marker isn't
	// a line of either side. An empty context line is a line, though: diffs
	// that strip its leading space still have to keep the count in step.
	diff := "diff --git a/img.png b/img.png\n" +
		"index 0000000..abc1234\n" +
		"Binary files /dev/null and b/img.png differ\n" +
		"diff --git a/a.txt b/a.txt\n" +
		"--- a/a.txt\n" +
		"+++ b/a.txt\n" +
		"@@ -1,3 +1,3 @@\n" +
		" first\n" +
		"\n" +
		"+added\n" +
		"\\ No newline at end of file\n"

	rendered, files := numberedDiff(diff)
	if !reflect.DeepEqual(files, []string{"a.txt"}) {
		t.Errorf("files = %v, want [a.txt]", files)
	}
	want := []string{
		"=== FILE: a.txt ===",
		"@@ -1,3 +1,3 @@",
		" 1:first",
		" 2:",
		"+3:added",
	}
	if got := canonical(rendered); !reflect.DeepEqual(got, want) {
		t.Errorf("numbered diff mismatch\ngot:\n%s\nwant:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestNumberedDiffWithoutHunks(t *testing.T) {
	// A diff the renderer can't make sense of yields nothing to annotate, and
	// buildPrompt falls back to the raw text.
	rendered, files := numberedDiff("not a diff at all")
	if rendered != "" || len(files) != 0 {
		t.Fatalf("numberedDiff(garbage) = %q, %v; want empty", rendered, files)
	}

	prompt := buildPrompt("not a diff at all", PRMetadata{})
	if !strings.Contains(prompt, "not a diff at all") {
		t.Error("prompt dropped the diff it could not number")
	}
	if !strings.Contains(prompt, "(none parsed") {
		t.Error("prompt did not tell the model there are no annotatable files")
	}
}

func TestBuildPromptListsFilesAndContext(t *testing.T) {
	prompt := buildPrompt(sampleDiff, PRMetadata{Title: "Add a thing", Body: "Because."})

	for _, want := range []string{
		"PR Title: Add a thing",
		"PR Description: Because.",
		"server/plugins.go",
		"README.md",
		"+    11 | \tfresh := run(ctx)",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestResponseFor(t *testing.T) {
	tests := []struct {
		name            string
		reply           string
		wantBody        string
		wantAnnotations []PluginAnnotation
	}{
		{
			name:     "structured reply",
			reply:    `{"summary":"- did a thing","annotations":[{"filename":"server/plugins.go","line":11,"severity":"warning","content":"looks wrong"}]}`,
			wantBody: "- did a thing",
			wantAnnotations: []PluginAnnotation{
				{Filename: "server/plugins.go", Line: 11, Severity: "warning", Content: "looks wrong"},
			},
		},
		{
			name:            "fenced reply",
			reply:           "```json\n{\"summary\":\"- did a thing\",\"annotations\":[]}\n```",
			wantBody:        "- did a thing",
			wantAnnotations: []PluginAnnotation{},
		},
		{
			name: "annotations normalized and unanchorable ones dropped",
			reply: `{"summary":"s","annotations":[
				{"filename":"./main.go","line":3,"severity":"WARNING","content":" spaced "},
				{"filename":"main.go","line":4,"severity":"","content":"no severity"},
				{"filename":"","line":5,"severity":"info","content":"no file"},
				{"filename":"main.go","line":0,"severity":"info","content":"no line"},
				{"filename":"main.go","line":6,"severity":"info","content":"  "}
			]}`,
			wantBody: "s",
			wantAnnotations: []PluginAnnotation{
				{Filename: "main.go", Line: 3, Severity: "warning", Content: "spaced"},
				{Filename: "main.go", Line: 4, Severity: severityInfo, Content: "no severity"},
			},
		},
		{
			// Plain text is what this plugin used to emit, and what it still
			// emits if the model ignores the schema.
			name:            "legacy plain text",
			reply:           "- just a summary\n- no JSON here",
			wantBody:        "- just a summary\n- no JSON here",
			wantAnnotations: []PluginAnnotation{},
		},
		{
			name:            "json without a summary",
			reply:           `{"summary":"","annotations":[{"filename":"main.go","line":2,"severity":"info","content":"x"}]}`,
			wantBody:        `{"summary":"","annotations":[{"filename":"main.go","line":2,"severity":"info","content":"x"}]}`,
			wantAnnotations: []PluginAnnotation{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := responseFor(tt.reply)
			if got.Body.BodyType != bodyTypeMarkdown {
				t.Errorf("body type = %q, want %q", got.Body.BodyType, bodyTypeMarkdown)
			}
			if got.Body.BodyContent != tt.wantBody {
				t.Errorf("body = %q, want %q", got.Body.BodyContent, tt.wantBody)
			}
			if !reflect.DeepEqual(got.Annotations, tt.wantAnnotations) {
				t.Errorf("annotations = %+v, want %+v", got.Annotations, tt.wantAnnotations)
			}
		})
	}
}

// TestEncodedOutputMatchesContract runs what the plugin writes to stdout
// through the server's parser, which is what decides whether a plugin speaks
// the contract or gets treated as legacy output.
func TestEncodedOutputMatchesContract(t *testing.T) {
	response := responseFor(`{"summary":"- <b>bold</b> & terse","annotations":[{"filename":"server/plugins.go","line":11,"severity":"warning","content":"looks wrong"}]}`)

	var out bytes.Buffer
	if err := encodeResponse(&out, response); err != nil {
		t.Fatalf("encodeResponse: %v", err)
	}
	if strings.Contains(out.String(), "\\u003c") {
		t.Error("stdout escaped HTML, which makes the stored raw result unreadable")
	}

	parsed := server.ParsePluginOutput(out.String())
	if parsed.Body.BodyType != server.BodyTypeMarkdown {
		t.Errorf("parsed body type = %q, want %q", parsed.Body.BodyType, server.BodyTypeMarkdown)
	}
	if parsed.Body.BodyContent != "- <b>bold</b> & terse" {
		t.Errorf("parsed body = %q", parsed.Body.BodyContent)
	}
	want := []server.PluginAnnotation{
		{Filename: "server/plugins.go", Line: 11, Severity: "warning", Content: "looks wrong"},
	}
	if !reflect.DeepEqual(parsed.Annotations, want) {
		t.Errorf("parsed annotations = %+v, want %+v", parsed.Annotations, want)
	}
}
