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
			name: "SyncReviewRequestsWorkflow single repo with diff",
			workflow: SyncReviewRequestsWorkflow{
				Repos:       []string{"owner/repo"},
				IncludeDiff: true,
			},
			expected: []PRRequirement{
				{Owner: "owner", Repo: "repo", State: "open", AuxData: AuxDataRequirement{Comments: true, CIStatus: true, Diff: true}},
			},
		},
		{
			name: "SyncReviewRequestsWorkflow single repo no diff",
			workflow: SyncReviewRequestsWorkflow{
				Repos:       []string{"owner/repo"},
				IncludeDiff: false,
			},
			expected: []PRRequirement{
				{Owner: "owner", Repo: "repo", State: "open", AuxData: AuxDataRequirement{Comments: true, CIStatus: true, Diff: false}},
			},
		},
		{
			name: "SyncReviewRequestsWorkflow",
			workflow: SyncReviewRequestsWorkflow{
				Repos:       []string{"owner/repo1", "owner/repo2"},
				IncludeDiff: true,
			},
			expected: []PRRequirement{
				{Owner: "owner", Repo: "repo1", State: "open", AuxData: AuxDataRequirement{Comments: true, CIStatus: true, Diff: true}},
				{Owner: "owner", Repo: "repo2", State: "open", AuxData: AuxDataRequirement{Comments: true, CIStatus: true, Diff: true}},
			},
		},
		{
			name: "SyncReviewRequestsWorkflow with PRState closed",
			workflow: SyncReviewRequestsWorkflow{
				Repos:       []string{"owner/repo1"},
				PRState:     "closed",
				IncludeDiff: false,
			},
			expected: []PRRequirement{
				{Owner: "owner", Repo: "repo1", State: "closed", AuxData: AuxDataRequirement{Comments: true, CIStatus: true, Diff: false}},
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
				if req.AuxData != tt.expected[i].AuxData {
					t.Errorf("requirement %d AuxData mismatch: got %+v, want %+v", i, req.AuxData, tt.expected[i].AuxData)
				}
			}
		})
	}
}
