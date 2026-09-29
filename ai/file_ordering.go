package ai

import (
	"context"
	"crs/llm"
	"crs/utils"
	"encoding/json"
	"fmt"
	"strings"
)

// FileOrderingID is the ID of the file-ordering feature.
const FileOrderingID = "file-ordering"

// FileOrdering orders the files of a PR's diff so a reviewer can read the PR
// from top to bottom: the entry points where the change is integrated first,
// then how it works, then styling, then tests.
//
// It is an applied feature: the server shows the diff in the stored order
// (OrderDiffFiles) when the order was computed for the PR's head SHA, and in
// its default order — test files last — otherwise. Rendering never waits for a
// run. The order depends on the code alone, so it is keyed by the head SHA
// only.
type FileOrdering struct{}

func (FileOrdering) ID() string   { return FileOrderingID }
func (FileOrdering) Name() string { return "File ordering" }

func (FileOrdering) Description() string {
	return "Orders the files of the diff so the PR reads top to bottom: where the change is integrated, " +
		"then how it works, then styling, then tests. The review shows the diff in this order once it has " +
		"been computed for the PR's head commit, and test files last until then."
}

// CodeOnly keys the feature's results by the head SHA alone.
func (FileOrdering) CodeOnly() bool { return true }

// Applied marks the order as something the server applies to the diff it
// serves, not a report.
func (FileOrdering) Applied() bool { return true }

// FileOrderingReport is the typed report file-ordering stores as the result's
// "report".
type FileOrderingReport struct {
	// Files is the reading order, one path per file as the model wrote it,
	// cleaned of list markers, quotes and a/ b/ prefixes. OrderDiffFiles
	// matches each against the diff's paths when the server applies it.
	Files []string `json:"files"`
}

func (FileOrdering) Run(ctx context.Context, req Request) (Result, error) {
	in, err := readDiffInput(req.Diff)
	if err != nil {
		return Result{}, err
	}
	runLog := RunLog{Input: in.describe()}
	if len(in.files) < 2 {
		runLog.Parsed = "a single file: nothing to order"
		return Result{
			Body:   Body{BodyType: BodyMarkdown, BodyContent: "**Nothing to order:** the diff has a single file.\n"},
			Report: FileOrderingReport{Files: []string{diffFileName(in.files[0])}},
			Log:    runLog,
		}, nil
	}

	text, err := req.Model.Generate(ctx, buildFileOrderingPrompt(in))
	if err != nil {
		return Result{Log: runLog}, err
	}
	files := parseFileOrdering(text)
	runLog.Parsed = fmt.Sprintf("%d ordered file paths", len(files))
	if len(files) == 0 {
		runLog.ResponseSnippet = llm.Snippet(text)
		return Result{Log: runLog}, &llm.CallError{Stage: llm.StageParse, Err: fmt.Errorf("response contained no file paths")}
	}
	return Result{
		Body:      Body{BodyType: BodyMarkdown, BodyContent: renderFileOrdering(files)},
		Report:    FileOrderingReport{Files: files},
		Truncated: in.truncated,
		Log:       runLog,
	}, nil
}

// buildFileOrderingPrompt asks the model for the order the diff's files read
// best in.
func buildFileOrderingPrompt(in diffInput) string {
	var b strings.Builder
	b.WriteString(`You are ordering the files of a pull request diff so a reviewer can read the PR from top to bottom and understand it.

Order the files by this priority:
1. The most important and relevant changes first: the entry points where the change is integrated (call sites, public APIs, top-level wiring).
2. Then helper functions and implementation details: how the change actually works.
3. Then styling changes such as CSS: after code, but before tests.
4. Then test files, last.

A reviewer should be able to read top to bottom: starting where the change is integrated, then into how it works, then the tests.
`)
	b.WriteString(fmt.Sprintf(`
Files in this diff:
%s
Full diff:
%s
`, in.fileList, in.text))
	b.WriteString(`
Respond with ONLY the file paths, one per line, in the order they should be displayed. Use the exact file paths listed above. Do not include numbering, bullets, commentary, or code fences.`)
	return b.String()
}

// parseFileOrdering reads the model's answer as file paths, one per line. A
// REVIEW_EASE line, which a model may add unasked, is not a path.
func parseFileOrdering(text string) []string {
	var files []string
	for _, line := range strings.Split(text, "\n") {
		if _, ok := reviewEaseLine(line); ok {
			continue
		}
		if name := cleanFileName(line); name != "" {
			files = append(files, name)
		}
	}
	return files
}

// cleanFileName normalizes a single line of the model's answer into a bare
// file path, stripping list markers, surrounding quotes/backticks, and a/ b/
// prefixes. It returns an empty string for lines that are not file paths.
func cleanFileName(line string) string {
	s := strings.TrimSpace(line)
	s = strings.Trim(s, "`")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "- ")
	s = strings.TrimPrefix(s, "* ")
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`")
	s = strings.TrimPrefix(s, "a/")
	s = strings.TrimPrefix(s, "b/")
	return strings.TrimSpace(s)
}

// renderFileOrdering is the order as markdown, for clients that show a
// feature's body.
func renderFileOrdering(files []string) string {
	var b strings.Builder
	b.WriteString("**Reading order**\n\n")
	for i, f := range files {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, codeSpan(f)))
	}
	b.WriteString("\n_The review shows the diff in this order. A file not listed keeps its place after the listed ones._\n")
	return b.String()
}

// StoredFileOrdering reads the order out of a stored file-ordering result (as
// Result.Encode wrote it); nil when it holds none.
func StoredFileOrdering(stored string) []string {
	var report FileOrderingReport
	doc := DecodeDocument(stored)
	if len(doc.Report) == 0 || json.Unmarshal(doc.Report, &report) != nil || len(report.Files) == 0 {
		return nil
	}
	return report.Files
}

// OrderDiffFiles returns files in the order names gives. Each name is matched
// to a file exactly, or failing that by base name, since a model may shorten a
// path. Files no name matched keep their original relative order after the
// ones that were.
func OrderDiffFiles(files []*utils.DiffFile, names []string) []*utils.DiffFile {
	used := make(map[*utils.DiffFile]bool)
	result := make([]*utils.DiffFile, 0, len(files))

	for _, name := range names {
		if f := matchDiffFile(files, name, used); f != nil {
			result = append(result, f)
			used[f] = true
		}
	}
	for _, f := range files {
		if !used[f] {
			result = append(result, f)
		}
	}
	return result
}

// matchDiffFile finds an unused diff file whose path matches name, trying an
// exact match first and then a basename match.
func matchDiffFile(files []*utils.DiffFile, name string, used map[*utils.DiffFile]bool) *utils.DiffFile {
	for _, f := range files {
		if !used[f] && diffFileName(f) == name {
			return f
		}
	}
	base := name[strings.LastIndex(name, "/")+1:]
	for _, f := range files {
		if used[f] {
			continue
		}
		fn := diffFileName(f)
		if fn[strings.LastIndex(fn, "/")+1:] == base {
			return f
		}
	}
	return nil
}
