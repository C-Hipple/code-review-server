package server

import (
	"crs/config"
	"crs/database"
	"encoding/json"
	"fmt"
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
		name    string
		plugin  config.Plugin
		force   bool
		section bool
		want    PluginCallType
	}{
		{"normal plugin, unforced", config.Plugin{Name: "p"}, false, false, PluginCallAutomatic},
		{"normal plugin, forced", config.Plugin{Name: "p"}, true, false, PluginCallRerun},
		// A section that always runs a plugin that runs on its own runs it
		// as though someone had asked for it, in place of the automatic run;
		// a rerun someone asked for there is still a rerun.
		{"normal plugin, unforced, in its section", config.Plugin{Name: "p"}, false, true, PluginCallExplicit},
		{"normal plugin, forced, in its section", config.Plugin{Name: "p"}, true, true, PluginCallRerun},
		{"deferred plugin, forced", config.Plugin{Name: "p", OnlyOnDemand: true}, true, false, PluginCallExplicit},
		// Only a section that always runs it gets a deferred plugin an
		// unforced run, and it runs as though someone had asked for it.
		{"deferred plugin, unforced", config.Plugin{Name: "p", OnlyOnDemand: true}, false, true, PluginCallExplicit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := callTypeFor(tt.plugin, tt.force, tt.section); got != tt.want {
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
	if _, err := db.UpsertPluginResult("owner", "repo", 1, "ok", "fine", "success", "sha123", "automatic"); err != nil {
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
	if _, err := db.UpsertPluginResult("owner", "repo", 1, "ok", "fine", "success", "sha123", "automatic"); err != nil {
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
			if _, err := db.UpsertPluginResult(aiOwner, aiRepo, aiNumber, "ok", "fine", "success", "sha-1", "automatic"); err != nil {
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

// runLogScript writes a plugin that appends its arguments to a log, one line
// per run, and returns the plugin's path and the log's.
func runLogScript(t *testing.T, name string) (script, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, name+".log")
	script = filepath.Join(dir, name+".sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\necho report\n"), 0o755); err != nil {
		t.Fatalf("failed to write plugin script: %v", err)
	}
	return script, log
}

// runs returns the arguments of each run a runLogScript plugin logged.
func runs(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// putInSection writes owner/repo#number into a section the way a workflow
// does, under the identifier the workflows give a PR's item.
func putInSection(t *testing.T, db *database.DB, section, owner, repo string, number int) {
	t.Helper()
	s, err := db.GetOrCreateSection(section, 0)
	if err != nil {
		t.Fatal(err)
	}
	identifier := fmt.Sprintf("%s/%s-%d", owner, repo, number)
	if _, err := db.UpsertItemWithWorkflow(s.ID, identifier, "TODO", "A PR", nil, nil, 0, section+" workflow"); err != nil {
		t.Fatal(err)
	}
}

// sectionPluginConfig configures a deferred plugin "style" that the "Team"
// section always runs, and a deferred plugin "costly" that nothing does.
func sectionPluginConfig(db *database.DB, style, costly string) config.Config {
	return config.Config{
		DB:            db,
		SleepDuration: 10 * time.Minute,
		Plugins: []config.Plugin{
			{Name: "style", Command: style, IncludeBranch: true, OnlyOnDemand: true},
			{Name: "costly", Command: costly, OnlyOnDemand: true},
		},
		RawWorkflows: []config.RawWorkflow{
			{WorkflowType: "SyncReviewRequestsWorkflow", Name: "Team workflow", SectionTitle: "Team", AlwaysRunPlugins: []string{"style"}},
			{WorkflowType: "SyncReviewRequestsWorkflow", Name: "Mine workflow", SectionTitle: "Mine"},
		},
	}
}

func TestRunPlugins_RunsTheDeferredPluginsThePRsSectionAlwaysRuns(t *testing.T) {
	db := setupTestDB(t)
	style, styleLog := runLogScript(t, "style")
	costly, costlyLog := runLogScript(t, "costly")
	config.SetC(sectionPluginConfig(db, style, costly))
	t.Cleanup(func() { config.SetC(config.Config{}) })
	putInSection(t, db, "Team", "owner", "repo", 1)
	putInSection(t, db, "Mine", "owner", "repo", 2)

	RunPlugins("owner", "repo", 1, "sha123", "", "", "", "main")
	RunPlugins("owner", "repo", 2, "sha123", "", "", "", "main")

	// The section's plugin runs as though it had been asked for by name.
	got := runs(t, styleLog)
	if len(got) != 1 || !strings.Contains(got[0], "--number 1 ") || !strings.Contains(got[0], "--call-type explicit") {
		t.Fatalf("style ran %q, want once for PR 1 with --call-type explicit", got)
	}
	if got := runs(t, costlyLog); len(got) != 0 {
		t.Errorf("costly ran %q; no section always runs it", got)
	}
	results, _ := db.GetPluginResults("owner", "repo", 1)
	if results["style"].Status != "success" || results["costly"].Status != "deferred" {
		t.Errorf("PR 1 results = %+v, want style succeeded and costly deferred", results)
	}
	results, _ = db.GetPluginResults("owner", "repo", 2)
	if results["style"].Status != "deferred" {
		t.Errorf("PR 2 style = %+v, want deferred outside the section", results["style"])
	}

	// Once per head, like any plugin that runs on its own.
	RunPlugins("owner", "repo", 1, "sha123", "", "", "", "main")
	if got := runs(t, styleLog); len(got) != 1 {
		t.Errorf("style ran %d times at one head, want 1", len(got))
	}
	RunPlugins("owner", "repo", 1, "sha456", "", "", "", "main")
	if got := runs(t, styleLog); len(got) != 2 {
		t.Errorf("style ran %d times after a push, want 2", len(got))
	}
}

func TestRunPlugins_KeepsADeferredPluginsResultForTheSameHead(t *testing.T) {
	// A deferred plugin someone ran isn't put back to "deferred" when the
	// post-update hooks next run for the same head - say when the PR is
	// opened again.
	db := setupTestDB(t)
	script, log := runLogScript(t, "costly")
	config.SetC(config.Config{
		DB:      db,
		Plugins: []config.Plugin{{Name: "costly", Command: script, OnlyOnDemand: true}},
	})
	if _, err := db.UpsertPluginResult("owner", "repo", 1, "costly", "report", "success", "sha123", "explicit"); err != nil {
		t.Fatal(err)
	}
	out := subscribedNotifier(t)

	RunPlugins("owner", "repo", 1, "sha123", "", "", "", "main")

	if got := pluginStatuses(t, out, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("announced %v, want nothing", got)
	}
	results, _ := db.GetPluginResults("owner", "repo", 1)
	if got := results["costly"]; got.Status != "success" || got.Result != "report" {
		t.Errorf("costly = %+v, want the run kept", got)
	}
	if got := runs(t, log); len(got) != 0 {
		t.Errorf("costly ran %q", got)
	}
}

func TestRunPluginsForce_RerunningEveryPluginIncludesTheSectionsDeferredOnes(t *testing.T) {
	db := setupTestDB(t)
	style, styleLog := runLogScript(t, "style")
	costly, costlyLog := runLogScript(t, "costly")
	config.SetC(sectionPluginConfig(db, style, costly))
	t.Cleanup(func() { config.SetC(config.Config{}) })
	putInSection(t, db, "Team", "owner", "repo", 1)
	if _, err := db.UpsertPluginResult("owner", "repo", 1, "style", "old report", "success", "sha123", "explicit"); err != nil {
		t.Fatal(err)
	}

	RunPluginsForce("owner", "repo", 1, "sha123", "", "", "", "main", true, nil)

	if got := runs(t, styleLog); len(got) != 1 || !strings.Contains(got[0], "--call-type explicit") {
		t.Errorf("style ran %q, want one explicit rerun", got)
	}
	if got := runs(t, costlyLog); len(got) != 0 {
		t.Errorf("costly ran %q; only naming it reruns it", got)
	}
}

func TestRunSectionPlugins_RunsWhatHasntRunAtTheHeadFromTheCaches(t *testing.T) {
	t.Setenv("CRS_HOME", t.TempDir())
	t.Setenv("CRS_GITHUB_TOKEN", "test-token")
	db := setupTestDB(t)
	style, styleLog := runLogScript(t, "style")
	costly, costlyLog := runLogScript(t, "costly")
	config.SetC(sectionPluginConfig(db, style, costly))
	t.Cleanup(func() { config.SetC(config.Config{}) })
	// Its inputs come from the caches, never GitHub.
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	// The post-update hooks ran before the cycle put the PR in the section.
	RunPlugins(aiOwner, aiRepo, aiNumber, "sha-1", "", "", "", "alice/greeting")
	putInSection(t, db, "Team", aiOwner, aiRepo, aiNumber)

	RunSectionPlugins(aiOwner, aiRepo, aiNumber)

	got := runs(t, styleLog)
	if len(got) != 1 {
		t.Fatalf("style ran %d times, want once", len(got))
	}
	for _, want := range []string{fmt.Sprintf("--number %d ", aiNumber), "--call-type explicit", "--branch alice/greeting"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("style ran with %q, want %q in it", got[0], want)
		}
	}
	if got := runs(t, costlyLog); len(got) != 0 {
		t.Errorf("costly ran %q", got)
	}
	results, _ := db.GetPluginResults(aiOwner, aiRepo, aiNumber)
	if results["style"].Status != "success" || results["costly"].Status != "deferred" {
		t.Errorf("results = %+v, want style succeeded and costly still deferred", results)
	}

	// Every cycle rewrites the PR's item; a run at this head isn't repeated.
	RunSectionPlugins(aiOwner, aiRepo, aiNumber)
	if got := runs(t, styleLog); len(got) != 1 {
		t.Errorf("style ran %d times at one head, want 1", len(got))
	}
}

// sectionNormalPluginConfig configures a plugin "validator" that runs on its
// own, which the "Team" section always runs.
func sectionNormalPluginConfig(db *database.DB, validator string) config.Config {
	return config.Config{
		DB:            db,
		SleepDuration: 10 * time.Minute,
		Plugins:       []config.Plugin{{Name: "validator", Command: validator}},
		RawWorkflows: []config.RawWorkflow{
			{WorkflowType: "SyncReviewRequestsWorkflow", Name: "Team workflow", SectionTitle: "Team", AlwaysRunPlugins: []string{"validator"}},
			{WorkflowType: "SyncReviewRequestsWorkflow", Name: "Mine workflow", SectionTitle: "Mine"},
		},
	}
}

func TestRunPlugins_ASectionRunsAPluginThatRunsOnItsOwnExplicitly(t *testing.T) {
	db := setupTestDB(t)
	validator, log := runLogScript(t, "validator")
	config.SetC(sectionNormalPluginConfig(db, validator))
	t.Cleanup(func() { config.SetC(config.Config{}) })
	putInSection(t, db, "Team", "owner", "repo", 1)
	putInSection(t, db, "Mine", "owner", "repo", 2)

	RunPlugins("owner", "repo", 1, "sha123", "", "", "", "main")
	RunPlugins("owner", "repo", 2, "sha123", "", "", "", "main")

	// In the section it runs as though someone had asked for it; elsewhere
	// it still runs on its own.
	got := runs(t, log)
	if len(got) != 2 ||
		!strings.Contains(got[0], "--number 1 ") || !strings.Contains(got[0], "--call-type explicit") ||
		!strings.Contains(got[1], "--number 2 ") || !strings.Contains(got[1], "--call-type automatic") {
		t.Fatalf("validator ran %q, want an explicit run for PR 1 and an automatic one for PR 2", got)
	}

	// Once per head.
	RunPlugins("owner", "repo", 1, "sha123", "", "", "", "main")
	if got := runs(t, log); len(got) != 2 {
		t.Errorf("validator ran %d times, want no second run at one head", len(got))
	}
	RunPlugins("owner", "repo", 1, "sha456", "", "", "", "main")
	if got := runs(t, log); len(got) != 3 || !strings.Contains(got[2], "--call-type explicit") {
		t.Errorf("validator ran %q after a push, want a third, explicit run", got)
	}
}

func TestRunSectionPlugins_ReplacesTheAutomaticRunAPRHadBeforeJoining(t *testing.T) {
	t.Setenv("CRS_HOME", t.TempDir())
	t.Setenv("CRS_GITHUB_TOKEN", "test-token")
	db := setupTestDB(t)
	validator, log := runLogScript(t, "validator")
	config.SetC(sectionNormalPluginConfig(db, validator))
	t.Cleanup(func() { config.SetC(config.Config{}) })
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	// The post-update hooks ran before the cycle put the PR in the section,
	// so it had an automatic run.
	RunPlugins(aiOwner, aiRepo, aiNumber, "sha-1", "", "", "", "alice/greeting")
	putInSection(t, db, "Team", aiOwner, aiRepo, aiNumber)

	RunSectionPlugins(aiOwner, aiRepo, aiNumber)

	got := runs(t, log)
	if len(got) != 2 || !strings.Contains(got[0], "--call-type automatic") || !strings.Contains(got[1], "--call-type explicit") {
		t.Fatalf("validator ran %q, want the automatic run, then the section's explicit one", got)
	}
	if _, status, callType, _ := db.GetPluginResultState(aiOwner, aiRepo, aiNumber, "validator"); status != "success" || callType != "explicit" {
		t.Errorf("stored %s/%s, want the explicit run's success", status, callType)
	}

	// Neither the next cycle nor the post-update hooks run it again at this
	// head.
	RunSectionPlugins(aiOwner, aiRepo, aiNumber)
	RunPlugins(aiOwner, aiRepo, aiNumber, "sha-1", "", "", "", "alice/greeting")
	if got := runs(t, log); len(got) != 2 {
		t.Errorf("validator ran %d times at one head, want 2", len(got))
	}
}

func TestRunSettled(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	tests := []struct {
		name           string
		sha, status    string
		storedCallType string
		callType       PluginCallType
		want           bool
	}{
		{"nothing stored", "", "", "", PluginCallAutomatic, false},
		{"an older head", "sha-0", "success", "explicit", PluginCallExplicit, false},
		{"the deferred placeholder", "sha-1", "deferred", "", PluginCallExplicit, false},
		{"an automatic run, for an automatic run", "sha-1", "success", "automatic", PluginCallAutomatic, true},
		{"an automatic run still going, for an automatic run", "sha-1", "pending", "automatic", PluginCallAutomatic, true},
		{"an automatic run, for a section's run", "sha-1", "success", "automatic", PluginCallExplicit, false},
		{"an explicit run, for an automatic run", "sha-1", "pending", "explicit", PluginCallAutomatic, true},
		{"an explicit run that failed, for a section's run", "sha-1", "error", "explicit", PluginCallExplicit, true},
		{"a rerun, for a section's run", "sha-1", "success", "rerun", PluginCallExplicit, true},
		// Stored before call types were: a section's run had it settled.
		{"a run of unknown type, for a section's run", "sha-1", "success", "", PluginCallExplicit, true},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.sha != "" {
				if _, err := db.UpsertPluginResult("owner", "repo", i, "p", "out", tt.status, tt.sha, tt.storedCallType); err != nil {
					t.Fatal(err)
				}
			}
			if got := runSettled("owner", "repo", i, "p", "sha-1", tt.callType); got != tt.want {
				t.Errorf("runSettled() = %v, want %v", got, tt.want)
			}
		})
	}
}
