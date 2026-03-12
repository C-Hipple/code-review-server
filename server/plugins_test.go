package server

import (
	"crs/config"
	"crs/database"
	"os"
	"path/filepath"
	"testing"
)

func setupTestDB(t *testing.T) *database.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := database.NewDB(dbPath)
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	return db
}

func TestRunPlugins_SkipsOnlyOnDemandPlugins(t *testing.T) {
	db := setupTestDB(t)
	// Create a script that writes a marker file when executed
	dir := t.TempDir()
	normalMarker := filepath.Join(dir, "normal_ran")
	onDemandMarker := filepath.Join(dir, "ondemand_ran")

	normalScript := filepath.Join(dir, "normal.sh")
	os.WriteFile(normalScript, []byte("#!/bin/sh\ntouch "+normalMarker), 0755)
	onDemandScript := filepath.Join(dir, "ondemand.sh")
	os.WriteFile(onDemandScript, []byte("#!/bin/sh\ntouch "+onDemandMarker), 0755)

	config.SetC(config.Config{
		DB: db,
		Plugins: []config.Plugin{
			{Name: "normal-plugin", Command: normalScript},
			{Name: "expensive-plugin", Command: onDemandScript, OnlyOnDemand: true},
		},
	})

	// Normal run (no specific plugins requested) should skip on-demand plugins
	RunPlugins("owner", "repo", 1, "sha123", "", "", "")

	if _, err := os.Stat(normalMarker); os.IsNotExist(err) {
		t.Error("expected normal plugin to run, but it did not")
	}
	if _, err := os.Stat(onDemandMarker); err == nil {
		t.Error("expected on-demand plugin to be skipped, but it ran")
	}

	// Check that the on-demand plugin got a "deferred" status in the DB
	results, err := db.GetPluginResults("owner", "repo", 1)
	if err != nil {
		t.Fatalf("failed to get plugin results: %v", err)
	}
	if r, ok := results["expensive-plugin"]; !ok {
		t.Error("expected deferred result for expensive-plugin, but none found")
	} else if r.Status != "deferred" {
		t.Errorf("expected status 'deferred' for expensive-plugin, got %q", r.Status)
	}
}

func TestRunPluginsForce_RunsOnDemandPluginWhenExplicitlyRequested(t *testing.T) {
	db := setupTestDB(t)
	dir := t.TempDir()
	onDemandMarker := filepath.Join(dir, "ondemand_ran")

	onDemandScript := filepath.Join(dir, "ondemand.sh")
	os.WriteFile(onDemandScript, []byte("#!/bin/sh\ntouch "+onDemandMarker), 0755)

	config.SetC(config.Config{
		DB: db,
		Plugins: []config.Plugin{
			{Name: "expensive-plugin", Command: onDemandScript, OnlyOnDemand: true},
		},
	})

	// Explicitly request the on-demand plugin by name
	RunPluginsForce("owner", "repo", 1, "sha123", "", "", "", true, []string{"expensive-plugin"})

	if _, err := os.Stat(onDemandMarker); os.IsNotExist(err) {
		t.Error("expected on-demand plugin to run when explicitly requested, but it did not")
	}
}

func TestRunPluginsForce_EmptyListSkipsOnDemandPlugins(t *testing.T) {
	db := setupTestDB(t)
	dir := t.TempDir()
	normalMarker := filepath.Join(dir, "normal_ran")
	onDemandMarker := filepath.Join(dir, "ondemand_ran")

	normalScript := filepath.Join(dir, "normal.sh")
	os.WriteFile(normalScript, []byte("#!/bin/sh\ntouch "+normalMarker), 0755)
	onDemandScript := filepath.Join(dir, "ondemand.sh")
	os.WriteFile(onDemandScript, []byte("#!/bin/sh\ntouch "+onDemandMarker), 0755)

	config.SetC(config.Config{
		DB: db,
		Plugins: []config.Plugin{
			{Name: "normal-plugin", Command: normalScript},
			{Name: "expensive-plugin", Command: onDemandScript, OnlyOnDemand: true},
		},
	})

	// Force rerun with empty plugin list should still skip on-demand plugins
	RunPluginsForce("owner", "repo", 1, "sha123", "", "", "", true, nil)

	if _, err := os.Stat(normalMarker); os.IsNotExist(err) {
		t.Error("expected normal plugin to run, but it did not")
	}
	if _, err := os.Stat(onDemandMarker); err == nil {
		t.Error("expected on-demand plugin to be skipped on empty rerun, but it ran")
	}
}

func TestParseConfig_OnlyOnDemandDefaultsFalse(t *testing.T) {
	content := `
[[Plugins]]
Name = "basic-plugin"
Command = "echo 1"

[[Plugins]]
Name = "expensive-plugin"
Command = "echo 2"
OnlyOnDemand = true
`
	cfg, err := config.ParseConfigForTest([]byte(content))
	if err != nil {
		t.Fatalf("parseConfig() error: %v", err)
	}
	if len(cfg.Plugins) != 2 {
		t.Fatalf("expected 2 plugins, got %d", len(cfg.Plugins))
	}
	if cfg.Plugins[0].OnlyOnDemand {
		t.Error("expected basic-plugin OnlyOnDemand to default to false")
	}
	if !cfg.Plugins[1].OnlyOnDemand {
		t.Error("expected expensive-plugin OnlyOnDemand to be true")
	}
}
