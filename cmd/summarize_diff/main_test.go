package main

import (
	"bytes"
	"crs/cmd/internal/pluginkit"
	"crs/server"
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
diff --git a/README.md b/README.md
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/README.md
@@ -0,0 +1,2 @@
+# Title
+body
`

func TestBuildPromptListsFilesAndContext(t *testing.T) {
	prompt := buildPrompt(sampleDiff, pluginkit.PRMetadata{Title: "Add a thing", Body: "Because."})

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

func TestBuildPromptWithoutHunks(t *testing.T) {
	prompt := buildPrompt("not a diff at all", pluginkit.PRMetadata{})

	if !strings.Contains(prompt, "not a diff at all") {
		t.Error("prompt dropped the diff it could not number")
	}
	if !strings.Contains(prompt, "(none parsed") {
		t.Error("prompt did not tell the model there are no annotatable files")
	}
}

func TestResponseFor(t *testing.T) {
	tests := []struct {
		name            string
		reply           string
		wantBody        string
		wantAnnotations []pluginkit.Annotation
	}{
		{
			name:     "structured reply",
			reply:    `{"summary":"- did a thing","annotations":[{"filename":"server/plugins.go","line":11,"severity":"warning","content":"looks wrong"}]}`,
			wantBody: "- did a thing",
			wantAnnotations: []pluginkit.Annotation{
				{Filename: "server/plugins.go", Line: 11, Severity: "warning", Content: "looks wrong"},
			},
		},
		{
			name:            "fenced reply",
			reply:           "```json\n{\"summary\":\"- did a thing\",\"annotations\":[]}\n```",
			wantBody:        "- did a thing",
			wantAnnotations: []pluginkit.Annotation{},
		},
		{
			name: "annotations normalized and unanchorable ones dropped",
			reply: `{"summary":"s","annotations":[
				{"filename":"./main.go","line":3,"severity":"WARNING","content":" spaced "},
				{"filename":"","line":5,"severity":"info","content":"no file"},
				{"filename":"main.go","line":0,"severity":"info","content":"no line"}
			]}`,
			wantBody: "s",
			wantAnnotations: []pluginkit.Annotation{
				{Filename: "main.go", Line: 3, Severity: pluginkit.SeverityWarning, Content: "spaced"},
			},
		},
		{
			// Plain text is what this plugin used to emit, and what it still
			// emits if the model ignores the schema.
			name:            "legacy plain text",
			reply:           "- just a summary\n- no JSON here",
			wantBody:        "- just a summary\n- no JSON here",
			wantAnnotations: []pluginkit.Annotation{},
		},
		{
			name:            "json without a summary",
			reply:           `{"summary":"","annotations":[{"filename":"main.go","line":2,"severity":"info","content":"x"}]}`,
			wantBody:        `{"summary":"","annotations":[{"filename":"main.go","line":2,"severity":"info","content":"x"}]}`,
			wantAnnotations: []pluginkit.Annotation{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := responseFor(tt.reply)
			if got.Body.BodyType != pluginkit.BodyTypeMarkdown {
				t.Errorf("body type = %q, want %q", got.Body.BodyType, pluginkit.BodyTypeMarkdown)
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
	response := responseFor(`{"summary":"- terse","annotations":[{"filename":"server/plugins.go","line":11,"severity":"warning","content":"looks wrong"}]}`)

	var out bytes.Buffer
	if err := pluginkit.EncodeResponse(&out, response); err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}

	parsed := server.ParsePluginOutput(out.String())
	if parsed.Body.BodyType != server.BodyTypeMarkdown || parsed.Body.BodyContent != "- terse" {
		t.Errorf("parsed body = %+v", parsed.Body)
	}
	want := []server.PluginAnnotation{
		{Filename: "server/plugins.go", Line: 11, Severity: "warning", Content: "looks wrong"},
	}
	if !reflect.DeepEqual(parsed.Annotations, want) {
		t.Errorf("parsed annotations = %+v, want %+v", parsed.Annotations, want)
	}
}
