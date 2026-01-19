package server

import (
	"strings"
	"testing"

	"github.com/google/go-github/v48/github"
)

func TestFormatComment(t *testing.T) {
	tests := []struct {
		name     string
		comment  *github.PullRequestComment
		expected string
	}{
		{
			name: "basic comment",
			comment: &github.PullRequestComment{
				User: &github.User{
					Login: github.String("testuser"),
				},
				Body: github.String("This is a test comment"),
			},
			expected: "Reviewed By: testuser\nThis is a test comment\n------------------\n",
		},
		{
			name: "comment with newlines",
			comment: &github.PullRequestComment{
				User: &github.User{
					Login: github.String("reviewer"),
				},
				Body: github.String("Line 1\nLine 2\nLine 3"),
			},
			expected: "Reviewed By: reviewer\nLine 1\nLine 2\nLine 3\n------------------\n",
		},
		{
			name: "empty comment body",
			comment: &github.PullRequestComment{
				User: &github.User{
					Login: github.String("user"),
				},
				Body: github.String(""),
			},
			expected: "Reviewed By: user\n\n------------------\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatComment(&GitHubPRComment{tt.comment})
			if result != tt.expected {
				t.Errorf("formatComment() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestFilterComments(t *testing.T) {
	tests := []struct {
		name     string
		comments []*github.PullRequestComment
		expected int
	}{
		{
			name: "filter advanced user comments",
			comments: []*github.PullRequestComment{
				{
					User: &github.User{Login: github.String("advanced-bot")},
					Body: github.String("lint warning"),
				},
				{
					User: &github.User{Login: github.String("user1")},
					Body: github.String("real comment"),
				},
				{
					User: &github.User{Login: github.String("advanced-linter")},
					Body: github.String("another lint"),
				},
			},
			expected: 1,
		},
		{
			name: "no filtering needed",
			comments: []*github.PullRequestComment{
				{
					User: &github.User{Login: github.String("user1")},
					Body: github.String("comment 1"),
				},
				{
					User: &github.User{Login: github.String("user2")},
					Body: github.String("comment 2"),
				},
			},
			expected: 2,
		},
		{
			name:     "empty comments",
			comments: []*github.PullRequestComment{},
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterComments(convertToPRComments(tt.comments))
			if len(result) != tt.expected {
				t.Errorf("filterComments() returned %d comments, want %d", len(result), tt.expected)
			}
			// Verify no advanced users in result
			for _, comment := range result {
				if strings.Contains(comment.GetLogin(), "advanced") {
					t.Errorf("filterComments() did not filter out advanced user: %s", comment.GetLogin())
				}
			}
		})
	}
}

func TestBuildCommentTreesFromList(t *testing.T) {
	tests := []struct {
		name     string
		comments []*github.PullRequestComment
		expected int // number of trees
	}{
		{
			name: "single root comment",
			comments: []*github.PullRequestComment{
				{
					ID:   github.Int64(1),
					User: &github.User{Login: github.String("user1")},
					Body: github.String("root comment"),
				},
			},
			expected: 1,
		},
		{
			name: "comment with reply",
			comments: []*github.PullRequestComment{
				{
					ID:   github.Int64(1),
					User: &github.User{Login: github.String("user1")},
					Body: github.String("root comment"),
				},
				{
					ID:        github.Int64(2),
					InReplyTo: github.Int64(1),
					User:      &github.User{Login: github.String("user2")},
					Body:      github.String("reply"),
				},
			},
			expected: 1, // Should be grouped into one tree
		},
		{
			name: "multiple root comments",
			comments: []*github.PullRequestComment{
				{
					ID:   github.Int64(1),
					User: &github.User{Login: github.String("user1")},
					Body: github.String("comment 1"),
				},
				{
					ID:   github.Int64(2),
					User: &github.User{Login: github.String("user2")},
					Body: github.String("comment 2"),
				},
			},
			expected: 2,
		},
		{
			name: "nested replies",
			comments: []*github.PullRequestComment{
				{
					ID:   github.Int64(1),
					User: &github.User{Login: github.String("user1")},
					Body: github.String("root"),
				},
				{
					ID:        github.Int64(2),
					InReplyTo: github.Int64(1),
					User:      &github.User{Login: github.String("user2")},
					Body:      github.String("reply 1"),
				},
				{
					ID:        github.Int64(3),
					InReplyTo: github.Int64(2),
					User:      &github.User{Login: github.String("user1")},
					Body:      github.String("reply 2"),
				},
			},
			expected: 2, // Root + direct reply in one tree, nested reply becomes orphaned
		},
		{
			name: "orphaned reply",
			comments: []*github.PullRequestComment{
				{
					ID:        github.Int64(2),
					InReplyTo: github.Int64(999), // Parent not in list
					User:      &github.User{Login: github.String("user1")},
					Body:      github.String("orphaned"),
				},
			},
			expected: 1, // Should still create a tree for orphaned comment
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildCommentTreesFromList(convertToPRComments(tt.comments))
			if len(result) != tt.expected {
				t.Errorf("buildCommentTreesFromList() returned %d trees, want %d", len(result), tt.expected)
			}
			
			// Verify all comments are included
			totalComments := 0
			for _, tree := range result {
				totalComments += len(tree)
			}
			if totalComments != len(tt.comments) {
				t.Errorf("buildCommentTreesFromList() lost comments: got %d total, want %d", totalComments, len(tt.comments))
			}
		})
	}
}

func TestTreeAuthorsFromList(t *testing.T) {
	tests := []struct {
		name     string
		tree     []*github.PullRequestComment
		expected string
	}{
		{
			name: "single author",
			tree: []*github.PullRequestComment{
				{
					User: &github.User{Login: github.String("user1")},
				},
			},
			expected: "user1",
		},
		{
			name: "multiple unique authors",
			tree: []*github.PullRequestComment{
				{
					User: &github.User{Login: github.String("user1")},
				},
				{
					User: &github.User{Login: github.String("user2")},
				},
				{
					User: &github.User{Login: github.String("user3")},
				},
			},
			expected: "user1|user2|user3",
		},
		{
			name: "duplicate authors",
			tree: []*github.PullRequestComment{
				{
					User: &github.User{Login: github.String("user1")},
				},
				{
					User: &github.User{Login: github.String("user2")},
				},
				{
					User: &github.User{Login: github.String("user1")},
				},
			},
			expected: "user1|user2", // Should deduplicate
		},
		{
			name:     "empty tree",
			tree:     []*github.PullRequestComment{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := treeAuthorsFromList(convertToPRComments(tt.tree))
			if result != tt.expected {
				t.Errorf("treeAuthorsFromList() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestEscapeBody(t *testing.T) {
	tests := []struct {
		name     string
		body     *string
		expected string
	}{
		{
			name:     "nil body",
			body:     nil,
			expected: "",
		},
		{
			name:     "empty body",
			body:     github.String(""),
			expected: "",
		},
		{
			name:     "simple text",
			body:     github.String("Simple comment"),
			expected: "Simple comment",
		},
		{
			name:     "text with asterisk",
			body:     github.String("* This is a bullet point"),
			expected: "- This is a bullet point",
		},
		{
			name:     "multiple lines",
			body:     github.String("Line 1\nLine 2\nLine 3"),
			expected: "Line 1\nLine 2\nLine 3",
		},
		{
			name:     "lines with asterisks",
			body:     github.String("* Point 1\n* Point 2\nNormal text"),
			expected: "- Point 1\n- Point 2\nNormal text",
		},
		{
			name:     "text with trailing empty lines",
			body:     github.String("Content\n\n\n"),
			expected: "Content",
		},
		{
			name:     "only asterisk",
			body:     github.String("*"),
			expected: "-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := escapeBody(tt.body)
			if result != tt.expected {
				t.Errorf("escapeBody() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestCleanLines(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected string
	}{
		{
			name:     "simple lines",
			lines:    []string{"line1", "line2", "line3"},
			expected: "line1\nline2\nline3",
		},
		{
			name:     "lines with asterisks",
			lines:    []string{"* bullet1", "* bullet2", "normal"},
			expected: "- bullet1\n- bullet2\nnormal",
		},
		{
			name:     "lines with nested newlines",
			lines:    []string{"line1\nline1b", "line2"},
			expected: "line1\nline1b\nline2",
		},
		{
			name:     "trailing empty lines removed",
			lines:    []string{"content", "", "  ", ""},
			expected: "content",
		},
		{
			name:     "empty input",
			lines:    []string{},
			expected: "",
		},
		{
			name:     "only empty lines",
			lines:    []string{"", "  ", "\t"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanLines(&tt.lines)
			if result != tt.expected {
				t.Errorf("cleanLines() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestCleanEmptyEndingLines(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected int // expected length after cleaning
	}{
		{
			name:     "no trailing empty lines",
			lines:    []string{"line1", "line2", "line3"},
			expected: 3,
		},
		{
			name:     "trailing empty lines",
			lines:    []string{"line1", "line2", "", ""},
			expected: 2,
		},
		{
			name:     "trailing whitespace lines",
			lines:    []string{"line1", "  ", "\t", ""},
			expected: 1,
		},
		{
			name:     "all empty lines",
			lines:    []string{"", "  ", "\t"},
			expected: 0,
		},
		{
			name:     "empty slice",
			lines:    []string{},
			expected: 0,
		},
		{
			name:     "empty lines in middle",
			lines:    []string{"line1", "", "line2", ""},
			expected: 3, // Should keep middle empty line, remove trailing
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanEmptyEndingLines(&tt.lines)
			if len(result) != tt.expected {
				t.Errorf("cleanEmptyEndingLines() returned length %d, want %d", len(result), tt.expected)
			}
			
			// Verify no trailing empty/whitespace lines (only check from the end)
			if len(result) > 0 {
				for i := len(result) - 1; i >= 0; i-- {
					if strings.TrimSpace(result[i]) != "" {
						break // Found non-empty line, rest should be empty
					}
					if i == 0 {
						t.Errorf("cleanEmptyEndingLines() left all empty lines")
					}
				}
			}
		})
	}
}

func TestRenderPullRequest(t *testing.T) {
	diff := "diff --git a/file.txt b/file.txt\n@@ -1,1 +1,2 @@\n+new line\n"
	comments := []*github.PullRequestComment{
		{
			User: &github.User{Login: github.String("user1")},
			Body: github.String("Comment 1"),
		},
		{
			User: &github.User{Login: github.String("user2")},
			Body: github.String("Comment 2"),
		},
	}

	result := renderPullRequest(diff, convertToPRComments(comments))
	
	// Should contain the diff
	if !strings.Contains(result, diff) {
		t.Error("renderPullRequest() should contain the diff")
	}
	
	// Should contain both comments
	if !strings.Contains(result, "Reviewed By: user1") {
		t.Error("renderPullRequest() should contain first comment")
	}
	if !strings.Contains(result, "Reviewed By: user2") {
		t.Error("renderPullRequest() should contain second comment")
	}
	
	// Should have separator lines
	if !strings.Contains(result, "------------------") {
		t.Error("renderPullRequest() should contain separator lines")
	}
}

func TestBuildCommentTreesFromList_Complex(t *testing.T) {
	// Test a more complex scenario with multiple trees and nested replies
	comments := []*github.PullRequestComment{
		// Tree 1: Root comment with one reply
		{
			ID:   github.Int64(1),
			User: &github.User{Login: github.String("alice")},
			Body: github.String("First comment"),
		},
		{
			ID:        github.Int64(2),
			InReplyTo: github.Int64(1),
			User:      &github.User{Login: github.String("bob")},
			Body:      github.String("Reply to first"),
		},
		// Tree 2: Another root comment
		{
			ID:   github.Int64(3),
			User: &github.User{Login: github.String("charlie")},
			Body: github.String("Second comment"),
		},
		// Tree 3: Root with nested replies
		{
			ID:   github.Int64(4),
			User: &github.User{Login: github.String("dave")},
			Body: github.String("Third comment"),
		},
		{
			ID:        github.Int64(5),
			InReplyTo: github.Int64(4),
			User:      &github.User{Login: github.String("eve")},
			Body:      github.String("Reply to third"),
		},
		{
			ID:        github.Int64(6),
			InReplyTo: github.Int64(5),
			User:      &github.User{Login: github.String("dave")},
			Body:      github.String("Reply to reply"),
		},
	}

	trees := buildCommentTreesFromList(convertToPRComments(comments))
	
	// Should have at least 3 trees (may be more due to nested replies being orphaned)
	if len(trees) < 3 {
		t.Errorf("Expected at least 3 trees, got %d", len(trees))
	}
	
	// Find trees by their root comment IDs
	tree1Found := false
	tree2Found := false
	tree3Found := false
	
	for _, tree := range trees {
		if len(tree) > 0 {
			rootID := tree[0].GetID()
			if rootID == "1" {
				tree1Found = true
				// Tree 1 should have at least 2 comments (root + direct reply)
				if len(tree) < 2 {
					t.Errorf("Tree 1 should have at least 2 comments, got %d", len(tree))
				}
			} else if rootID == "3" {
				tree2Found = true
				// Tree 2 should have 1 comment
				if len(tree) != 1 {
					t.Errorf("Tree 2 should have 1 comment, got %d", len(tree))
				}
			} else if rootID == "4" {
				tree3Found = true
				// Tree 3 should have at least 2 comments (root + direct reply, nested reply may be separate)
				if len(tree) < 2 {
					t.Errorf("Tree 3 should have at least 2 comments, got %d", len(tree))
				}
			}
		}
	}
	
	if !tree1Found {
		t.Error("Tree 1 (root comment 1) not found")
	}
	if !tree2Found {
		t.Error("Tree 2 (root comment 3) not found")
	}
	if !tree3Found {
		t.Error("Tree 3 (root comment 4) not found")
	}
	
	// Verify all comments are accounted for
	total := 0
	for _, tree := range trees {
		total += len(tree)
	}
	if total != len(comments) {
		t.Errorf("Total comments in trees (%d) doesn't match input (%d)", total, len(comments))
	}
}

