package workflows

import (
	"crs/config"
	"strings"
	"testing"
)

// TestFilterRegistryCoversFilterFuncMap guards the two lists from drifting: a
// filter that BuildFiltersList accepts but FilterTypes doesn't list would be
// rejected by ValidateWorkflows even though the workflow runs fine.
func TestFilterRegistryCoversFilterFuncMap(t *testing.T) {
	for name := range filter_func_map {
		info, ok := lookupFilter(name)
		if !ok {
			t.Errorf("filter %q is buildable but missing from the filter registry", name)
			continue
		}
		if info.RequiresArg {
			t.Errorf("filter %q takes no argument in filter_func_map but the registry says it requires one", name)
		}
	}
}

// TestArgumentFiltersAreBuildable checks the other direction for the filters
// BuildFiltersList special-cases: each one must build when given an argument.
func TestArgumentFiltersAreBuildable(t *testing.T) {
	for _, info := range FilterTypes() {
		if !info.RequiresArg {
			continue
		}
		raw := config.RawWorkflow{Name: "test", Filters: []string{info.Name + ":value"}}
		filters, err := BuildFiltersList(&raw)
		if err != nil {
			t.Errorf("filter %q is in the registry but failed to build: %v", info.Name, err)
			continue
		}
		if len(filters) != 1 {
			t.Errorf("expected filter %q to build one filter, got %d", info.Name, len(filters))
		}
	}
}

// TestRegisteredWorkflowTypesAreBuildable keeps the type registry in sync with
// the switch in MatchWorkflows.
func TestRegisteredWorkflowTypesAreBuildable(t *testing.T) {
	repos := []string{"owner/repo"}
	for _, wt := range WorkflowTypes() {
		raw := config.RawWorkflow{
			WorkflowType: wt.Name,
			Name:         "test",
			SectionTitle: "Section",
		}
		// Fill in whatever the type declares it needs.
		for _, field := range wt.RequiredFields {
			switch field {
			case "Repo":
				raw.Repo = "owner/repo"
			case "JiraEpic":
				raw.JiraEpic = "BOARD-1"
			}
		}
		built := MatchWorkflows([]config.RawWorkflow{raw}, &repos, "jira.example.com")
		if len(built) != 1 {
			t.Errorf("workflow type %q is registered but MatchWorkflows built %d workflows", wt.Name, len(built))
		}
	}
}

func TestValidateWorkflowsAcceptsAWorkingWorkflow(t *testing.T) {
	raws := []config.RawWorkflow{{
		WorkflowType: "SyncReviewRequestsWorkflow",
		Name:         "My Open PRs",
		SectionTitle: "My PRs",
		Filters:      []string{"FilterNotDraft", "FilterByLabel:bug"},
		Repos:        []string{"owner/repo"},
		PRState:      "open",
	}}
	if problems := ValidateWorkflows(raws, nil); len(problems) != 0 {
		t.Errorf("expected no problems, got %v", problems)
	}
}

func TestValidateWorkflows(t *testing.T) {
	tests := []struct {
		name        string
		raw         config.RawWorkflow
		globalRepos []string
		wantField   string
		wantMessage string // substring
	}{
		{
			name:        "unknown workflow type",
			raw:         config.RawWorkflow{WorkflowType: "MagicWorkflow", Name: "w", SectionTitle: "s"},
			globalRepos: []string{"owner/repo"},
			wantField:   "WorkflowType",
			wantMessage: "unknown workflow type",
		},
		{
			name:        "unknown filter",
			raw:         config.RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: "w", SectionTitle: "s", Filters: []string{"FilterNope"}},
			globalRepos: []string{"owner/repo"},
			wantField:   "Filters",
			wantMessage: "unknown filter",
		},
		{
			name:        "filter missing its argument",
			raw:         config.RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: "w", SectionTitle: "s", Filters: []string{"FilterByLabel"}},
			globalRepos: []string{"owner/repo"},
			wantField:   "Filters",
			wantMessage: "requires an argument",
		},
		{
			name:        "argument on a filter that takes none",
			raw:         config.RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: "w", SectionTitle: "s", Filters: []string{"FilterNotDraft:yes"}},
			globalRepos: []string{"owner/repo"},
			wantField:   "Filters",
			wantMessage: "does not take an argument",
		},
		{
			name:        "malformed workflow repo",
			raw:         config.RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: "w", SectionTitle: "s", Repos: []string{"repo"}},
			wantField:   "Repos",
			wantMessage: "owner/repo",
		},
		{
			name:      "no repos anywhere",
			raw:       config.RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: "w", SectionTitle: "s"},
			wantField: "Repos",
			// falls back to the root-level list, which is empty here
			wantMessage: "root-level Repos",
		},
		{
			name:        "unknown PR state",
			raw:         config.RawWorkflow{WorkflowType: "SyncReviewRequestsWorkflow", Name: "w", SectionTitle: "s", PRState: "merged"},
			globalRepos: []string{"owner/repo"},
			wantField:   "PRState",
			wantMessage: "unknown state",
		},
		{
			name:        "project list without an epic",
			raw:         config.RawWorkflow{WorkflowType: "ProjectListWorkflow", Name: "w", SectionTitle: "s"},
			globalRepos: []string{"owner/repo"},
			wantField:   "JiraEpic",
			wantMessage: "required",
		},
		{
			name:        "single repo workflow without a repo",
			raw:         config.RawWorkflow{WorkflowType: "SingleRepoSyncReviewRequestsWorkflow", Name: "w", SectionTitle: "s"},
			globalRepos: []string{"owner/repo"},
			wantField:   "Repo",
			wantMessage: "owner/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := ValidateWorkflows([]config.RawWorkflow{tt.raw}, tt.globalRepos)
			if len(problems) == 0 {
				t.Fatalf("expected a validation problem, got none")
			}
			found := false
			for _, p := range problems {
				if p.Workflow != 0 {
					t.Errorf("expected the problem to point at workflow 0, got %d", p.Workflow)
				}
				if p.Field == tt.wantField && strings.Contains(p.Message, tt.wantMessage) {
					found = true
				}
			}
			if !found {
				t.Errorf("expected a %s problem mentioning %q, got %v", tt.wantField, tt.wantMessage, problems)
			}
		})
	}
}
