package pluginkit

import (
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
	rendered, files := NumberedDiff(sampleDiff)

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

	rendered, files := NumberedDiff(diff)
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

func TestPromptDiffWithoutHunks(t *testing.T) {
	// A diff the renderer can't make sense of yields nothing to annotate, so
	// the raw text is what a prompt has to fall back to.
	rendered, files := NumberedDiff("not a diff at all")
	if rendered != "" || len(files) != 0 {
		t.Fatalf("NumberedDiff(garbage) = %q, %v; want empty", rendered, files)
	}

	rendered, fileList := PromptDiff("not a diff at all")
	if rendered != "not a diff at all" {
		t.Errorf("rendered = %q, want the diff verbatim", rendered)
	}
	if !strings.Contains(fileList, "(none parsed") {
		t.Errorf("file list = %q, want a note that there are no annotatable files", fileList)
	}
}

func TestPromptDiffListsFiles(t *testing.T) {
	rendered, fileList := PromptDiff(sampleDiff)

	if fileList != "server/plugins.go\nREADME.md\nold.txt" {
		t.Errorf("file list = %q", fileList)
	}
	if !strings.Contains(rendered, "+    11 | \tfresh := run(ctx)") {
		t.Errorf("rendered diff missing a numbered line:\n%s", rendered)
	}
}
