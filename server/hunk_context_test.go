package server

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// numberedFile is n lines, "line 1" to "line n", each ending in a newline the
// way git show and the contents API return a file.
func numberedFile(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestHunkContextLines(t *testing.T) {
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

	// The same lines whether or not the file ends in a newline: the newline
	// ends line 10 rather than starting an 11th.
	files := []struct{ name, content string }{
		{"without a final newline", content},
		{"with a final newline", content + "\n"},
	}
	for _, file := range files {
		t.Run(file.name, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					reply := hunkContext(file.content, &GetHunkContextArgs{AnchorLine: tt.anchor, Direction: tt.direction, Count: tt.count})
					if reply.StartLine != tt.wantStart || reply.EndLine != tt.wantEnd {
						t.Errorf("range = [%d, %d], want [%d, %d]", reply.StartLine, reply.EndLine, tt.wantStart, tt.wantEnd)
					}
					if len(reply.Lines) != len(tt.wantLines) {
						t.Fatalf("got %d lines, want %d", len(reply.Lines), len(tt.wantLines))
					}
					for i, l := range reply.Lines {
						if l != tt.wantLines[i] {
							t.Errorf("line[%d] = %q, want %q", i, l, tt.wantLines[i])
						}
					}
				})
			}
		})
	}
}

// TestHunkContextAtTheEndOfAFile reads files that end in a newline, as git
// show and the contents API return them. That newline is not an empty line
// past the end, to return or to count in the header; an empty line the file
// really ends with is.
func TestHunkContextAtTheEndOfAFile(t *testing.T) {
	file := numberedFile(10)

	tests := []struct {
		name    string
		content string
		args    GetHunkContextArgs
		want    GetHunkContextReply
	}{
		{
			name:    "after from the last line",
			content: file,
			args:    GetHunkContextArgs{AnchorLine: 10, Direction: "after", Count: 20, OrigStart: 8, OrigLength: 3, NewStart: 8, NewLength: 3},
			want:    GetHunkContextReply{Lines: []string{}},
		},
		{
			name:    "after runs up to the last line",
			content: file,
			args:    GetHunkContextArgs{AnchorLine: 7, Direction: "after", Count: 20, OrigStart: 5, OrigLength: 3, NewStart: 5, NewLength: 3},
			want: GetHunkContextReply{
				Lines:       []string{"line 8", "line 9", "line 10"},
				StartLine:   8,
				EndLine:     10,
				RangeHeader: "@@ -5,6 +5,6 @@",
			},
		},
		{
			name:    "before from the last line",
			content: file,
			args:    GetHunkContextArgs{AnchorLine: 10, Direction: "before", Count: 3, OrigStart: 10, OrigLength: 1, NewStart: 10, NewLength: 1},
			want: GetHunkContextReply{
				Lines:       []string{"line 7", "line 8", "line 9"},
				StartLine:   7,
				EndLine:     9,
				RangeHeader: "@@ -7,4 +7,4 @@",
			},
		},
		{
			name:    "an empty last line is still returned",
			content: file + "\n",
			args:    GetHunkContextArgs{AnchorLine: 9, Direction: "after", Count: 20, OrigStart: 9, OrigLength: 1, NewStart: 9, NewLength: 1},
			want: GetHunkContextReply{
				Lines:       []string{"line 10", ""},
				StartLine:   10,
				EndLine:     11,
				RangeHeader: "@@ -9,3 +9,3 @@",
			},
		},
		{
			name:    "after from an empty last line",
			content: file + "\n",
			args:    GetHunkContextArgs{AnchorLine: 11, Direction: "after", Count: 20, OrigStart: 11, OrigLength: 1, NewStart: 11, NewLength: 1},
			want:    GetHunkContextReply{Lines: []string{}},
		},
		{
			// A diff older than the head the file was read at.
			name:    "before from past the end",
			content: file,
			args:    GetHunkContextArgs{AnchorLine: 20, Direction: "before", Count: 5, OrigStart: 20, OrigLength: 1, NewStart: 20, NewLength: 1},
			want:    GetHunkContextReply{Lines: []string{}},
		},
		{
			name:    "an empty file has no lines",
			content: "",
			args:    GetHunkContextArgs{AnchorLine: 2, Direction: "before", Count: 5, OrigStart: 2, OrigLength: 1, NewStart: 2, NewLength: 1},
			want:    GetHunkContextReply{Lines: []string{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hunkContext(tt.content, &tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestSplitFileLines(t *testing.T) {
	tests := []struct {
		content string
		want    []string
	}{
		{"", nil},
		{"\n", []string{""}},
		{"a", []string{"a"}},
		{"a\n", []string{"a"}},
		{"a\nb\n", []string{"a", "b"}},
		{"a\n\n", []string{"a", ""}},
	}
	for _, tt := range tests {
		if got := splitFileLines(tt.content); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("splitFileLines(%q) = %q, want %q", tt.content, got, tt.want)
		}
	}
}

// TestGetHunkContextFromALocalClone runs the RPC against a real clone, so the
// file comes from git show, ending in a newline.
func TestGetHunkContextFromALocalClone(t *testing.T) {
	db := aiTestSetup(t, nil)
	shas := gitRepo(t, "gadgets", map[string]string{"main.go": "package main\n\nfunc main() {\n}\n"})
	if err := db.UpsertPullRequest(7, "gadgets", shas[0], "", ""); err != nil {
		t.Fatal(err)
	}
	h := &RPCHandler{}

	// @@ -1,2 +1,2 @@ covers lines 1-2 of the 4.
	args := GetHunkContextArgs{
		Owner: "octocat", Repo: "gadgets", Number: 7, Filename: "main.go",
		Side: "new", AnchorLine: 2, Direction: "after", Count: 20,
		OrigStart: 1, OrigLength: 2, NewStart: 1, NewLength: 2,
	}
	var reply GetHunkContextReply
	if err := h.GetHunkContext(&args, &reply); err != nil {
		t.Fatal(err)
	}
	want := GetHunkContextReply{Lines: []string{"func main() {", "}"}, StartLine: 3, EndLine: 4, RangeHeader: "@@ -1,4 +1,4 @@"}
	if !reflect.DeepEqual(reply, want) {
		t.Errorf("after line 2: got  %#v\nwant %#v", reply, want)
	}

	// Expanded, the hunk runs to the last line, and nothing is below it.
	args.AnchorLine, args.OrigLength, args.NewLength = 4, 4, 4
	var last GetHunkContextReply
	if err := h.GetHunkContext(&args, &last); err != nil {
		t.Fatal(err)
	}
	if want := (GetHunkContextReply{Lines: []string{}}); !reflect.DeepEqual(last, want) {
		t.Errorf("after the last line: got  %#v\nwant %#v", last, want)
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

func TestGetHunkContextCountDefaults(t *testing.T) {
	// Long enough that only the count stops an expansion from line 1.
	content := numberedFile(200)

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
			reply := hunkContext(content, &GetHunkContextArgs{AnchorLine: 1, Direction: "after", Count: tt.input})
			if len(reply.Lines) != tt.expected {
				t.Errorf("got %d lines, want %d", len(reply.Lines), tt.expected)
			}
		})
	}
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
	fileContent := numberedFile(30)

	// --- Initial hunk state ---
	// @@ -15,3 +15,4 @@ func doStuff()
	// Old side: lines 15,16,17   New side: lines 15,16,17,18
	origStart, origLength := 15, 3
	newStart, newLength := 15, 4
	hunkHeader := "func doStuff()"

	// expand asks for 3 lines from anchor, sending the hunk as it stands.
	expand := func(anchor int, direction string) GetHunkContextReply {
		return hunkContext(fileContent, &GetHunkContextArgs{
			AnchorLine: anchor, Direction: direction, Count: 3,
			OrigStart: origStart, OrigLength: origLength,
			NewStart: newStart, NewLength: newLength,
			HunkHeader: hunkHeader,
		})
	}

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
	reply := expand(topAnchor, "before")

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
	reply = expand(topAnchor, "before")

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
	reply = expand(bottomAnchor, "after")

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

func TestHunkContextRangeHeader(t *testing.T) {
	content := numberedFile(50)

	tests := []struct {
		name       string
		direction  string
		count      int
		origStart  int
		origLength int
		newStart   int
		newLength  int
		hunkHeader string
		wantHeader string
	}{
		{
			name:      "expand before — 5 extra lines",
			direction: "before",
			count:     5,
			origStart: 10, origLength: 7,
			newStart: 12, newLength: 8,
			hunkHeader: "func main()",
			wantHeader: "@@ -5,12 +7,13 @@ func main()",
		},
		{
			name:      "expand after — 3 extra lines",
			direction: "after",
			count:     3,
			origStart: 10, origLength: 7,
			newStart: 12, newLength: 8,
			hunkHeader: "",
			wantHeader: "@@ -10,10 +12,11 @@",
		},
		{
			name:      "expand before — clamped (fewer lines available)",
			direction: "before",
			count:     5, // only lines 1-2 are above the hunk
			origStart: 3, origLength: 4,
			newStart: 3, newLength: 5,
			hunkHeader: "",
			wantHeader: "@@ -1,6 +1,7 @@",
		},
		{
			name:      "expand after with hunk header",
			direction: "after",
			count:     10,
			origStart: 20, origLength: 5,
			newStart: 25, newLength: 6,
			hunkHeader: "type Foo struct",
			wantHeader: "@@ -20,15 +25,16 @@ type Foo struct",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Anchored where the clients anchor: the hunk's first new-side
			// line going up, its last going down.
			anchor := tt.newStart
			if tt.direction == "after" {
				anchor = tt.newStart + tt.newLength - 1
			}
			reply := hunkContext(content, &GetHunkContextArgs{
				AnchorLine: anchor, Direction: tt.direction, Count: tt.count,
				OrigStart: tt.origStart, OrigLength: tt.origLength,
				NewStart: tt.newStart, NewLength: tt.newLength,
				HunkHeader: tt.hunkHeader,
			})
			if reply.RangeHeader != tt.wantHeader {
				t.Errorf("got %q, want %q", reply.RangeHeader, tt.wantHeader)
			}
		})
	}
}
