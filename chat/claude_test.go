package chat

import (
	"context"
	"crs/config"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestClaudeInfoOffersTheDefaultFirst(t *testing.T) {
	b := &ClaudeBackend{
		Settings: func() config.ChatBackendSettings { return config.ChatBackendSettings{} },
		LookPath: func(string) (string, error) { return "/usr/bin/claude", nil },
	}
	info := b.Info()
	if !info.Available || info.ID != "claude" || info.Name != "Claude Code" {
		t.Fatalf("unexpected info: %+v", info)
	}
	var ids []string
	for _, m := range info.Models {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"", "opus", "sonnet", "haiku"}) {
		t.Errorf("models = %q", ids)
	}
	if info.Models[1].Name != "Opus" {
		t.Errorf("an alias should be capitalized, got %q", info.Models[1].Name)
	}
}

func TestClaudeInfoSaysWhyItIsUnavailable(t *testing.T) {
	missing := &ClaudeBackend{
		Settings: func() config.ChatBackendSettings { return config.ChatBackendSettings{} },
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
	}
	if info := missing.Info(); info.Available || !strings.Contains(info.Reason, "isn't installed") {
		t.Errorf("a missing program should be unavailable with a reason, got %+v", info)
	}

	disabled := &ClaudeBackend{
		Settings: func() config.ChatBackendSettings {
			return config.ChatBackendSettings{Disabled: true, Models: []string{"claude-opus-5-5"}, DefaultModel: "claude-opus-5-5"}
		},
		LookPath: func(string) (string, error) { return "/usr/bin/claude", nil },
	}
	info := disabled.Info()
	if info.Available || !strings.Contains(info.Reason, "disabled") {
		t.Errorf("a disabled backend should say so, got %+v", info)
	}
	if len(info.Models) != 2 || info.Models[1].Name != "claude-opus-5-5" || info.DefaultModel != "claude-opus-5-5" {
		t.Errorf("configured models should replace the built-in ones, got %+v", info)
	}
}

func TestClaudeArgs(t *testing.T) {
	b := &ClaudeBackend{}
	first := b.args(Turn{SessionID: "s-1", Model: "sonnet", SystemPrompt: "be brief", ReadDirs: []string{"/src/widgets"}})
	for _, want := range [][]string{
		{"-p"}, {"--output-format", "stream-json"}, {"--verbose"}, {"--include-partial-messages"},
		{"--session-id", "s-1"}, {"--model", "sonnet"}, {"--append-system-prompt", "be brief"},
		{"--add-dir", "/src/widgets"}, {"--allowedTools", "Read,Grep,Glob"},
	} {
		if !containsRun(first, want) {
			t.Errorf("first turn args %q lack %q", first, want)
		}
	}
	if slices.Contains(first, "--resume") {
		t.Errorf("a first turn must not resume: %q", first)
	}
	disallowed := first[slices.Index(first, "--disallowedTools")+1]
	for _, tool := range []string{"Bash", "Edit", "Write", "WebFetch"} {
		if !strings.Contains(disallowed, tool) {
			t.Errorf("%s should be disallowed, got %q", tool, disallowed)
		}
	}

	later := b.args(Turn{SessionID: "s-1", Resume: true})
	if !containsRun(later, []string{"--resume", "s-1"}) || slices.Contains(later, "--session-id") || slices.Contains(later, "--model") {
		t.Errorf("a later turn should resume and nothing else: %q", later)
	}
}

func containsRun(args, run []string) bool {
	for i := 0; i+len(run) <= len(args); i++ {
		if slices.Equal(args[i:i+len(run)], run) {
			return true
		}
	}
	return false
}

func TestClaudeStreamReadsTheConversationOnly(t *testing.T) {
	var events []Event
	s := newClaudeStream(func(e Event) { events = append(events, e) })
	s.dirs = []string{"/home/me/.crs/chats/c1", "/src/widgets"}
	input := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-9"}`,
		`not json at all`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}},"parent_tool_use_id":null}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Grep","input":{"pattern":"func Run","path":"/src/widgets/server"}}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"from a subagent"}]},"parent_tool_use_id":"t1"}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"denied"}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/home/me/.crs/chats/c1/pr.diff"}}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]},"parent_tool_use_id":null}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"sess-9","total_cost_usd":0.5,"result":"Hello"}`,
	}, "\n")
	if err := s.read(strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}

	want := []Event{
		{Kind: EventSession, SessionID: "sess-9"},
		{Kind: EventTextDelta, Text: "Hel"},
		{Kind: EventToolUse, ToolUseID: "t1", Tool: "Grep", Detail: "func Run in server"},
		{Kind: EventToolResult, ToolUseID: "t1", IsError: true},
		{Kind: EventToolUse, ToolUseID: "t2", Tool: "Read", Detail: "pr.diff"},
		{Kind: EventText, Text: "Hello"},
	}
	if !slices.Equal(events, want) {
		t.Errorf("events:\n got %+v\nwant %+v", events, want)
	}
	if !s.finished || s.failure != "" || s.result.SessionID != "sess-9" || s.result.CostUSD != 0.5 || !s.result.Answered {
		t.Errorf("unexpected end of stream: finished %v, failure %q, result %+v", s.finished, s.failure, s.result)
	}
}

func TestClaudeStreamReportsAFailedResult(t *testing.T) {
	s := newClaudeStream(func(Event) {})
	s.line([]byte(`{"type":"result","subtype":"error_max_turns","is_error":true}`))
	if !s.finished || !strings.Contains(s.failure, "turn limit") {
		t.Errorf("failure = %q", s.failure)
	}
}

func TestClaudeTurnRunsTheCommand(t *testing.T) {
	b := &ClaudeBackend{}
	runs := useFakeClaude(t, b, "ok")
	dir := t.TempDir()
	var events []Event
	res, err := b.Turn(context.Background(), Turn{WorkDir: dir, Prompt: "What does this do?", SessionID: "s-1"},
		func(e Event) { events = append(events, e) })
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if res.SessionID != "s-1" || res.CostUSD != 0.02 || !res.Answered {
		t.Errorf("result = %+v", res)
	}
	got := runs()
	if len(got) != 1 {
		t.Fatalf("expected one run, got %d", len(got))
	}
	if got[0].Stdin != "What does this do?" || got[0].flag("--session-id") != "s-1" {
		t.Errorf("run = %+v", got[0])
	}
	if mustEval(t, got[0].Dir) != mustEval(t, dir) {
		t.Errorf("ran in %q, want %q", got[0].Dir, dir)
	}
	var texts []string
	for _, e := range events {
		if e.Kind == EventText {
			texts = append(texts, e.Text)
		}
		if e.Kind == EventToolUse && e.Detail != DiffFile {
			t.Errorf("a read of the working directory's diff should show as %q, got %q", DiffFile, e.Detail)
		}
	}
	if !slices.Equal(texts, []string{"Looks fine."}) {
		t.Errorf("texts = %q", texts)
	}
}

func TestClaudeTurnFailures(t *testing.T) {
	for _, tt := range []struct {
		mode, want string
	}{
		{"crash", "something broke"},
		{"error-result", "API Error: overloaded"},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			b := &ClaudeBackend{}
			useFakeClaude(t, b, tt.mode)
			_, err := b.Turn(context.Background(), Turn{WorkDir: t.TempDir(), Prompt: "hi", SessionID: "s-1"}, func(Event) {})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestClaudeEnvDropsTheNestedSessionMarker(t *testing.T) {
	env := claudeEnv([]string{"PATH=/bin", "CLAUDECODE=1", "HOME=/home/me"})
	if !slices.Equal(env, []string{"PATH=/bin", "HOME=/home/me"}) {
		t.Errorf("env = %q", env)
	}
}

func mustEval(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
