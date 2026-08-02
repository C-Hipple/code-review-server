package pluginkit

import (
	"bytes"
	"crs/server"
	"reflect"
	"strings"
	"testing"
)

func TestSanitizeAnnotations(t *testing.T) {
	got := SanitizeAnnotations([]Annotation{
		{Filename: "./main.go", Line: 3, Severity: "WARNING", Content: " spaced "},
		{Filename: "main.go", Line: 4, Severity: "", Content: "no severity"},
		{Filename: "", Line: 5, Severity: SeverityInfo, Content: "no file"},
		{Filename: "main.go", Line: 0, Severity: SeverityInfo, Content: "no line"},
		{Filename: "main.go", Line: 6, Severity: SeverityInfo, Content: "  "},
	})

	want := []Annotation{
		{Filename: "main.go", Line: 3, Severity: SeverityWarning, Content: "spaced"},
		{Filename: "main.go", Line: 4, Severity: SeverityInfo, Content: "no severity"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("annotations = %+v, want %+v", got, want)
	}
}

func TestSanitizeAnnotationsIsNeverNil(t *testing.T) {
	// The contract's annotations key should encode as [], not null.
	if got := SanitizeAnnotations(nil); got == nil || len(got) != 0 {
		t.Errorf("SanitizeAnnotations(nil) = %#v, want an empty slice", got)
	}
}

func TestTrimCodeFence(t *testing.T) {
	tests := map[string]string{
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		"```\n{\"a\":1}\n```":     `{"a":1}`,
		` {"a":1} `:               `{"a":1}`,
		"```no newline":           "```no newline",
	}
	for reply, want := range tests {
		if got := TrimCodeFence(reply); got != want {
			t.Errorf("TrimCodeFence(%q) = %q, want %q", reply, got, want)
		}
	}
}

func TestPRMetadataContext(t *testing.T) {
	if got := (PRMetadata{}).Context(); got != "" {
		t.Errorf("empty metadata context = %q, want empty", got)
	}
	got := PRMetadata{Title: "Add a thing", Body: "Because."}.Context()
	if got != "PR Title: Add a thing\nPR Description: Because.\n" {
		t.Errorf("context = %q", got)
	}
}

// TestEncodedResponseMatchesContract runs what a plugin writes to stdout
// through the server's parser, which is what decides whether a plugin speaks
// the contract or gets treated as legacy output.
func TestEncodedResponseMatchesContract(t *testing.T) {
	response := MarkdownResponse("- <b>bold</b> & terse", []Annotation{
		{Filename: "server/plugins.go", Line: 11, Severity: SeverityWarning, Content: "looks wrong"},
	})

	var out bytes.Buffer
	if err := EncodeResponse(&out, response); err != nil {
		t.Fatalf("EncodeResponse: %v", err)
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
		{Filename: "server/plugins.go", Line: 11, Severity: SeverityWarning, Content: "looks wrong"},
	}
	if !reflect.DeepEqual(parsed.Annotations, want) {
		t.Errorf("parsed annotations = %+v, want %+v", parsed.Annotations, want)
	}
}
