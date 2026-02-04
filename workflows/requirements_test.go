package workflows

import (
	"testing"
)

func TestGetPRRequirements(t *testing.T) {
	tests := []struct {
		name     string
		workflow Workflow
		expected []PRRequirement
	}{
		{
			name: "SingleRepoSyncReviewRequestsWorkflow",
			workflow: SingleRepoSyncReviewRequestsWorkflow{
				Repo: "owner/repo",
			},
			expected: []PRRequirement{
				{Owner: "owner", Repo: "repo", State: "open"},
			},
		},
		{
			name: "SyncReviewRequestsWorkflow",
			workflow: SyncReviewRequestsWorkflow{
				Repos: []string{"owner/repo1", "owner/repo2"},
			},
			expected: []PRRequirement{
				{Owner: "owner", Repo: "repo1", State: "open"},
				{Owner: "owner", Repo: "repo2", State: "open"},
			},
		},
		{
			name: "ListMyPRsWorkflow",
			workflow: ListMyPRsWorkflow{
				Repos:   []string{"owner/repo1"},
				PRState: "closed",
			},
			expected: []PRRequirement{
				{Owner: "owner", Repo: "repo1", State: "closed"},
			},
		},
		{
			name: "ProjectListWorkflow",
			workflow: ProjectListWorkflow{
				Owner:      "owner",
				Repo:       "repo",
				JiraEpic:   "EPIC-123",
				JiraDomain: "domain.atlassian.net",
			},
			expected: nil, // Should be nil after revert
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqs := tt.workflow.GetPRRequirements()
			if len(reqs) != len(tt.expected) {
				t.Fatalf("expected %d requirements, got %d", len(tt.expected), len(reqs))
			}
			for i, req := range reqs {
				if req.Owner != tt.expected[i].Owner || req.Repo != tt.expected[i].Repo || req.State != tt.expected[i].State {
					t.Errorf("requirement %d mismatch: got %+v, want %+v", i, req, tt.expected[i])
				}
				if len(req.PRNumbers) != len(tt.expected[i].PRNumbers) {
					t.Errorf("requirement %d PRNumbers length mismatch", i)
				}
			}
		})
	}
}
