package server

import (
	"crs/config"
	"crs/database"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	RunPlugins("owner", "repo", 1, "sha123", "", "", "", "main")

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
	RunPluginsForce("owner", "repo", 1, "sha123", "", "", "", "feature-branch", true, []string{"expensive-plugin"})

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
	RunPluginsForce("owner", "repo", 1, "sha123", "", "", "", "main", true, nil)

	if _, err := os.Stat(normalMarker); os.IsNotExist(err) {
		t.Error("expected normal plugin to run, but it did not")
	}
	if _, err := os.Stat(onDemandMarker); err == nil {
		t.Error("expected on-demand plugin to be skipped on empty rerun, but it ran")
	}
}

func TestCallTypeFor(t *testing.T) {
	tests := []struct {
		name   string
		plugin config.Plugin
		force  bool
		want   PluginCallType
	}{
		{"normal plugin, unforced", config.Plugin{Name: "p"}, false, PluginCallAutomatic},
		{"normal plugin, forced", config.Plugin{Name: "p"}, true, PluginCallRerun},
		{"deferred plugin, forced", config.Plugin{Name: "p", OnlyOnDemand: true}, true, PluginCallExplicit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := callTypeFor(tt.plugin, tt.force); got != tt.want {
				t.Errorf("callTypeFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

// argRecorderScript writes a plugin invocation's arguments to argsPath so a
// test can assert on the flags the server passed.
func argRecorderScript(t *testing.T, dir, name, argsPath string) string {
	t.Helper()
	script := filepath.Join(dir, name)
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" > "+argsPath+"\n"), 0755); err != nil {
		t.Fatalf("failed to write plugin script: %v", err)
	}
	return script
}

func recordedArgs(t *testing.T, argsPath string) string {
	t.Helper()
	got, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("plugin did not record its arguments: %v", err)
	}
	return string(got)
}

func TestRunPlugins_PassesAutomaticCallType(t *testing.T) {
	db := setupTestDB(t)
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := argRecorderScript(t, dir, "normal.sh", argsPath)

	config.SetC(config.Config{
		DB:      db,
		Plugins: []config.Plugin{{Name: "normal-plugin", Command: script}},
	})

	RunPlugins("owner", "repo", 1, "sha123", "", "", "", "main")

	if args := recordedArgs(t, argsPath); !strings.Contains(args, "--call-type automatic") {
		t.Errorf("expected --call-type automatic in plugin args, got %q", args)
	}
}

func TestRunPluginsForce_PassesRerunCallType(t *testing.T) {
	db := setupTestDB(t)
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := argRecorderScript(t, dir, "normal.sh", argsPath)

	config.SetC(config.Config{
		DB:      db,
		Plugins: []config.Plugin{{Name: "normal-plugin", Command: script}},
	})

	RunPluginsForce("owner", "repo", 1, "sha123", "", "", "", "main", true, []string{"normal-plugin"})

	if args := recordedArgs(t, argsPath); !strings.Contains(args, "--call-type rerun") {
		t.Errorf("expected --call-type rerun in plugin args, got %q", args)
	}
}

func TestRunPluginsForce_PassesExplicitCallTypeForDeferredPlugin(t *testing.T) {
	db := setupTestDB(t)
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := argRecorderScript(t, dir, "ondemand.sh", argsPath)

	config.SetC(config.Config{
		DB:      db,
		Plugins: []config.Plugin{{Name: "expensive-plugin", Command: script, OnlyOnDemand: true}},
	})

	RunPluginsForce("owner", "repo", 1, "sha123", "", "", "", "feature-branch", true, []string{"expensive-plugin"})

	if args := recordedArgs(t, argsPath); !strings.Contains(args, "--call-type explicit") {
		t.Errorf("expected --call-type explicit in plugin args, got %q", args)
	}
}

// fillAutomaticPluginSlots takes every automatic-run slot for the duration of
// the test, simulating a background fan-out already saturating the cap.
func fillAutomaticPluginSlots(t *testing.T) {
	t.Helper()
	for i := 0; i < maxAutomaticPluginProcs; i++ {
		automaticPluginSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < maxAutomaticPluginProcs; i++ {
			<-automaticPluginSlots
		}
	})
}

func TestRunPluginsForce_ExplicitRunIsNotQueuedBehindAutomaticRuns(t *testing.T) {
	// A rerun someone asked for has a person waiting on it, so it must not
	// wait for the automatic background runs to drain.
	db := setupTestDB(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "plugin.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker), 0755); err != nil {
		t.Fatalf("failed to write plugin script: %v", err)
	}

	config.SetC(config.Config{
		DB:      db,
		Plugins: []config.Plugin{{Name: "normal-plugin", Command: script}},
	})

	fillAutomaticPluginSlots(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		RunPluginsForce("owner", "repo", 1, "sha123", "", "", "", "main", true, []string{"normal-plugin"})
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("explicit plugin rerun blocked behind the automatic-run cap")
	}

	if _, err := os.Stat(marker); os.IsNotExist(err) {
		t.Error("expected the explicitly requested plugin to run")
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
