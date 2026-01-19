package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitialize_DuplicatePlugins(t *testing.T) {
	home_dir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home directory: %v", err)
	}
	configDir := filepath.Join(home_dir, ".config")
	err = os.MkdirAll(configDir, 0755)
	if err != nil {
		t.Fatalf("failed to create config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "codereviewserver.toml")

	// Backup existing config if it exists
	var backupPath string
	if _, err := os.Stat(configPath); err == nil {
		backupPath = configPath + ".bak"
		err = os.Rename(configPath, backupPath)
		if err != nil {
			t.Fatalf("failed to backup config: %v", err)
		}
		defer os.Rename(backupPath, configPath)
	}

	content := `
[[Plugins]]
Name = "test-plugin"
Command = "echo 1"

[[Plugins]]
Name = "test-plugin"
Command = "echo 2"
`
	err = os.WriteFile(configPath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	defer os.Remove(configPath)

	err = Initialize()
	if err == nil {
		t.Errorf("expected error for duplicate plugin names, got nil")
	} else if err.Error() != "duplicate plugin name found: test-plugin" {
		t.Errorf("expected 'duplicate plugin name found: test-plugin', got '%v'", err)
	}
}

func TestInitialize_UniquePlugins(t *testing.T) {
	home_dir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("failed to get home directory: %v", err)
	}
	configDir := filepath.Join(home_dir, ".config")
	err = os.MkdirAll(configDir, 0755)
	if err != nil {
		t.Fatalf("failed to create config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "codereviewserver.toml")

	// Backup existing config if it exists
	var backupPath string
	if _, err := os.Stat(configPath); err == nil {
		backupPath = configPath + ".bak"
		err = os.Rename(configPath, backupPath)
		if err != nil {
			t.Fatalf("failed to backup config: %v", err)
		}
		defer os.Rename(backupPath, configPath)
	}

	content := `
[[Plugins]]
Name = "plugin-1"
Command = "echo 1"

[[Plugins]]
Name = "plugin-2"
Command = "echo 2"
`
	err = os.WriteFile(configPath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}
	defer os.Remove(configPath)

	err = Initialize()
	if err != nil {
		t.Errorf("expected no error for unique plugin names, got %v", err)
	}
}
