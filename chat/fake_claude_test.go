package chat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"crs/config"
)

// The fake claude is this test binary run again: TestFakeClaude does nothing
// in a normal run, and plays Claude Code when CRS_FAKE_CLAUDE is set. Its
// command is the binary with "-test.run" picking that test and "--" ending the
// test flags, so the arguments a chat passes reach it untouched.
const (
	fakeClaudeEnv     = "CRS_FAKE_CLAUDE"      // the mode: ok, fail-resume, error-result, hang, crash
	fakeClaudeLogEnv  = "CRS_FAKE_CLAUDE_LOG"  // a file each run appends {args, stdin, cwd} to
	fakeClaudeTextEnv = "CRS_FAKE_CLAUDE_TEXT" // the reply; "Looks fine." by default
)

// fakeRun is what one run of the fake was given.
type fakeRun struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
	Dir   string   `json:"dir"`
}

func (r fakeRun) flag(name string) string {
	if i := slices.Index(r.Args, name); i >= 0 && i+1 < len(r.Args) {
		return r.Args[i+1]
	}
	return ""
}

func TestFakeClaude(t *testing.T) {
	mode := os.Getenv(fakeClaudeEnv)
	if mode == "" {
		return
	}
	args := os.Args
	if i := slices.Index(args, "--"); i >= 0 {
		args = args[i+1:]
	}
	stdin, _ := io.ReadAll(os.Stdin)
	dir, _ := os.Getwd()
	if log := os.Getenv(fakeClaudeLogEnv); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			line, _ := json.Marshal(fakeRun{Args: args, Stdin: string(stdin), Dir: dir})
			f.Write(append(line, '\n'))
			f.Close()
		}
	}
	session := fakeRun{Args: args}.flag("--session-id")
	if resume := (fakeRun{Args: args}).flag("--resume"); resume != "" {
		session = resume
		if mode == "fail-resume" {
			fmt.Fprintf(os.Stderr, "No conversation found with session ID: %s\n", resume)
			os.Exit(1)
		}
	}
	reply := os.Getenv(fakeClaudeTextEnv)
	if reply == "" {
		reply = "Looks fine."
	}
	out := bufio.NewWriter(os.Stdout)
	emit := func(v any) {
		line, _ := json.Marshal(v)
		out.Write(append(line, '\n'))
		out.Flush()
	}
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": session, "cwd": dir})
	if mode == "crash" {
		fmt.Fprintln(os.Stderr, "something broke")
		os.Exit(3)
	}
	if mode == "hang" {
		emit(map[string]any{"type": "stream_event", "session_id": session, "parent_tool_use_id": nil,
			"event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Let me"}}})
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	emit(map[string]any{"type": "assistant", "session_id": session, "parent_tool_use_id": nil, "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Read",
			"input": map[string]any{"file_path": filepath.Join(dir, DiffFile)}}}}})
	// A subagent's message, which the chat leaves out.
	emit(map[string]any{"type": "assistant", "session_id": session, "parent_tool_use_id": "toolu_x", "message": map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "subagent chatter"}}}})
	emit(map[string]any{"type": "user", "session_id": session, "parent_tool_use_id": nil, "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "1\tdiff"}}}})
	for _, piece := range strings.SplitAfter(reply, " ") {
		emit(map[string]any{"type": "stream_event", "session_id": session, "parent_tool_use_id": nil,
			"event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": piece}}})
	}
	emit(map[string]any{"type": "assistant", "session_id": session, "parent_tool_use_id": nil, "message": map[string]any{
		"content": []any{map[string]any{"type": "thinking", "thinking": "hmm"}, map[string]any{"type": "text", "text": reply}}}})
	if mode == "error-result" {
		emit(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "session_id": session,
			"result": "API Error: overloaded", "total_cost_usd": 0.01})
		os.Exit(1)
	}
	emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": session,
		"result": reply, "total_cost_usd": 0.02})
	os.Exit(0)
}

// useFakeClaude makes b run the fake in mode, logging its runs; it returns
// the log's reader.
func useFakeClaude(t *testing.T, b *ClaudeBackend, mode string) func() []fakeRun {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "runs.jsonl")
	t.Setenv(fakeClaudeEnv, mode)
	t.Setenv(fakeClaudeLogEnv, log)
	b.Settings = func() config.ChatBackendSettings {
		return config.ChatBackendSettings{Command: fmt.Sprintf("%q -test.run=^TestFakeClaude$ --", exe)}
	}
	b.LookPath = func(string) (string, error) { return exe, nil }
	return func() []fakeRun {
		data, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		var runs []fakeRun
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var r fakeRun
			if json.Unmarshal([]byte(line), &r) == nil {
				runs = append(runs, r)
			}
		}
		return runs
	}
}
