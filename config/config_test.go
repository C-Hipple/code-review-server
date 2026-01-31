package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitialize_DuplicatePlugins(t *testing.T) {
	// Use a temporary directory for XDG_CONFIG_HOME to avoid touching real config
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	// Mock UserHomeDir to avoid accessing real home directory during migration check
	oldUserHomeDir := UserHomeDir
	defer func() { UserHomeDir = oldUserHomeDir }()
	UserHomeDir = func() (string, error) {
		return tempDir, nil
	}

	configPath := filepath.Join(tempDir, "codereviewserver.toml")

	content := `
[[Plugins]]
Name = "test-plugin"
Command = "echo 1"

[[Plugins]]
Name = "test-plugin"
Command = "echo 2"
`
	err := os.WriteFile(configPath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	err = Initialize()
	if err == nil {
		t.Errorf("expected error for duplicate plugin names, got nil")
	} else if err.Error() != "duplicate plugin name found: test-plugin" {
		t.Errorf("expected 'duplicate plugin name found: test-plugin', got '%v'", err)
	}
}

func TestInitialize_UniquePlugins(t *testing.T) {
	// Use a temporary directory for XDG_CONFIG_HOME to avoid touching real config
	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	// Mock UserHomeDir to avoid accessing real home directory during migration check
	oldUserHomeDir := UserHomeDir
	defer func() { UserHomeDir = oldUserHomeDir }()
	UserHomeDir = func() (string, error) {
		return tempDir, nil
	}

	configPath := filepath.Join(tempDir, "codereviewserver.toml")

	content := `
[[Plugins]]
Name = "plugin-1"
Command = "echo 1"

[[Plugins]]
Name = "plugin-2"
Command = "echo 2"
`
	err := os.WriteFile(configPath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	err = Initialize()
	if err != nil {
		t.Errorf("expected no error for unique plugin names, got %v", err)
	}

	// Verify DB was created in the correct location (subdir of HOME/.crs)
	dbPath := filepath.Join(tempDir, ".crs", "codereviewserver.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("expected database to be created at %s, but it was not found", dbPath)
	}
}

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    *Config
		wantErr bool
	}{
		{
			name: "Basic Config",
			content: `
GithubUsername = "user"
Repos = ["owner/repo"]
`,
			want: &Config{
				GithubUsername: "user",
				Repos:          []string{"owner/repo"},
				RepoLocation:   "~/",
				SleepDuration:  10 * time.Minute,
			},
			wantErr: false,
		},
		{
			name: "Section Priority",
			content: `
[SectionPriority]
"High Priority" = 10
"Low Priority" = 100
`,
			want: &Config{
				RepoLocation:  "~/",
				SleepDuration: 10 * time.Minute,
				SectionPriority: map[string]int{
					"High Priority": 10,
					"Low Priority":  100,
				},
			},
			wantErr: false,
		},
		{
			name: "Custom Sleep and Repo Location",
			content: `
RepoLocation = "/custom/path"
SleepDuration = 5
`,
			want: &Config{
				RepoLocation:  "/custom/path",
				SleepDuration: 5 * time.Minute,
			},
			wantErr: false,
		},
		{
			name: "Duplicate Plugins",
			content: `
[[Plugins]]
Name = "dup"
Command = "echo 1"

[[Plugins]]
Name = "dup"
Command = "echo 2"
`,
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseConfig([]byte(tt.content))
			if (err != nil) != tt.wantErr {
				t.Errorf("parseConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.GithubUsername != tt.want.GithubUsername {
					t.Errorf("GithubUsername = %v, want %v", got.GithubUsername, tt.want.GithubUsername)
				}
				if got.RepoLocation != tt.want.RepoLocation {
					t.Errorf("RepoLocation = %v, want %v", got.RepoLocation, tt.want.RepoLocation)
				}
				if got.SleepDuration != tt.want.SleepDuration {
					t.Errorf("SleepDuration = %v, want %v", got.SleepDuration, tt.want.SleepDuration)
				}
				if len(got.Repos) != len(tt.want.Repos) {
					t.Errorf("Repos length = %v, want %v", len(got.Repos), len(tt.want.Repos))
				}
				if len(got.SectionPriority) != len(tt.want.SectionPriority) {
					t.Errorf("SectionPriority length = %v, want %v", len(got.SectionPriority), len(tt.want.SectionPriority))
				}
				for k, v := range tt.want.SectionPriority {
					if got.SectionPriority[k] != v {
						t.Errorf("SectionPriority[%s] = %v, want %v", k, got.SectionPriority[k], v)
					}
				}
			}
		})
	}
}