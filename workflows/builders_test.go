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

