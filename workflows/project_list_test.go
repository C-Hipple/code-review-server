package workflows

import (
	"crs/config"
	"crs/jira"
	"reflect"
	"testing"
)

func TestResolveRepoRefs(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		want    []jira.RepoRef
	}{
		{
			name:    "single repo",
			entries: []string{"C-Hipple/code-review-server"},
			want:    []jira.RepoRef{{Owner: "C-Hipple", Repo: "code-review-server"}},
		},
		{
			name:    "multiple repos keep configured order",
			entries: []string{"C-Hipple/code-review-server", "C-Hipple/diff-lsp"},
			want: []jira.RepoRef{
				{Owner: "C-Hipple", Repo: "code-review-server"},
				{Owner: "C-Hipple", Repo: "diff-lsp"},
			},
		},
		{
			name:    "repos spanning owners",
			entries: []string{"org-a/service", "org-b/service"},
			want: []jira.RepoRef{
				{Owner: "org-a", Repo: "service"},
				{Owner: "org-b", Repo: "service"},
			},
		},
		{
			name:    "surrounding whitespace is trimmed",
			entries: []string{"  C-Hipple/diff-lsp  "},
			want:    []jira.RepoRef{{Owner: "C-Hipple", Repo: "diff-lsp"}},
		},
		{
			name:    "malformed entries are skipped, valid ones kept",
			entries: []string{"not-a-repo", "C-Hipple/diff-lsp", "too/many/parts"},
			want:    []jira.RepoRef{{Owner: "C-Hipple", Repo: "diff-lsp"}},
		},
		{
			name:    "duplicates collapse so a repo is only fetched once",
			entries: []string{"C-Hipple/diff-lsp", "C-Hipple/diff-lsp"},
			want:    []jira.RepoRef{{Owner: "C-Hipple", Repo: "diff-lsp"}},
		},
		{
			name:    "no valid entries",
			entries: []string{"nope"},
			want:    []jira.RepoRef{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveRepoRefs(tt.entries)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolveRepoRefs(%v) = %v, want %v", tt.entries, got, tt.want)
			}
		})
	}
}

// A project spanning several repos should be configurable as one workflow, so
// the whole Repos list has to survive the build.
func TestBuildProjectListWorkflowKeepsAllRepos(t *testing.T) {
	globalRepos := []string{"C-Hipple/global-repo"}

	tests := []struct {
		name      string
		raw       config.RawWorkflow
		wantRepos []string
	}{
		{
			name: "explicit multi-repo list",
			raw: config.RawWorkflow{
				WorkflowType: "ProjectListWorkflow",
				Name:         "Project - Multi",
				SectionTitle: "Multi Repo Project",
				JiraEpic:     "BOARD-123",
				Repos:        []string{"C-Hipple/code-review-server", "C-Hipple/diff-lsp"},
			},
			wantRepos: []string{"C-Hipple/code-review-server", "C-Hipple/diff-lsp"},
		},
		{
			name: "falls back to the root-level Repos list",
			raw: config.RawWorkflow{
				WorkflowType: "ProjectListWorkflow",
				Name:         "Project - Global",
				SectionTitle: "Global Repo Project",
				JiraEpic:     "BOARD-123",
			},
			wantRepos: globalRepos,
		},
		{
			name: "legacy singular Owner/Repo still works",
			raw: config.RawWorkflow{
				WorkflowType: "ProjectListWorkflow",
				Name:         "Project - Legacy",
				SectionTitle: "Legacy Project",
				JiraEpic:     "BOARD-123",
				Owner:        "C-Hipple",
				Repo:         "diff-lsp",
			},
			wantRepos: []string{"C-Hipple/diff-lsp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repos := append([]string{}, globalRepos...)
			built, err := BuildProjectListWorkflow(&tt.raw, &repos, "https://example.atlassian.net")
			if err != nil {
				t.Fatalf("BuildProjectListWorkflow returned an error: %v", err)
			}
			wf, ok := built.(ProjectListWorkflow)
			if !ok {
				t.Fatalf("expected a ProjectListWorkflow, got %T", built)
			}
			if !reflect.DeepEqual(wf.Repos, tt.wantRepos) {
				t.Errorf("Repos = %v, want %v", wf.Repos, tt.wantRepos)
			}
			if wf.JiraEpic != tt.raw.JiraEpic {
				t.Errorf("JiraEpic = %q, want %q", wf.JiraEpic, tt.raw.JiraEpic)
			}
		})
	}
}

// The manager collects PRs up front for workflows that declare requirements;
// ProjectListWorkflow resolves its PRs via Jira at run time instead, for every
// repo it spans.
func TestProjectListWorkflowDeclaresNoRequirements(t *testing.T) {
	wf := ProjectListWorkflow{
		Repos:    []string{"C-Hipple/code-review-server", "C-Hipple/diff-lsp"},
		JiraEpic: "BOARD-123",
	}
	if reqs := wf.GetPRRequirements(); reqs != nil {
		t.Errorf("GetPRRequirements() = %v, want nil", reqs)
	}
}
