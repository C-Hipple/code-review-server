package main

import (
	"crs/cmd/internal/pluginkit"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// style_guidelines emits the plugin response contract described in
// docs/plugins.md: a markdown body holding the compliance report, plus an
// annotation on each line of the diff that breaks one of the user's rules.

// maxAnnotations is what the model is asked for. A style guide can be broken
// on a lot of lines at once, and a diff buried in flags is unreadable, so the
// worst offenders are worth more than an exhaustive list.
const maxAnnotations = 10

// modelOutput is the shape Gemini is asked to return, mirroring
// responseSchema below.
type modelOutput struct {
	Report      string                 `json:"report"`
	Annotations []pluginkit.Annotation `json:"annotations"`
}

// responseSchema constrains Gemini's reply to the report and annotations we
// know how to turn into a plugin response.
func responseSchema() *pluginkit.Schema {
	return &pluginkit.Schema{
		Type: "OBJECT",
		Properties: map[string]*pluginkit.Schema{
			"report": {
				Type:        "STRING",
				Description: "Markdown report on the diff's compliance with the style guidelines.",
			},
			"annotations": pluginkit.AnnotationsSchema(maxAnnotations),
		},
		PropertyOrdering: []string{"report", "annotations"},
		Required:         []string{"report", "annotations"},
	}
}

func loadStyleGuidelines() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	path := filepath.Join(home, ".config", "style_guidelines.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("could not read style guidelines from %s: %w", path, err)
	}
	return string(data), nil
}

func buildPrompt(diff string, metadata pluginkit.PRMetadata, styleGuidelines string) string {
	rendered, fileList := pluginkit.PromptDiff(diff)

	return fmt.Sprintf(`You are a code reviewer evaluating a pull request against style guidelines.

## Style Guidelines

%s

## PR Context

%s
## Files

%s

## Diff

%s

%s

## Instructions

Evaluate the diff against the style guidelines above, and return both a report
and the violating lines. Focus only on style guideline compliance. Be direct
and specific.

report: a concise markdown report with
- any violations, with file and line references
- areas that follow the guidelines well (briefly)
- a short overall assessment

annotations: at most %d remarks, each anchored to one line of the diff that
breaks a guideline. Only annotate a line the guidelines actually speak to; an
empty list is fine, and a clean diff should have one.
- filename: one of the paths listed under Files, copied exactly.
- line: the number shown beside that line in the diff. Lines marked "-" were
  removed and have no number, so they cannot be annotated.
- severity: %s for a nit, %s for a clear violation, %s for one that breaks a
  guideline stated as a hard requirement.
- content: the guideline broken and how to fix the line, in one or two
  sentences.
`, styleGuidelines, metadata.Context(), fileList, rendered, pluginkit.DiffLegend,
		maxAnnotations, pluginkit.SeverityInfo, pluginkit.SeverityWarning, pluginkit.SeverityError)
}

// responseFor turns Gemini's reply into a plugin response. A reply that isn't
// the JSON we asked for becomes the body verbatim, which is what this plugin
// emitted before it spoke the contract.
func responseFor(reply string) pluginkit.Response {
	var parsed modelOutput
	if err := json.Unmarshal([]byte(pluginkit.TrimCodeFence(reply)), &parsed); err != nil || strings.TrimSpace(parsed.Report) == "" {
		return pluginkit.MarkdownResponse(reply, nil)
	}

	return pluginkit.MarkdownResponse(strings.TrimSpace(parsed.Report), parsed.Annotations)
}

func main() {
	diff := flag.String("diff", "", "PR diff content")
	owner := flag.String("owner", "", "PR owner")
	repo := flag.String("repo", "", "PR repo")
	number := flag.Int("number", 0, "PR number")
	commentsJSON := flag.String("comments", "", "PR comments JSON")
	headersJSON := flag.String("headers", "", "PR metadata JSON")

	flag.Parse()

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

	styleGuidelines, err := loadStyleGuidelines()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	result, err := pluginkit.Generate(buildPrompt(*diff, metadata, styleGuidelines), responseSchema(), geminiToken)
	if err != nil {
		fmt.Printf("Error calling Gemini: %v\n", err)
		os.Exit(1)
	}

	if err := pluginkit.EncodeResponse(os.Stdout, responseFor(result)); err != nil {
		fmt.Printf("Error encoding plugin response: %v\n", err)
		os.Exit(1)
	}
}
