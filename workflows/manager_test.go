package workflows

import (
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
				{FileChange: &FileChanges{ChangeType: "Addition", Identifier: "test-repo-1", Title: "Test Item 1", SectionName: "Section 1"}},
			},
			expected: 1,
		},
		{
			name: "Add and Update",
			changes: []SerializedFileChange{
				{FileChange: &FileChanges{ChangeType: "Addition", Identifier: "test-repo-1", Title: "Test Item 1", SectionName: "Section 1"}},
				{FileChange: &FileChanges{ChangeType: "Update", Identifier: "test-repo-1", Title: "Test Item 1", SectionName: "Section 1"}},
			},
			expected: 1,
		},
		{
			name: "Add, Update and Delete",
			changes: []SerializedFileChange{
				{FileChange: &FileChanges{ChangeType: "Addition", Identifier: "test-repo-1", Title: "Test Item 1", SectionName: "Section 1"}},
				{FileChange: &FileChanges{ChangeType: "Update", Identifier: "test-repo-1", Title: "Test Item 1", SectionName: "Section 1"}},
				{FileChange: &FileChanges{ChangeType: "Delete", Identifier: "test-repo-1", Title: "Test Item 1", SectionName: "Section 1"}},
			},
			expected: 1,
		},
		{
			name: "Only Deletes",
			changes: []SerializedFileChange{
				{FileChange: &FileChanges{ChangeType: "Delete", Identifier: "test-repo-1", Title: "Test Item 1", SectionName: "Section 1"}},
				{FileChange: &FileChanges{ChangeType: "Delete", Identifier: "test-repo-1", Title: "Test Item 1", SectionName: "Section 1"}},
			},
			expected: 1,
		},
		{
			name: "Multiple Items",
			changes: []SerializedFileChange{
				{FileChange: &FileChanges{ChangeType: "Addition", Identifier: "test-repo-1", Title: "Test Item 1", SectionName: "Section 1"}},
				{FileChange: &FileChanges{ChangeType: "Update", Identifier: "test-repo-1", Title: "Test Item 1", SectionName: "Section 1"}},
				{FileChange: &FileChanges{ChangeType: "Addition", Identifier: "test-repo-2", Title: "Test Item 2", SectionName: "Section 1"}},
				{FileChange: &FileChanges{ChangeType: "Delete", Identifier: "test-repo-2", Title: "Test Item 2", SectionName: "Section 1"}},
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
