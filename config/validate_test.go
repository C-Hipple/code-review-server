package config

import (
	"strings"
	"testing"
	"time"
)

// validConfig is the baseline each test case perturbs.
func validConfig() *Config {
	return &Config{
		Repos:         []string{"owner/repo"},
		SleepDuration: 10 * time.Minute,
		RawWorkflows: []RawWorkflow{{
			WorkflowType: "SyncReviewRequestsWorkflow",
			Name:         "My Open PRs",
			SectionTitle: "My PRs",
		}},
	}
}

func TestValidateAcceptsAWorkingConfig(t *testing.T) {
	if problems := Validate(validConfig()); len(problems) != 0 {
		t.Errorf("expected no problems, got %v", problems)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*Config)
		wantField   string
		wantMessage string // substring
	}{
		{
			name:        "zero sleep duration",
			mutate:      func(c *Config) { c.SleepDuration = 0 },
			wantField:   "SleepDuration",
			wantMessage: "positive",
		},
		{
			name:        "absurd sleep duration",
			mutate:      func(c *Config) { c.SleepDuration = 48 * time.Hour },
			wantField:   "SleepDuration",
			wantMessage: "at most",
		},
		{
			name:        "repo missing owner",
			mutate:      func(c *Config) { c.Repos = []string{"repo"} },
			wantField:   "Repos",
			wantMessage: "owner/repo",
		},
		{
			name:        "unknown section sorting",
			mutate:      func(c *Config) { c.SectionSorting = map[string]string{"My PRs": "alphabetical"} },
			wantField:   "SectionSorting",
			wantMessage: "newest_first",
		},
		{
			name:        "workflow without a name",
			mutate:      func(c *Config) { c.RawWorkflows[0].Name = "  " },
			wantField:   "Name",
			wantMessage: "required",
		},
		{
			name:        "workflow without a section title",
			mutate:      func(c *Config) { c.RawWorkflows[0].SectionTitle = "" },
			wantField:   "SectionTitle",
			wantMessage: "required",
		},
		{
			name:        "workflow without a type",
			mutate:      func(c *Config) { c.RawWorkflows[0].WorkflowType = "" },
			wantField:   "WorkflowType",
			wantMessage: "required",
		},
		{
			name: "duplicate workflow names",
			mutate: func(c *Config) {
				c.RawWorkflows = append(c.RawWorkflows, c.RawWorkflows[0])
			},
			wantField:   "Name",
			wantMessage: "duplicates",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)
			problems := Validate(cfg)
			if len(problems) == 0 {
				t.Fatalf("expected a validation problem, got none")
			}
			found := false
			for _, p := range problems {
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

func TestValidationErrorMessage(t *testing.T) {
	wf := ValidationError{Workflow: 2, Field: "Name", Message: "is required"}
	if got := wf.Error(); got != "Workflows[2].Name: is required" {
		t.Errorf("unexpected workflow error text: %q", got)
	}
	root := ValidationError{Workflow: -1, Field: "SleepDuration", Message: "must be positive"}
	if got := root.Error(); got != "SleepDuration: must be positive" {
		t.Errorf("unexpected root error text: %q", got)
	}
}
