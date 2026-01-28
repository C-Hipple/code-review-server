package config

import (
	"os"
	"path/filepath"
	"testing"
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

	// Verify DB was created in the correct location (subdir of XDG_CONFIG_HOME)
	dbPath := filepath.Join(tempDir, "codereviewserver", "codereviewserver.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("expected database to be created at %s, but it was not found", dbPath)
	}
}