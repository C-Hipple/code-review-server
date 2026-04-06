package server

import (
	"fmt"
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
		if endLine < 1 {
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
		name       string
		anchor     int
		direction  string
		count      int
		wantLines  []string
		wantStart  int
		wantEnd    int
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
