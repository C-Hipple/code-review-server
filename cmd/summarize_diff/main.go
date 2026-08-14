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
// docs/plugins.md: a markdown body explaining what the PR is trying to do,
// plus annotations marking the hotspots — the lines carrying the PR's real
// implementation or business logic — anchored to the head side of the diff.

// maxHotspots is what the model is asked for. Annotating anything a reviewer
// could see for themselves buries the few lines that actually decide whether
// the PR is correct, so the cap is deliberately tight.
const maxHotspots = 4

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
				Description: "Terse markdown summary of what the PR is trying to accomplish and how.",
			},
			"annotations": pluginkit.AnnotationsSchema(maxHotspots),
		},
		PropertyOrdering: []string{"summary", "annotations"},
		Required:         []string{"summary", "annotations"},
	}
}

func buildPrompt(diff string, metadata pluginkit.PRMetadata) string {
	rendered, fileList := pluginkit.PromptDiff(diff)

	return fmt.Sprintf(`Explain what this PR is trying to accomplish, then mark its hotspots.

summary: what the PR is for, in markdown. Lead with one or two sentences on the
goal — the problem it solves or the behaviour it changes, not a list of the
files it touches. Then 2-4 bullets on how it gets there: the approach taken and
any design decision, tradeoff or edge case a reviewer would otherwise have to
reconstruct from the diff. Infer the goal from the changes themselves; the PR
title and description are a hint, not the answer. Be terse. No fluff.

annotations: at most %d hotspots, each anchored to one line of the diff. A
hotspot is a line carrying the PR's key implementation or business logic — the
place where, if this PR is wrong, it is wrong. Think: the core algorithm or
state transition, a condition or boundary that decides behaviour, a change to
how data is persisted or invalidated, error and concurrency handling, a
security or permission check, an assumption that holds only if callers behave.
Pick the load-bearing lines, and pick fewer than the cap when the PR has fewer;
an empty list is fine for a PR that is genuinely mechanical.

Do not annotate what a reviewer can already see: renames, moved code, imports,
formatting, logging, comments, generated or vendored files, test scaffolding,
or a line whose remark would just restate what the line does. One hotspot per
distinct piece of logic — do not spread several annotations across one
function.

- filename: one of the paths listed under Files, copied exactly.
- line: the number shown beside that line in the diff. Lines marked "-" were
  removed and have no number, so they cannot be annotated.
- severity: %s where the logic is central and a reviewer should follow it
  closely, %s where it is subtle or easy to get wrong, %s where it looks
  incorrect or risky as written.
- content: one or two sentences saying why this line is load-bearing and what
  specifically to check about it. Do not describe the change.

Files:
%s

%s

%sDiff:
%s
`, maxHotspots, pluginkit.SeverityInfo, pluginkit.SeverityWarning, pluginkit.SeverityError,
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
	// The summary doesn't vary by call type, but the flag still has to be
	// accepted since the server always passes it.
	pluginkit.RegisterCallTypeFlag()

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
