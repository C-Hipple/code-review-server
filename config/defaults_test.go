package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// isolateConfigHome points config lookups at a temp directory so a test never
// reads (or writes) the developer's real ~/.config.
func isolateConfigHome(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)
	t.Setenv("CRS_HOME", tempDir)
	oldUserHomeDir := UserHomeDir
	t.Cleanup(func() { UserHomeDir = oldUserHomeDir })
	UserHomeDir = func() (string, error) { return tempDir, nil }
	return tempDir
}

func TestDefaultConfigParses(t *testing.T) {
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("the built-in default config does not parse: %v", err)
	}

	if !cfg.UsingDefaults {
		t.Error("DefaultConfig should flag itself as defaults")
	}
	if cfg.SleepDuration != 10*time.Minute {
		t.Errorf("SleepDuration = %v, want 10m", cfg.SleepDuration)
	}
	if len(cfg.Repos) != 0 {
		t.Errorf("defaults should need no Repos, got %v", cfg.Repos)
	}

	if len(cfg.RawWorkflows) != 2 {
		t.Fatalf("expected 2 default workflows, got %d", len(cfg.RawWorkflows))
	}
	want := []struct{ workflowType, section string }{
		{"WaitingOnMeWorkflow", "Waiting On Me"},
		{"MyReviewRequestsWorkflow", "Review Requested"},
	}
	for i, w := range want {
		got := cfg.RawWorkflows[i]
		if got.WorkflowType != w.workflowType {
			t.Errorf("workflow %d type = %q, want %q", i, got.WorkflowType, w.workflowType)
		}
		if got.SectionTitle != w.section {
			t.Errorf("workflow %d section = %q, want %q", i, got.SectionTitle, w.section)
		}
		if got.Name == "" {
			t.Errorf("workflow %d has no Name", i)
		}
	}

	// Both sections are ordered, so a first run doesn't get an arbitrary one.
	for _, section := range []string{"Waiting On Me", "Review Requested"} {
		if _, ok := cfg.SectionPriority[section]; !ok {
			t.Errorf("SectionPriority has no entry for %q", section)
		}
	}
}

func TestDefaultConfigPassesRootValidation(t *testing.T) {
	cfg, err := DefaultConfig()
	if err != nil {
		t.Fatalf("DefaultConfig: %v", err)
	}
	// workflows.ValidateWorkflows covers the type/filter registries and runs in
	// that package's tests; this is the half config owns.
	if problems := Validate(cfg); len(problems) > 0 {
		t.Errorf("the default config must validate cleanly, got %v", problems)
	}
}

func TestLoadConfigFallsBackToDefaultsWhenFileMissing(t *testing.T) {
	isolateConfigHome(t)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("a missing config file must not be an error, got %v", err)
	}
	if !cfg.UsingDefaults {
		t.Error("expected the defaults to be flagged as such")
	}
	if len(cfg.RawWorkflows) != 2 {
		t.Errorf("expected the 2 default workflows, got %d", len(cfg.RawWorkflows))
	}
}

func TestLoadConfigStillFailsOnUnparseableFile(t *testing.T) {
	tempDir := isolateConfigHome(t)
	path := filepath.Join(tempDir, "codereviewserver.toml")
	if err := os.WriteFile(path, []byte("this is not = = toml"), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	if _, err := loadConfig(); err == nil {
		t.Error("a malformed config file must still be reported, not silently replaced by defaults")
	}
}

func TestLoadConfigUsesFileWhenPresent(t *testing.T) {
	tempDir := isolateConfigHome(t)
	path := filepath.Join(tempDir, "codereviewserver.toml")
	content := `Repos = ["owner/repo"]

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "mine"
SectionTitle = "Mine"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.UsingDefaults {
		t.Error("a config file exists; UsingDefaults should be false")
	}
	if len(cfg.RawWorkflows) != 1 || cfg.RawWorkflows[0].Name != "mine" {
		t.Errorf("expected the file's own workflow, got %+v", cfg.RawWorkflows)
	}
}

// Editing one setting from a client while the server is running on defaults
// must not throw the default workflows away: the first save writes out the
// configuration the user was actually looking at.
func TestRenderSeedsTheDefaultsWhenNoFileExists(t *testing.T) {
	isolateConfigHome(t)

	sleep := 15
	_, cfg, err := Update{SleepDuration: &sleep}.Render()
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if cfg.SleepDuration != 15*time.Minute {
		t.Errorf("SleepDuration = %v, want the update's 15m", cfg.SleepDuration)
	}
	if len(cfg.RawWorkflows) != 2 {
		t.Fatalf("the default workflows must survive the first save, got %d", len(cfg.RawWorkflows))
	}
	if cfg.RawWorkflows[0].WorkflowType != "WaitingOnMeWorkflow" {
		t.Errorf("unexpected first workflow %+v", cfg.RawWorkflows[0])
	}
	if len(cfg.SectionPriority) != 2 {
		t.Errorf("section ordering should survive too, got %v", cfg.SectionPriority)
	}
}

func TestLoginResolverFillsMissingUsername(t *testing.T) {
	old := LoginResolver
	t.Cleanup(func() { LoginResolver = old })
	LoginResolver = func() string { return "resolved-user" }

	cfg, err := parseConfig([]byte(`
[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "mine"
SectionTitle = "Mine"
`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.GithubUsername != "resolved-user" {
		t.Errorf("GithubUsername = %q, want the resolved login", cfg.GithubUsername)
	}
	// The workflow-level copy has to see the resolved value too, since that is
	// what the identity filters are built from.
	if cfg.RawWorkflows[0].GithubUsername != "resolved-user" {
		t.Errorf("workflow GithubUsername = %q, want the resolved login", cfg.RawWorkflows[0].GithubUsername)
	}
}

func TestLoginResolverDoesNotOverrideConfiguredUsername(t *testing.T) {
	old := LoginResolver
	t.Cleanup(func() { LoginResolver = old })
	LoginResolver = func() string { return "resolved-user" }

	cfg, err := parseConfig([]byte("GithubUsername = \"configured-user\"\n"))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.GithubUsername != "configured-user" {
		t.Errorf("GithubUsername = %q, want the configured value to win", cfg.GithubUsername)
	}
}

func TestParseConfigWithoutResolverLeavesUsernameEmpty(t *testing.T) {
	old := LoginResolver
	t.Cleanup(func() { LoginResolver = old })
	LoginResolver = nil

	cfg, err := parseConfig([]byte("Repos = [\"owner/repo\"]\n"))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.GithubUsername != "" {
		t.Errorf("GithubUsername = %q, want empty when nothing can resolve it", cfg.GithubUsername)
	}
}
