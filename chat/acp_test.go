package chat

import (
	"context"
	"crs/config"
	"crs/database"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// acpRun is one Turn of a backend, with what it emitted.
type acpRun struct {
	mu     sync.Mutex
	events []Event
}

func (r *acpRun) emit(e Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

// newACPTestBackend is an ACP backend running the fake with flags; its chat's
// working directory holds a diff.
func newACPTestBackend(t *testing.T, flags string, settings config.ChatBackendSettings) (*ACPBackend, func() []fakeACPEntry, string) {
	t.Helper()
	b := NewACPBackend("fake", nil)
	logs := useFakeACP(t, b, flags, settings)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DiffFile), []byte("diff --git a/x b/x\n+line two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return b, logs, dir
}

func runTurn(t *testing.T, b *ACPBackend, turn Turn) (TurnResult, *acpRun, error) {
	t.Helper()
	run := &acpRun{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := b.Turn(ctx, turn, run.emit)
	return res, run, err
}

func TestACPTurnStreamsTheReplyAndTheToolCalls(t *testing.T) {
	b, logs, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	checkout := t.TempDir()
	res, run, err := runTurn(t, b, Turn{WorkDir: dir, ReadDirs: []string{checkout}, Prompt: "Is it fine?", SystemPrompt: "Be brief.", SessionID: "ours"})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if !strings.HasPrefix(res.SessionID, "sess-") || !res.Answered {
		t.Errorf("result = %+v", res)
	}

	var got []string
	for _, e := range run.events {
		got = append(got, string(e.Kind)+":"+e.Text+e.SessionID+e.ToolUseID+"|"+e.Tool+"|"+e.Detail)
	}
	want := []string{
		"session:" + res.SessionID + "||",
		"text_delta:Let me ||", "text_delta:look.||", "text:Let me look.||",
		"tool_use:t1|Read|File", // the first description, until the agent says what it reads
		"tool_use:t1|Read|pr.diff",
		"tool_result:t1||",
		"text_delta:Looks ||", "text_delta:fine.||", "text:Looks fine.||",
	}
	if !slices.Equal(got, want) {
		t.Errorf("events =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	entries := logs()
	if m := methods(entries); !slices.Equal(m, []string{"initialize", "session/new", "session/prompt"}) {
		t.Errorf("methods = %v", m)
	}
	var init acpInitializeRequest
	entries[0].params(&init)
	if init.ProtocolVersion != 1 || !init.ClientCapabilities.FS.ReadTextFile || init.ClientCapabilities.FS.WriteTextFile ||
		init.ClientCapabilities.Terminal || init.ClientInfo.Name == "" || init.ClientInfo.Version == "" {
		t.Errorf("initialize = %+v", init)
	}
	var newSession acpNewSessionRequest
	entries[1].params(&newSession)
	if newSession.Cwd != dir || newSession.McpServers == nil || newSession.AdditionalDirectories != nil {
		t.Errorf("session/new = %+v (no additionalDirectories for an agent that doesn't offer them)", newSession)
	}
	var prompt acpPromptRequest
	entries[2].params(&prompt)
	if len(prompt.Prompt) != 2 || prompt.Prompt[0].Text != "Be brief." || prompt.Prompt[1].Text != "Is it fine?" {
		t.Errorf("a new conversation's prompt should carry the system prompt first: %+v", prompt.Prompt)
	}

	// The agent's requests, as the server answered them.
	if result, msg, _ := response(entries, "r1"); msg != "" || !strings.Contains(string(result), `"diff --git a/x b/x\n"`) {
		t.Errorf("reading the diff's first line: %s %s", result, msg)
	}
	if _, msg, _ := response(entries, "r2"); !strings.Contains(msg, "outside the directories this chat may read") {
		t.Errorf("reading outside the chat's directories should be refused, got %q", msg)
	}
	if result, _, _ := response(entries, "r3"); !strings.Contains(string(result), `"optionId":"no"`) {
		t.Errorf("running a command should be refused: %s", result)
	}
	if result, _, _ := response(entries, "r4"); !strings.Contains(string(result), `"optionId":"once"`) {
		t.Errorf("a read (kind known from its tool_call) should be allowed once: %s", result)
	}
	if _, msg, _ := response(entries, "r5"); !strings.Contains(msg, "read-only") {
		t.Errorf("writing should be refused, got %q", msg)
	}
}

func TestACPKeepsTheAgentRunningForTheChatsNextTurn(t *testing.T) {
	b, logs, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	first, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain", SystemPrompt: "Be brief."})
	if err != nil {
		t.Fatal(err)
	}
	second, run, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain again", SystemPrompt: "Be brief.", SessionID: first.SessionID, Resume: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionID != first.SessionID {
		t.Errorf("the session should carry on: %q then %q", first.SessionID, second.SessionID)
	}
	entries := logs()
	if p := pids(entries); len(p) != 1 {
		t.Errorf("both turns should go to one agent, went to %v", p)
	}
	if m := methods(entries); !slices.Equal(m, []string{"initialize", "session/new", "session/prompt", "session/prompt"}) {
		t.Errorf("methods = %v", m)
	}
	var prompt acpPromptRequest
	entries[len(entries)-1].params(&prompt)
	if len(prompt.Prompt) != 1 || prompt.Prompt[0].Text != "@plain again" {
		t.Errorf("a resumed turn's prompt is the message alone: %+v", prompt.Prompt)
	}
	for _, e := range run.events {
		if e.Kind == EventSession {
			t.Errorf("a resumed turn has no new session to report: %+v", e)
		}
	}
	if b.running() != 1 {
		t.Errorf("the agent should wait for the next turn")
	}
	b.Release(dir)
	if b.running() != 0 {
		t.Errorf("Release should stop the chat's agent")
	}
}

func TestACPPicksTheConversationUpInANewProcess(t *testing.T) {
	for _, tt := range []struct{ flags, method string }{
		{"load", "session/load"},
		{"load,resume,dirs", "session/resume"}, // resume, which replays nothing, first
	} {
		t.Run(tt.flags, func(t *testing.T) {
			b, logs, dir := newACPTestBackend(t, tt.flags, config.ChatBackendSettings{})
			checkout := t.TempDir()
			first, _, err := runTurn(t, b, Turn{WorkDir: dir, ReadDirs: []string{checkout}, Prompt: "@plain"})
			if err != nil {
				t.Fatal(err)
			}
			b.ReleaseAll()
			second, run, err := runTurn(t, b, Turn{WorkDir: dir, ReadDirs: []string{checkout}, Prompt: "@plain", SessionID: first.SessionID, Resume: true})
			if err != nil {
				t.Fatal(err)
			}
			if second.SessionID != first.SessionID {
				t.Errorf("session = %q, want %q", second.SessionID, first.SessionID)
			}
			entries := logs()
			if p := pids(entries); len(p) != 2 {
				t.Errorf("the second turn should start a new agent: %v", p)
			}
			m := methods(entries)
			if !slices.Contains(m, tt.method) {
				t.Errorf("methods = %v, want %s", m, tt.method)
			}
			for _, e := range entries {
				if e.method() != tt.method {
					continue
				}
				var load acpLoadSessionRequest
				e.params(&load)
				if load.SessionID != first.SessionID || load.Cwd != dir {
					t.Errorf("%s = %+v", tt.method, load)
				}
				if strings.Contains(tt.flags, "dirs") != slices.Equal(load.AdditionalDirectories, []string{checkout}) {
					t.Errorf("additionalDirectories = %v", load.AdditionalDirectories)
				}
			}
			var texts []string
			for _, e := range run.events {
				texts = append(texts, e.Text)
			}
			if strings.Contains(strings.Join(texts, ""), "old answer") {
				t.Errorf("session/load's replay leaked into the turn: %q", texts)
			}
		})
	}
}

func TestACPCantResumeWithoutLoadOrResume(t *testing.T) {
	b, _, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	first, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"})
	if err != nil {
		t.Fatal(err)
	}
	b.ReleaseAll()
	res, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain", SessionID: first.SessionID, Resume: true})
	if err == nil || !strings.Contains(err.Error(), "neither session/load nor session/resume") || res.Answered {
		t.Errorf("Turn = %+v, %v", res, err)
	}
}

func TestACPChoosesTheModeAndModel(t *testing.T) {
	for _, tt := range []struct {
		flags string
		want  []string
	}{
		{"", []string{`session/set_mode {"sessionId":"S","modeId":"plan"}`, `session/set_model {"sessionId":"S","modelId":"big"}`}},
		{"configopts", []string{`session/set_config_option {"sessionId":"S","configId":"approval","value":"plan"}`,
			`session/set_config_option {"sessionId":"S","configId":"llm","value":"big"}`}},
	} {
		t.Run(tt.flags, func(t *testing.T) {
			b, logs, dir := newACPTestBackend(t, tt.flags, config.ChatBackendSettings{Mode: "plan"})
			res, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain", Model: "big"})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, e := range logs() {
				if m := e.method(); strings.HasPrefix(m, "session/set_") {
					var params json.RawMessage
					e.params(&params)
					got = append(got, m+" "+strings.ReplaceAll(string(params), res.SessionID, "S"))
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}

	b, _, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain", Model: "huge"}); err == nil ||
		!strings.Contains(err.Error(), `no model "huge"; it offers small, big`) {
		t.Errorf("an unknown model: %v", err)
	}
	b, _, dir = newACPTestBackend(t, "nomodels", config.ChatBackendSettings{})
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain", Model: "big"}); err == nil ||
		!strings.Contains(err.Error(), "doesn't let a client choose its model: take Models and DefaultModel out of [Chat.Agents.fake]") {
		t.Errorf("a model for an agent with no choice of them: %v", err)
	}
}

func TestACPSignsIn(t *testing.T) {
	b, _, dir := newACPTestBackend(t, "auth", config.ChatBackendSettings{})
	_, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"})
	if err == nil || !strings.Contains(err.Error(), "fake needs signing in first (API key: Run `fake login` in a terminal)") ||
		!strings.Contains(err.Error(), "[Chat.Agents.fake] AuthMethod to one of: key") {
		t.Errorf("an agent that needs signing in: %v", err)
	}

	b, logs, dir := newACPTestBackend(t, "auth", config.ChatBackendSettings{AuthMethod: "key"})
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"}); err != nil {
		t.Fatalf("with AuthMethod: %v", err)
	}
	if m := methods(logs()); !slices.Equal(m, []string{"initialize", "session/new", "authenticate", "session/new", "session/prompt"}) {
		t.Errorf("methods = %v", m)
	}
}

func TestACPStopsAnAgentThatActsWithoutAsking(t *testing.T) {
	b, logs, dir := newACPTestBackend(t, "", config.ChatBackendSettings{Name: "Fake"})
	res, run, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@unasked"})
	if err == nil || !strings.Contains(err.Error(), `Stopped: Fake ran "rm -rf build" without asking first`) {
		t.Errorf("err = %v", err)
	}
	if !res.Answered || !slices.Contains(methods(logs()), "session/cancel") {
		t.Errorf("the turn should be cancelled: %+v %v", res, methods(logs()))
	}
	if len(run.events) != 3 || run.events[1].Kind != EventToolUse || run.events[1].Tool != "Run" || run.events[2].Kind != EventToolResult {
		t.Errorf("events = %+v", run.events)
	}
}

func TestACPSaysWhatItRefusedWhenTheAgentGivesUp(t *testing.T) {
	b, _, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	_, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@refuse-quits"})
	if err == nil || err.Error() != `fake stopped when the server refused to let it run "make": the chat is read-only.` {
		t.Errorf("err = %v", err)
	}
}

func TestACPStopCancelsTheTurn(t *testing.T) {
	b, logs, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &acpRun{}
	done := make(chan error, 1)
	go func() {
		_, err := b.Turn(ctx, Turn{WorkDir: dir, Prompt: "@hang"}, run.emit)
		done <- err
	}()
	waitFor(t, func() bool {
		run.mu.Lock()
		defer run.mu.Unlock()
		return len(run.events) >= 2 // the session, then "Let me"
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if !slices.Contains(methods(logs()), "session/cancel") {
		t.Errorf("methods = %v", methods(logs()))
	}
	// The agent acknowledged the cancel, so it is kept for the next turn.
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"}); err != nil {
		t.Fatal(err)
	}
	if p := pids(logs()); len(p) != 1 {
		t.Errorf("the agent should be kept: %v", p)
	}
}

func TestACPKillsAnAgentThatIgnoresTheCancel(t *testing.T) {
	grace := acpCancelGrace
	acpCancelGrace = 100 * time.Millisecond
	t.Cleanup(func() { acpCancelGrace = grace })
	b, logs, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := b.Turn(ctx, Turn{WorkDir: dir, Prompt: "@ignore-cancel"}, func(Event) {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"}); err != nil {
		t.Fatal(err)
	}
	if p := pids(logs()); len(p) != 2 {
		t.Errorf("a killed agent should be replaced: %v", p)
	}
}

func TestACPReportsAnAgentThatExits(t *testing.T) {
	b, logs, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@crash"}); err == nil || !strings.Contains(err.Error(), "fake exited: something broke") {
		t.Errorf("err = %v", err)
	}
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"}); err != nil {
		t.Fatal(err)
	}
	if p := pids(logs()); len(p) != 2 {
		t.Errorf("an agent that exited should be replaced: %v", p)
	}
}

func TestACPSaysWhenTheCommandDoesntSpeakACP(t *testing.T) {
	b := NewACPBackend("cat", func() config.ChatBackendSettings {
		return config.ChatBackendSettings{Command: "sh -c 'echo not acp; exit 0'"}
	})
	_, err := b.Turn(context.Background(), Turn{WorkDir: t.TempDir(), Prompt: "hi"}, func(Event) {})
	if err == nil || !strings.Contains(err.Error(), "cat didn't start speaking ACP (is [Chat.Agents.cat] Command the command that starts it in ACP mode?)") {
		t.Errorf("err = %v", err)
	}
	if b.running() != 0 {
		t.Errorf("a failed start shouldn't be kept")
	}
}

func TestACPStopsAnIdleAgent(t *testing.T) {
	b, _, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	b.IdleTimeout = 50 * time.Millisecond
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return b.running() == 0 })
}

func TestACPInfo(t *testing.T) {
	settings := config.ChatBackendSettings{Command: "my-agent --acp", Models: []string{"big", "org/model-2"}, DefaultModel: "big"}
	b := NewACPBackend("mine", func() config.ChatBackendSettings { return settings })
	b.LookPath = func(string) (string, error) { return "/bin/my-agent", nil }
	info := b.Info()
	if !info.Available || info.Name != "mine" || info.DefaultModel != "big" || len(info.Models) != 3 ||
		info.Models[0] != (Model{ID: "", Name: "Default"}) || info.Models[1] != (Model{ID: "big", Name: "Big"}) || info.Models[2].Name != "org/model-2" {
		t.Errorf("info = %+v", info)
	}
	settings.Name = "My Agent"
	if info := b.Info(); info.Name != "My Agent" {
		t.Errorf("Name = %q", info.Name)
	}

	for _, tt := range []struct {
		settings config.ChatBackendSettings
		lookErr  bool
		want     string
	}{
		{config.ChatBackendSettings{Command: "my-agent", Disabled: true}, false, "disabled in the config ([Chat.Agents.mine] Disabled)"},
		{config.ChatBackendSettings{}, false, "no Command is set: set [Chat.Agents.mine] Command"},
		{config.ChatBackendSettings{Command: `"unclosed`}, false, "its command can't be read"},
		{config.ChatBackendSettings{Command: "my-agent"}, true, "my-agent isn't installed where the server can find it"},
	} {
		settings = tt.settings
		b.LookPath = func(string) (string, error) {
			if tt.lookErr {
				return "", errors.New("not found")
			}
			return "/bin/x", nil
		}
		if info := b.Info(); info.Available || !strings.Contains(info.Reason, tt.want) {
			t.Errorf("%+v: info = %+v, want a reason with %q", tt.settings, info, tt.want)
		}
	}
}

func TestACPReadFileStaysInTheChatsDirectories(t *testing.T) {
	dir, checkout, outside := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "a.txt"), "one\ntwo\nthree\n")
	write(filepath.Join(checkout, "b.go"), "package b\n")
	write(filepath.Join(outside, "secret"), "no")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	turn := newACPTurn("fake", func(Event) {}, Turn{WorkDir: dir, ReadDirs: []string{checkout}})
	read := func(path string, line, limit *int) (string, string) {
		params, _ := json.Marshal(acpReadTextFileRequest{Path: path, Line: line, Limit: limit})
		result, rpcErr := turn.readFile(params)
		if rpcErr != nil {
			return "", rpcErr.Message
		}
		return result.(acpReadTextFileResponse).Content, ""
	}
	two, one := 2, 1
	for _, tt := range []struct {
		path        string
		line, limit *int
		want, err   string
	}{
		{path: filepath.Join(dir, "a.txt"), want: "one\ntwo\nthree\n"},
		{path: filepath.Join(dir, "a.txt"), line: &two, want: "two\nthree\n"},
		{path: filepath.Join(dir, "a.txt"), line: &two, limit: &one, want: "two\n"},
		{path: filepath.Join(checkout, "b.go"), want: "package b\n"},
		{path: filepath.Join(dir, "..", filepath.Base(outside), "secret"), err: "outside the directories"},
		{path: filepath.Join(dir, "link"), err: "outside the directories"},
		{path: filepath.Join(dir, "missing"), err: "no such file"},
		{path: "a.txt", err: "must be absolute"},
		{path: dir, err: "is a directory"},
	} {
		got, err := read(tt.path, tt.line, tt.limit)
		if got != tt.want || !strings.Contains(err, tt.err) || (tt.err == "") != (err == "") {
			t.Errorf("read(%s) = %q, %q; want %q, %q", tt.path, got, err, tt.want, tt.err)
		}
	}
}

func TestACPToolDetail(t *testing.T) {
	turn := newACPTurn("fake", func(Event) {}, Turn{WorkDir: "/chats/abc", ReadDirs: []string{"/src/repo"}})
	for _, tt := range []struct {
		call acpTool
		want string
	}{
		{acpTool{kind: "read", title: "Read /chats/abc/pr.diff"}, "pr.diff"},
		{acpTool{kind: "read", title: "read_file: /src/repo/server/chat.go"}, "server/chat.go"},
		{acpTool{kind: "search", title: "grep \"Greet\" /src/repo"}, `grep "Greet" /src/repo`},
		{acpTool{kind: "read", title: "", locations: []string{"/src/repo/a.go"}}, "a.go"},
		{acpTool{kind: "other", title: "Todo list"}, "Todo list"},
		{acpTool{kind: "read", title: "Reading /src/repo/a.go"}, "a.go"},
		// A file whose name starts with the tool's keeps it.
		{acpTool{kind: "read", title: "README.md"}, "README.md"},
		{acpTool{kind: "edit", title: "editor.go"}, "editor.go"},
		{acpTool{kind: "execute", title: "Runner.sh --fast"}, "Runner.sh --fast"},
		{acpTool{kind: "execute", title: "run_shell_command: make"}, "make"},
	} {
		if got := turn.detail(&tt.call); got != tt.want {
			t.Errorf("detail(%+v) = %q, want %q", tt.call, got, tt.want)
		}
	}
}

func TestACPErrorCarriesItsData(t *testing.T) {
	for _, tt := range []struct {
		err  acpError
		want string
	}{
		{acpError{Code: -32602, Message: "Invalid params"}, "Invalid params"},
		{acpError{Code: -32603, Message: "Internal error", Data: json.RawMessage(`"Failed to resolve provider"`)}, "Internal error: Failed to resolve provider"},
		{acpError{Code: -32602, Message: "Invalid params", Data: json.RawMessage(`{"path": ["clientInfo", "version"]}`)}, `Invalid params: {"path":["clientInfo","version"]}`},
		{acpError{Code: -32000}, "error -32000"},
	} {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
	}
}

func TestACPChatThroughTheManager(t *testing.T) {
	b, _, _ := newACPTestBackend(t, "", config.ChatBackendSettings{})
	db, err := database.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := NewManager(Fixed{b})
	root := t.TempDir()
	m.Store = func() Store { return db }
	m.Dir = func() (string, error) { return root, nil }
	m.LoadPR = func(_ context.Context, owner, repo string, number int) (PRContext, error) { return testPR, nil }
	idle := make(chan struct{}, 4)
	m.OnChange = func(_ database.ChatSession, status string) {
		if status == StatusIdle {
			idle <- struct{}{}
		}
	}
	session, err := m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "Is it fine?"})
	if err != nil {
		t.Fatal(err)
	}
	v := waitView(t, m, session.ID, idle)
	var got []string
	for _, msg := range v.Messages {
		got = append(got, msg.Role+": "+msg.Content+" "+msg.Meta)
	}
	want := []string{
		"user: Is it fine? " + v.Messages[0].Meta,
		"assistant: Let me look. ",
		`tool: pr.diff {"status":"done","tool":"Read","tool_use_id":"t1"}`, // its detail updated as the agent said more
		"assistant: Looks fine. ",
	}
	if !slices.Equal(got, want) {
		t.Errorf("messages =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if !strings.HasPrefix(v.Session.BackendSessionID, "sess-") {
		t.Errorf("the agent's own session ID should be kept: %+v", v.Session)
	}

	// Deleting the chat stops its agent.
	if b.running() != 1 {
		t.Fatalf("the chat's agent should wait for its next turn")
	}
	if err := m.Delete(session.ID); err != nil {
		t.Fatal(err)
	}
	if b.running() != 0 {
		t.Errorf("deleting the chat should stop its agent")
	}
}

func TestACPManagerStartsOverForAnAgentThatCantResume(t *testing.T) {
	b, logs, _ := newACPTestBackend(t, "", config.ChatBackendSettings{})
	db, err := database.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := NewManager(Fixed{b})
	root := t.TempDir()
	m.Store = func() Store { return db }
	m.Dir = func() (string, error) { return root, nil }
	m.LoadPR = func(context.Context, string, string, int) (PRContext, error) { return testPR, nil }
	idle := make(chan struct{}, 4)
	m.OnChange = func(_ database.ChatSession, status string) {
		if status == StatusIdle {
			idle <- struct{}{}
		}
	}
	session, err := m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "@plain first"})
	if err != nil {
		t.Fatal(err)
	}
	waitView(t, m, session.ID, idle)
	b.ReleaseAll() // the agent exits; this one can't pick the conversation up
	if err := m.Send(context.Background(), session.ID, "@plain second"); err != nil {
		t.Fatal(err)
	}
	v := waitView(t, m, session.ID, idle)
	if last := v.Messages[len(v.Messages)-1]; last.Role != RoleAssistant || last.Content != "Plain answer." {
		t.Fatalf("the chat should carry on in a new conversation: %s", dump(v.Messages))
	}
	var prompts []string
	for _, e := range logs() {
		if e.method() == "session/prompt" {
			var p acpPromptRequest
			e.params(&p)
			prompts = append(prompts, p.Prompt[len(p.Prompt)-1].Text)
		}
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "This conversation started earlier") || !strings.Contains(prompts[1], "@plain first") {
		t.Errorf("the new conversation should carry what was said: %q", prompts)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestACPKeepsNoConversationWhosePromptTheAgentRejected(t *testing.T) {
	b, _, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	res, run, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@reject"})
	if err == nil || !strings.Contains(err.Error(), "the prompt was rejected") {
		t.Errorf("err = %v", err)
	}
	if res.SessionID != "" || res.Answered || res.Resumed {
		t.Errorf("a conversation that never took its first prompt isn't the chat's: %+v", res)
	}
	for _, e := range run.events {
		if e.Kind == EventSession {
			t.Errorf("the session was reported, so the chat would resume it without the PR: %+v", run.events)
		}
	}
}

func TestACPManagerDoesntStartOverAfterAResumedTurnFails(t *testing.T) {
	b, logs, _ := newACPTestBackend(t, "", config.ChatBackendSettings{})
	db, err := database.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := NewManager(Fixed{b})
	root := t.TempDir()
	m.Store = func() Store { return db }
	m.Dir = func() (string, error) { return root, nil }
	m.LoadPR = func(context.Context, string, string, int) (PRContext, error) { return testPR, nil }
	idle := make(chan struct{}, 4)
	m.OnChange = func(_ database.ChatSession, status string) {
		if status == StatusIdle {
			idle <- struct{}{}
		}
	}
	session, err := m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "@plain first"})
	if err != nil {
		t.Fatal(err)
	}
	first := waitView(t, m, session.ID, idle).Session.BackendSessionID
	if err := m.Send(context.Background(), session.ID, "@refusal second"); err != nil {
		t.Fatal(err)
	}
	v := waitView(t, m, session.ID, idle)
	if last := v.Messages[len(v.Messages)-1]; last.Role != RoleError || last.Content != "The agent refused to answer." {
		t.Errorf("the refusal should be shown: %s", dump(v.Messages))
	}
	if v.Session.BackendSessionID != first {
		t.Errorf("the conversation should be kept: %q, was %q", v.Session.BackendSessionID, first)
	}
	if m := methods(logs()); !slices.Equal(m, []string{"initialize", "session/new", "session/prompt", "session/prompt"}) {
		t.Errorf("the message should be sent once, to the same conversation: %v", m)
	}
}

func TestACPStopsAnAgentThatActsAfterARefusal(t *testing.T) {
	b, _, dir := newACPTestBackend(t, "", config.ChatBackendSettings{Name: "Fake"})
	_, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@refused-anyway"})
	if err == nil || !strings.Contains(err.Error(), `Stopped: Fake ran "make" after the server refused to let it.`) {
		t.Errorf("err = %v", err)
	}
}

func TestACPStopsAnUnaskedCallWhoseKindSaysNothing(t *testing.T) {
	for _, prompt := range []string{"@unasked-other", "@unasked-nokind"} {
		b, _, dir := newACPTestBackend(t, "", config.ChatBackendSettings{Name: "Fake"})
		_, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: prompt})
		if err == nil || !strings.Contains(err.Error(), `Stopped: Fake used "write_notes" without asking first.`) {
			t.Errorf("%s: err = %v", prompt, err)
		}
	}
}

func TestACPSaysWhatItRefusedByTheKindItWasAskedFor(t *testing.T) {
	b, _, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	_, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@refuse-kind"})
	if err == nil || err.Error() != `fake stopped when the server refused to let it run "make test": the chat is read-only.` {
		t.Errorf("err = %v", err)
	}
}

func TestACPStartsANewAgentForANewMode(t *testing.T) {
	b, logs, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	configured := b.Settings
	mode := ""
	b.Settings = func() config.ChatBackendSettings {
		s := configured()
		s.Mode = mode
		return s
	}
	first, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"})
	if err != nil {
		t.Fatal(err)
	}
	mode = "plan"
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain", SessionID: first.SessionID, Resume: true}); err == nil ||
		!strings.Contains(err.Error(), "can't pick up a conversation") {
		t.Errorf("a new Mode should take a new agent, which can't resume this one: %v", err)
	}
	if p := pids(logs()); len(p) != 2 {
		t.Errorf("pids = %v", p)
	}
}

func TestStopAgentsStopsTheKeptAgents(t *testing.T) {
	b, _, dir := newACPTestBackend(t, "", config.ChatBackendSettings{})
	if _, _, err := runTurn(t, b, Turn{WorkDir: dir, Prompt: "@plain"}); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	conn := b.conns[dir].conn
	b.mu.Unlock()
	StopAgents()
	select {
	case <-conn.done:
	default:
		t.Fatal("the kept agent should have exited")
	}
	running.mu.Lock()
	defer running.mu.Unlock()
	if len(running.conns) != 0 {
		t.Errorf("%d agents still running", len(running.conns))
	}
}
