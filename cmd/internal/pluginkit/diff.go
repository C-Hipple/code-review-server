package pluginkit

import (
	"fmt"
	"strconv"
	"strings"
)

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

// NumberedDiff rewrites a unified diff with each line's head-side line number
// — the number an annotation anchors to — and returns the files it found.
// Lines are rendered as "<mark> <line> | <content>"; removed lines exist only
// on the base side and so carry no number.
func NumberedDiff(diff string) (string, []string) {
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

// DiffLegend explains the rendering of NumberedDiff to a model being asked for
// annotations.
const DiffLegend = `The diff is rendered as "<mark> <line number> | <content>", where mark is "+"
for an added line, "-" for a removed one and blank for an unchanged one. The
line number is the line's position in the file after this PR is merged.`

// PromptDiff renders a diff for a prompt along with the list of files that can
// be annotated. A diff the renderer can't make sense of is passed through
// verbatim with an empty file list, so the model still sees the changes.
func PromptDiff(diff string) (rendered, fileList string) {
	rendered, files := NumberedDiff(diff)
	if rendered == "" {
		rendered = diff
	}
	if len(files) == 0 {
		return rendered, "(none parsed - return an empty annotations list)"
	}
	return rendered, strings.Join(files, "\n")
}
