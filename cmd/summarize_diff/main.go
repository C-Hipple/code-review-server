package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// summarize_diff emits the plugin response contract described in
// docs/plugins.md: a markdown body holding the summary, plus line-level
// annotations anchored to the head side of the PR's diff.

const (
	bodyTypeMarkdown = "markdown"
	severityInfo     = "info"

	// maxAnnotations is what the model is asked for. A summary plugin that
	// flags every other line is noise once the annotations render inline.
	maxAnnotations = 6
)

// PluginBody is the renderable portion of a plugin response.
type PluginBody struct {
	BodyType    string `json:"body_type"`
	BodyContent string `json:"body_content"`
}

// PluginAnnotation anchors a remark to a line of a file in the PR.
type PluginAnnotation struct {
	Filename string `json:"filename"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Content  string `json:"content"`
}

// PluginResponse is the document written to stdout.
type PluginResponse struct {
	Body        PluginBody         `json:"body"`
	Annotations []PluginAnnotation `json:"annotations"`
}

// modelOutput is the shape Gemini is asked to return, mirroring
// responseSchema below.
type modelOutput struct {
	Summary     string             `json:"summary"`
	Annotations []PluginAnnotation `json:"annotations"`
}

type GeminiPart struct {
	Text string `json:"text"`
}

type GeminiContent struct {
	Parts []GeminiPart `json:"parts"`
}

// GeminiSchema is the subset of the OpenAPI schema dialect Gemini accepts as
// a response schema.
type GeminiSchema struct {
	Type             string                   `json:"type"`
	Description      string                   `json:"description,omitempty"`
	Enum             []string                 `json:"enum,omitempty"`
	Items            *GeminiSchema            `json:"items,omitempty"`
	Properties       map[string]*GeminiSchema `json:"properties,omitempty"`
	PropertyOrdering []string                 `json:"propertyOrdering,omitempty"`
	Required         []string                 `json:"required,omitempty"`
}

type GeminiGenerationConfig struct {
	ResponseMimeType string        `json:"responseMimeType,omitempty"`
	ResponseSchema   *GeminiSchema `json:"responseSchema,omitempty"`
}

type GeminiRequest struct {
	Contents         []GeminiContent         `json:"contents"`
	GenerationConfig *GeminiGenerationConfig `json:"generationConfig,omitempty"`
}

type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

type PRMetadata struct {
	Number       int      `json:"number"`
	Title        string   `json:"title"`
	Author       string   `json:"author"`
	BaseRef      string   `json:"base_ref"`
	HeadRef      string   `json:"head_ref"`
	State        string   `json:"state"`
	Milestone    string   `json:"milestone"`
	Labels       []string `json:"labels"`
	Assignees    []string `json:"assignees"`
	Reviewers    []string `json:"reviewers"`
	Draft        bool     `json:"draft"`
	CIStatus     string   `json:"ci_status"`
	CIFailures   []string `json:"ci_failures"`
	Body         string   `json:"body"`
	URL          string   `json:"url"`
	WorktreePath string   `json:"worktree_path"`
}

// responseSchema constrains Gemini's reply to the summary and annotations we
// know how to turn into a plugin response.
func responseSchema() *GeminiSchema {
	return &GeminiSchema{
		Type: "OBJECT",
		Properties: map[string]*GeminiSchema{
			"summary": {
				Type:        "STRING",
				Description: "Terse markdown summary of the PR.",
			},
			"annotations": {
				Type:        "ARRAY",
				Description: "Line-level remarks, at most " + strconv.Itoa(maxAnnotations) + ".",
				Items: &GeminiSchema{
					Type: "OBJECT",
					Properties: map[string]*GeminiSchema{
						"filename": {
							Type:        "STRING",
							Description: "Path of the annotated file, copied exactly from the diff.",
						},
						"line": {
							Type:        "INTEGER",
							Description: "Head-side line number shown beside the annotated line.",
						},
						"severity": {
							Type: "STRING",
							Enum: []string{severityInfo, "warning", "error"},
						},
						"content": {
							Type:        "STRING",
							Description: "One or two sentences about that line.",
						},
					},
					PropertyOrdering: []string{"filename", "line", "severity", "content"},
					Required:         []string{"filename", "line", "severity", "content"},
				},
			},
		},
		PropertyOrdering: []string{"summary", "annotations"},
		Required:         []string{"summary", "annotations"},
	}
}

// trimDiffPath cleans a path out of a diff file header, dropping git's a/ or
// b/ prefix and reporting /dev/null as no path at all.
func trimDiffPath(raw, prefix string) string {
	path := strings.TrimSpace(raw)
	if path == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(path, prefix)
}

// hunkNewStart pulls the head-side starting line out of a hunk header such as
// "@@ -12,7 +14,9 @@ func main() {".
func hunkNewStart(header string) (int, bool) {
	_, after, found := strings.Cut(header, "+")
	if !found {
		return 0, false
	}
	field, _, _ := strings.Cut(after, " ")
	field, _, _ = strings.Cut(field, ",")
	start, err := strconv.Atoi(field)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

// numberedDiff rewrites a unified diff with each line's head-side line number
// — the number an annotation anchors to — and returns the files it found.
// Lines are rendered as "<mark> <line> | <content>"; removed lines exist only
// on the base side and so carry no number.
func numberedDiff(diff string) (string, []string) {
	var (
		out      strings.Builder
		files    []string
		baseName string
		newLine  int
		inHunk   bool
	)

	for _, line := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk, baseName = false, ""
		case strings.HasPrefix(line, "--- "):
			baseName = trimDiffPath(strings.TrimPrefix(line, "--- "), "a/")
			inHunk = false
		case strings.HasPrefix(line, "+++ "):
			inHunk = false
			name := trimDiffPath(strings.TrimPrefix(line, "+++ "), "b/")
			if name == "" {
				// Deleted file: the base-side path is the only one it has.
				name = baseName
			}
			if name == "" {
				continue
			}
			files = append(files, name)
			fmt.Fprintf(&out, "\n=== FILE: %s ===\n", name)
		case strings.HasPrefix(line, "@@"):
			start, ok := hunkNewStart(line)
			if !ok {
				continue
			}
			newLine, inHunk = start, true
			fmt.Fprintf(&out, "%s\n", line)
		case !inHunk:
			// index/mode/rename lines between files: nothing to number.
		case line == `\ No newline at end of file`:
		case strings.HasPrefix(line, "-"):
			fmt.Fprintf(&out, "- %5s | %s\n", "", line[1:])
		case strings.HasPrefix(line, "+"):
			fmt.Fprintf(&out, "+ %5d | %s\n", newLine, line[1:])
			newLine++
		default:
			fmt.Fprintf(&out, "  %5d | %s\n", newLine, strings.TrimPrefix(line, " "))
			newLine++
		}
	}

	return strings.TrimSpace(out.String()), files
}

func buildPrompt(diff string, metadata PRMetadata) string {
	var contextInfo string
	if metadata.Title != "" {
		contextInfo += fmt.Sprintf("PR Title: %s\n", metadata.Title)
	}
	if metadata.Body != "" {
		contextInfo += fmt.Sprintf("PR Description: %s\n", metadata.Body)
	}

	rendered, files := numberedDiff(diff)
	fileList := "(none parsed - return an empty annotations list)"
	if len(files) > 0 {
		fileList = strings.Join(files, "\n")
	}
	if rendered == "" {
		rendered = diff
	}

	return fmt.Sprintf(`Summarize this PR, and flag the specific lines a reviewer should look at.

summary: 2-4 bullet points on key changes (one line each), then 1-2 brief
suggestions if any. Markdown. Be terse. No fluff.

annotations: at most %d remarks, each anchored to one line of the diff. Only
annotate a line that genuinely warrants a reviewer's attention; an empty list
is fine.
- filename: one of the paths listed under Files, copied exactly.
- line: the number shown beside that line in the diff. Lines marked "-" were
  removed and have no number, so they cannot be annotated.
- severity: %s, warning or error.
- content: one or two sentences.

Files:
%s

The diff is rendered as "<mark> <line number> | <content>", where mark is "+"
for an added line, "-" for a removed one and blank for an unchanged one. The
line number is the line's position in the file after this PR is merged.

%sDiff:
%s
`, maxAnnotations, severityInfo, fileList, contextInfo, rendered)
}

func callGemini(diff string, metadata PRMetadata, geminiToken string) (string, error) {
	// Using gemini-2.5-flash
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key=" + geminiToken

	reqBody := GeminiRequest{
		Contents: []GeminiContent{
			{
				Parts: []GeminiPart{
					{Text: buildPrompt(diff, metadata)},
				},
			},
		},
		GenerationConfig: &GeminiGenerationConfig{
			ResponseMimeType: "application/json",
			ResponseSchema:   responseSchema(),
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var geminiResp GeminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return "", err
	}

	if len(geminiResp.Candidates) > 0 && len(geminiResp.Candidates[0].Content.Parts) > 0 {
		return geminiResp.Candidates[0].Content.Parts[0].Text, nil
	}

	return "", fmt.Errorf("no content in response")
}

// trimCodeFence unwraps a ```json fence, which models add when they answer in
// prose formatting despite the requested response MIME type.
func trimCodeFence(reply string) string {
	trimmed := strings.TrimSpace(reply)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	_, after, found := strings.Cut(trimmed, "\n")
	if !found {
		return trimmed
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(after), "```"))
}

// sanitizeAnnotations drops annotations the server would drop anyway (no
// filename, no positive line) or that carry no text, and normalizes the rest.
func sanitizeAnnotations(annotations []PluginAnnotation) []PluginAnnotation {
	cleaned := []PluginAnnotation{}
	for _, a := range annotations {
		a.Filename = strings.TrimPrefix(strings.TrimSpace(a.Filename), "./")
		a.Content = strings.TrimSpace(a.Content)
		a.Severity = strings.ToLower(strings.TrimSpace(a.Severity))
		if a.Filename == "" || a.Line < 1 || a.Content == "" {
			continue
		}
		if a.Severity == "" {
			a.Severity = severityInfo
		}
		cleaned = append(cleaned, a)
	}
	return cleaned
}

// responseFor turns Gemini's reply into a plugin response. A reply that isn't
// the JSON we asked for becomes the body verbatim, which is what this plugin
// emitted before it spoke the contract.
func responseFor(reply string) PluginResponse {
	var parsed modelOutput
	if err := json.Unmarshal([]byte(trimCodeFence(reply)), &parsed); err != nil || strings.TrimSpace(parsed.Summary) == "" {
		return PluginResponse{
			Body:        PluginBody{BodyType: bodyTypeMarkdown, BodyContent: reply},
			Annotations: []PluginAnnotation{},
		}
	}

	return PluginResponse{
		Body:        PluginBody{BodyType: bodyTypeMarkdown, BodyContent: strings.TrimSpace(parsed.Summary)},
		Annotations: sanitizeAnnotations(parsed.Annotations),
	}
}

// encodeResponse writes the response contract document a client will parse.
// HTML escaping is off so the stored raw output stays readable, and the
// indentation is there for the same reason.
func encodeResponse(w io.Writer, response PluginResponse) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(response)
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

	var metadata PRMetadata
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

	summary, err := callGemini(*diff, metadata, geminiToken)
	if err != nil {
		fmt.Printf("Error calling Gemini: %v\n", err)
		os.Exit(1)
	}

	if err := encodeResponse(os.Stdout, responseFor(summary)); err != nil {
		fmt.Printf("Error encoding plugin response: %v\n", err)
		os.Exit(1)
	}
}
