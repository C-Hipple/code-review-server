package ai

import (
	"crs/llm"
	"crs/utils"
	"fmt"
	"strings"
)

// maxDiffInputBytes caps how much of the diff the file-ordering and
// review-ease prompts carry. The list of files always goes in whole.
const maxDiffInputBytes = 200000

// diffInput is a PR's diff as the file-ordering and review-ease prompts carry
// it: the list of its files and, after it, the diff itself.
type diffInput struct {
	files []*utils.DiffFile
	// fileList has one "- <path>" line per file, in diff order.
	fileList string
	// text is the diff with each file labelled, cut to maxDiffInputBytes.
	text string
	// diffBytes is the size of text before the cut.
	diffBytes int
	truncated bool
}

// readDiffInput parses a PR's diff — raw, or as the server renders it — for
// the file-ordering and review-ease prompts. A PR whose diff is missing or
// can't be read fails at stage input: the stored error is retried when the
// feature is next asked for, by which time the diff may be there.
func readDiffInput(diff string) (diffInput, error) {
	if strings.TrimSpace(diff) == "" {
		return diffInput{}, &llm.CallError{Stage: StageInput, Err: fmt.Errorf("no diff is available for this PR")}
	}
	parsed, err := utils.Parse(diff)
	if err != nil {
		return diffInput{}, &llm.CallError{Stage: StageInput, Err: fmt.Errorf("the PR's diff could not be parsed: %w", err)}
	}
	if len(parsed.Files) == 0 {
		return diffInput{}, &llm.CallError{Stage: StageInput, Err: fmt.Errorf("the PR's diff names no files")}
	}

	in := diffInput{files: parsed.Files}
	var list strings.Builder
	for _, f := range parsed.Files {
		list.WriteString("- " + diffFileName(f) + "\n")
	}
	in.fileList = list.String()
	in.text = buildDiffText(parsed.Files)
	in.diffBytes = len(in.text)
	if len(in.text) > maxDiffInputBytes {
		in.text = truncateText(in.text, maxDiffInputBytes)
		in.truncated = true
	}
	return in, nil
}

// describe says what a run worked from, for its call log entry.
func (in diffInput) describe() string {
	s := fmt.Sprintf("%d file(s), %d-byte diff", len(in.files), in.diffBytes)
	if in.truncated {
		s += fmt.Sprintf(" (truncated to %d bytes for the prompt)", maxDiffInputBytes)
	}
	return s
}

// diffFileName returns the path used to identify a diff file, preferring the
// new name and falling back to the original name for deleted files.
func diffFileName(file *utils.DiffFile) string {
	if file.NewName != "" {
		return file.NewName
	}
	return file.OrigName
}

// buildDiffText renders the diff files into a plain-text form for the model,
// labelling each file so it can refer back to exact paths.
func buildDiffText(files []*utils.DiffFile) string {
	var b strings.Builder
	for _, f := range files {
		b.WriteString("=== FILE: " + diffFileName(f) + " ===\n")
		for _, hunk := range f.Hunks {
			b.WriteString(hunk.RangeHeader() + "\n")
			for _, line := range hunk.WholeRange.Lines {
				b.WriteString(line.Render())
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
