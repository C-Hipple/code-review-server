package workflows

import (
	"gtdbot/org"
	"log/slog"
	"os"
	"testing"
)

func TestDeduplicateChanges(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	tests := []struct {
		name     string
		changes  []SerializedFileChange
		expected int
	}{
		{
			name: "Single Add",
			changes: []SerializedFileChange{
				{FileChange: &FileChanges{ChangeType: "Addition", Item: org.NewOrgItem("Test Item 1", []string{"1", "test-repo"}, "TODO", []string{}, 0, 1)}},
			},
			expected: 1,
		},
		{
			name: "Add and Update",
			changes: []SerializedFileChange{
				{FileChange: &FileChanges{ChangeType: "Addition", Item: org.NewOrgItem("Test Item 1", []string{"1", "test-repo"}, "TODO", []string{}, 0, 1)}},
				{FileChange: &FileChanges{ChangeType: "Update", Item: org.NewOrgItem("Test Item 1", []string{"1", "test-repo"}, "TODO", []string{}, 0, 1)}},
			},
			expected: 1,
		},
		{
			name: "Add, Update and Delete",
			changes: []SerializedFileChange{
				{FileChange: &FileChanges{ChangeType: "Addition", Item: org.NewOrgItem("Test Item 1", []string{"1", "test-repo"}, "TODO", []string{}, 0, 1)}},
				{FileChange: &FileChanges{ChangeType: "Update", Item: org.NewOrgItem("Test Item 1", []string{"1", "test-repo"}, "TODO", []string{}, 0, 1)}},
				{FileChange: &FileChanges{ChangeType: "Delete", Item: org.NewOrgItem("Test Item 1", []string{"1", "test-repo"}, "TODO", []string{}, 0, 1)}},
			},
			expected: 1,
		},
		{
			name: "Only Deletes",
			changes: []SerializedFileChange{
				{FileChange: &FileChanges{ChangeType: "Delete", Item: org.NewOrgItem("Test Item 1", []string{"1", "test-repo"}, "TODO", []string{}, 0, 1)}},
				{FileChange: &FileChanges{ChangeType: "Delete", Item: org.NewOrgItem("Test Item 1", []string{"1", "test-repo"}, "TODO", []string{}, 0, 1)}},
			},
			expected: 1,
		},
		{
			name: "Multiple Items",
			changes: []SerializedFileChange{
				{FileChange: &FileChanges{ChangeType: "Addition", Item: org.NewOrgItem("Test Item 1", []string{"1", "test-repo"}, "TODO", []string{}, 0, 1)}},
				{FileChange: &FileChanges{ChangeType: "Update", Item: org.NewOrgItem("Test Item 1", []string{"1", "test-repo"}, "TODO", []string{}, 0, 1)}},
				{FileChange: &FileChanges{ChangeType: "Addition", Item: org.NewOrgItem("Test Item 2", []string{"2", "test-repo"}, "TODO", []string{}, 0, 1)}},
				{FileChange: &FileChanges{ChangeType: "Delete", Item: org.NewOrgItem("Test Item 2", []string{"2", "test-repo"}, "TODO", []string{}, 0, 1)}},
			},
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := deduplicateChanges(logger, tt.changes)
			if len(result) != tt.expected {
				t.Errorf("expected %d changes, got %d", tt.expected, len(result))
			}
		})
	}
}
