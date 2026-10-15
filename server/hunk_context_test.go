package server

import (
	"crs/config"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// extractHunkContextLines is the pure logic extracted for testing.
// It mirrors the line-slicing logic in GetHunkContext.
func extractHunkContextLines(content string, anchorLine int, direction string, count int) ([]string, int, int) {
	fileLines := strings.Split(content, "\n")
	totalLines := len(fileLines)

	var startLine, endLine int
	if direction == "before" {
		endLine = anchorLine - 1
		startLine = endLine - count + 1
		if startLine < 1 {
			startLine = 1
		}
		if endLine > totalLines {
			endLine = totalLines
		}
		if endLine < 1 || startLine > endLine {
			return []string{}, 0, 0
		}
	} else {
		startLine = anchorLine + 1
		endLine = startLine + count - 1
		if startLine > totalLines {
			return []string{}, 0, 0
		}
		if endLine > totalLines {
			endLine = totalLines
		}
	}

	return fileLines[startLine-1 : endLine], startLine, endLine
}

func TestExtractHunkContextLines(t *testing.T) {
	// 10-line file
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"

	tests := []struct {
		name      string
		anchor    int
		direction string
		count     int
		wantLines []string
		wantStart int
		wantEnd   int
	}{
		{
			name:      "3 lines before anchor at line 5",
			anchor:    5,
			direction: "before",
			count:     3,
			wantLines: []string{"line2", "line3", "line4"},
			wantStart: 2,
			wantEnd:   4,
		},
		{
			name:      "3 lines after anchor at line 5",
			anchor:    5,
			direction: "after",
			count:     3,
			wantLines: []string{"line6", "line7", "line8"},
			wantStart: 6,
			wantEnd:   8,
		},
		{
			name:      "before at line 1 — no lines above",
			anchor:    1,
			direction: "before",
			count:     5,
			wantLines: []string{},
			wantStart: 0,
			wantEnd:   0,
		},
		{
			name:      "after at line 10 — no lines below",
			anchor:    10,
			direction: "after",
			count:     5,
			wantLines: []string{},
			wantStart: 0,
			wantEnd:   0,
		},
		{
			name:      "before at line 3 — clamped to start",
			anchor:    3,
			direction: "before",
			count:     10,
			wantLines: []string{"line1", "line2"},
			wantStart: 1,
			wantEnd:   2,
		},
		{
			name:      "after at line 8 — clamped to end",
			anchor:    8,
			direction: "after",
			count:     10,
			wantLines: []string{"line9", "line10"},
			wantStart: 9,
			wantEnd:   10,
		},
		{
			name:      "before at line 2 — exactly 1 line",
			anchor:    2,
			direction: "before",
			count:     1,
			wantLines: []string{"line1"},
			wantStart: 1,
			wantEnd:   1,
		},
		{
			name:      "after at line 9 — exactly 1 line",
			anchor:    9,
			direction: "after",
			count:     1,
			wantLines: []string{"line10"},
			wantStart: 10,
			wantEnd:   10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, start, end := extractHunkContextLines(content, tt.anchor, tt.direction, tt.count)
			if start != tt.wantStart || end != tt.wantEnd {
				t.Errorf("range = [%d, %d], want [%d, %d]", start, end, tt.wantStart, tt.wantEnd)
			}
			if len(lines) != len(tt.wantLines) {
				t.Fatalf("got %d lines, want %d", len(lines), len(tt.wantLines))
			}
			for i, l := range lines {
				if l != tt.wantLines[i] {
					t.Errorf("line[%d] = %q, want %q", i, l, tt.wantLines[i])
				}
			}
		})
	}
}

func TestGetHunkContextValidation(t *testing.T) {
	h := &RPCHandler{}

	tests := []struct {
		name    string
		args    GetHunkContextArgs
		wantErr string
	}{
		{
			name:    "invalid direction",
			args:    GetHunkContextArgs{Direction: "left", Side: "new", AnchorLine: 1},
			wantErr: `Direction must be "before" or "after"`,
		},
		{
			name:    "invalid side",
			args:    GetHunkContextArgs{Direction: "before", Side: "middle", AnchorLine: 1},
			wantErr: `Side must be "old" or "new"`,
		},
		{
			name:    "anchor line zero",
			args:    GetHunkContextArgs{Direction: "before", Side: "new", AnchorLine: 0},
			wantErr: "AnchorLine must be >= 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reply GetHunkContextReply
			err := h.GetHunkContext(&tt.args, &reply)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// hunkContextRepo commits files to a git repository under a fresh
// RepoLocation, where GetHunkContext reads them, and caches the commit as the
// head of PR acme/widgets#7. It returns the commit's SHA.
func hunkContextRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	db := setupTestDB(t)
	location := t.TempDir()
	config.SetC(config.Config{DB: db, RepoLocation: location})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	dir := filepath.Join(location, "widgets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "files")
	sha := git("rev-parse", "HEAD")
	if err := db.UpsertPullRequest("acme", "widgets", 7, sha, sha, "diff"); err != nil {
		t.Fatal(err)
	}
	return sha
}

// An anchor past the end of the file — a client's diff newer than the cached
// head, or a made-up line — has nothing before it to show. It used to slice
// past the end of the file and panic, which on a hosted server took every
// user's connection down with it.
func TestGetHunkContextAnchorPastEndOfFile(t *testing.T) {
	hunkContextRepo(t, map[string]string{"f.go": "one\ntwo\nthree\n"})
	h := &RPCHandler{}

	for _, anchor := range []int{500, 30} {
		for _, direction := range []string{"before", "after"} {
			reply := GetHunkContextReply{}
			args := &GetHunkContextArgs{Owner: "acme", Repo: "widgets", Number: 7, Filename: "f.go", Side: "new",
				AnchorLine: anchor, Direction: direction, Count: 20}
			if err := h.GetHunkContext(args, &reply); err != nil {
				t.Fatalf("anchor %d %s: %v", anchor, direction, err)
			}
			if len(reply.Lines) != 0 || reply.StartLine != 0 || reply.EndLine != 0 {
				t.Errorf("anchor %d %s: got lines %q (%d-%d), want none", anchor, direction, reply.Lines, reply.StartLine, reply.EndLine)
			}
		}
	}

	// Lines before an anchor just past the end are the file's last ones.
	reply := GetHunkContextReply{}
	args := &GetHunkContextArgs{Owner: "acme", Repo: "widgets", Number: 7, Filename: "f.go", Side: "new",
		AnchorLine: 6, Direction: "before", Count: 3, NewStart: 6, OrigStart: 6}
	if err := h.GetHunkContext(args, &reply); err != nil {
		t.Fatal(err)
	}
	if strings.Join(reply.Lines, "|") != "three|" || reply.StartLine != 3 || reply.EndLine != 4 {
		t.Errorf("before line 6: got %q (%d-%d)", reply.Lines, reply.StartLine, reply.EndLine)
	}
}

func TestGetHunkContextCountDefaults(t *testing.T) {
	// Verify count capping logic
	tests := []struct {
		input    int
		expected int
	}{
		{0, 20},
		{-5, 20},
		{50, 50},
		{150, 100},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("count_%d", tt.input), func(t *testing.T) {
			count := tt.input
			if count <= 0 {
				count = 20
			}
			if count > 100 {
				count = 100
			}
			if count != tt.expected {
				t.Errorf("capped count = %d, want %d", count, tt.expected)
			}
		})
	}
}

// simulateGetHunkContext runs the pure line-extraction and header-computation
// logic from GetHunkContext against in-memory file content, without needing
// GitHub API access. This lets us chain consecutive calls in tests.
func simulateGetHunkContext(
	fileContent string,
	anchorLine int,
	direction string,
	count int,
	origStart, origLength, newStart, newLength int,
	hunkHeader string,
) (GetHunkContextReply, error) {
	if direction != "before" && direction != "after" {
		return GetHunkContextReply{}, fmt.Errorf("bad direction %q", direction)
	}

	fileLines := strings.Split(fileContent, "\n")
	totalLines := len(fileLines)

	var startLine, endLine int
	if direction == "before" {
		endLine = anchorLine - 1
		startLine = endLine - count + 1
		if startLine < 1 {
			startLine = 1
		}
		if endLine < 1 {
			return GetHunkContextReply{Lines: []string{}}, nil
		}
	} else {
		startLine = anchorLine + 1
		endLine = startLine + count - 1
		if startLine > totalLines {
			return GetHunkContextReply{Lines: []string{}}, nil
		}
		if endLine > totalLines {
			endLine = totalLines
		}
	}

	extraCount := endLine - startLine + 1
	if direction == "before" {
		origStart -= extraCount
		origLength += extraCount
		newStart -= extraCount
		newLength += extraCount
	} else {
		origLength += extraCount
		newLength += extraCount
	}

	headerSuffix := ""
	if hunkHeader != "" {
		headerSuffix = " " + hunkHeader
	}

	return GetHunkContextReply{
		Lines:       fileLines[startLine-1 : endLine],
		StartLine:   startLine,
		EndLine:     endLine,
		RangeHeader: fmt.Sprintf("@@ -%d,%d +%d,%d @@%s", origStart, origLength, newStart, newLength, headerSuffix),
	}, nil
}

// TestConsecutiveHunkExpansion simulates a client expanding a single hunk
// three times in a row — twice upward ("before") and once downward ("after") —
// and verifies that the returned lines and range headers accumulate correctly.
//
// Scenario: a 30-line file, hunk originally at @@ -15,3 +15,4 @@ func doStuff()
//
//	Expansion 1: 3 lines before (top of hunk)  → lines 12-14, header @@ -12,6 +12,7 @@
//	Expansion 2: 3 more lines before            → lines 9-11,  header @@ -9,9 +9,10 @@
//	Expansion 3: 3 lines after (bottom of hunk) → lines 19-21, header @@ -9,12 +9,13 @@
func TestConsecutiveHunkExpansion(t *testing.T) {
	// Build a 30-line file: "line 1\nline 2\n...line 30"
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	fileContent := strings.Join(lines, "\n")

	// --- Initial hunk state ---
	// @@ -15,3 +15,4 @@ func doStuff()
	// Old side: lines 15,16,17   New side: lines 15,16,17,18
	origStart, origLength := 15, 3
	newStart, newLength := 15, 4
	hunkHeader := "func doStuff()"

	// The client tracks the top and bottom anchors of the visible hunk.
	// "before" anchor = first line of the hunk on the new side = newStart
	// "after"  anchor = last line of the hunk on the new side  = newStart + newLength - 1
	topAnchor := newStart                    // 15
	bottomAnchor := newStart + newLength - 1 // 18

	// Track all context lines the client has accumulated (prepended/appended).
	var contextBefore []string
	var contextAfter []string

	// ──────────────────────────────────────────────────
	// Expansion 1: expand 3 lines BEFORE the hunk
	// ──────────────────────────────────────────────────
	reply, err := simulateGetHunkContext(fileContent, topAnchor, "before", 3,
		origStart, origLength, newStart, newLength, hunkHeader)
	if err != nil {
		t.Fatalf("expansion 1 error: %v", err)
	}

	// Should return lines 12, 13, 14
	if reply.StartLine != 12 || reply.EndLine != 14 {
		t.Errorf("exp1: range = [%d,%d], want [12,14]", reply.StartLine, reply.EndLine)
	}
	wantLines := []string{"line 12", "line 13", "line 14"}
	if len(reply.Lines) != len(wantLines) {
		t.Fatalf("exp1: got %d lines, want %d", len(reply.Lines), len(wantLines))
	}
	for i, l := range reply.Lines {
		if l != wantLines[i] {
			t.Errorf("exp1: line[%d] = %q, want %q", i, l, wantLines[i])
		}
	}
	if reply.RangeHeader != "@@ -12,6 +12,7 @@ func doStuff()" {
		t.Errorf("exp1: header = %q, want %q", reply.RangeHeader, "@@ -12,6 +12,7 @@ func doStuff()")
	}

	// Client updates its state from the reply
	contextBefore = append(reply.Lines, contextBefore...)
	origStart, origLength = 12, 6
	newStart, newLength = 12, 7
	topAnchor = reply.StartLine // 12

	// ──────────────────────────────────────────────────
	// Expansion 2: expand 3 more lines BEFORE the hunk
	// ──────────────────────────────────────────────────
	reply, err = simulateGetHunkContext(fileContent, topAnchor, "before", 3,
		origStart, origLength, newStart, newLength, hunkHeader)
	if err != nil {
		t.Fatalf("expansion 2 error: %v", err)
	}

	// Should return lines 9, 10, 11
	if reply.StartLine != 9 || reply.EndLine != 11 {
		t.Errorf("exp2: range = [%d,%d], want [9,11]", reply.StartLine, reply.EndLine)
	}
	wantLines = []string{"line 9", "line 10", "line 11"}
	if len(reply.Lines) != len(wantLines) {
		t.Fatalf("exp2: got %d lines, want %d", len(reply.Lines), len(wantLines))
	}
	for i, l := range reply.Lines {
		if l != wantLines[i] {
			t.Errorf("exp2: line[%d] = %q, want %q", i, l, wantLines[i])
		}
	}
	if reply.RangeHeader != "@@ -9,9 +9,10 @@ func doStuff()" {
		t.Errorf("exp2: header = %q, want %q", reply.RangeHeader, "@@ -9,9 +9,10 @@ func doStuff()")
	}

	// Client updates its state
	contextBefore = append(reply.Lines, contextBefore...)
	origStart, origLength = 9, 9
	newStart, newLength = 9, 10
	topAnchor = reply.StartLine // 9

	// ──────────────────────────────────────────────────
	// Expansion 3: expand 3 lines AFTER the hunk
	// ──────────────────────────────────────────────────
	// bottomAnchor is still 18 (last line of original new-side hunk content).
	reply, err = simulateGetHunkContext(fileContent, bottomAnchor, "after", 3,
		origStart, origLength, newStart, newLength, hunkHeader)
	if err != nil {
		t.Fatalf("expansion 3 error: %v", err)
	}

	// Should return lines 19, 20, 21
	if reply.StartLine != 19 || reply.EndLine != 21 {
		t.Errorf("exp3: range = [%d,%d], want [19,21]", reply.StartLine, reply.EndLine)
	}
	wantLines = []string{"line 19", "line 20", "line 21"}
	if len(reply.Lines) != len(wantLines) {
		t.Fatalf("exp3: got %d lines, want %d", len(reply.Lines), len(wantLines))
	}
	for i, l := range reply.Lines {
		if l != wantLines[i] {
			t.Errorf("exp3: line[%d] = %q, want %q", i, l, wantLines[i])
		}
	}
	if reply.RangeHeader != "@@ -9,12 +9,13 @@ func doStuff()" {
		t.Errorf("exp3: header = %q, want %q", reply.RangeHeader, "@@ -9,12 +9,13 @@ func doStuff()")
	}

	// Client updates its state
	contextAfter = append(contextAfter, reply.Lines...)
	bottomAnchor = reply.EndLine // 21

	// ──────────────────────────────────────────────────
	// Final verification: the accumulated context lines
	// ──────────────────────────────────────────────────
	// Before context (prepended top-down): line 9..14
	expectedBefore := []string{"line 9", "line 10", "line 11", "line 12", "line 13", "line 14"}
	if len(contextBefore) != len(expectedBefore) {
		t.Fatalf("contextBefore: got %d lines, want %d", len(contextBefore), len(expectedBefore))
	}
	for i, l := range contextBefore {
		if l != expectedBefore[i] {
			t.Errorf("contextBefore[%d] = %q, want %q", i, l, expectedBefore[i])
		}
	}

	// After context (appended): line 19..21
	expectedAfter := []string{"line 19", "line 20", "line 21"}
	if len(contextAfter) != len(expectedAfter) {
		t.Fatalf("contextAfter: got %d lines, want %d", len(contextAfter), len(expectedAfter))
	}
	for i, l := range contextAfter {
		if l != expectedAfter[i] {
			t.Errorf("contextAfter[%d] = %q, want %q", i, l, expectedAfter[i])
		}
	}

	// The fully expanded hunk now spans lines 9-21 in the file (13 lines on new side).
	// Verify the final visible range makes sense:
	// top = topAnchor (9), bottom = bottomAnchor (21)
	if topAnchor != 9 {
		t.Errorf("final topAnchor = %d, want 9", topAnchor)
	}
	if bottomAnchor != 21 {
		t.Errorf("final bottomAnchor = %d, want 21", bottomAnchor)
	}
}

func TestComputeRangeHeader(t *testing.T) {
	tests := []struct {
		name       string
		direction  string
		extraCount int
		origStart  int
		origLength int
		newStart   int
		newLength  int
		hunkHeader string
		wantHeader string
	}{
		{
			name:       "expand before — 5 extra lines",
			direction:  "before",
			extraCount: 5,
			origStart:  10, origLength: 7,
			newStart: 12, newLength: 8,
			hunkHeader: "func main()",
			wantHeader: "@@ -5,12 +7,13 @@ func main()",
		},
		{
			name:       "expand after — 3 extra lines",
			direction:  "after",
			extraCount: 3,
			origStart:  10, origLength: 7,
			newStart: 12, newLength: 8,
			hunkHeader: "",
			wantHeader: "@@ -10,10 +12,11 @@",
		},
		{
			name:       "expand before — clamped (fewer lines available)",
			direction:  "before",
			extraCount: 2,
			origStart:  3, origLength: 4,
			newStart: 3, newLength: 5,
			hunkHeader: "",
			wantHeader: "@@ -1,6 +1,7 @@",
		},
		{
			name:       "expand after with hunk header",
			direction:  "after",
			extraCount: 10,
			origStart:  20, origLength: 5,
			newStart: 25, newLength: 6,
			hunkHeader: "type Foo struct",
			wantHeader: "@@ -20,15 +25,16 @@ type Foo struct",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origStart := tt.origStart
			origLength := tt.origLength
			newStart := tt.newStart
			newLength := tt.newLength

			if tt.direction == "before" {
				origStart -= tt.extraCount
				origLength += tt.extraCount
				newStart -= tt.extraCount
				newLength += tt.extraCount
			} else {
				origLength += tt.extraCount
				newLength += tt.extraCount
			}

			headerSuffix := ""
			if tt.hunkHeader != "" {
				headerSuffix = " " + tt.hunkHeader
			}
			got := fmt.Sprintf("@@ -%d,%d +%d,%d @@%s", origStart, origLength, newStart, newLength, headerSuffix)
			if got != tt.wantHeader {
				t.Errorf("got %q, want %q", got, tt.wantHeader)
			}
		})
	}
}
