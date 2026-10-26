package server

import (
	"crs/config"
	"crs/database"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestRunPlugins_PassesTheLLMBackendInTheEnvironment(t *testing.T) {
	db := setupTestDB(t)
	dir := t.TempDir()
	// A backend exported to the server reaches a plugin that sets none of its
	// own; a plugin's Provider and Model replace it.
	t.Setenv(config.PluginProviderEnv, "gemini")
	t.Setenv(config.PluginModelEnv, "")
	record := func(name string) (string, string) {
		out := filepath.Join(dir, name+".env")
		script := filepath.Join(dir, name+".sh")
		body := "#!/bin/sh\nprintf '%s|%s' \"$" + config.PluginProviderEnv + "\" \"$" + config.PluginModelEnv + "\" > " + out + "\n"
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		return script, out
	}
	plainScript, plainOut := record("plain")
	routedScript, routedOut := record("routed")

	config.SetC(config.Config{
		DB: db,
		Plugins: []config.Plugin{
			{Name: "plain", Command: plainScript},
			{Name: "routed", Command: routedScript, Provider: "openrouter", Model: "anthropic/claude-sonnet-4.5"},
		},
	})

	RunPlugins("owner", "repo", 1, "sha123", "", "", "", "main")

	if got := recordedArgs(t, plainOut); got != "gemini|" {
		t.Errorf("plain plugin saw %q, want the server's environment", got)
	}
	if got := recordedArgs(t, routedOut); got != "openrouter|anthropic/claude-sonnet-4.5" {
		t.Errorf("routed plugin saw %q", got)
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

func TestRunPlugins_StoresAFailedRunWithItsOutput(t *testing.T) {
	// Plugins run through the shared subprocess helper; a failure must still
	// be stored with everything the plugin wrote, so it can be diagnosed.
	db := setupTestDB(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "broken.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho partial\necho boom >&2\nexit 3\n"), 0755); err != nil {
		t.Fatalf("failed to write plugin script: %v", err)
	}
	config.SetC(config.Config{
		DB:      db,
		Plugins: []config.Plugin{{Name: "broken", Command: script}},
	})

	RunPlugins("owner", "repo", 1, "sha123", "", "", "", "main")

	results, err := db.GetPluginResults("owner", "repo", 1)
	if err != nil {
		t.Fatalf("failed to get plugin results: %v", err)
	}
	got := results["broken"]
	if got.Status != "error" {
		t.Fatalf("status = %q, want error", got.Status)
	}
	for _, want := range []string{"exit status 3", "Stderr: boom", "Stdout: partial"} {
		if !strings.Contains(got.Result, want) {
			t.Errorf("stored result missing %q:\n%s", want, got.Result)
		}
	}
}

// subscribedNotifier stands a subscribed notifier in for the stdio
// connection's for the rest of the test, and returns what it writes.
func subscribedNotifier(t *testing.T) <-chan string {
	t.Helper()
	w, out := lines(t)
	saved := notifier
	notifier = newNotifier(w)
	notifier.Subscribe(time.Hour)
	t.Cleanup(func() {
		notifier.Unsubscribe()
		notifier = saved
	})
	return out
}

// pluginStatuses collects the Server.PluginOutputChanged notifications for
// owner/repo#1 written until the output has been quiet for `quiet`, as each
// plugin's statuses in order.
func pluginStatuses(t *testing.T, out <-chan string, quiet time.Duration) map[string][]string {
	t.Helper()
	return prPluginStatuses(t, out, quiet, "owner", "repo", 1)
}

func prPluginStatuses(t *testing.T, out <-chan string, quiet time.Duration, owner, repo string, number int) map[string][]string {
	t.Helper()
	got := map[string][]string{}
	for {
		var line string
		select {
		case line = <-out:
		case <-time.After(quiet):
			return got
		}
		var n struct {
			Method string                `json:"method"`
			Params []PluginOutputChanged `json:"params"`
			ID     any                   `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &n); err != nil {
			t.Fatalf("not JSON: %s: %v", line, err)
		}
		if n.Method != PluginOutputChangedMethod || n.ID != nil || len(n.Params) != 1 {
			t.Fatalf("not a %s notification: %s", PluginOutputChangedMethod, line)
		}
		p := n.Params[0]
		if p.Owner != owner || p.Repo != repo || p.Number != number {
			t.Errorf("notification names %s/%s#%d, want %s/%s#%d", p.Owner, p.Repo, p.Number, owner, repo, number)
		}
		got[p.Plugin] = append(got[p.Plugin], p.Status)
	}
}

func TestRunPlugins_AnnouncesEachResultItStores(t *testing.T) {
	db := setupTestDB(t)
	dir := t.TempDir()
	okScript := filepath.Join(dir, "ok.sh")
	os.WriteFile(okScript, []byte("#!/bin/sh\necho fine\n"), 0755)
	brokenScript := filepath.Join(dir, "broken.sh")
	os.WriteFile(brokenScript, []byte("#!/bin/sh\nexit 3\n"), 0755)
	config.SetC(config.Config{
		DB: db,
		Plugins: []config.Plugin{
			{Name: "ok", Command: okScript},
			{Name: "broken", Command: brokenScript},
			{Name: "expensive", Command: okScript, OnlyOnDemand: true},
		},
	})
	out := subscribedNotifier(t)

	RunPlugins("owner", "repo", 1, "sha123", "", "", "", "main")

	got := pluginStatuses(t, out, 100*time.Millisecond)
	want := map[string][]string{
		"ok":        {"pending", "success"},
		"broken":    {"pending", "error"},
		"expensive": {"deferred"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("announced %v, want %v", got, want)
	}

	// Each announcement follows its write: what it says is what a client
	// reading the output now sees.
	results, err := db.GetPluginResults("owner", "repo", 1)
	if err != nil {
		t.Fatalf("failed to get plugin results: %v", err)
	}
	for name, statuses := range want {
		if last := statuses[len(statuses)-1]; results[name].Status != last {
			t.Errorf("%s is stored %q, announced %q", name, results[name].Status, last)
		}
	}
}

func TestRunPlugins_AnnouncesNothingWhenNoRunIsNeeded(t *testing.T) {
	db := setupTestDB(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "ok.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho fine\n"), 0755)
	config.SetC(config.Config{
		DB:      db,
		Plugins: []config.Plugin{{Name: "ok", Command: script}},
	})
	if err := db.UpsertPluginResult("owner", "repo", 1, "ok", "fine", "success", "sha123"); err != nil {
		t.Fatal(err)
	}
	out := subscribedNotifier(t)

	// The stored result already covers this head, so nothing is written and
	// a client has nothing to read again.
	RunPlugins("owner", "repo", 1, "sha123", "", "", "", "main")

	if got := pluginStatuses(t, out, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("announced %v for a run that was skipped", got)
	}
}

func TestRunPluginsForce_AnnouncesTheRerun(t *testing.T) {
	db := setupTestDB(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "ok.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho again\n"), 0755)
	config.SetC(config.Config{
		DB:      db,
		Plugins: []config.Plugin{{Name: "ok", Command: script}},
	})
	if err := db.UpsertPluginResult("owner", "repo", 1, "ok", "fine", "success", "sha123"); err != nil {
		t.Fatal(err)
	}
	out := subscribedNotifier(t)

	RunPluginsForce("owner", "repo", 1, "sha123", "", "", "", "main", true, []string{"ok"})

	want := map[string][]string{"ok": {"pending", "success"}}
	if got := pluginStatuses(t, out, 100*time.Millisecond); !reflect.DeepEqual(got, want) {
		t.Errorf("announced %v, want %v", got, want)
	}
}

func TestRerunPlugins_AnnouncesTheClearedResultThenTheRun(t *testing.T) {
	tests := []struct {
		name    string
		plugins []string
		want    map[string][]string
	}{
		{
			name:    "one plugin",
			plugins: []string{"ok"},
			want:    map[string][]string{"ok": {"", "pending", "success"}},
		},
		{
			// Every result is cleared at once, then each plugin is announced
			// as its rerun goes.
			name: "every plugin",
			want: map[string][]string{
				"":          {""},
				"ok":        {"pending", "success"},
				"expensive": {"deferred"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CRS_HOME", t.TempDir())
			t.Setenv("CRS_GITHUB_TOKEN", "test-token")
			db := setupTestDB(t)
			script := filepath.Join(t.TempDir(), "ok.sh")
			os.WriteFile(script, []byte("#!/bin/sh\necho again\n"), 0755)
			config.SetC(config.Config{
				DB:            db,
				SleepDuration: 10 * time.Minute,
				Plugins: []config.Plugin{
					{Name: "ok", Command: script},
					{Name: "expensive", Command: script, OnlyOnDemand: true},
				},
			})
			t.Cleanup(func() { config.SetC(config.Config{}) })
			// The rerun reads its inputs from the caches, never GitHub.
			seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
			if err := db.UpsertPluginResult(aiOwner, aiRepo, aiNumber, "ok", "fine", "success", "sha-1"); err != nil {
				t.Fatal(err)
			}
			out := subscribedNotifier(t)

			var reply RerunPluginsReply
			args := &RerunPluginsArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Plugins: tt.plugins}
			if err := (&RPCHandler{}).RerunPlugins(args, &reply); err != nil || !reply.Okay {
				t.Fatalf("RerunPlugins = %v, okay %v", err, reply.Okay)
			}

			got := prPluginStatuses(t, out, time.Second, aiOwner, aiRepo, aiNumber)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("announced %v, want %v", got, tt.want)
			}
		})
	}
}
