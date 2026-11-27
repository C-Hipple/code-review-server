package chat

import (
	"crs/config"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// newAntigravityTestBackend is the Antigravity agent running the fake as
// Antigravity, with flags besides; its chat's working directory holds a
// diff.
func newAntigravityTestBackend(t *testing.T, flags string, settings config.ChatBackendSettings) (*ACPBackend, func() []fakeACPEntry, string) {
	t.Helper()
	b := NewAntigravityBackend(config.ChatBackendAntigravity, nil)
	logs := useFakeACP(t, b, strings.Trim("antigravity,"+flags, ","), settings)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DiffFile), []byte("diff --git a/x b/x\n+line two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return b, logs, dir
}

// setCalls are the session/set_* requests logged, the session ID as S.
func setCalls(entries []fakeACPEntry, sessionID string) []string {
	var got []string
	for _, e := range entries {
		if m := e.method(); strings.HasPrefix(m, "session/set_") {
			var params json.RawMessage
			e.params(&params)
			got = append(got, m+" "+strings.ReplaceAll(string(params), sessionID, "S"))
		}
	}
	return got
}

// fakeEnv is what the fake logged of the environment it was given.
func fakeEnv(t *testing.T, entries []fakeACPEntry) map[string]string {
	t.Helper()
	for _, e := range entries {
		if e.method() == "_env" {
			var env map[string]string
			e.params(&env)
			return env
		}
	}
	t.Fatal("the fake logged no environment")
	return nil
}

func TestAntigravityTurnLetsItsOwnToolsThrough(t *testing.T) {
	b, logs, dir := newAntigravityTestBackend(t, "", config.ChatBackendSettings{})
	res, run, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@agy-tools", SystemPrompt: "Be brief."})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if !strings.HasPrefix(res.SessionID, "sess-") || !res.Answered {
		t.Errorf("result = %+v", res)
	}

	var got []string
	for _, e := range run.events {
		if e.Kind == EventToolUse || e.Kind == EventToolResult || e.Kind == EventText {
			got = append(got, string(e.Kind)+":"+e.ToolUseID+"|"+e.Tool+"|"+e.Detail+"|"+e.Text+map[bool]string{true: "|error"}[e.IsError])
		}
	}
	want := []string{
		"tool_use:s1|Plan|Read the diff|", "tool_result:s1|||",
		"tool_use:l1|List|.|", "tool_result:l1|||",
		"tool_use:v1|Read|pr.diff|", "tool_result:v1|||",
		"tool_use:a1|Subagent|Code explorer|", "tool_result:a1|||",
		"tool_use:c1|Run|go test ./...|", "tool_result:c1||||error",
		"text:|||It looks fine.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("events =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	entries := logs()
	if m := methods(entries); !slices.Equal(m, []string{"initialize", "session/new", "session/set_mode", "session/prompt"}) {
		t.Errorf("methods = %v (initialize naming version 2 is taken in version 1's shape)", m)
	}
	// Switched from its own "auto" to "default", where it asks first.
	if set := setCalls(entries, res.SessionID); !slices.Equal(set, []string{`session/set_mode {"sessionId":"S","modeId":"default"}`}) {
		t.Errorf("set calls = %v", set)
	}
	// The system prompt goes with the session, not ahead of the first message.
	var newSession struct {
		Meta struct {
			Antigravity struct {
				SystemPrompt struct {
					Append string `json:"append"`
				} `json:"systemPrompt"`
			} `json:"antigravity"`
		} `json:"_meta"`
	}
	var prompt acpPromptRequest
	for _, e := range entries {
		switch e.method() {
		case "session/new":
			e.params(&newSession)
		case "session/prompt":
			e.params(&prompt)
		}
	}
	if newSession.Meta.Antigravity.SystemPrompt.Append != "Be brief." {
		t.Errorf("session/new should carry the system prompt: %+v", newSession)
	}
	if len(prompt.Prompt) != 1 || prompt.Prompt[0].Text != "@agy-tools" {
		t.Errorf("the prompt should carry the message alone: %+v", prompt.Prompt)
	}

	// The client read is allowed, and served (to its subagent's session too);
	// the command is refused.
	if result, _, _ := response(entries, "r1"); !strings.Contains(string(result), `"optionId":"proceed_once"`) {
		t.Errorf("a read should be allowed: %s", result)
	}
	if result, msg, _ := response(entries, "r2"); msg != "" || !strings.Contains(string(result), "line two") {
		t.Errorf("reading the diff: %s %s", result, msg)
	}
	if result, _, _ := response(entries, "r3"); !strings.Contains(string(result), `"optionId":"cancel"`) {
		t.Errorf("a command should be refused: %s", result)
	}
}

func TestAntigravityStopsAToolOfItsOwnThatActs(t *testing.T) {
	b, logs, dir := newAntigravityTestBackend(t, "", config.ChatBackendSettings{})
	_, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@agy-schedule"})
	if err == nil || !strings.Contains(err.Error(), `Stopped: Antigravity used "schedule" without asking first`) {
		t.Errorf("err = %v", err)
	}
	if !slices.Contains(methods(logs()), "session/cancel") {
		t.Errorf("the turn should be cancelled: %v", methods(logs()))
	}
}

func TestAntigravityAsksTheReviewer(t *testing.T) {
	b, logs, dir := newAntigravityTestBackend(t, "", config.ChatBackendSettings{})
	res, run, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@agy-question"})
	if err != nil {
		t.Fatalf("a turn that stops for the reviewer's answer isn't a failure: %v", err)
	}
	if !res.Answered {
		t.Errorf("result = %+v", res)
	}
	var texts []string
	for _, e := range run.events {
		if e.Kind == EventText {
			texts = append(texts, e.Text)
		}
		if e.Kind == EventToolUse {
			t.Errorf("a question isn't a tool call: %+v", e)
		}
	}
	want := []string{"Before I answer:", "**Antigravity asks:** Which file should I look at first?\n\n- server/chat.go\n- chat/acp.go"}
	if !slices.Equal(texts, want) {
		t.Errorf("texts = %q, want %q", texts, want)
	}
	if result, _, _ := response(logs(), "r1"); !strings.Contains(string(result), `"outcome":"cancelled"`) {
		t.Errorf("the server can't answer for the reviewer: %s", result)
	}
}

func TestAntigravitySaysItsQuotaRanOut(t *testing.T) {
	b, _, dir := newAntigravityTestBackend(t, "", config.ChatBackendSettings{})
	_, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@refusal"})
	if err == nil || !strings.Contains(err.Error(), "your Antigravity quota is used up for now") {
		t.Errorf("err = %v", err)
	}
}

func TestAntigravityChoosesTheModelAndKeepsAChosenMode(t *testing.T) {
	b, logs, dir := newAntigravityTestBackend(t, "", config.ChatBackendSettings{Mode: "auto"})
	res, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain", Model: "claude-sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	// Its model is a config option of no category, found by its ID; the
	// session is in the configured mode already.
	if set := setCalls(logs(), res.SessionID); !slices.Equal(set, []string{`session/set_config_option {"sessionId":"S","configId":"model","value":"claude-sonnet"}`}) {
		t.Errorf("set calls = %v", set)
	}

	b, logs, dir = newAntigravityTestBackend(t, "", config.ChatBackendSettings{})
	res, _, err = runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain", Model: "gemini-flash"})
	if err != nil {
		t.Fatal(err)
	}
	if set := setCalls(logs(), res.SessionID); !slices.Equal(set, []string{`session/set_mode {"sessionId":"S","modeId":"default"}`}) {
		t.Errorf("a model the session has already needs no call: %v", set)
	}

	b, _, dir = newAntigravityTestBackend(t, "", config.ChatBackendSettings{})
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain", Model: "gpt-9"}); err == nil ||
		!strings.Contains(err.Error(), `Antigravity has no model "gpt-9"; it offers gemini-flash, claude-sonnet`) {
		t.Errorf("an unknown model: %v", err)
	}
}

func TestAntigravitySaysHowToSignIn(t *testing.T) {
	const how = "Antigravity needs signing in first. Sign in by running `agy` in a terminal and signing in with Google, " +
		"or set [Chat.Antigravity] AuthMethod to one of: oauth-personal, gemini-api-key."
	for _, tt := range []struct {
		name, flags string
		settings    config.ChatBackendSettings
		calls       []string
	}{
		{"it says so", "agynobrowser", config.ChatBackendSettings{}, []string{"initialize", "session/new"}},
		{"it can't sign in without a browser", "agynobrowser", config.ChatBackendSettings{AuthMethod: "oauth-personal"},
			[]string{"initialize", "session/new", "authenticate"}},
		// It waits for a code it can't be given, so it is stopped at once.
		{"it would sign in over SSH", "agysignin", config.ChatBackendSettings{}, []string{"initialize", "session/new"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, logs, dir := newAntigravityTestBackend(t, tt.flags, tt.settings)
			start := time.Now()
			_, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"})
			if err == nil || err.Error() != how {
				t.Errorf("err = %v\nwant %s", err, how)
			}
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Errorf("took %s", elapsed)
			}
			if m := methods(logs()); !slices.Equal(m, tt.calls) {
				t.Errorf("methods = %v", m)
			}
		})
	}
}

func TestAntigravityRunsInATemporaryDirectoryOfItsOwnWithoutTheServersKeys(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "the-servers-key")
	b, logs, dir := newAntigravityTestBackend(t, "", config.ChatBackendSettings{Env: map[string]string{"AGY_EXTRA": "1"}})
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"}); err != nil {
		t.Fatal(err)
	}
	env := fakeEnv(t, logs())
	if env["GEMINI_API_KEY"] != "" || env["AGY_EXTRA"] != "1" {
		t.Errorf("env = %v: the server's Gemini key stays out, the table's Env goes in", env)
	}
	tmp := env["TMPDIR"]
	if tmp == "" || !strings.HasPrefix(filepath.Base(tmp), "crs-agent-") {
		t.Fatalf("TMPDIR = %q", tmp)
	}
	if _, err := os.Stat(filepath.Join(tmp, "_MEIfake", "agy")); err != nil {
		t.Fatalf("the agent should have unpacked itself there: %v", err)
	}
	b.ReleaseAll()
	waitFor(t, func() bool {
		_, err := os.Stat(tmp)
		return b.running() == 0 && errors.Is(err, os.ErrNotExist)
	})

	// A key the table's Env sets is the agent's.
	b, logs, dir = newAntigravityTestBackend(t, "", config.ChatBackendSettings{Env: map[string]string{"GEMINI_API_KEY": "its-own"}})
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"}); err != nil {
		t.Fatal(err)
	}
	if env := fakeEnv(t, logs()); env["GEMINI_API_KEY"] != "its-own" {
		t.Errorf("env = %v", env)
	}
}

func TestAntigravityInfo(t *testing.T) {
	var settings config.ChatBackendSettings
	b := NewAntigravityBackend(config.ChatBackendAntigravity, func() config.ChatBackendSettings { return settings })
	var looked string
	b.LookPath = func(name string) (string, error) { looked = name; return "/home/me/.local/bin/agy", nil }
	info := b.Info()
	if !info.Available || info.Name != "Antigravity" || looked != "agy" || len(info.Models) != 1 {
		t.Errorf("info = %+v, looked for %q", info, looked)
	}
	argv, err := b.argv(settings)
	if err != nil || !slices.Equal(argv, []string{"agy", "acp"}) {
		t.Errorf("argv = %v, %v", argv, err)
	}

	b.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	if info := b.Info(); info.Available || !strings.Contains(info.Reason, "agy isn't installed where the server can find it (set [Chat.Antigravity] Command to its path)") {
		t.Errorf("info = %+v", info)
	}

	settings = config.ChatBackendSettings{Name: "Gemini at work", Command: "/opt/agy/bin/agy acp", Models: []string{"gemini-flash"}}
	b.LookPath = func(name string) (string, error) { looked = name; return name, nil }
	if info := b.Info(); info.Name != "Gemini at work" || looked != "/opt/agy/bin/agy" || len(info.Models) != 2 {
		t.Errorf("info = %+v", info)
	}
}

func TestACPToolID(t *testing.T) {
	for title, want := range map[string]string{
		"Running list_dir":       "list_dir",
		"Run update_steps":       "update_steps",
		"Run client_view_file?":  "client_view_file",
		"run_command":            "run_command",
		"Run mcp_github_x":       "mcp_github_x",
		"Read /src/a.go":         "",
		"Read File":              "",
		"README.md":              "",
		"Running":                "",
		"MCP Tool create_issue":  "",
		"Run make test":          "",
		"Run Make":               "",
		"  Running view_file  ":  "view_file",
		"Running view_file.go":   "",
		"Run _private":           "",
		"Running 2fa":            "",
		"Running list_dir?\n":    "list_dir",
		"Running list_dir??":     "",
		"Running list-dir":       "",
		"Run edit_file?":         "edit_file",
		"Running start_subagent": "start_subagent",
	} {
		if got := acpToolID(title); got != want {
			t.Errorf("acpToolID(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestACPVersion1Shape(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"protocolVersion":2,"agentCapabilities":{},"authMethods":[]}`: true,
		`{"protocolVersion":2,"agentInfo":{"name":"x"}}`:                true,
		`{"protocolVersion":2,"capabilities":{},"info":{"name":"x"}}`:   false,
		`{"protocolVersion":2}`:                                         false,
		`[]`:                                                            false,
	} {
		if got := acpVersion1Shape(json.RawMessage(raw)); got != want {
			t.Errorf("acpVersion1Shape(%s) = %v, want %v", raw, got, want)
		}
	}
}

func TestACPOffersTheModelsTheAgentOfferedItsLastSession(t *testing.T) {
	b, _, dir := newAntigravityTestBackend(t, "", config.ChatBackendSettings{})
	if models := b.Info().Models; len(models) != 1 {
		t.Fatalf("before any session, only its default: %+v", models)
	}
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"}); err != nil {
		t.Fatal(err)
	}
	want := []Model{{ID: "", Name: "Default"}, {ID: "gemini-flash", Name: "Gemini Flash"}, {ID: "claude-sonnet", Name: "Claude Sonnet"}}
	if models := b.Info().Models; !slices.Equal(models, want) {
		t.Errorf("models = %+v, want %+v", models, want)
	}

	// The older models list, for a plain ACP agent; the table's Models win.
	acp, _, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	if _, _, err := runTurn(t, acp, Turn{WorkDir: dir, Prompt: "@plain"}); err != nil {
		t.Fatal(err)
	}
	want = []Model{{ID: "", Name: "Default"}, {ID: "small", Name: "Small"}, {ID: "big", Name: "Big"}}
	if models := acp.Info().Models; !slices.Equal(models, want) {
		t.Errorf("models = %+v, want %+v", models, want)
	}
	settings := acp.Settings()
	settings.Models = []string{"big"}
	acp.Settings = func() config.ChatBackendSettings { return settings }
	if models := acp.Info().Models; !slices.Equal(models, []Model{{ID: "", Name: "Default"}, {ID: "big", Name: "Big"}}) {
		t.Errorf("models = %+v", models)
	}
}
