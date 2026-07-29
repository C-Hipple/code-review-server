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
}
