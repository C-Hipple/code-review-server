package main

import (
	"crs/cmd/internal/pluginkit"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

// summarize_diff emits the plugin response contract described in
// docs/plugins.md: a markdown body holding the summary, plus line-level
// annotations anchored to the head side of the PR's diff.

// maxAnnotations is what the model is asked for. A summary plugin that flags
// every other line is noise once the annotations render inline.
const maxAnnotations = 6

// modelOutput is the shape Gemini is asked to return, mirroring
// responseSchema below.
type modelOutput struct {
	Summary     string                 `json:"summary"`
	Annotations []pluginkit.Annotation `json:"annotations"`
}

// responseSchema constrains Gemini's reply to the summary and annotations we
// know how to turn into a plugin response.
func responseSchema() *pluginkit.Schema {
	return &pluginkit.Schema{
		Type: "OBJECT",
		Properties: map[string]*pluginkit.Schema{
			"summary": {
				Type:        "STRING",
				Description: "Terse markdown summary of the PR.",
			},
			"annotations": pluginkit.AnnotationsSchema(maxAnnotations),
		},
		PropertyOrdering: []string{"summary", "annotations"},
		Required:         []string{"summary", "annotations"},
	}
}

func buildPrompt(diff string, metadata pluginkit.PRMetadata) string {
	rendered, fileList := pluginkit.PromptDiff(diff)

	return fmt.Sprintf(`Summarize this PR, and flag the specific lines a reviewer should look at.

summary: 2-4 bullet points on key changes (one line each), then 1-2 brief
suggestions if any. Markdown. Be terse. No fluff.

annotations: at most %d remarks, each anchored to one line of the diff. Only
annotate a line that genuinely warrants a reviewer's attention; an empty list
is fine.
- filename: one of the paths listed under Files, copied exactly.
- line: the number shown beside that line in the diff. Lines marked "-" were
  removed and have no number, so they cannot be annotated.
- severity: %s, %s or %s.
- content: one or two sentences.

Files:
%s

%s

%sDiff:
%s
`, maxAnnotations, pluginkit.SeverityInfo, pluginkit.SeverityWarning, pluginkit.SeverityError,
		fileList, pluginkit.DiffLegend, metadata.Context(), rendered)
}

// responseFor turns Gemini's reply into a plugin response. A reply that isn't
// the JSON we asked for becomes the body verbatim, which is what this plugin
// emitted before it spoke the contract.
func responseFor(reply string) pluginkit.Response {
	var parsed modelOutput
	if err := json.Unmarshal([]byte(pluginkit.TrimCodeFence(reply)), &parsed); err != nil || strings.TrimSpace(parsed.Summary) == "" {
		return pluginkit.MarkdownResponse(reply, nil)
	}

	return pluginkit.MarkdownResponse(strings.TrimSpace(parsed.Summary), parsed.Annotations)
}

func main() {
	diff := flag.String("diff", "", "PR diff content")
	owner := flag.String("owner", "", "PR owner")
	repo := flag.String("repo", "", "PR repo")
	number := flag.Int("number", 0, "PR number")
	commentsJSON := flag.String("comments", "", "PR comments JSON")
	headersJSON := flag.String("headers", "", "PR metadata JSON")

	flag.Parse()

	// Suppress unused warnings for now if we don't use them all
	_ = owner
	_ = repo
	_ = number
	_ = commentsJSON

	var metadata pluginkit.PRMetadata
	if *headersJSON != "" {
		if err := json.Unmarshal([]byte(*headersJSON), &metadata); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse headers: %v\n", err)
		}
	}

	geminiToken := os.Getenv("GEMINI_API_KEY")
	if geminiToken == "" {
		fmt.Println("Error: GEMINI_API_KEY environment variable not set")
		os.Exit(1)
	}

	if *diff == "" {
		fmt.Println("Error: No diff provided")
		os.Exit(1)
	}

	summary, err := pluginkit.Generate(buildPrompt(*diff, metadata), responseSchema(), geminiToken)
	if err != nil {
		fmt.Printf("Error calling Gemini: %v\n", err)
		os.Exit(1)
	}

	if err := pluginkit.EncodeResponse(os.Stdout, responseFor(summary)); err != nil {
		fmt.Printf("Error encoding plugin response: %v\n", err)
		os.Exit(1)
	}
}
