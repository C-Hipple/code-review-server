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
				{Owner: "owner", Repo: "repo", State: "open", AuxData: AuxDataRequirement{Diff: true}},
			},
		},
		{
			name: "SyncReviewRequestsWorkflow single repo no diff",
			workflow: SyncReviewRequestsWorkflow{
				Repos:       []string{"owner/repo"},
				IncludeDiff: false,
			},
			expected: []PRRequirement{
				{Owner: "owner", Repo: "repo", State: "open", AuxData: AuxDataRequirement{}},
			},
		},
		{
			name: "SyncReviewRequestsWorkflow WaitingOnMe aux data",
			workflow: SyncReviewRequestsWorkflow{
				Repos:       []string{"owner/repo"},
				IncludeDiff: false,
				AuxDataReq:  AuxDataRequirement{Comments: true, Reviews: true, Commits: true},
			},
			expected: []PRRequirement{
				{Owner: "owner", Repo: "repo", State: "open", AuxData: AuxDataRequirement{Comments: true, Reviews: true, Commits: true}},
			},
		},
		{
			name: "SyncReviewRequestsWorkflow multi-repo with diff",
			workflow: SyncReviewRequestsWorkflow{
				Repos:       []string{"owner/repo1", "owner/repo2"},
				IncludeDiff: true,
			},
			expected: []PRRequirement{
				{Owner: "owner", Repo: "repo1", State: "open", AuxData: AuxDataRequirement{Diff: true}},
				{Owner: "owner", Repo: "repo2", State: "open", AuxData: AuxDataRequirement{Diff: true}},
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
				{Owner: "owner", Repo: "repo1", State: "closed", AuxData: AuxDataRequirement{}},
			},
		},
		{
			name: "ProjectListWorkflow",
			workflow: ProjectListWorkflow{
				Repos:      []string{"owner/repo"},
				JiraEpic:   "EPIC-123",
				JiraDomain: "domain.atlassian.net",
			},
			expected: nil,
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

func TestComputeAuxRequirements(t *testing.T) {
	tests := []struct {
		name        string
		filterNames []string
		includeDiff bool
		expected    AuxDataRequirement
	}{
		{
			name:        "NoFilters",
			filterNames: []string{},
			includeDiff: false,
			expected:    AuxDataRequirement{},
		},
		{
			name:        "DiffOnly",
			filterNames: []string{},
			includeDiff: true,
			expected:    AuxDataRequirement{Diff: true},
		},
		{
			name:        "FilterWaitingOnMe",
			filterNames: []string{"FilterWaitingOnMe"},
			includeDiff: false,
			expected:    AuxDataRequirement{Comments: true, Reviews: true, Commits: true},
		},
		{
			name:        "FilterWaitingOnAuthor",
			filterNames: []string{"FilterWaitingOnAuthor"},
			includeDiff: false,
			expected:    AuxDataRequirement{Comments: true, Reviews: true, Commits: true},
		},
		{
			name:        "FilterCIPassing",
			filterNames: []string{"FilterCIPassing"},
			includeDiff: false,
			expected:    AuxDataRequirement{CIStatus: true},
		},
		{
			name:        "FilterCIFailing",
			filterNames: []string{"FilterCIFailing"},
			includeDiff: false,
			expected:    AuxDataRequirement{CIStatus: true},
		},
		{
			name:        "FilterMyReviewRequested",
			filterNames: []string{"FilterMyReviewRequested"},
			includeDiff: false,
			expected:    AuxDataRequirement{Reviews: true},
		},
		{
			name:        "MultipleFilters",
			filterNames: []string{"FilterNotDraft", "FilterWaitingOnMe", "FilterCIPassing"},
			includeDiff: true,
			expected:    AuxDataRequirement{Comments: true, CIStatus: true, Diff: true, Reviews: true, Commits: true},
		},
		{
			name:        "UnrelatedFilters",
			filterNames: []string{"FilterNotDraft", "FilterNotMyPRs", "FilterStale"},
			includeDiff: false,
			expected:    AuxDataRequirement{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeAuxRequirements(tt.filterNames, tt.includeDiff)
			if got != tt.expected {
				t.Errorf("computeAuxRequirements(%v, %v) = %+v, want %+v", tt.filterNames, tt.includeDiff, got, tt.expected)
			}
		})
	}
}
