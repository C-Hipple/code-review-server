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
			name: "Unknown filter is skipped",
			rawWorkflow: config.RawWorkflow{
				Name:    "test",
				Filters: []string{"FilterNotDraft", "UnknownFilter"},
			},
			expectedCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filters := BuildFiltersList(&tt.rawWorkflow)
			if len(filters) != tt.expectedCount {
				t.Errorf("expected %d filters, got %d", tt.expectedCount, len(filters))
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

	filters := BuildFiltersList(&rawWorkflow)
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

	filters1 := BuildFiltersList(&workflow1)
	filters2 := BuildFiltersList(&workflow2)

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
		expectedCount int      // Number of filters created
		expectedPRs   []int    // IDs of PRs that pass the filter
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
			name:          "FilterByLabel with missing argument (invalid)",
			filtersConfig: []string{"FilterByLabel"}, // Should be skipped or strictly checked if logic allows
			prs: []*github.PullRequest{
				makePR(1, "bug"),
			},
			expectedCount: 0, // Current logic: no colon -> filterName="FilterByLabel" -> map lookup nil -> skip
			expectedPRs:   []int{},
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
			// Note: Filters are additive (AND logic usually implies applying all filters sequentially).
			// If BuildFiltersList returns a list of filters, the workflow usually applies them one by one.
			// Ideally we want to test if the constructed filters work.
			// Let's assume the workflow applies all filters.
			expectedCount: 2,
			expectedPRs:   []int{4}, // Only PR 4 has both bug AND urgent
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawWorkflow := config.RawWorkflow{
				Name:    "test",
				Filters: tt.filtersConfig,
			}
			filters := BuildFiltersList(&rawWorkflow)

			if len(filters) != tt.expectedCount {
				t.Errorf("expected %d filters, got %d", tt.expectedCount, len(filters))
			}

			// If we created filters, verify they work as expected (chaining them)
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
