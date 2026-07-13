package workflows

import (
	"sync"
	"testing"

	"github.com/google/go-github/v74/github"
)

func TestDeduplicateChanges(t *testing.T) {
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
			result := deduplicateChanges(tt.changes)
			if len(result) != tt.expected {
				t.Errorf("expected %d changes, got %d", tt.expected, len(result))
			}
		})
	}
}

// mockWorkflow implements Workflow for testing without hitting real APIs.
type mockWorkflow struct {
	name     string
	reqs     []PRRequirement
	runCalls int
}

func (m *mockWorkflow) GetName() string                    { return m.name }
func (m *mockWorkflow) GetOrgSectionName() string          { return m.name }
func (m *mockWorkflow) GetPRRequirements() []PRRequirement { return m.reqs }
func (m *mockWorkflow) Run(_ []*github.PullRequest, _ chan FileChanges, _ *sync.WaitGroup) (RunResult, error) {
	m.runCalls++
	return RunResult{}, nil
}

func TestHasUnfetchedRequirements(t *testing.T) {
	pr1 := &github.PullRequest{}

	tests := []struct {
		name         string
		workflow     Workflow
		repoStatePRs map[string]map[string][]*github.PullRequest
		specificPRs  map[string]map[int]*github.PullRequest
		want         bool
	}{
		{
			name: "state fetch succeeded with PRs",
			workflow: &mockWorkflow{reqs: []PRRequirement{
				{Owner: "owner", Repo: "repo", State: "open"},
			}},
			repoStatePRs: map[string]map[string][]*github.PullRequest{
				"owner/repo": {"open": []*github.PullRequest{pr1}},
			},
			specificPRs: map[string]map[int]*github.PullRequest{},
			want:        false,
		},
		{
			name: "state fetch succeeded with zero PRs (empty but non-nil)",
			workflow: &mockWorkflow{reqs: []PRRequirement{
				{Owner: "owner", Repo: "repo", State: "open"},
			}},
			repoStatePRs: map[string]map[string][]*github.PullRequest{
				"owner/repo": {"open": make([]*github.PullRequest, 0)},
			},
			specificPRs: map[string]map[int]*github.PullRequest{},
			want:        false, // zero PRs is legitimate — section should be cleared
		},
		{
			name: "state fetch failed (nil after rate limit)",
			workflow: &mockWorkflow{reqs: []PRRequirement{
				{Owner: "owner", Repo: "repo", State: "open"},
			}},
			repoStatePRs: map[string]map[string][]*github.PullRequest{
				"owner/repo": {"open": nil}, // fetch error left this nil
			},
			specificPRs: map[string]map[int]*github.PullRequest{},
			want:        true, // must skip to protect DB
		},
		{
			name: "state fetch not even attempted (repo key missing)",
			workflow: &mockWorkflow{reqs: []PRRequirement{
				{Owner: "owner", Repo: "repo", State: "open"},
			}},
			repoStatePRs: map[string]map[string][]*github.PullRequest{},
			specificPRs:  map[string]map[int]*github.PullRequest{},
			want:         true, // missing == not fetched
		},
		{
			name: "multi-repo: one repo failed",
			workflow: &mockWorkflow{reqs: []PRRequirement{
				{Owner: "owner", Repo: "repoA", State: "open"},
				{Owner: "owner", Repo: "repoB", State: "open"},
			}},
			repoStatePRs: map[string]map[string][]*github.PullRequest{
				"owner/repoA": {"open": []*github.PullRequest{pr1}},
				"owner/repoB": {"open": nil}, // fetch error
			},
			specificPRs: map[string]map[int]*github.PullRequest{},
			want:        true, // whole workflow must be skipped
		},
		{
			name: "specific PR fetch succeeded",
			workflow: &mockWorkflow{reqs: []PRRequirement{
				{Owner: "owner", Repo: "repo", PRNumbers: []int{42}},
			}},
			repoStatePRs: map[string]map[string][]*github.PullRequest{},
			specificPRs: map[string]map[int]*github.PullRequest{
				"owner/repo": {42: pr1},
			},
			want: false,
		},
		{
			name: "specific PR fetch failed (nil after rate limit)",
			workflow: &mockWorkflow{reqs: []PRRequirement{
				{Owner: "owner", Repo: "repo", PRNumbers: []int{42}},
			}},
			repoStatePRs: map[string]map[string][]*github.PullRequest{},
			specificPRs: map[string]map[int]*github.PullRequest{
				"owner/repo": {42: nil}, // fetch error left this nil
			},
			want: true,
		},
		{
			name:         "workflow with no requirements (e.g. ProjectListWorkflow)",
			workflow:     &mockWorkflow{reqs: nil},
			repoStatePRs: map[string]map[string][]*github.PullRequest{},
			specificPRs:  map[string]map[int]*github.PullRequest{},
			want:         false, // no requirements = nothing to block on
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasUnfetchedRequirements(tt.workflow, tt.repoStatePRs, tt.specificPRs)
			if got != tt.want {
				t.Errorf("hasUnfetchedRequirements() = %v, want %v", got, tt.want)
			}
		})
	}
}
