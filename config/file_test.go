package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// useTempConfig points the config package at a throwaway config file and
// returns its path.
func useTempConfig(t *testing.T, contents string) string {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	oldUserHomeDir := UserHomeDir
	t.Cleanup(func() { UserHomeDir = oldUserHomeDir })
	UserHomeDir = func() (string, error) { return tempDir, nil }

	path := filepath.Join(tempDir, configFileName)
	if contents != "" {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatalf("failed to write test config: %v", err)
		}
	}
	return path
}

const sampleConfig = `
Repos = ["owner/repo"]
SleepDuration = 5
GithubUsername = "me"
UnknownFutureKey = "keep me"

[SectionPriority]
"My PRs" = 10

[[Plugins]]
Name = "Summarize"
Command = "summarize_diff"
IncludeDiff = true

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "My Open PRs"
Filters = ["FilterNotDraft"]
SectionTitle = "My PRs"
`

func TestUpdateRenderReplacesWorkflowsAndKeepsEverythingElse(t *testing.T) {
	useTempConfig(t, sampleConfig)

	newWorkflows := []RawWorkflow{{
		WorkflowType: "SyncReviewRequestsWorkflow",
		Name:         "Team Reviews",
		SectionTitle: "Needs Review",
		Filters:      []string{"FilterNotDraft", "FilterByLabel:bug"},
		Teams:        []string{"my-team"},
	}}
	data, cfg, err := Update{Workflows: &newWorkflows}.Render()
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	if len(cfg.RawWorkflows) != 1 || cfg.RawWorkflows[0].Name != "Team Reviews" {
		t.Errorf("expected the rendered config to hold only the new workflow, got %+v", cfg.RawWorkflows)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0] != "owner/repo" {
		t.Errorf("Repos should be untouched, got %v", cfg.Repos)
	}
	if cfg.SleepDuration != 5*time.Minute {
		t.Errorf("SleepDuration should be untouched, got %v", cfg.SleepDuration)
	}
	if len(cfg.Plugins) != 1 || cfg.Plugins[0].Name != "Summarize" {
		t.Errorf("Plugins should be untouched, got %+v", cfg.Plugins)
	}
	if cfg.SectionPriority["My PRs"] != 10 {
		t.Errorf("SectionPriority should be untouched, got %v", cfg.SectionPriority)
	}
	if !strings.Contains(string(data), "UnknownFutureKey") {
		t.Errorf("keys the server doesn't model should survive a write, got:\n%s", data)
	}
	// Unset workflow fields shouldn't be written out as empty values.
	if strings.Contains(string(data), "JiraEpic") {
		t.Errorf("empty workflow fields should be omitted, got:\n%s", data)
	}
}

func TestUpdateRenderDropsInheritedGithubUsername(t *testing.T) {
	useTempConfig(t, sampleConfig)

	// parseConfig copies the root GithubUsername into every workflow, which is
	// what a client reads back before submitting an edit.
	parsed, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if parsed.RawWorkflows[0].GithubUsername != "me" {
		t.Fatalf("expected the parsed workflow to inherit the root username, got %q", parsed.RawWorkflows[0].GithubUsername)
	}

	data, _, err := Update{Workflows: &parsed.RawWorkflows}.Render()
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if strings.Count(string(data), "GithubUsername") != 1 {
		t.Errorf("the inherited username should not be written onto the workflow, got:\n%s", data)
	}
}

func TestUpdateRenderCreatesMissingConfigFile(t *testing.T) {
	useTempConfig(t, "")

	repos := []string{"owner/repo"}
	_, cfg, err := Update{Repos: &repos}.Render()
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0] != "owner/repo" {
		t.Errorf("expected the new config to hold the submitted repos, got %v", cfg.Repos)
	}
}

func TestApplyWritesReloadsAndBacksUp(t *testing.T) {
	path := useTempConfig(t, sampleConfig)
	if err := Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}

	sleep := 30
	problems, err := Apply(Update{SleepDuration: &sleep}, nil)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("expected no validation problems, got %v", problems)
	}

	if C().SleepDuration != 30*time.Minute {
		t.Errorf("Apply should reload the running config, got %v", C().SleepDuration)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written config: %v", err)
	}
	if !strings.Contains(string(written), "SleepDuration = 30") {
		t.Errorf("expected the new SleepDuration on disk, got:\n%s", written)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("expected a backup of the previous config: %v", err)
	}
	if !strings.Contains(string(backup), "SleepDuration = 5") {
		t.Errorf("expected the backup to hold the previous config, got:\n%s", backup)
	}
}

func TestApplyLeavesFileAloneWhenValidationFails(t *testing.T) {
	path := useTempConfig(t, sampleConfig)

	bad := []RawWorkflow{{WorkflowType: "SyncReviewRequestsWorkflow", SectionTitle: "Section"}}
	problems, err := Apply(Update{Workflows: &bad}, Validate)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(problems) == 0 {
		t.Fatal("expected the nameless workflow to be rejected")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if string(after) != sampleConfig {
		t.Errorf("a rejected update must not touch the file, got:\n%s", after)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("a rejected update must not write a backup either")
	}
}

func TestUpdateIsEmpty(t *testing.T) {
	if !(Update{}).IsEmpty() {
		t.Error("an update with no fields set should be empty")
	}
	repos := []string{}
	if (Update{Repos: &repos}).IsEmpty() {
		t.Error("clearing a list is still a change")
	}
	plugins := []Plugin{}
	if (Update{Plugins: &plugins}).IsEmpty() {
		t.Error("clearing the plugins is still a change")
	}
	if (Update{AI: &AISettings{}}).IsEmpty() {
		t.Error("clearing [AI] is still a change")
	}
	features := []AIFeature{}
	if (Update{AIFeatures: &features}).IsEmpty() {
		t.Error("clearing the AI features is still a change")
	}
}

func TestUpdateRenderReplacesPluginsAndAI(t *testing.T) {
	useTempConfig(t, sampleConfig)

	plugins := []Plugin{
		{Name: "Summarize", Command: "summarize_diff", IncludeDiff: true, Provider: "openrouter", Model: "anthropic/claude-sonnet-4.5"},
		{Name: "Style", Command: "style_guidelines", IncludeDiff: true, IncludeHeaders: true, OnlyOnDemand: true},
	}
	ai := AISettings{DefaultCommand: "claude -p"}
	features := []AIFeature{
		{ID: "comments-addressed", Enabled: true, Automatic: true, Mode: "agent"},
		{ID: "review-ease", Enabled: false},
	}
	data, cfg, err := Update{Plugins: &plugins, AI: &ai, AIFeatures: &features}.Render()
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	if len(cfg.Plugins) != 2 || cfg.Plugins[0] != plugins[0] || cfg.Plugins[1] != plugins[1] {
		t.Errorf("plugins did not round-trip: %+v", cfg.Plugins)
	}
	if cfg.AI != ai {
		t.Errorf("[AI] did not round-trip: %+v", cfg.AI)
	}
	if len(cfg.AIFeatures) != 2 || cfg.AIFeatures[0] != features[0] || cfg.AIFeatures[1] != features[1] {
		t.Errorf("[[AIFeatures]] did not round-trip: %+v", cfg.AIFeatures)
	}
	if len(cfg.RawWorkflows) != 1 || cfg.SleepDuration != 5*time.Minute {
		t.Errorf("settings the update leaves alone should be untouched: %+v", cfg)
	}

	written := string(data)
	// Unset options stay out of the file, as they do for workflows...
	for _, unset := range []string{"IncludeComments", "IncludeBranch", "DefaultProvider", "DefaultModel", "Command = ''"} {
		if strings.Contains(written, unset) {
			t.Errorf("unset %s should be omitted, got:\n%s", unset, written)
		}
	}
	// ...but Enabled is written either way: an entry switched off reads plainer
	// saying so.
	if strings.Count(written, "Enabled = ") != 2 {
		t.Errorf("expected both entries to say whether they are enabled, got:\n%s", written)
	}
}

func TestUpdateRenderRemovesEmptiedPluginsAndAI(t *testing.T) {
	useTempConfig(t, sampleConfig+`
[AI]
DefaultCommand = "claude -p"

[[AIFeatures]]
ID = "comments-addressed"
Enabled = true
`)
	plugins := []Plugin{}
	features := []AIFeature{}
	data, cfg, err := Update{Plugins: &plugins, AI: &AISettings{}, AIFeatures: &features}.Render()
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(cfg.Plugins) != 0 || cfg.AI != (AISettings{}) || len(cfg.AIFeatures) != 0 {
		t.Errorf("expected plugins and AI settings cleared, got %+v %+v %+v", cfg.Plugins, cfg.AI, cfg.AIFeatures)
	}
	for _, key := range []string{"Plugins", "[AI]", "AIFeatures"} {
		if strings.Contains(string(data), key) {
			t.Errorf("an emptied %s should leave the file, got:\n%s", key, data)
		}
	}
}

func TestApplyReportsDuplicatePluginsAndAIFeatures(t *testing.T) {
	path := useTempConfig(t, sampleConfig)

	plugins := []Plugin{{Name: "Summarize", Command: "a"}, {Name: "Summarize", Command: "b"}}
	features := []AIFeature{{ID: "review-ease"}, {ID: "file-ordering"}, {ID: "review-ease", Enabled: true}}
	problems, err := Apply(Update{Plugins: &plugins, AIFeatures: &features}, Validate)
	if err != nil {
		t.Fatalf("duplicates should be validation problems, not an error: %v", err)
	}
	fields := map[string]bool{}
	for _, p := range problems {
		fields[p.Field] = true
	}
	if !fields["Plugins[1].Name"] || !fields["AIFeatures[2].ID"] || len(problems) != 2 {
		t.Errorf("expected the second Summarize and the second review-ease reported, got %v", problems)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if string(after) != sampleConfig {
		t.Errorf("a rejected update must not touch the file, got:\n%s", after)
	}
}
