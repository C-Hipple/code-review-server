package git_tools

import (
    "testing"

    "github.com/google/go-github/v48/github"
)

func TestMakeTeamFilters(t *testing.T) {
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
        return &github.PullRequest{
            Number:         &number,
            RequestedTeams: teams,
        }
    }

    tests := []struct {
        name            string
        filterTeams     []string
        prs             []*github.PullRequest
        expectedCount   int
        expectedNumbers []int
    }{
        {
            name:            "No PRs",
            filterTeams:     []string{"team-a"},
            prs:             []*github.PullRequest{},
            expectedCount:   0,
            expectedNumbers: []int{},
        },
        {
            name:        "Single matching team",
            filterTeams: []string{"team-a"},
            prs: []*github.PullRequest{
                makePR(1, "team-a"),
                makePR(2, "team-b"),
            },
            expectedCount:   1,
            expectedNumbers: []int{1},
        },
        {
            name:        "Multiple filter teams",
            filterTeams: []string{"team-a", "team-b"},
            prs: []*github.PullRequest{
                makePR(1, "team-a"),
                makePR(2, "team-b"),
                makePR(3, "team-c"),
            },
            expectedCount:   2,
            expectedNumbers: []int{1, 2},
        },
        {
            name:        "PR with multiple teams matches one filter",
            filterTeams: []string{"team-b"},
            prs: []*github.PullRequest{
                makePR(1, "team-a", "team-b", "team-c"),
            },
            expectedCount:   1,
            expectedNumbers: []int{1},
        },
        {
            name:        "No matching teams",
            filterTeams: []string{"team-x", "team-y"},
            prs: []*github.PullRequest{
                makePR(1, "team-a"),
                makePR(2, "team-b"),
            },
            expectedCount:   0,
            expectedNumbers: []int{},
        },
        {
            name:        "Empty filter teams matches nothing",
            filterTeams: []string{},
            prs: []*github.PullRequest{
                makePR(1, "team-a"),
            },
            expectedCount:   0,
            expectedNumbers: []int{},
        },
        {
            name:        "PR with no requested teams",
            filterTeams: []string{"team-a"},
            prs: []*github.PullRequest{
                makePR(1), // No teams
                makePR(2, "team-a"),
            },
            expectedCount:   1,
            expectedNumbers: []int{2},
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            filter := MakeTeamFilters(tt.filterTeams)
            result := filter(tt.prs)

            if len(result) != tt.expectedCount {
                t.Errorf("expected %d PRs, got %d", tt.expectedCount, len(result))
            }

            for i, pr := range result {
                if i < len(tt.expectedNumbers) && *pr.Number != tt.expectedNumbers[i] {
                    t.Errorf("expected PR #%d at index %d, got #%d", tt.expectedNumbers[i], i, *pr.Number)
                }
            }
        })
    }
}

func TestMakeLabelFilter(t *testing.T) {
    // Helper to create a label
    makeLabel := func(name string) *github.Label {
        return &github.Label{Name: &name}
    }

    // Helper to create a PR with labels
    makePR := func(number int, labelNames ...string) *github.PullRequest {
        labels := make([]*github.Label, len(labelNames))
        for i, name := range labelNames {
            labels[i] = makeLabel(name)
        }
        return &github.PullRequest{
            Number: &number,
            Labels: labels,
        }
    }

    tests := []struct {
        name            string
        filterLabel     string
        prs             []*github.PullRequest
        expectedCount   int
        expectedNumbers []int
    }{
        {
            name:            "No PRs",
            filterLabel:     "bug",
            prs:             []*github.PullRequest{},
            expectedCount:   0,
            expectedNumbers: []int{},
        },
        {
            name:        "Single matching label",
            filterLabel: "bug",
            prs: []*github.PullRequest{
                makePR(1, "bug"),
                makePR(2, "feature"),
            },
            expectedCount:   1,
            expectedNumbers: []int{1},
        },
        {
            name:        "Nested matching label",
            filterLabel: "security",
            prs: []*github.PullRequest{
                makePR(1, "bug", "security"),
                makePR(2, "feature"),
            },
            expectedCount:   1,
            expectedNumbers: []int{1},
        },
        {
            name:        "No matching labels",
            filterLabel: "urgent",
            prs: []*github.PullRequest{
                makePR(1, "bug"),
                makePR(2, "feature"),
            },
            expectedCount:   0,
            expectedNumbers: []int{},
        },
        {
            name:        "Multiple matches",
            filterLabel: "bug",
            prs: []*github.PullRequest{
                makePR(1, "bug"),
                makePR(2, "bug", "urgent"),
                makePR(3, "feature"),
            },
            expectedCount:   2,
            expectedNumbers: []int{1, 2},
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            filter := MakeLabelFilter(tt.filterLabel)
            result := filter(tt.prs)

            if len(result) != tt.expectedCount {
                t.Errorf("expected %d PRs, got %d", tt.expectedCount, len(result))
            }

            for i, pr := range result {
                if i < len(tt.expectedNumbers) && *pr.Number != tt.expectedNumbers[i] {
                    t.Errorf("expected PR #%d at index %d, got #%d", tt.expectedNumbers[i], i, *pr.Number)
                }
            }
        })
    }
}
