package workflows

import (
	"crs/config"
	"reflect"
	"testing"
)

func TestBuildSyncReviewRequestWorkflow_RepoOverride(t *testing.T) {
	globalRepos := []string{"global/repo1", "global/repo2"}

	tests := []struct {
		name          string
		rawWorkflow   config.RawWorkflow
		expectedRepos []string
	}{
		{
			name: "No override",
			rawWorkflow: config.RawWorkflow{
				Name: "test-no-override",
			},
			expectedRepos: globalRepos,
		},
		{
			name: "With override",
			rawWorkflow: config.RawWorkflow{
				Name:  "test-override",
				Repos: []string{"override/repo1"},
			},
			expectedRepos: []string{"override/repo1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := BuildSyncReviewRequestWorkflow(&tt.rawWorkflow, &globalRepos)
			syncWf, ok := wf.(SyncReviewRequestsWorkflow)
			if !ok {
				t.Fatalf("Expected SyncReviewRequestsWorkflow, got %T", wf)
			}
			if !reflect.DeepEqual(syncWf.Repos, tt.expectedRepos) {
				t.Errorf("expected repos %v, got %v", tt.expectedRepos, syncWf.Repos)
			}
		})
	}
}

func TestBuildListMyPRsWorkflow_RepoOverride(t *testing.T) {
	globalRepos := []string{"global/repo1", "global/repo2"}

	tests := []struct {
		name          string
		rawWorkflow   config.RawWorkflow
		expectedRepos []string
	}{
		{
			name: "No override",
			rawWorkflow: config.RawWorkflow{
				Name: "test-no-override",
			},
			expectedRepos: globalRepos,
		},
		{
			name: "With override",
			rawWorkflow: config.RawWorkflow{
				Name:  "test-override",
				Repos: []string{"override/repo1"},
			},
			expectedRepos: []string{"override/repo1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := BuildListMyPRsWorkflow(&tt.rawWorkflow, &globalRepos)
			listWf, ok := wf.(ListMyPRsWorkflow)
			if !ok {
				t.Fatalf("Expected ListMyPRsWorkflow, got %T", wf)
			}
			if !reflect.DeepEqual(listWf.Repos, tt.expectedRepos) {
				t.Errorf("expected repos %v, got %v", tt.expectedRepos, listWf.Repos)
			}
		})
	}
}
