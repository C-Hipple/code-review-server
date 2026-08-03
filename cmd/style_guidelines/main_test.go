package main

import (
	"bytes"
	"crs/cmd/internal/pluginkit"
	"crs/server"
	"reflect"
	"strings"
	"testing"
)

const sampleDiff = `diff --git a/api/views.py b/api/views.py
index 1111111..2222222 100644
--- a/api/views.py
+++ b/api/views.py
@@ -10,3 +10,5 @@ def index():
 	return render()
+def AdminPanel():
+	return secret(42)
`

const styleGuide = "# Style Guidelines\n\n- Use snake_case for all function names.\n"

func TestBuildPromptCarriesGuidelinesFilesAndContext(t *testing.T) {
	prompt := buildPrompt(sampleDiff, pluginkit.PRMetadata{Title: "Add a panel", Body: "Because."}, styleGuide)

	for _, want := range []string{
		"Use snake_case for all function names.",
		"PR Title: Add a panel",
		"PR Description: Because.",
		"api/views.py",
		// The head-side line number is what an annotation anchors to.
		"+    11 | def AdminPanel():",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestBuildPromptWithoutHunks(t *testing.T) {
	prompt := buildPrompt("not a diff at all", pluginkit.PRMetadata{}, styleGuide)

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
			reply:    `{"report":"## Violations\n- api/views.py:11","annotations":[{"filename":"api/views.py","line":11,"severity":"warning","content":"Function names must be snake_case."}]}`,
			wantBody: "## Violations\n- api/views.py:11",
			wantAnnotations: []pluginkit.Annotation{
				{Filename: "api/views.py", Line: 11, Severity: pluginkit.SeverityWarning, Content: "Function names must be snake_case."},
			},
		},
		{
			name:            "fenced reply",
			reply:           "```json\n{\"report\":\"looks clean\",\"annotations\":[]}\n```",
			wantBody:        "looks clean",
			wantAnnotations: []pluginkit.Annotation{},
		},
		{
			name: "annotations normalized and unanchorable ones dropped",
			reply: `{"report":"r","annotations":[
				{"filename":"./api/views.py","line":11,"severity":"ERROR","content":" snake_case "},
				{"filename":"api/views.py","line":12,"severity":"","content":"no severity"},
				{"filename":"","line":13,"severity":"info","content":"no file"},
				{"filename":"api/views.py","line":0,"severity":"info","content":"no line"},
				{"filename":"api/views.py","line":14,"severity":"info","content":"  "}
			]}`,
			wantBody: "r",
			wantAnnotations: []pluginkit.Annotation{
				{Filename: "api/views.py", Line: 11, Severity: pluginkit.SeverityError, Content: "snake_case"},
				{Filename: "api/views.py", Line: 12, Severity: pluginkit.SeverityInfo, Content: "no severity"},
			},
		},
		{
			// Plain text is what this plugin used to emit, and what it still
			// emits if the model ignores the schema.
			name:            "legacy plain text",
			reply:           "No violations found.\nOverall: fine.",
			wantBody:        "No violations found.\nOverall: fine.",
			wantAnnotations: []pluginkit.Annotation{},
		},
		{
			name:            "json without a report",
			reply:           `{"report":"","annotations":[{"filename":"api/views.py","line":11,"severity":"info","content":"x"}]}`,
			wantBody:        `{"report":"","annotations":[{"filename":"api/views.py","line":11,"severity":"info","content":"x"}]}`,
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
	response := responseFor(`{"report":"## Violations\n- api/views.py:11","annotations":[{"filename":"api/views.py","line":11,"severity":"warning","content":"Function names must be snake_case."}]}`)

	var out bytes.Buffer
	if err := pluginkit.EncodeResponse(&out, response); err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}

	parsed := server.ParsePluginOutput(out.String())
	if parsed.Body.BodyType != server.BodyTypeMarkdown || parsed.Body.BodyContent != "## Violations\n- api/views.py:11" {
		t.Errorf("parsed body = %+v", parsed.Body)
	}
	want := []server.PluginAnnotation{
		{Filename: "api/views.py", Line: 11, Severity: "warning", Content: "Function names must be snake_case."},
	}
	if !reflect.DeepEqual(parsed.Annotations, want) {
		t.Errorf("parsed annotations = %+v, want %+v", parsed.Annotations, want)
	}
}
