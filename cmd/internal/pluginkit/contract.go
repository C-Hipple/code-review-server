// Package pluginkit holds the pieces a plugin binary in this repo needs to
// speak the plugin response contract described in docs/plugins.md: a body the
// client renders, plus line-level annotations anchored to the head side of the
// PR's diff.
package pluginkit

import (
	"encoding/json"
	"io"
	"strings"
)

const (
	BodyTypeMarkdown = "markdown"

	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// Body is the renderable portion of a plugin response.
type Body struct {
	BodyType    string `json:"body_type"`
	BodyContent string `json:"body_content"`
}

// Annotation anchors a remark to a line of a file in the PR.
type Annotation struct {
	Filename string `json:"filename"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Content  string `json:"content"`
}

// Response is the document a plugin writes to stdout.
type Response struct {
	Body        Body         `json:"body"`
	Annotations []Annotation `json:"annotations"`
}

// MarkdownResponse builds a response whose body is rendered as markdown,
// dropping annotations the server would drop anyway.
func MarkdownResponse(body string, annotations []Annotation) Response {
	return Response{
		Body:        Body{BodyType: BodyTypeMarkdown, BodyContent: body},
		Annotations: SanitizeAnnotations(annotations),
	}
}

// SanitizeAnnotations drops annotations that can't be anchored (no filename,
// no positive line) or that carry no text, and normalizes the rest.
func SanitizeAnnotations(annotations []Annotation) []Annotation {
	cleaned := []Annotation{}
	for _, a := range annotations {
		a.Filename = strings.TrimPrefix(strings.TrimSpace(a.Filename), "./")
		a.Content = strings.TrimSpace(a.Content)
		a.Severity = strings.ToLower(strings.TrimSpace(a.Severity))
		if a.Filename == "" || a.Line < 1 || a.Content == "" {
			continue
		}
		if a.Severity == "" {
			a.Severity = SeverityInfo
		}
		cleaned = append(cleaned, a)
	}
	return cleaned
}

// TrimCodeFence unwraps a ```json fence, which models add when they answer in
// prose formatting despite the requested response MIME type.
func TrimCodeFence(reply string) string {
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

// EncodeResponse writes the response contract document a client will parse.
// HTML escaping is off so the stored raw output stays readable, and the
// indentation is there for the same reason.
func EncodeResponse(w io.Writer, response Response) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(response)
}

// PRMetadata is the PR shape passed to a plugin via the --headers flag.
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

// Context renders the PR title and description a prompt wants alongside the
// diff, as lines ending in a newline (empty when the PR has neither).
func (m PRMetadata) Context() string {
	var context string
	if m.Title != "" {
		context += "PR Title: " + m.Title + "\n"
	}
	if m.Body != "" {
		context += "PR Description: " + m.Body + "\n"
	}
	return context
}
