package chat

import (
	"bufio"
	"crs/config"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake ACP agent is this test binary run again, as the fake claude is:
// TestFakeACP does nothing in a normal run, and plays an ACP agent when
// CRS_FAKE_ACP is set to its flags. Every message it receives is appended to
// the CRS_FAKE_ACP_LOG file, with the process it went to.
//
// Its flags say what it offers: load (session/load), resume (session/resume),
// dirs (additionalDirectories), auth (sign in with the "key" method first),
// configopts (mode and model as config options rather than modes and models),
// nomodels (no choice of model at all). antigravity makes it answer as
// Antigravity's `agy acp` does: initialize names protocol version 2 in version
// 1's shape, with Antigravity's sign-in methods and system prompt, sessions
// start in its "auto" mode with its model option, and it logs (as "_env") the
// TMPDIR and GEMINI_API_KEY it was given, unpacking itself into the first.
// With agysignin it prints the link it signs in with over SSH as a session is
// opened, and never answers; with agynobrowser it says it needs signing in,
// and answers authenticate as it does without a browser. What a prompt does
// is picked by a word in it: @hang (wait for session/cancel), @ignore-cancel
// (never answer), @crash, @unasked (run a command without asking),
// @unasked-other (make a call of kind "other" without asking), @unasked-nokind
// (one of no kind), @refused-anyway (run a command it was refused),
// @refuse-quits (end the turn when a permission is refused), @refuse-kind (ask
// for a call announced without a kind, then give up), @refusal (refuse to
// answer, saying nothing), @reject (fail the prompt before working on it),
// @plain (just answer), @agy-tools (use Antigravity's own tools, as it reports
// them), @agy-question (ask the reviewer a question, as Antigravity does, then
// stop), @agy-schedule (schedule a wake-up without asking); otherwise the full
// flow of reading, asking and answering.
const (
	fakeACPEnv    = "CRS_FAKE_ACP"
	fakeACPLogEnv = "CRS_FAKE_ACP_LOG"
)

// fakeACPEntry is one message the fake received.
type fakeACPEntry struct {
	PID int             `json:"pid"`
	Msg json.RawMessage `json:"msg"`
}

func (e fakeACPEntry) method() string {
	var m struct {
		Method string `json:"method"`
	}
	json.Unmarshal(e.Msg, &m)
	return m.Method
}

func (e fakeACPEntry) params(v any) {
	var m struct {
		Params json.RawMessage `json:"params"`
	}
	json.Unmarshal(e.Msg, &m)
	json.Unmarshal(m.Params, v)
}

// response is the answer to the fake's request id, as received.
func response(entries []fakeACPEntry, id string) (result json.RawMessage, errMsg string, ok bool) {
	for _, e := range entries {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *acpError       `json:"error"`
		}
		json.Unmarshal(e.Msg, &m)
		if m.Method != "" || string(m.ID) != fmt.Sprintf("%q", id) {
			continue
		}
		if m.Error != nil {
			return nil, m.Error.Message, true
		}
		return m.Result, "", true
	}
	return nil, "", false
}

func TestFakeACP(t *testing.T) {
	flags := os.Getenv(fakeACPEnv)
	if flags == "" {
		return
	}
	f := &fakeACP{flags: strings.Split(flags, ","), sessions: map[string]bool{}, waiters: map[string]chan json.RawMessage{}}
	f.run()
	os.Exit(0)
}

type fakeACP struct {
	flags    []string
	writeMu  sync.Mutex
	mu       sync.Mutex
	sessions map[string]bool
	waiters  map[string]chan json.RawMessage
	nextReq  int
	nextSess int
	signedIn bool
	cancel   chan struct{}
	cwd      string
}

func (f *fakeACP) has(flag string) bool { return slices.Contains(f.flags, flag) }

func (f *fakeACP) send(v any) {
	line, _ := json.Marshal(v)
	f.writeMu.Lock()
	os.Stdout.Write(append(line, '\n'))
	f.writeMu.Unlock()
}

func (f *fakeACP) log(raw []byte) {
	path := os.Getenv(fakeACPLogEnv)
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	line, _ := json.Marshal(fakeACPEntry{PID: os.Getpid(), Msg: json.RawMessage(raw)})
	file.Write(append(line, '\n'))
}

func (f *fakeACP) run() {
	// A line on stdout that isn't JSON-RPC, as some agents print.
	fmt.Println("fake agent starting")
	if f.has("antigravity") {
		// What it was given, and its unpacking, as a one-file bundle does.
		tmp := os.Getenv("TMPDIR")
		if tmp != "" {
			os.MkdirAll(filepath.Join(tmp, "_MEIfake"), 0o755)
			os.WriteFile(filepath.Join(tmp, "_MEIfake", "agy"), []byte("1 GB"), 0o644)
		}
		env, _ := json.Marshal(map[string]any{"method": "_env", "params": map[string]string{
			"TMPDIR": tmp, "GEMINI_API_KEY": os.Getenv("GEMINI_API_KEY"), "AGY_EXTRA": os.Getenv("AGY_EXTRA"),
		}})
		f.log(env)
	}
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			raw := []byte(strings.TrimSpace(string(line)))
			f.log(raw)
			f.handle(raw)
		}
		if err != nil {
			return
		}
	}
}

func (f *fakeACP) reply(id json.RawMessage, result any) {
	f.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (f *fakeACP) fail(id json.RawMessage, code int, msg string) {
	f.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

func (f *fakeACP) handle(raw []byte) {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	if m.Method == "" {
		var id string
		json.Unmarshal(m.ID, &id)
		f.mu.Lock()
		ch := f.waiters[id]
		delete(f.waiters, id)
		f.mu.Unlock()
		if ch != nil {
			ch <- raw
		}
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
		MethodID  string `json:"methodId"`
		ModelID   string `json:"modelId"`
	}
	json.Unmarshal(m.Params, &p)
	switch m.Method {
	case "initialize":
		if f.has("antigravity") {
			f.reply(m.ID, map[string]any{
				"protocolVersion": 2,
				"agentCapabilities": map[string]any{
					"loadSession":         true,
					"sessionCapabilities": map[string]any{"list": map[string]any{}, "resume": map[string]any{}},
					"_meta":               map[string]any{"antigravity": map[string]any{"systemPrompt": map[string]any{"append": true}}},
				},
				"authMethods": []any{
					map[string]any{"id": "oauth-personal", "name": "Log in with Google", "description": "Log in with your Google account"},
					map[string]any{"id": "gemini-api-key", "name": "Gemini API key", "description": "Use an API key with Gemini Developer API"},
				},
				"agentInfo": map[string]any{"name": "Antigravity ACP Server", "title": "Google Antigravity", "version": "1.5.12"},
			})
			return
		}
		caps := map[string]any{}
		session := map[string]any{}
		if f.has("load") {
			caps["loadSession"] = true
		}
		if f.has("resume") {
			session["resume"] = map[string]any{}
		}
		if f.has("dirs") {
			session["additionalDirectories"] = map[string]any{}
		}
		caps["sessionCapabilities"] = session
		f.reply(m.ID, map[string]any{
			"protocolVersion": 1, "agentCapabilities": caps,
			"authMethods": []any{map[string]any{"id": "key", "name": "API key", "description": "Run `fake login` in a terminal"}},
			"agentInfo":   map[string]any{"name": "fake", "version": "1"},
		})
	case "authenticate":
		if f.has("agynobrowser") {
			f.fail(m.ID, -32603, "No browser is available here. Run `agy acp --login` in a terminal to sign in.")
			return
		}
		if p.MethodID != "key" {
			f.fail(m.ID, -32602, "unknown method")
			return
		}
		f.signedIn = true
		f.reply(m.ID, map[string]any{})
	case "session/new":
		if f.has("agysignin") {
			// Over SSH it signs in with a link, and waits for a code typed
			// into its stdin, which on ACP is the client's.
			fmt.Println("Open the following link to authenticate the ACP server: https://accounts.google.com/o/oauth2/auth?response_type=code&state=s&redirect_uri=https%3A%2F%2Fantigravity.google%2Foauth-callback")
			return
		}
		if (f.has("auth") || f.has("agynobrowser")) && !f.signedIn {
			f.fail(m.ID, -32000, "Authentication required")
			return
		}
		f.mu.Lock()
		f.nextSess++
		id := fmt.Sprintf("sess-%d-%d", os.Getpid(), f.nextSess)
		f.sessions[id] = true
		f.cwd = p.Cwd
		f.mu.Unlock()
		f.reply(m.ID, f.sessionState(map[string]any{"sessionId": id}))
	case "session/load", "session/resume":
		f.mu.Lock()
		f.sessions[p.SessionID] = true
		f.cwd = p.Cwd
		f.mu.Unlock()
		if m.Method == "session/load" {
			// The replay, which the chat must not take for an answer.
			f.update(p.SessionID, map[string]any{"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": "old question"}})
			f.update(p.SessionID, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "old answer"}})
		}
		f.reply(m.ID, f.sessionState(map[string]any{}))
	case "session/set_mode", "session/set_model", "session/set_config_option":
		f.reply(m.ID, map[string]any{})
	case "session/cancel":
		f.mu.Lock()
		if f.cancel != nil {
			close(f.cancel)
			f.cancel = nil
		}
		f.mu.Unlock()
	case "session/prompt":
		f.mu.Lock()
		known := f.sessions[p.SessionID]
		f.cancel = make(chan struct{})
		cancel := f.cancel
		f.mu.Unlock()
		if !known {
			f.fail(m.ID, -32002, "no such session: "+p.SessionID)
			return
		}
		var prompt struct {
			Prompt []acpContent `json:"prompt"`
		}
		json.Unmarshal(m.Params, &prompt)
		var text string
		for _, c := range prompt.Prompt {
			text += c.Text
		}
		if strings.Contains(text, "@reject") {
			// What opening the session says, then no to the prompt.
			f.update(p.SessionID, map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{}})
			f.fail(m.ID, -32603, "Internal error: the prompt was rejected")
			return
		}
		go f.prompt(m.ID, p.SessionID, text, cancel)
	default:
		if len(m.ID) > 0 {
			f.fail(m.ID, -32601, "unknown method "+m.Method)
		}
	}
}

func (f *fakeACP) sessionState(state map[string]any) map[string]any {
	switch {
	case f.has("antigravity"):
		// Its own "auto" mode acts unasked.
		state["modes"] = map[string]any{"currentModeId": "auto", "availableModes": []any{
			map[string]any{"id": "default", "name": "Default"}, map[string]any{"id": "auto", "name": "Auto"}}}
		state["configOptions"] = []any{
			map[string]any{"id": "model", "name": "Model", "type": "select", "currentValue": "gemini-flash",
				"options": []any{map[string]any{"value": "gemini-flash", "name": "Gemini Flash"}, map[string]any{"value": "claude-sonnet", "name": "Claude Sonnet"}}},
			map[string]any{"id": "thinking", "name": "Thinking", "category": "thought_level", "type": "select", "currentValue": "high",
				"options": []any{map[string]any{"value": "low", "name": "Low"}, map[string]any{"value": "high", "name": "High"}}},
		}
	case f.has("nomodels"):
	case f.has("configopts"):
		state["configOptions"] = []any{
			map[string]any{"id": "approval", "name": "Mode", "category": "mode", "type": "select", "currentValue": "ask",
				"options": []any{map[string]any{"value": "ask", "name": "Ask"}, map[string]any{"value": "plan", "name": "Plan"}}},
			map[string]any{"id": "llm", "name": "Model", "category": "model", "type": "select", "currentValue": "small",
				"options": []any{map[string]any{"group": "g", "name": "Group", "options": []any{
					map[string]any{"value": "small", "name": "Small"}, map[string]any{"value": "big", "name": "Big"}}}}},
		}
	default:
		state["modes"] = map[string]any{"currentModeId": "default", "availableModes": []any{
			map[string]any{"id": "default", "name": "Default"}, map[string]any{"id": "plan", "name": "Plan"}}}
		state["models"] = map[string]any{"currentModelId": "small", "availableModels": []any{
			map[string]any{"modelId": "small", "name": "Small"}, map[string]any{"modelId": "big", "name": "Big"}}}
	}
	return state
}

func (f *fakeACP) update(session string, u map[string]any) {
	f.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": session, "update": u}})
}

func (f *fakeACP) chunk(session, text string) {
	f.update(session, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}})
}

// request asks the client something and waits for its answer.
func (f *fakeACP) request(method string, params any) {
	f.mu.Lock()
	f.nextReq++
	id := fmt.Sprintf("r%d", f.nextReq)
	ch := make(chan json.RawMessage, 1)
	f.waiters[id] = ch
	f.mu.Unlock()
	f.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
	}
}

func (f *fakeACP) prompt(id json.RawMessage, session, text string, cancel chan struct{}) {
	end := func(reason string) { f.reply(id, map[string]any{"stopReason": reason}) }
	waitCancel := func() {
		select {
		case <-cancel:
			end("cancelled")
		case <-time.After(30 * time.Second):
			end("end_turn")
		}
	}
	tool := func(u map[string]any) {
		u["sessionUpdate"] = "tool_call"
		f.update(session, u)
	}
	permission := func(callID, kind, title string) {
		call := map[string]any{"toolCallId": callID}
		if kind != "" {
			call["kind"], call["title"] = kind, title
		}
		f.request("session/request_permission", map[string]any{"sessionId": session, "toolCall": call, "options": []any{
			map[string]any{"optionId": "always", "name": "Always", "kind": "allow_always"},
			map[string]any{"optionId": "once", "name": "Allow", "kind": "allow_once"},
			map[string]any{"optionId": "no", "name": "Reject", "kind": "reject_once"},
		}})
	}
	switch {
	case strings.Contains(text, "@ignore-cancel"):
		f.chunk(session, "Thinking")
		select {}
	case strings.Contains(text, "@hang"):
		f.chunk(session, "Let me")
		waitCancel()
	case strings.Contains(text, "@crash"):
		fmt.Fprintln(os.Stderr, "something broke")
		os.Exit(3)
	case strings.Contains(text, "@unasked-other"):
		tool(map[string]any{"toolCallId": "o1", "title": "write_notes", "kind": "other", "status": "completed"})
		waitCancel()
	case strings.Contains(text, "@unasked-nokind"):
		tool(map[string]any{"toolCallId": "o1", "title": "write_notes", "status": "completed"})
		waitCancel()
	case strings.Contains(text, "@unasked"):
		tool(map[string]any{"toolCallId": "x1", "title": "rm -rf build", "kind": "execute", "status": "completed"})
		waitCancel()
	case strings.Contains(text, "@refused-anyway"):
		tool(map[string]any{"toolCallId": "m1", "title": "make", "kind": "execute", "status": "pending"})
		permission("m1", "", "")
		f.update(session, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "m1", "status": "completed"})
		waitCancel()
	case strings.Contains(text, "@refuse-kind"):
		tool(map[string]any{"toolCallId": "m1", "title": "make test", "status": "pending"})
		permission("m1", "execute", "make test")
		end("cancelled")
	case strings.Contains(text, "@refusal"):
		end("refusal")
	case strings.Contains(text, "@refuse-quits"):
		tool(map[string]any{"toolCallId": "m1", "title": "make", "kind": "execute", "status": "pending"})
		permission("m1", "", "")
		end("cancelled")
	case strings.Contains(text, "@plain"):
		f.chunk(session, "Plain answer.")
		end("end_turn")
	case strings.Contains(text, "@agy-tools"):
		// Its checklist, a directory listed and a subagent, unasked and of
		// kind "other"; a file read through the client, asked about.
		diff := filepath.Join(f.cwd, DiffFile)
		tool(map[string]any{"toolCallId": "s1", "title": "Run update_steps", "kind": "other", "status": "in_progress",
			"rawInput": map[string]any{"steps": []any{
				map[string]any{"title": "Read the diff", "status": "in_progress"}, map[string]any{"title": "Answer", "status": "pending"}}}})
		f.update(session, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "s1", "status": "completed"})
		tool(map[string]any{"toolCallId": "l1", "title": "Run list_dir", "kind": "other", "status": "in_progress",
			"rawInput": map[string]any{"DirectoryPath": f.cwd + "/"}})
		f.update(session, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "l1", "status": "completed"})
		tool(map[string]any{"toolCallId": "v1", "title": "Running client_view_file", "kind": "read", "status": "pending",
			"rawInput": map[string]any{"absolute_path": diff}, "locations": []any{map[string]any{"path": diff}}})
		f.request("session/request_permission", map[string]any{"sessionId": session, "toolCall": map[string]any{
			"toolCallId": "v1", "title": "Run client_view_file?", "kind": "read", "rawInput": map[string]any{"absolute_path": diff}},
			"options": []any{
				map[string]any{"optionId": "proceed_once", "name": "Allow", "kind": "allow_once"},
				map[string]any{"optionId": "cancel", "name": "Deny", "kind": "reject_once"},
			}})
		f.request("fs/read_text_file", map[string]any{"sessionId": "subagent-conversation", "path": diff})
		f.update(session, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "v1", "status": "completed"})
		tool(map[string]any{"toolCallId": "a1", "title": "Run invoke_subagent", "kind": "other", "status": "in_progress",
			"rawInput": map[string]any{"Role": "Code explorer", "Prompt": "Find the callers"}})
		f.update(session, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "a1", "status": "completed"})
		// A command, asked about and refused.
		tool(map[string]any{"toolCallId": "c1", "title": "run_command", "kind": "execute", "status": "pending",
			"rawInput": map[string]any{"CommandLine": "go test ./...", "Cwd": f.cwd}})
		f.request("session/request_permission", map[string]any{"sessionId": session, "toolCall": map[string]any{
			"toolCallId": "c1", "title": "run_command", "kind": "execute"},
			"options": []any{
				map[string]any{"optionId": "proceed_once", "name": "Allow", "kind": "allow_once"},
				map[string]any{"optionId": "cancel", "name": "Deny", "kind": "reject_once"},
			}})
		f.update(session, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "c1", "status": "failed"})
		f.chunk(session, "It looks fine.")
		f.update(session, map[string]any{"sessionUpdate": "usage_update", "used": 10, "size": 100})
		end("end_turn")
	case strings.Contains(text, "@agy-question"):
		f.chunk(session, "Before I answer:")
		f.request("session/request_permission", map[string]any{"sessionId": session, "toolCall": map[string]any{
			"toolCallId": "interaction_9960062f", "status": "pending", "title": "Which file should I look at first?", "rawInput": map[string]any{}},
			"options": []any{
				map[string]any{"optionId": "1", "name": "server/chat.go", "kind": "allow_once"},
				map[string]any{"optionId": "2", "name": "chat/acp.go", "kind": "allow_once"},
			}})
		end("cancelled")
	case strings.Contains(text, "@agy-schedule"):
		tool(map[string]any{"toolCallId": "w1", "title": "Run schedule", "kind": "other", "status": "completed",
			"rawInput": map[string]any{"DurationSeconds": 30, "Prompt": "Check again."}})
		waitCancel()
	default:
		diff := filepath.Join(f.cwd, DiffFile)
		f.chunk(session, "")
		f.chunk(session, "Let me ")
		f.chunk(session, "look.")
		tool(map[string]any{"toolCallId": "t1", "title": "Read File", "kind": "read", "status": "pending"})
		tool(map[string]any{"toolCallId": "t1", "title": "Read " + diff, "kind": "read", "status": "pending",
			"locations": []any{map[string]any{"path": diff}}})
		f.request("fs/read_text_file", map[string]any{"sessionId": session, "path": diff, "line": 1, "limit": 1})
		f.request("fs/read_text_file", map[string]any{"sessionId": session, "path": "/etc/hostname"})
		permission("t2", "execute", "make test")
		permission("t1", "", "")
		f.request("fs/write_text_file", map[string]any{"sessionId": session, "path": filepath.Join(f.cwd, "x"), "content": "x"})
		f.update(session, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed"})
		f.update(session, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "hmm"}})
		f.chunk(session, "Looks ")
		f.chunk(session, "fine.")
		end("end_turn")
	}
}

// useFakeACP makes b run the fake with flags, logging what it receives; it
// returns the log's reader.
func useFakeACP(t *testing.T, b *ACPBackend, flags string, settings config.ChatBackendSettings) func() []fakeACPEntry {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "acp.jsonl")
	t.Setenv(fakeACPEnv, "on,"+flags) // never empty, which would leave the fake inert
	t.Setenv(fakeACPLogEnv, log)
	settings.Command = fmt.Sprintf("%q -test.run=^TestFakeACP$ --", exe)
	settings.Protocol = config.ChatProtocolACP
	b.Settings = func() config.ChatBackendSettings { return settings }
	b.LookPath = func(string) (string, error) { return exe, nil }
	t.Cleanup(b.ReleaseAll)
	return func() []fakeACPEntry {
		data, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		var entries []fakeACPEntry
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var e fakeACPEntry
			if json.Unmarshal([]byte(line), &e) == nil {
				entries = append(entries, e)
			}
		}
		return entries
	}
}

// methods are the methods of the messages logged, in order, less what the
// fake logged of itself ("_env").
func methods(entries []fakeACPEntry) []string {
	var out []string
	for _, e := range entries {
		if m := e.method(); m != "" && !strings.HasPrefix(m, "_") {
			out = append(out, m)
		}
	}
	return out
}

// pids are the processes the messages went to, in order of first sight.
func pids(entries []fakeACPEntry) []int {
	var out []int
	for _, e := range entries {
		if !slices.Contains(out, e.PID) {
			out = append(out, e.PID)
		}
	}
	return out
}
