package server

import (
	"crs/config"
	"crs/database"
	"crs/utils"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
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

func TestSplitComments(t *testing.T) {
	// Root is outdated
	root := &github.PullRequestComment{
		ID:               github.Int64(1),
		User:             &github.User{Login: github.String("user1")},
		Body:             github.String("outdated root"),
		Position:         nil, // Outdated
		OriginalPosition: github.Int(10),
		Line:             nil,
		OriginalLine:     github.Int(10),
	}
	// Reply to outdated root (not explicitly outdated itself, but should inherit)
	reply := &github.PullRequestComment{
		ID:        github.Int64(2),
		InReplyTo: github.Int64(1),
		User:      &github.User{Login: github.String("user2")},
		Body:      github.String("reply to outdated"),
		Position:  github.Int(15),
		Line:      github.Int(15),
	}
	// Another root, not outdated
	activeRoot := &github.PullRequestComment{
		ID:       github.Int64(3),
		User:     &github.User{Login: github.String("user3")},
		Body:     github.String("active root"),
		Position: github.Int(20),
		Line:     github.Int(20),
	}

	comments := convertToPRComments([]*github.PullRequestComment{root, reply, activeRoot})
	active, outdated := splitComments(comments)

	if len(outdated) != 2 {
		t.Errorf("Expected 2 outdated comments, got %d", len(outdated))
	}
	if len(active) != 1 {
		t.Errorf("Expected 1 active comment, got %d", len(active))
	}

	// Verify reply (ID 2) is in outdated list
	foundReply := false
	for _, c := range outdated {
		if c.ID == "2" {
			foundReply = true
			if !c.Outdated {
				t.Errorf("Reply (ID 2) should be marked as outdated")
			}
		}
	}
	if !foundReply {
		t.Errorf("Reply (ID 2) not found in outdated list")
	}

	// Verify activeRoot (ID 3) is in active list
	foundActive := false
	for _, c := range active {
		if c.ID == "3" {
			foundActive = true
		}
	}
	if !foundActive {
		t.Errorf("Active root (ID 3) not found in active list")
	}
}

func TestSortEntries(t *testing.T) {
	now := time.Now()
	makeEntries := func() []sectionEntry {
		mk := func(id int64, title string, createdAt time.Time, status string) sectionEntry {
			return sectionEntry{
				item:   &database.Item{ID: id, Title: title, CreatedAt: createdAt},
				review: ReviewItem{Title: title, Status: status, CreatedAt: createdAt},
			}
		}
		return []sectionEntry{
			mk(1, "middle", now.Add(-1*time.Hour), "TODO"),
			mk(2, "oldest", now.Add(-2*time.Hour), "TODO"),
			mk(3, "newest", now, "TODO"),
		}
	}
	titles := func(entries []sectionEntry) [3]string {
		return [3]string{entries[0].item.Title, entries[1].item.Title, entries[2].item.Title}
	}

	t.Run("newest_first", func(t *testing.T) {
		entries := makeEntries()
		sortEntries(entries, SortNewestFirst, true)
		if titles(entries) != [3]string{"newest", "middle", "oldest"} {
			t.Errorf("newest_first: got order %v", titles(entries))
		}
	})

	t.Run("oldest_first", func(t *testing.T) {
		entries := makeEntries()
		sortEntries(entries, SortOldestFirst, true)
		if titles(entries) != [3]string{"oldest", "middle", "newest"} {
			t.Errorf("oldest_first: got order %v", titles(entries))
		}
	})

	t.Run("unknown configured sort preserves order", func(t *testing.T) {
		entries := makeEntries()
		sortEntries(entries, "unknown", true)
		if titles(entries) != [3]string{"middle", "oldest", "newest"} {
			t.Errorf("unknown sort should preserve order, got %v", titles(entries))
		}
	})

	t.Run("unconfigured uses canonical review ordering", func(t *testing.T) {
		entries := makeEntries()
		sortEntries(entries, "", false)
		// reviewItemLess: same status, so most recently created first.
		if titles(entries) != [3]string{"newest", "middle", "oldest"} {
			t.Errorf("default sort should order newest first, got %v", titles(entries))
		}
	})
}

func TestPrStatusOrder(t *testing.T) {
	tests := []struct {
		name     string
		item     ReviewItem
		expected int
	}{
		{"open PR", ReviewItem{Status: "TODO"}, 0},
		{"draft PR", ReviewItem{Status: "WAITING"}, 1},
		{"merged PR", ReviewItem{Status: "DONE", Tags: "repo,merged"}, 2},
		{"closed PR", ReviewItem{Status: "DONE", Tags: "repo"}, 3},
		{"closed PR no tags", ReviewItem{Status: "DONE"}, 3},
		{"unknown status", ReviewItem{Status: "OTHER"}, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prStatusOrder(tt.item)
			if got != tt.expected {
				t.Errorf("prStatusOrder() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestPRMetadataStatsFieldsSerialization(t *testing.T) {
	meta := PRMetadata{
		Number:       42,
		Title:        "Add feature",
		ChangedFiles: 5,
		Additions:    120,
		Deletions:    30,
	}

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}

	// Verify correct JSON key names
	jsonStr := string(data)
	for _, key := range []string{`"changed_files":5`, `"additions":120`, `"deletions":30`} {
		if !strings.Contains(jsonStr, key) {
			t.Errorf("marshaled JSON missing %s; got: %s", key, jsonStr)
		}
	}

	// Roundtrip
	var got PRMetadata
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() error: %v", err)
	}
	if got.ChangedFiles != meta.ChangedFiles {
		t.Errorf("ChangedFiles: got %d, want %d", got.ChangedFiles, meta.ChangedFiles)
	}
	if got.Additions != meta.Additions {
		t.Errorf("Additions: got %d, want %d", got.Additions, meta.Additions)
	}
	if got.Deletions != meta.Deletions {
		t.Errorf("Deletions: got %d, want %d", got.Deletions, meta.Deletions)
	}
}

func TestPRMetadataStatsZeroValues(t *testing.T) {
	// Zero values should serialize as 0 (not omitted) so the client can distinguish
	// a cached entry with no stats from a PR with actual zero changes.
	meta := PRMetadata{Number: 1}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	jsonStr := string(data)
	for _, key := range []string{`"changed_files":0`, `"additions":0`, `"deletions":0`} {
		if !strings.Contains(jsonStr, key) {
			t.Errorf("marshaled JSON missing zero-value field %s; got: %s", key, jsonStr)
		}
	}
}

func TestPRMetadataStatsRoundtripFromGitHub(t *testing.T) {
	// Simulate the values populated from go-github getters (which return int).
	changedFiles := 3
	additions := 50
	deletions := 10

	meta := PRMetadata{
		Number:       7,
		ChangedFiles: changedFiles,
		Additions:    additions,
		Deletions:    deletions,
	}

	data, _ := json.Marshal(meta)
	var got PRMetadata
	json.Unmarshal(data, &got)

	if got.ChangedFiles != changedFiles {
		t.Errorf("ChangedFiles roundtrip: got %d, want %d", got.ChangedFiles, changedFiles)
	}
	if got.Additions != additions {
		t.Errorf("Additions roundtrip: got %d, want %d", got.Additions, additions)
	}
	if got.Deletions != deletions {
		t.Errorf("Deletions roundtrip: got %d, want %d", got.Deletions, deletions)
	}
}

func TestGetAllReviewsSorting(t *testing.T) {
	// Shuffled list covering all statuses, multiple repos, and various PR numbers.
	items := []ReviewItem{
		{Status: "DONE", Tags: "repo-b", Repo: "repo-b", Number: 10},       // closed
		{Status: "TODO", Tags: "repo-a", Repo: "repo-a", Number: 5},        // open
		{Status: "DONE", Tags: "repo-a,merged", Repo: "repo-a", Number: 3}, // merged
		{Status: "WAITING", Tags: "repo-b", Repo: "repo-b", Number: 1},     // draft
		{Status: "TODO", Tags: "repo-b", Repo: "repo-b", Number: 2},        // open
		{Status: "TODO", Tags: "repo-a", Repo: "repo-a", Number: 1},        // open
		{Status: "DONE", Tags: "repo-a", Repo: "repo-a", Number: 7},        // closed
		{Status: "DONE", Tags: "repo-b,merged", Repo: "repo-b", Number: 4}, // merged
		{Status: "WAITING", Tags: "repo-a", Repo: "repo-a", Number: 9},     // draft
	}

	sort.SliceStable(items, func(i, j int) bool {
		si, sj := prStatusOrder(items[i]), prStatusOrder(items[j])
		if si != sj {
			return si < sj
		}
		if items[i].Repo != items[j].Repo {
			return items[i].Repo < items[j].Repo
		}
		return items[i].Number < items[j].Number
	})

	// Expected: open → draft → merged → closed; within each group: repo-a before repo-b, lower number first.
	type want struct {
		status, repo string
		number       int
	}
	expected := []want{
		{"TODO", "repo-a", 1},
		{"TODO", "repo-a", 5},
		{"TODO", "repo-b", 2},
		{"WAITING", "repo-a", 9},
		{"WAITING", "repo-b", 1},
		{"DONE", "repo-a", 3},  // merged
		{"DONE", "repo-b", 4},  // merged
		{"DONE", "repo-a", 7},  // closed
		{"DONE", "repo-b", 10}, // closed
	}

	if len(items) != len(expected) {
		t.Fatalf("length mismatch: got %d, want %d", len(items), len(expected))
	}
	for i, w := range expected {
		got := items[i]
		if got.Status != w.status || got.Repo != w.repo || got.Number != w.number {
			t.Errorf("[%d] got {%s %s #%d}, want {%s %s #%d}",
				i, got.Status, got.Repo, got.Number, w.status, w.repo, w.number)
		}
	}
}

func TestSortFilesTestsLast(t *testing.T) {
	files := []*utils.DiffFile{
		{NewName: "server/server.go"},
		{NewName: "server/server_test.go"},
		{NewName: "config/config.go"},
		{NewName: "utils/diff_parser_test.go"},
		{NewName: "client.el/test.el"},
		{NewName: "README.md"},
	}

	got := sortFilesTestsLast(files)

	wantOrder := []string{
		"server/server.go",
		"config/config.go",
		"README.md",
		"server/server_test.go",
		"utils/diff_parser_test.go",
		"client.el/test.el",
	}
	for i, w := range wantOrder {
		if got[i].NewName != w {
			t.Errorf("[%d] got %q, want %q", i, got[i].NewName, w)
		}
	}

	// Original slice must not be mutated.
	if files[1].NewName != "server/server_test.go" {
		t.Errorf("input slice was mutated: %q", files[1].NewName)
	}
}

func TestIsTestFile(t *testing.T) {
	tests := []struct {
		file *utils.DiffFile
		want bool
	}{
		{&utils.DiffFile{NewName: "server/server_test.go"}, true},
		{&utils.DiffFile{NewName: "server/server.go"}, false},
		{&utils.DiffFile{NewName: "tests/integration.go"}, true},
		{&utils.DiffFile{NewName: "FooTest.java"}, true},
		{&utils.DiffFile{NewName: "", OrigName: "old_test.go"}, true},
	}
	for i, tc := range tests {
		if got := isTestFile(tc.file); got != tc.want {
			t.Errorf("[%d] isTestFile(%+v) = %v, want %v", i, tc.file, got, tc.want)
		}
	}
}

func TestOrderDiffFilesDefaultsToTestsLast(t *testing.T) {
	// With the experimental flag off, ordering must match sortFilesTestsLast
	// and make no network calls.
	config.SetC(config.Config{})
	files := []*utils.DiffFile{
		{NewName: "server/server_test.go"},
		{NewName: "server/server.go"},
	}
	got := orderDiffFiles(files, "code-review-server", 1, "")
	if got[0].NewName != "server/server.go" || got[1].NewName != "server/server_test.go" {
		t.Errorf("orderDiffFiles did not fall back to tests-last sort: got %q, %q",
			got[0].NewName, got[1].NewName)
	}
}

func TestOrderDiffFilesUsesCachedOrdering(t *testing.T) {
	// With the flag on and a cache entry for the PR SHA, orderDiffFiles must
	// apply the cached ordering without contacting the LLM.
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, ExperimentalLLMFileOrdering: true})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	files := []*utils.DiffFile{
		{NewName: "a_test.go"},
		{NewName: "main.go"},
		{NewName: "helper.go"},
	}

	cached, _ := json.Marshal([]string{"main.go", "helper.go", "a_test.go"})
	if err := db.UpsertDiffFileOrdering(7, "code-review-server", "sha-abc", string(cached)); err != nil {
		t.Fatalf("failed to seed cache: %v", err)
	}

	got := orderDiffFiles(files, "code-review-server", 7, "sha-abc")

	wantOrder := []string{"main.go", "helper.go", "a_test.go"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d files, want %d", len(got), len(wantOrder))
	}
	for i, w := range wantOrder {
		if got[i].NewName != w {
			t.Errorf("[%d] got %q, want %q", i, got[i].NewName, w)
		}
	}
}

func TestOrderDiffFilesFallsBackWithoutWaitingOnLLM(t *testing.T) {
	// With the flag on and nothing cached for the SHA, the render must not
	// block on the analysis: it falls back to the tests-last sort and leaves
	// the LLM call to the background dispatch.
	t.Setenv("CRS_HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", "")

	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, ExperimentalLLMFileOrdering: true})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	files := []*utils.DiffFile{
		{NewName: "a_test.go"},
		{NewName: "main.go"},
	}

	done := make(chan []*utils.DiffFile, 1)
	go func() { done <- orderDiffFiles(files, "code-review-server", 12, "sha-uncached") }()

	select {
	case got := <-done:
		if got[0].NewName != "main.go" || got[1].NewName != "a_test.go" {
			t.Errorf("expected tests-last fallback, got %q, %q", got[0].NewName, got[1].NewName)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("orderDiffFiles blocked on a cache miss instead of falling back")
	}
}

func TestReviewEaseCacheRoundtrip(t *testing.T) {
	db := setupTestDB(t)

	// Missing entry returns empty string, no error.
	got, err := db.GetReviewEase(1, "code-review-server", "sha1")
	if err != nil {
		t.Fatalf("unexpected error on cache miss: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty result on cache miss, got %q", got)
	}

	// Storing an ease rating must not clobber an existing ordering, and
	// vice versa.
	if err := db.UpsertDiffFileOrdering(1, "code-review-server", "sha1", `["a","b"]`); err != nil {
		t.Fatalf("ordering upsert failed: %v", err)
	}
	if err := db.UpsertReviewEase(1, "code-review-server", "sha1", "hard"); err != nil {
		t.Fatalf("ease upsert failed: %v", err)
	}
	if err := db.UpsertDiffFileOrdering(1, "code-review-server", "sha1", `["b","a"]`); err != nil {
		t.Fatalf("ordering re-upsert failed: %v", err)
	}

	gotEase, err := db.GetReviewEase(1, "code-review-server", "sha1")
	if err != nil {
		t.Fatalf("get ease failed: %v", err)
	}
	if gotEase != "hard" {
		t.Errorf("got ease %q, want %q", gotEase, "hard")
	}
	gotOrdering, err := db.GetDiffFileOrdering(1, "code-review-server", "sha1")
	if err != nil {
		t.Fatalf("get ordering failed: %v", err)
	}
	if gotOrdering != `["b","a"]` {
		t.Errorf("got ordering %q, want %q", gotOrdering, `["b","a"]`)
	}
}

func TestGetLatestReviewEaseSkipsUnratedRows(t *testing.T) {
	db := setupTestDB(t)

	// No rows at all: empty, no error.
	got, err := db.GetLatestReviewEase(9, "code-review-server")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty rating, got %q", got)
	}

	// A newer ordering-only row must not shadow the rated row.
	if err := db.UpsertReviewEase(9, "code-review-server", "sha-old", "easy"); err != nil {
		t.Fatalf("ease upsert failed: %v", err)
	}
	if err := db.UpsertDiffFileOrdering(9, "code-review-server", "sha-new", `["a"]`); err != nil {
		t.Fatalf("ordering upsert failed: %v", err)
	}

	got, err = db.GetLatestReviewEase(9, "code-review-server")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "easy" {
		t.Errorf("got rating %q, want %q", got, "easy")
	}
}

// detailsWithSubtrees mirrors the element layout PRToFileChangesDetails
// produces: flat metadata, then one element per subtree heading followed by
// that subtree's body.
func detailsWithSubtrees() []string {
	return []string{
		"42",
		"Repo: C-Hipple/code-review-server",
		"*** SUCCESS CI Status\n",
		" build: success\n",
		"*** Diff\n",
		"diff --git a/x.go b/x.go\n+added line\n-removed line\n",
		"*** BODY\n the pr body\n",
		"*** Comments [1]\n",
		" alice: nice change\n",
	}
}

func TestBuildItemLinesRenderOptions(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	section, err := db.GetOrCreateSection("Test Section", 0)
	if err != nil {
		t.Fatalf("failed to create section: %v", err)
	}
	item, err := db.UpsertItem(section.ID, "42", "TODO", "Some PR",
		detailsWithSubtrees(), []string{"code-review-server"}, 0)
	if err != nil {
		t.Fatalf("failed to create item: %v", err)
	}
	r := NewOrgRenderer(db)

	for _, tc := range []struct {
		name    string
		opts    RenderOptions
		want    []string
		notWant []string
	}{
		{
			name:    "compact zero value drops diff and comments",
			opts:    RenderOptions{},
			want:    []string{"CI Status", "build: success", "Repo:"},
			notWant: []string{"*** Diff", "added line", "*** Comments", "alice", "the pr body"},
		},
		{
			name:    "diff opted in",
			opts:    RenderOptions{IncludeDiff: true},
			want:    []string{"*** Diff", "added line", "CI Status"},
			notWant: []string{"*** Comments", "alice", "the pr body"},
		},
		{
			name:    "comments opted in",
			opts:    RenderOptions{IncludeComments: true},
			want:    []string{"*** Comments", "alice", "CI Status"},
			notWant: []string{"*** Diff", "added line", "the pr body"},
		},
		{
			name:    "full render keeps both but never the body",
			opts:    FullRenderOptions(),
			want:    []string{"*** Diff", "added line", "*** Comments", "alice"},
			notWant: []string{"the pr body"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(r.buildItemLines(item, 2, tc.opts), "")
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("rendered output missing %q:\n%s", want, got)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(got, notWant) {
					t.Errorf("rendered output should not contain %q:\n%s", notWant, got)
				}
			}
		})
	}
}

func TestDetailSubtree(t *testing.T) {
	for _, tc := range []struct {
		detail string
		want   string
	}{
		{"*** Diff\n", "Diff"},
		{"*** Comments [4]\n", "Comments [4]"},
		{"** TODO Some PR\n", "TODO Some PR"},
		{"Repo: C-Hipple/code-review-server", ""},
		{" build: success\n", ""},
		// A diff body is a single element and must not read as a heading,
		// even when a diff line happens to begin with stars.
		{"diff --git a/x.go b/x.go\n+*** not a heading\n", ""},
		{"***nospace", ""},
	} {
		if got := detailSubtree(tc.detail); got != tc.want {
			t.Errorf("detailSubtree(%q) = %q, want %q", tc.detail, got, tc.want)
		}
	}
}

func TestBuildItemLinesIncludesReviewEaseTag(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, ExperimentalLLMReviewEase: true})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	section, err := db.GetOrCreateSection("Test Section", 0)
	if err != nil {
		t.Fatalf("failed to create section: %v", err)
	}
	details := []string{
		"83",
		"Repo: C-Hipple/code-review-server",
		"https://github.com/C-Hipple/code-review-server/pull/83",
	}
	item, err := db.UpsertItem(section.ID, "83", "TODO", "Debug PR", details, []string{"code-review-server"}, 0)
	if err != nil {
		t.Fatalf("failed to create item: %v", err)
	}
	if err := db.UpsertReviewEase(83, "code-review-server", "sha-1", "medium"); err != nil {
		t.Fatalf("failed to seed review ease: %v", err)
	}

	r := NewOrgRenderer(db)
	lines := r.buildItemLines(item, 2, FullRenderOptions())
	if len(lines) == 0 {
		t.Fatal("no lines rendered")
	}
	if !strings.Contains(lines[0], ":code-review-server:medium:") {
		t.Errorf("title line %q missing review-ease tag", lines[0])
	}

	// With the flag off, the headline keeps only the stored tags.
	config.SetC(config.Config{DB: db})
	lines = r.buildItemLines(item, 2, FullRenderOptions())
	if !strings.Contains(lines[0], ":code-review-server:") || strings.Contains(lines[0], "medium") {
		t.Errorf("title line %q should not include review-ease tag when disabled", lines[0])
	}
}

func TestDiffFileOrderingCacheRoundtrip(t *testing.T) {
	db := setupTestDB(t)

	// Missing entry returns empty string, no error.
	got, err := db.GetDiffFileOrdering(1, "code-review-server", "sha1")
	if err != nil {
		t.Fatalf("unexpected error on cache miss: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty result on cache miss, got %q", got)
	}

	// Stored entry round-trips, and a re-upsert for the same SHA overwrites.
	if err := db.UpsertDiffFileOrdering(1, "code-review-server", "sha1", `["a","b"]`); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if err := db.UpsertDiffFileOrdering(1, "code-review-server", "sha1", `["b","a"]`); err != nil {
		t.Fatalf("re-upsert failed: %v", err)
	}
	got, err = db.GetDiffFileOrdering(1, "code-review-server", "sha1")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got != `["b","a"]` {
		t.Errorf("got %q, want %q", got, `["b","a"]`)
	}
}
