package workflows

import (
	"crs/config"
	"testing"

	"github.com/google/go-github/v48/github"
)

func TestBuildFiltersList(t *testing.T) {
	tests := []struct {
		name           string
		rawWorkflow    config.RawWorkflow
		expectedCount  int
		wantErr        bool
	}{
		{
			name: "Empty filters",
			rawWorkflow: config.RawWorkflow{
				Name:    "test",
				Filters: []string{},
			},
			expectedCount: 0,
		},
		{
			name: "Standard filters only",
			rawWorkflow: config.RawWorkflow{
				Name:    "test",
				Filters: []string{"FilterNotDraft", "FilterNotMyPRs"},
			},
			expectedCount: 2,
		},
		{
			name: "Teams configured adds team filter automatically",
			rawWorkflow: config.RawWorkflow{
				Name:    "test",
				Filters: []string{},
				Teams:   []string{"team-a", "team-b"},
			},
			expectedCount: 1,
		},
		{
			name: "Empty teams does not add team filter",
			rawWorkflow: config.RawWorkflow{
				Name:    "test",
				Filters: []string{"FilterNotDraft"},
				Teams:   []string{},
			},
			expectedCount: 1,
		},
		{
			name: "Teams with other filters",
			rawWorkflow: config.RawWorkflow{
				Name:    "test",
				Filters: []string{"FilterNotDraft", "FilterNotMyPRs"},
				Teams:   []string{"team-a"},
			},
			expectedCount: 3, // team filter + 2 standard filters
		},
		{
			name: "Unknown filter returns error",
			rawWorkflow: config.RawWorkflow{
				Name:    "test",
				Filters: []string{"FilterNotDraft", "UnknownFilter"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filters, err := BuildFiltersList(&tt.rawWorkflow)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(filters) != tt.expectedCount {
				t.Errorf("expected %d filters, got %d", tt.expectedCount, len(filters))
			}
		})
	}
}

// TestBuildFiltersList_AllKnownFilters ensures every entry in filter_func_map
// resolves through BuildFiltersList without error.  If a new filter is added to
// the map, or an existing key is mistyped, this test will fail.
func TestBuildFiltersList_AllKnownFilters(t *testing.T) {
	for name := range filter_func_map {
		t.Run(name, func(t *testing.T) {
			raw := config.RawWorkflow{
				Name:    "test",
				Filters: []string{name},
			}
			filters, err := BuildFiltersList(&raw)
			if err != nil {
				t.Fatalf("BuildFiltersList returned error for known filter %q: %v", name, err)
			}
			if len(filters) != 1 {
				t.Errorf("expected 1 filter for %q, got %d", name, len(filters))
			}
		})
	}

	// Parameterized filters each require an argument; verify they resolve too.
	parameterized := []string{"FilterByLabel:somelabel", "FilterByAuthor:someuser", "FilterExcludeAuthor:someuser"}
	for _, name := range parameterized {
		t.Run(name, func(t *testing.T) {
			raw := config.RawWorkflow{
				Name:    "test",
				Filters: []string{name},
			}
			filters, err := BuildFiltersList(&raw)
			if err != nil {
				t.Fatalf("BuildFiltersList returned error for parameterized filter %q: %v", name, err)
			}
			if len(filters) != 1 {
				t.Errorf("expected 1 filter for %q, got %d", name, len(filters))
			}
		})
	}
}

func TestBuildFiltersList_TeamFilterBehavior(t *testing.T) {
	// Helper to create a team with a slug
	makeTeam := func(slug string) *github.Team {
		return &github.Team{Slug: &slug}
	}

	// Helper to create a PR with requested teams
	makePR := func(number int, teamSlugs ...string) *github.PullRequest {
		teams := make([]*github.Team, len(teamSlugs))
		for i, slug := range teamSlugs {
			teams[i] = makeTeam(slug)
		}
		draft := false
		return &github.PullRequest{
			Number:         &number,
			RequestedTeams: teams,
			Draft:          &draft,
		}
	}

	// Test that the team filter is automatically added when Teams is configured
	rawWorkflow := config.RawWorkflow{
		Name:  "test",
		Teams: []string{"growth-team", "backend-team"},
	}

	filters, err := BuildFiltersList(&rawWorkflow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(filters) != 1 {
		t.Fatalf("expected 1 filter (auto-added team filter), got %d", len(filters))
	}

	prs := []*github.PullRequest{
		makePR(1, "growth-team"),
		makePR(2, "frontend-team"),
		makePR(3, "backend-team"),
		makePR(4, "other-team"),
	}

	// Apply the filter
	result := filters[0](prs)

	if len(result) != 2 {
		t.Errorf("expected 2 PRs after filtering, got %d", len(result))
	}

	// Verify the correct PRs are returned
	expectedNumbers := map[int]bool{1: true, 3: true}
	for _, pr := range result {
		if !expectedNumbers[*pr.Number] {
			t.Errorf("unexpected PR #%d in results", *pr.Number)
		}
	}
}

func TestBuildFiltersListPerWorkflowTeams(t *testing.T) {
	// Test that different workflows can have different team configurations
	makeTeam := func(slug string) *github.Team {
		return &github.Team{Slug: &slug}
	}

	makePR := func(number int, teamSlugs ...string) *github.PullRequest {
		teams := make([]*github.Team, len(teamSlugs))
		for i, slug := range teamSlugs {
			teams[i] = makeTeam(slug)
		}
		return &github.PullRequest{
			Number:         &number,
			RequestedTeams: teams,
		}
	}

	// Workflow 1 targets growth team (team filter auto-added via Teams field)
	workflow1 := config.RawWorkflow{
		Name:  "Growth Reviews",
		Teams: []string{"growth-team"},
	}

	// Workflow 2 targets backend team (team filter auto-added via Teams field)
	workflow2 := config.RawWorkflow{
		Name:  "Backend Reviews",
		Teams: []string{"backend-team"},
	}

	filters1, err := BuildFiltersList(&workflow1)
	if err != nil {
		t.Fatalf("unexpected error for workflow1: %v", err)
	}
	filters2, err := BuildFiltersList(&workflow2)
	if err != nil {
		t.Fatalf("unexpected error for workflow2: %v", err)
	}

	prs := []*github.PullRequest{
		makePR(1, "growth-team"),
		makePR(2, "backend-team"),
		makePR(3, "growth-team", "backend-team"),
	}

	// Workflow 1 should match PRs 1 and 3
	result1 := filters1[0](prs)
	if len(result1) != 2 {
		t.Errorf("workflow1: expected 2 PRs, got %d", len(result1))
	}

	// Workflow 2 should match PRs 2 and 3
	result2 := filters2[0](prs)
	if len(result2) != 2 {
		t.Errorf("workflow2: expected 2 PRs, got %d", len(result2))
	}

	// Verify workflow 1 got the right PRs
	for _, pr := range result1 {
		hasGrowthTeam := false
		for _, team := range pr.RequestedTeams {
			if *team.Slug == "growth-team" {
				hasGrowthTeam = true
				break
			}
		}
		if !hasGrowthTeam {
			t.Errorf("workflow1: PR #%d should have growth-team", *pr.Number)
		}
	}

	// Verify workflow 2 got the right PRs
	for _, pr := range result2 {
		hasBackendTeam := false
		for _, team := range pr.RequestedTeams {
			if *team.Slug == "backend-team" {
				hasBackendTeam = true
				break
			}
		}
		if !hasBackendTeam {
			t.Errorf("workflow2: PR #%d should have backend-team", *pr.Number)
		}
	}
}

func TestBuildFiltersList_ParameterizedFilters(t *testing.T) {
	// Helper to create a PR with labels
	makePR := func(number int, labelNames ...string) *github.PullRequest {
		labels := make([]*github.Label, len(labelNames))
		for i, name := range labelNames {
			copiedName := name // Avoid closure loop issue
			labels[i] = &github.Label{Name: &copiedName}
		}
		return &github.PullRequest{
			Number: &number,
			Labels: labels,
		}
	}

	tests := []struct {
		name          string
		filtersConfig []string
		prs           []*github.PullRequest
		expectedCount int
		expectedPRs   []int
		wantErr       bool
	}{
		{
			name:          "FilterByLabel with simple label",
			filtersConfig: []string{"FilterByLabel:bug"},
			prs: []*github.PullRequest{
				makePR(1, "bug"),
				makePR(2, "feature"),
			},
			expectedCount: 1,
			expectedPRs:   []int{1},
		},
		{
			name:          "FilterByLabel with complex label",
			filtersConfig: []string{"FilterByLabel:area/backend"},
			prs: []*github.PullRequest{
				makePR(1, "area/backend"),
				makePR(2, "area/frontend"),
			},
			expectedCount: 1,
			expectedPRs:   []int{1},
		},
		{
			name:          "FilterByLabel missing argument returns error",
			filtersConfig: []string{"FilterByLabel"},
			wantErr:       true,
		},
		{
			name:          "Multiple FilterByLabel",
			filtersConfig: []string{"FilterByLabel:bug", "FilterByLabel:urgent"},
			prs: []*github.PullRequest{
				makePR(1, "bug"),
				makePR(2, "urgent"),
				makePR(3, "feature"),
				makePR(4, "bug", "urgent"),
			},
			expectedCount: 2,
			expectedPRs:   []int{4}, // Only PR 4 has both bug AND urgent
		},
		{
			name:          "FilterByAuthor with argument",
			filtersConfig: []string{"FilterByAuthor:alice"},
			prs:           []*github.PullRequest{},
			expectedCount: 1,
			expectedPRs:   []int{},
		},
		{
			name:          "FilterExcludeAuthor with argument",
			filtersConfig: []string{"FilterExcludeAuthor:bob"},
			prs:           []*github.PullRequest{},
			expectedCount: 1,
			expectedPRs:   []int{},
		},
		{
			name:          "FilterByAuthor missing argument returns error",
			filtersConfig: []string{"FilterByAuthor"},
			wantErr:       true,
		},
		{
			name:          "FilterExcludeAuthor missing argument returns error",
			filtersConfig: []string{"FilterExcludeAuthor"},
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawWorkflow := config.RawWorkflow{
				Name:    "test",
				Filters: tt.filtersConfig,
			}
			filters, err := BuildFiltersList(&rawWorkflow)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(filters) != tt.expectedCount {
				t.Errorf("expected %d filters, got %d", tt.expectedCount, len(filters))
			}

			if tt.expectedCount > 0 {
				currentPRs := tt.prs
				for _, filter := range filters {
					currentPRs = filter(currentPRs)
				}

				if len(currentPRs) != len(tt.expectedPRs) {
					t.Errorf("expected %d PRs after filtering, got %d", len(tt.expectedPRs), len(currentPRs))
				}

				for i, pr := range currentPRs {
					if i < len(tt.expectedPRs) && *pr.Number != tt.expectedPRs[i] {
						t.Errorf("expected PR #%d at index %d, got #%d", tt.expectedPRs[i], i, *pr.Number)
					}
				}
			}
		})
	}
}

func TestParseFilterString(t *testing.T) {
	tests := []struct {
		input        string
		expectedName string
		expectedArg  string
	}{
		{"FilterName", "FilterName", ""},
		{"FilterName:Arg", "FilterName", "Arg"},
		{"Filter:With:Colons", "Filter", "With:Colons"},
		{"Filter:", "Filter", ""},
		{":ArgOnly", "", "ArgOnly"},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			name, arg := ParseFilterString(tt.input)
			if name != tt.expectedName {
				t.Errorf("expected name %q, got %q", tt.expectedName, name)
			}
			if arg != tt.expectedArg {
				t.Errorf("expected arg %q, got %q", tt.expectedArg, arg)
			}
		})
	}
}

func TestMatchWorkflows_SkipsInvalid(t *testing.T) {
	repos := []string{"owner/repo"}

	tests := []struct {
		name            string
		rawWorkflows    []config.RawWorkflow
		expectedCount   int
	}{
		{
			name: "unknown WorkflowType is skipped",
			rawWorkflows: []config.RawWorkflow{
				{WorkflowType: "NonExistentWorkflow", Name: "bad"},
				{WorkflowType: "SyncReviewRequestsWorkflow", Name: "good", Owner: "owner"},
			},
			expectedCount: 1,
		},
		{
			name: "typo in WorkflowType is skipped",
			rawWorkflows: []config.RawWorkflow{
				{WorkflowType: "SyncReviewRequestWorkflow", Name: "typo"}, // missing 's' in Requests
			},
			expectedCount: 0,
		},
		{
			name: "unknown filter causes workflow to be skipped",
			rawWorkflows: []config.RawWorkflow{
				{WorkflowType: "SyncReviewRequestsWorkflow", Name: "bad-filter", Owner: "owner", Filters: []string{"FilterNonExistent"}},
				{WorkflowType: "SyncReviewRequestsWorkflow", Name: "good", Owner: "owner", Filters: []string{"FilterNotDraft"}},
			},
			expectedCount: 1,
		},
		{
			name: "all valid workflows are kept",
			rawWorkflows: []config.RawWorkflow{
				{WorkflowType: "SyncReviewRequestsWorkflow", Name: "wf1", Owner: "owner"},
				{WorkflowType: "SingleRepoSyncReviewRequestsWorkflow", Name: "wf2", Owner: "owner", Repo: "repo"},
				{WorkflowType: "ListMyPRsWorkflow", Name: "wf3", Owner: "owner"},
			},
			expectedCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workflows := MatchWorkflows(tt.rawWorkflows, &repos, "")
			if len(workflows) != tt.expectedCount {
				t.Errorf("expected %d workflows, got %d", tt.expectedCount, len(workflows))
			}
		})
	}
}
