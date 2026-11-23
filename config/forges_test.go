package config

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestForgesDefaultToGitHub(t *testing.T) {
	cfg, err := parseConfig([]byte(`
[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "Team"
SectionTitle = "Team"
`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if got := cfg.DefaultForgeID(); got != ForgeGitHub {
		t.Errorf("DefaultForgeID() = %q, want %q", got, ForgeGitHub)
	}
	if got := cfg.WorkflowForgeID(cfg.RawWorkflows[0]); got != ForgeGitHub {
		t.Errorf("a workflow naming no forge should talk to %q, got %q", ForgeGitHub, got)
	}
	s, ok := cfg.Forge(ForgeGitHub)
	if !ok || s.Kind != ForgeKindGitHub {
		t.Errorf("the built-in forge should be defined without a table, got %+v, %v", s, ok)
	}
	if got := cfg.ForgeIDs(); !slices.Equal(got, []string{ForgeGitHub}) {
		t.Errorf("ForgeIDs() = %v, want just the built-in forge", got)
	}
	if problems := validateForges(cfg); len(problems) != 0 {
		t.Errorf("a config that names no forge should validate, got %v", problems)
	}
}

func TestParseConfigReadsForges(t *testing.T) {
	cfg, err := parseConfig([]byte(`
DefaultForge = "work"

[Forges.work]
Kind = "github"

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "Work"
SectionTitle = "Work"

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "Open source"
SectionTitle = "Open source"
Forge = " github "
`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if got := cfg.DefaultForgeID(); got != "work" {
		t.Errorf("DefaultForgeID() = %q, want %q", got, "work")
	}
	if got := cfg.WorkflowForgeID(cfg.RawWorkflows[0]); got != "work" {
		t.Errorf("a workflow naming no forge should get DefaultForge, got %q", got)
	}
	if got := cfg.WorkflowForgeID(cfg.RawWorkflows[1]); got != ForgeGitHub {
		t.Errorf("a workflow's own Forge should win, trimmed, got %q", got)
	}
	if s, ok := cfg.Forge("work"); !ok || s.Kind != ForgeKindGitHub {
		t.Errorf("Forge(work) = %+v, %v", s, ok)
	}
	if _, ok := cfg.Forge("nowhere"); ok {
		t.Error("an undefined forge should not be found")
	}
	if got := cfg.ForgeIDs(); !slices.Equal(got, []string{ForgeGitHub, "work"}) {
		t.Errorf("ForgeIDs() = %v", got)
	}
	if problems := validateForges(cfg); len(problems) != 0 {
		t.Errorf("unexpected problems: %v", problems)
	}
}

func TestForgeKindDefaultsToTheTableID(t *testing.T) {
	cfg, err := parseConfig([]byte(`
[Forges.github]
`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if s, _ := cfg.Forge(ForgeGitHub); s.Kind != ForgeKindGitHub {
		t.Errorf("[Forges.github] without Kind should be of kind github, got %q", s.Kind)
	}
}

func TestValidateForges(t *testing.T) {
	tests := []struct {
		name  string
		toml  string
		field string
		want  string
	}{
		{
			name:  "default names no forge",
			toml:  `DefaultForge = "gitlab"`,
			field: "DefaultForge",
			want:  `there is no forge "gitlab"`,
		},
		{
			name: "workflow names no forge",
			toml: `
[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "Work"
SectionTitle = "Work"
Forge = "work"
`,
			field: "Workflows[0].Forge",
			want:  "define it in [Forges.work]",
		},
		{
			name: "a kind with no driver",
			toml: `
[Forges.gitlab]
`,
			field: "Forges.gitlab.Kind",
			want:  `unknown kind "gitlab"`,
		},
		{
			name: "the built-in forge of another kind",
			toml: `
[Forges.github]
Kind = "gitlab"
`,
			field: "Forges.github.Kind",
			want:  "unknown kind",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseConfig([]byte(tt.toml))
			if err != nil {
				t.Fatalf("parseConfig: %v", err)
			}
			problems := Validate(cfg)
			for _, p := range problems {
				if strings.HasPrefix(p.Error(), tt.field+":") && strings.Contains(p.Message, tt.want) {
					return
				}
			}
			t.Errorf("want a problem on %s containing %q, got %v", tt.field, tt.want, problems)
		})
	}
}

func TestUpdateRenderKeepsForges(t *testing.T) {
	// The config RPCs don't model the forges; saving anything else carries
	// them over, and a workflow's Forge rides along with the workflows.
	// Root keys first: after sampleConfig they would land in its last table.
	useTempConfig(t, `DefaultForge = "work"`+sampleConfig+`
[Forges.work]
Kind = "github"
`)
	workflows := []RawWorkflow{{
		WorkflowType: "SyncReviewRequestsWorkflow",
		Name:         "Open source",
		SectionTitle: "Open source",
		Forge:        ForgeGitHub,
	}}
	data, cfg, err := Update{Workflows: &workflows}.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if cfg.DefaultForge != "work" || cfg.Forges["work"].Kind != ForgeKindGitHub {
		t.Errorf("forge settings lost in the round trip: DefaultForge=%q Forges=%v", cfg.DefaultForge, cfg.Forges)
	}
	if got := cfg.RawWorkflows[0].Forge; got != ForgeGitHub {
		t.Errorf("the workflow's Forge was lost: %q", got)
	}
	// A workflow that names no forge is written without the key, rather than
	// having the default baked into it.
	none := []RawWorkflow{{WorkflowType: "SyncReviewRequestsWorkflow", Name: "W", SectionTitle: "W"}}
	data, _, err = Update{Workflows: &none}.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if regexp.MustCompile(`(?m)^\s*Forge\s*=`).Match(data) {
		t.Errorf("a workflow without a Forge should be written without one:\n%s", data)
	}
}
