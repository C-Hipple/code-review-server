package chat

import (
	"context"
	"crs/config"
	"crs/subprocess"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ACPBackend drives any agent that speaks the Agent Client Protocol
// (https://agentclientprotocol.com) — Gemini CLI, claude-code-acp, opencode,
// goose and the rest — over JSON-RPC on the agent's stdin and stdout. It is the
// config.ChatProtocolACP protocol, run from a [Chat.Agents.<id>] table whose
// Command starts the agent speaking ACP.
//
// A chat gets an agent process of its own, started in the chat's working
// directory on its first turn and kept for the next one for IdleTimeout. A
// turn is a session/prompt on the chat's session: session/new opens it (the
// agent names it, and the Manager keeps that), and a process started later
// picks it up again with session/resume or session/load, whichever the agent
// offers. An agent that offers neither can't, and the Manager starts a new
// conversation carrying what was said.
//
// The agent is kept read-only, as Claude Code's chats are, as far as ACP lets
// a client: the server tells it it can read files (fs/read_text_file, under
// the chat's working directory and the PR's checkout only) but not write them
// or run terminals, and answers its permission requests itself — yes to
// reading, searching and thinking, no to anything else. A client can only
// refuse what an agent asks about, so an agent that makes any other call — an
// edit, a command, a fetch, one whose kind says nothing — without asking, or
// after it was refused, has its turn stopped when the call is reported done,
// with a message saying to configure it to ask.
type ACPBackend struct {
	AgentID string
	// Settings returns the agent's [Chat.Agents] table as config has it now.
	Settings func() config.ChatBackendSettings
	// LookPath finds the program; tests replace it.
	LookPath func(string) (string, error)
	// IdleTimeout is how long an agent is kept running after a turn, for its
	// chat's next one; zero means acpIdleTimeout.
	IdleTimeout time.Duration

	mu    sync.Mutex
	conns map[string]*acpEntry // by the chat's working directory
}

// acpEntry is a chat's agent: running a turn (busy) or waiting for the next.
type acpEntry struct {
	conn  *acpConn
	busy  bool
	drop  bool // close it once its turn ends
	timer *time.Timer
}

const (
	// acpProtocolVersion is the ACP version the server speaks.
	acpProtocolVersion = 1
	// acpIdleTimeout is how long a chat's agent waits for its next turn.
	acpIdleTimeout = 10 * time.Minute
	// acpSetupTimeout bounds each request that sets a turn up: starting the
	// agent, opening its session, choosing its mode and model.
	acpSetupTimeout = 2 * time.Minute
	// acpMaxFileBytes caps a file the agent asks the server to read.
	acpMaxFileBytes = 16 << 20
)

// Variables, so tests can shorten them.
var (
	// acpCancelGrace is how long a stopped turn waits for the agent to
	// acknowledge session/cancel before it is killed.
	acpCancelGrace = 10 * time.Second
	// acpCloseGrace is how long an agent whose stdin was closed has to exit.
	acpCloseGrace = 3 * time.Second
)

// JSON-RPC and ACP error codes.
const (
	acpErrInvalidParams    = -32602
	acpErrMethodNotFound   = -32601
	acpErrInternal         = -32603
	acpErrAuthRequired     = -32000
	acpErrResourceNotFound = -32002
)

// NewACPBackend is the ACP agent with the given ID, reading its settings from
// settings.
func NewACPBackend(id string, settings func() config.ChatBackendSettings) *ACPBackend {
	return &ACPBackend{AgentID: id, Settings: settings, LookPath: exec.LookPath}
}

func (b *ACPBackend) ID() string { return b.AgentID }

func (b *ACPBackend) settings() config.ChatBackendSettings {
	if b.Settings == nil {
		return config.ChatBackendSettings{}
	}
	return b.Settings()
}

func (b *ACPBackend) name(s config.ChatBackendSettings) string { return agentName(s, b.ID()) }

// argv is the configured command line, split like a shell would.
func (b *ACPBackend) argv(s config.ChatBackendSettings) ([]string, error) {
	command := strings.TrimSpace(s.Command)
	if command == "" {
		return nil, fmt.Errorf("no Command is set: set %s Command to the command that starts the agent speaking ACP", config.ChatTable(b.ID()))
	}
	argv, err := subprocess.SplitCommand(command)
	if err == nil && len(argv) == 0 {
		err = errors.New("it names no program")
	}
	return argv, err
}

func (b *ACPBackend) Info() BackendInfo {
	s := b.settings()
	table := config.ChatTable(b.ID())
	info := BackendInfo{ID: b.ID(), Name: b.name(s), DefaultModel: s.DefaultModel, Models: modelChoices(s, nil)}
	switch argv, err := b.argv(s); {
	case s.Disabled:
		info.Reason = "it is disabled in the config (" + table + " Disabled)"
	case err != nil:
		info.Reason = "its command can't be read: " + err.Error()
	default:
		lookPath := b.LookPath
		if lookPath == nil {
			lookPath = exec.LookPath
		}
		if _, err := lookPath(argv[0]); err != nil {
			info.Reason = fmt.Sprintf("%s isn't installed where the server can find it (set %s Command to its path)", argv[0], table)
		} else {
			info.Available = true
		}
	}
	return info
}

func (b *ACPBackend) Turn(ctx context.Context, t Turn, emit func(Event)) (TurnResult, error) {
	s := b.settings()
	argv, err := b.argv(s)
	if err != nil {
		return TurnResult{}, fmt.Errorf("the %s Command can't be read: %w", config.ChatTable(b.ID()), err)
	}
	conn, err := b.acquire(ctx, s, t.WorkDir, argv, agentEnv(os.Environ(), s.Env))
	if err != nil {
		return TurnResult{}, err
	}
	defer b.park(t.WorkDir, conn)

	turn := newACPTurn(b.name(s), emit, t)
	conn.attach(turn)
	res, err := b.turn(ctx, conn, turn, t, s)
	conn.attach(nil)
	// What the agent wrote after its last tool call is the reply's last block.
	turn.flush()
	res.Answered = turn.answered
	// The conversation is the chat's to resume once the agent has taken the
	// prompt: a new one whose first prompt it never took doesn't have the PR.
	if turn.accepted {
		res.SessionID = turn.session
	}
	return res, err
}

// turn opens the chat's session on conn and sends the prompt.
func (b *ACPBackend) turn(ctx context.Context, conn *acpConn, turn *acpTurn, t Turn, s config.ChatBackendSettings) (TurnResult, error) {
	sessionID, err := b.openSession(ctx, conn, t, s)
	if err != nil {
		return TurnResult{}, err
	}
	// A conversation picked up again is the chat's already, so a failure from
	// here on isn't a failure to resume. A new one becomes the chat's once the
	// agent takes the prompt (accept): one it never takes leaves the chat to
	// start over, carrying the PR again.
	res := TurnResult{Resumed: t.Resume}
	conn.locked(func() {
		turn.session, turn.quiet, turn.accepted = sessionID, false, t.Resume
	})

	// ACP has no system prompt for a client to add to, so a new
	// conversation's first prompt carries it.
	var prompt []acpContent
	if !t.Resume && strings.TrimSpace(t.SystemPrompt) != "" {
		prompt = append(prompt, acpText(t.SystemPrompt))
	}
	prompt = append(prompt, acpText(t.Prompt))
	ch, err := conn.start("session/prompt", acpPromptRequest{SessionID: sessionID, Prompt: prompt})
	if err != nil {
		return res, err
	}
	var r acpResponse
	cancel := func() {
		_ = conn.notify("session/cancel", acpSessionRef{SessionID: sessionID})
		select {
		case <-ch:
		case <-time.After(acpCancelGrace):
			conn.kill()
		}
	}
	select {
	case r = <-ch:
	case <-ctx.Done():
		cancel()
		return res, ctx.Err()
	case violation := <-turn.stop:
		cancel()
		return res, errors.New(violation)
	}
	var pr acpPromptResponse
	if err := r.decode(&pr); err != nil {
		return res, b.describe(conn, s, err)
	}
	conn.locked(turn.accept)
	switch pr.StopReason {
	case "end_turn", "":
		return res, nil
	case "cancelled":
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		var refused string
		conn.locked(func() { refused = turn.refused })
		if refused != "" {
			return res, fmt.Errorf("%s stopped when the server refused to let it %s: the chat is read-only.", b.name(s), refused)
		}
		return res, errors.New("The agent cancelled its answer.")
	case "max_tokens":
		return res, errors.New("The agent stopped: it reached its token limit.")
	case "max_turn_requests":
		return res, errors.New("The agent stopped: it reached its turn limit.")
	case "refusal":
		return res, errors.New("The agent refused to answer.")
	}
	return res, fmt.Errorf("The agent stopped (%s).", pr.StopReason)
}

// openSession makes the chat's session the one conn has open: a new one, or
// the one the turn resumes, picked up again when this process hasn't had it.
// It returns the session's ID.
func (b *ACPBackend) openSession(ctx context.Context, conn *acpConn, t Turn, s config.ChatBackendSettings) (string, error) {
	if t.Resume && t.SessionID != "" && conn.sessionID == t.SessionID {
		return t.SessionID, nil // open since an earlier turn, mode and model chosen
	}
	name := b.name(s)
	var dirs []string
	if conn.caps.SessionCapabilities.AdditionalDirectories != nil {
		dirs = t.ReadDirs
	}
	var (
		sessionID string
		state     acpSessionState
	)
	switch {
	case !t.Resume:
		var r acpNewSessionResponse
		err := b.signedIn(ctx, conn, s, func() error {
			return setupCall(ctx, conn, "session/new", acpNewSessionRequest{Cwd: t.WorkDir, AdditionalDirectories: dirs, McpServers: []any{}}, &r)
		})
		if err != nil {
			return "", b.failed(conn, s, "start a conversation", err)
		}
		if r.SessionID == "" {
			return "", fmt.Errorf("%s started a conversation without naming it", name)
		}
		sessionID, state = r.SessionID, r.acpSessionState
	case t.SessionID == "":
		return "", fmt.Errorf("there is no conversation with %s to resume", name)
	case conn.caps.SessionCapabilities.Resume != nil || conn.caps.LoadSession:
		method := "session/load" // the agent replays the conversation, which the turn, quiet until the prompt, drops
		if conn.caps.SessionCapabilities.Resume != nil {
			method = "session/resume"
		}
		var r acpSessionState
		err := b.signedIn(ctx, conn, s, func() error {
			return setupCall(ctx, conn, method, acpLoadSessionRequest{SessionID: t.SessionID, Cwd: t.WorkDir, AdditionalDirectories: dirs, McpServers: []any{}}, &r)
		})
		if err != nil {
			return "", b.failed(conn, s, "pick up the conversation again", err)
		}
		sessionID, state = t.SessionID, r
	default:
		return "", fmt.Errorf("%s can't pick up a conversation once it has exited: it offers neither session/load nor session/resume", name)
	}
	conn.sessionID = sessionID
	if mode := strings.TrimSpace(s.Mode); mode != "" {
		if err := b.choose(ctx, conn, s, sessionID, state, "mode", mode); err != nil {
			return "", err
		}
	}
	if model := strings.TrimSpace(t.Model); model != "" {
		if err := b.choose(ctx, conn, s, sessionID, state, "model", model); err != nil {
			return "", err
		}
	}
	return sessionID, nil
}

// choose sets the session's mode or model: through the agent's config option
// of that category when it has one, else its older session/set_mode or
// session/set_model.
func (b *ACPBackend) choose(ctx context.Context, conn *acpConn, s config.ChatBackendSettings, sessionID string, state acpSessionState, category, value string) error {
	name := b.name(s)
	var (
		method  string
		params  any
		choices []string
	)
	for _, opt := range state.ConfigOptions {
		if opt.Category != category {
			continue
		}
		method, params = "session/set_config_option", acpSetConfigOptionRequest{SessionID: sessionID, ConfigID: opt.ID, Value: value}
		choices = opt.values()
		break
	}
	if method == "" {
		switch {
		case category == "mode" && state.Modes != nil:
			method, params = "session/set_mode", acpSetModeRequest{SessionID: sessionID, ModeID: value}
			for _, m := range state.Modes.AvailableModes {
				choices = append(choices, m.ID)
			}
		case category == "model" && state.Models != nil:
			method, params = "session/set_model", acpSetModelRequest{SessionID: sessionID, ModelID: value}
			for _, m := range state.Models.AvailableModels {
				choices = append(choices, m.ModelID)
			}
		default:
			setting := "Mode"
			if category == "model" {
				setting = "Models and DefaultModel"
			}
			return fmt.Errorf("%s doesn't let a client choose its %s: take %s out of %s", name, category, setting, config.ChatTable(b.ID()))
		}
	}
	if len(choices) > 0 && !slices.Contains(choices, value) {
		return fmt.Errorf("%s has no %s %q; it offers %s", name, category, value, strings.Join(choices, ", "))
	}
	if err := setupCall(ctx, conn, method, params, nil); err != nil {
		return b.failed(conn, s, fmt.Sprintf("switch to the %s %q", category, value), err)
	}
	return nil
}

// signedIn runs open, and when the agent says it needs signing in first,
// signs in with the configured AuthMethod and runs it again.
func (b *ACPBackend) signedIn(ctx context.Context, conn *acpConn, s config.ChatBackendSettings, open func() error) error {
	err := open()
	var rpcErr *acpError
	if !errors.As(err, &rpcErr) || rpcErr.Code != acpErrAuthRequired {
		return err
	}
	method := strings.TrimSpace(s.AuthMethod)
	if method == "" {
		return err // describe says how to sign in
	}
	if err := setupCall(ctx, conn, "authenticate", acpAuthenticateRequest{MethodID: method}, nil); err != nil {
		return fmt.Errorf("signing in with %q: %w", method, err)
	}
	return open()
}

// failed says the agent couldn't do what, unless it needs signing in, which
// describe explains instead.
func (b *ACPBackend) failed(conn *acpConn, s config.ChatBackendSettings, what string, err error) error {
	if d := b.describe(conn, s, err); d != err {
		return d
	}
	return fmt.Errorf("%s couldn't %s: %w", b.name(s), what, err)
}

// describe adds to an error from the agent what the reviewer can do about
// it: for a request the agent refused until it is signed in, how to sign in.
// Any other error comes back as it is.
func (b *ACPBackend) describe(conn *acpConn, s config.ChatBackendSettings, err error) error {
	var rpcErr *acpError
	if !errors.As(err, &rpcErr) || rpcErr.Code != acpErrAuthRequired {
		return err
	}
	var methods, ids []string
	for _, m := range conn.authMethods {
		ids = append(ids, m.ID)
		method := strings.TrimSpace(m.Name)
		if method == "" {
			method = m.ID
		}
		if d := strings.TrimSpace(m.Description); d != "" {
			method += ": " + d
		}
		methods = append(methods, method)
	}
	msg := b.name(s) + " needs signing in first"
	if len(methods) > 0 {
		msg += " (" + strings.Join(methods, "; ") + ")"
	}
	msg += ". Sign in by running it in a terminal"
	if len(ids) > 0 {
		msg += fmt.Sprintf(", or set %s AuthMethod to one of: %s", config.ChatTable(b.ID()), strings.Join(ids, ", "))
	}
	return errors.New(msg + ".")
}

// setupCall is a request that sets a turn up, bounded by acpSetupTimeout.
func setupCall(ctx context.Context, conn *acpConn, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(ctx, acpSetupTimeout)
	defer cancel()
	err := conn.call(ctx, method, params, result)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
		return fmt.Errorf("%s didn't answer %s within %s", conn.name, method, acpSetupTimeout)
	}
	return err
}

// acquire hands the turn the chat's agent: the one kept from its last turn
// when it is still running the same command, else a new one, initialized.
func (b *ACPBackend) acquire(ctx context.Context, s config.ChatBackendSettings, workDir string, argv, env []string) (*acpConn, error) {
	// The mode is chosen as the session opens, which a kept agent's has, so a
	// new Mode takes a new agent too.
	key := strings.Join(argv, "\x00") + "\x01" + strings.Join(env, "\x00") + "\x01" + strings.TrimSpace(s.Mode)
	b.mu.Lock()
	if b.conns == nil {
		b.conns = map[string]*acpEntry{}
	}
	e := b.conns[workDir]
	if e != nil && e.busy {
		b.mu.Unlock()
		return nil, ErrBusy
	}
	if e != nil {
		if e.timer != nil {
			e.timer.Stop()
		}
		if e.conn.alive() && e.conn.key == key {
			e.busy = true
			b.mu.Unlock()
			return e.conn, nil
		}
		delete(b.conns, workDir)
		go e.conn.close()
	}
	b.mu.Unlock()

	conn, err := startACP(b.name(s), argv, env, workDir, key)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.conns[workDir] = &acpEntry{conn: conn, busy: true}
	b.mu.Unlock()
	if err := b.initialize(ctx, conn, s); err != nil {
		b.mu.Lock()
		if e := b.conns[workDir]; e != nil && e.conn == conn {
			delete(b.conns, workDir)
		}
		b.mu.Unlock()
		conn.kill()
		return nil, err
	}
	return conn, nil
}

// initialize is the handshake: the server says what it offers the agent —
// reading files, nothing else — and the agent what it can do.
func (b *ACPBackend) initialize(ctx context.Context, conn *acpConn, s config.ChatBackendSettings) error {
	var r acpInitializeResponse
	err := setupCall(ctx, conn, "initialize", acpInitializeRequest{
		ProtocolVersion: acpProtocolVersion,
		ClientCapabilities: acpClientCapabilities{
			FS:       acpFSCapabilities{ReadTextFile: true, WriteTextFile: false},
			Terminal: false,
		},
		ClientInfo: acpImplementation{Name: "code-review-server", Title: "Code Review Server", Version: serverVersion()},
	}, &r)
	if err != nil {
		return fmt.Errorf("%s didn't start speaking ACP (is %s Command the command that starts it in ACP mode?): %w",
			b.name(s), config.ChatTable(b.ID()), err)
	}
	if r.ProtocolVersion != acpProtocolVersion {
		return fmt.Errorf("%s speaks ACP version %d; the server speaks version %d", b.name(s), r.ProtocolVersion, acpProtocolVersion)
	}
	conn.caps, conn.authMethods = r.AgentCapabilities, r.AuthMethods
	return nil
}

// park keeps the chat's agent for its next turn, for IdleTimeout, unless it
// has exited or the chat let go of it.
func (b *ACPBackend) park(workDir string, conn *acpConn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.conns[workDir]
	if e == nil || e.conn != conn {
		go conn.close()
		return
	}
	e.busy = false
	if e.drop || !conn.alive() {
		delete(b.conns, workDir)
		go conn.close()
		return
	}
	idle := b.IdleTimeout
	if idle <= 0 {
		idle = acpIdleTimeout
	}
	e.timer = time.AfterFunc(idle, func() { b.expire(workDir, conn) })
}

// expire stops a chat's agent that has waited too long for its next turn.
func (b *ACPBackend) expire(workDir string, conn *acpConn) {
	b.mu.Lock()
	e := b.conns[workDir]
	if e == nil || e.conn != conn || e.busy {
		b.mu.Unlock()
		return
	}
	delete(b.conns, workDir)
	b.mu.Unlock()
	conn.close()
}

// Release stops the agent of the chat whose working directory is workDir,
// once its turn (if one is running) has ended.
func (b *ACPBackend) Release(workDir string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.releaseLocked(workDir)
}

// ReleaseAll stops every chat's agent, each once its turn has ended.
func (b *ACPBackend) ReleaseAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for workDir := range b.conns {
		b.releaseLocked(workDir)
	}
}

func (b *ACPBackend) releaseLocked(workDir string) {
	e := b.conns[workDir]
	if e == nil {
		return
	}
	if e.busy {
		e.drop = true
		return
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	delete(b.conns, workDir)
	go e.conn.close()
}

// running reports how many chats have an agent running; for tests.
func (b *ACPBackend) running() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.conns)
}

// acpTurn is a turn as the agent's messages arrive: it turns session updates
// into the Manager's events and answers the agent's requests.
type acpTurn struct {
	agent string
	emit  func(Event)
	// dirs are the directories the agent may read, as given and resolved.
	dirs []string
	// session is the session the turn prompts; quiet drops its updates until
	// the prompt is sent, so a session/load's replay of the conversation
	// isn't taken for a new answer. accepted is set once the agent has taken
	// the prompt: it is working on it, or has answered it.
	session  string
	quiet    bool
	accepted bool
	text     strings.Builder
	answered bool
	tools    map[string]*acpTool
	// denied are the tool calls the server refused, with the kind each was
	// refused as; stop gets why the turn must stop, once, when the agent made
	// a call it wasn't let make.
	denied  map[string]string
	stopped bool
	stop    chan string
	// refused is the last call the server refused, in words ("Run \"make\"").
	refused string
}

// acpTool is a tool call as the agent has described it so far.
type acpTool struct {
	title, kind string
	locations   []string
	// shown is the tool and detail last emitted.
	shown     string
	announced bool
	settled   bool
}

func newACPTurn(agent string, emit func(Event), t Turn) *acpTurn {
	turn := &acpTurn{
		agent: agent, emit: emit, quiet: true,
		tools: map[string]*acpTool{}, denied: map[string]string{}, stop: make(chan string, 1),
	}
	for _, dir := range append([]string{t.WorkDir}, t.ReadDirs...) {
		if dir == "" {
			continue
		}
		dir = filepath.Clean(dir)
		turn.dirs = append(turn.dirs, dir)
		if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
			turn.dirs = append(turn.dirs, resolved)
		}
	}
	return turn
}

// flush ends the block of the reply being written.
func (t *acpTurn) flush() {
	text := t.text.String()
	t.text.Reset()
	if strings.TrimSpace(text) != "" {
		t.emit(Event{Kind: EventText, Text: text})
	}
}

// accept records that the agent has taken the prompt, and for a new
// conversation tells the Manager its handle, kept from then on so a turn that
// dies later can be resumed.
func (t *acpTurn) accept() {
	if t.accepted || t.session == "" {
		return
	}
	t.accepted = true
	t.emit(Event{Kind: EventSession, SessionID: t.session})
}

func (t *acpTurn) notify(method string, params json.RawMessage) {
	if method != "session/update" || t.quiet {
		return
	}
	var n acpSessionNotification
	if json.Unmarshal(params, &n) != nil || n.SessionID != t.session {
		return
	}
	u := n.Update
	switch u.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update", "plan":
		// Working on the prompt. (Other updates, the commands it offers
		// say, can come of opening the session.)
		t.accept()
	}
	switch u.SessionUpdate {
	case "agent_message_chunk":
		if u.Content == nil || u.Content.Type != "text" || u.Content.Text == "" {
			return
		}
		t.answered = true
		t.text.WriteString(u.Content.Text)
		t.emit(Event{Kind: EventTextDelta, Text: u.Content.Text})
	case "tool_call", "tool_call_update":
		if u.ToolCallID == "" {
			return
		}
		t.flush()
		t.toolCall(u)
	}
}

// toolCall records what an update says about a tool call, and tells the
// Manager when that changes what the reviewer sees or the call ends. An agent
// may describe a call more than once as it learns what it is for (its title
// gains the file read), so a later description updates the first.
func (t *acpTurn) toolCall(u acpUpdate) {
	call := t.tools[u.ToolCallID]
	if call == nil {
		call = &acpTool{}
		t.tools[u.ToolCallID] = call
	}
	if call.settled {
		return
	}
	if u.Title != nil && strings.TrimSpace(*u.Title) != "" {
		call.title = *u.Title
	}
	if u.Kind != nil && *u.Kind != "" {
		call.kind = *u.Kind
	}
	if len(u.Locations) > 0 {
		call.locations = call.locations[:0]
		for _, l := range u.Locations {
			call.locations = append(call.locations, l.Path)
		}
	}
	tool, detail := acpToolName(call.kind), t.detail(call)
	if shown := tool + "\x00" + detail; !call.announced || shown != call.shown {
		t.answered = true
		call.announced, call.shown = true, shown
		t.emit(Event{Kind: EventToolUse, ToolUseID: u.ToolCallID, Tool: tool, Detail: detail})
	}
	if u.Status != nil && (*u.Status == "completed" || *u.Status == "failed") {
		call.settled = true
		t.emit(Event{Kind: EventToolResult, ToolUseID: u.ToolCallID, IsError: *u.Status == "failed"})
		// The server only lets reads, searches and thinking through, so any
		// other call that completed was made without its leave: unasked, or
		// refused and made anyway.
		deniedAs, denied := t.denied[u.ToolCallID]
		if *u.Status == "completed" && !t.stopped && (!acpReadOnlyKinds[call.kind] || denied) {
			t.stopped = true
			kind, how := call.kind, "without asking first"
			if denied {
				kind, how = deniedAs, "after the server refused to let it"
			}
			t.stop <- fmt.Sprintf("Stopped: %s %s %s. The chat is read-only, and the server can only "+
				"refuse what an agent asks to do: configure %s to ask before it runs commands, changes files or fetches URLs "+
				"(see the review chat in docs/configuration.md).", t.agent, acpDid(kind, detail), how, t.agent)
		}
	}
}

// acpDid says what a tool call of kind on detail did ("ran \"make\"").
func acpDid(kind, detail string) string {
	verb, ok := map[string]string{
		"read": "read", "search": "searched", "think": "thought about", "edit": "edited", "delete": "deleted",
		"move": "moved", "execute": "ran", "fetch": "fetched", "switch_mode": "switched its mode to",
	}[kind]
	switch {
	case detail == "" && ok:
		return verb + " something"
	case detail == "":
		return "used a tool"
	case !ok:
		return "used " + strconv.Quote(detail)
	}
	return verb + " " + strconv.Quote(detail)
}

// acpToolWords are the first words of a title that only repeat the tool's
// name, by its name: the name inflected, or snake_cased into a longer one
// ("read_file").
var acpToolWords = map[string][]string{
	"Read":        {"read", "reads", "reading"},
	"Search":      {"search", "searches", "searching", "searched"},
	"Edit":        {"edit", "edits", "editing", "edited"},
	"Delete":      {"delete", "deletes", "deleting", "deleted"},
	"Move":        {"move", "moves", "moving", "moved"},
	"Run":         {"run", "runs", "running", "ran"},
	"Think":       {"think", "thinks", "thinking", "thought"},
	"Fetch":       {"fetch", "fetches", "fetching", "fetched"},
	"Switch mode": {"switch", "switches", "switching", "switched"},
}

// repeatsTool reports whether word, a title's first, only repeats the tool's
// name: "Reading" or "read_file:" does for Read, "README.md" doesn't.
func repeatsTool(word, tool string) bool {
	word = strings.TrimSuffix(strings.ToLower(word), ":")
	for _, w := range acpToolWords[tool] {
		if word == w || strings.HasPrefix(word, w+"_") {
			return true
		}
	}
	return false
}

// acpToolName names a tool by its kind, as Claude Code's tools read ("Read",
// "Search"); "" for a kind that says nothing, whose title says it all.
func acpToolName(kind string) string {
	switch kind {
	case "read":
		return "Read"
	case "search":
		return "Search"
	case "edit":
		return "Edit"
	case "delete":
		return "Delete"
	case "move":
		return "Move"
	case "execute":
		return "Run"
	case "think":
		return "Think"
	case "fetch":
		return "Fetch"
	case "switch_mode":
		return "Switch mode"
	}
	return ""
}

// detail says in a few words what a tool call is on: its title, with paths
// shown relative to the turn's directories, less a first word that only
// repeats the tool's name ("Read pr.diff" under "Read" is "pr.diff"); else the
// first file it touches.
func (t *acpTurn) detail(call *acpTool) string {
	detail := strings.Join(strings.Fields(t.relativeAll(call.title)), " ")
	if tool := acpToolName(call.kind); tool != "" {
		if first, rest, _ := strings.Cut(detail, " "); repeatsTool(first, tool) {
			detail = rest
		}
	}
	if detail == "" && len(call.locations) > 0 {
		detail = t.relativeAll(call.locations[0])
	}
	if len(detail) > 200 {
		detail = cutAtLine(detail, 199) + "…"
	}
	return detail
}

// relativeAll shows each path under one of the turn's directories in s
// relative to it.
func (t *acpTurn) relativeAll(s string) string {
	dirs := slices.Clone(t.dirs)
	slices.SortFunc(dirs, func(a, b string) int { return len(b) - len(a) }) // the deepest first
	for _, dir := range dirs {
		s = strings.ReplaceAll(s, dir+string(filepath.Separator), "")
	}
	return s
}

// acpReadOnlyKinds are the kinds of tool call the agent is let make.
var acpReadOnlyKinds = map[string]bool{"read": true, "search": true, "think": true}

func (t *acpTurn) request(method string, params json.RawMessage) (any, *acpError) {
	switch method {
	case "session/request_permission":
		return t.permission(params)
	case "fs/read_text_file":
		return t.readFile(params)
	case "fs/write_text_file":
		return nil, &acpError{Code: acpErrInternal, Message: "this chat is read-only: the agent can't change files"}
	}
	return nil, &acpError{Code: acpErrMethodNotFound, Message: "the server doesn't offer " + method}
}

// permission answers the agent's request to make a tool call: yes when the
// call only reads, no otherwise.
func (t *acpTurn) permission(params json.RawMessage) (any, *acpError) {
	var r acpPermissionRequest
	if err := json.Unmarshal(params, &r); err != nil {
		return nil, &acpError{Code: acpErrInvalidParams, Message: err.Error()}
	}
	if !t.quiet && r.SessionID == t.session {
		t.accept() // asking is working on the prompt
	}
	kind := ""
	if r.ToolCall.Kind != nil {
		kind = *r.ToolCall.Kind
	}
	if call := t.tools[r.ToolCall.ToolCallID]; kind == "" && call != nil {
		kind = call.kind
	}
	want := []string{"reject_once", "reject_always"}
	if acpReadOnlyKinds[kind] {
		want = []string{"allow_once", "allow_always"}
	}
	if !acpReadOnlyKinds[kind] {
		t.denied[r.ToolCall.ToolCallID] = kind
		call := &acpTool{}
		if known := t.tools[r.ToolCall.ToolCallID]; known != nil {
			*call = *known
		}
		call.kind = kind // the request's, which the call was refused for
		if r.ToolCall.Title != nil && strings.TrimSpace(*r.ToolCall.Title) != "" {
			call.title = *r.ToolCall.Title
		}
		t.refused = strings.TrimSpace(strings.ToLower(acpToolName(call.kind)) + " " + strconvQuote(t.detail(call)))
		if t.refused == "" {
			t.refused = "use a tool"
		}
	}
	for _, k := range want {
		for _, o := range r.Options {
			if o.Kind == k {
				return acpPermissionResponse{Outcome: acpPermissionOutcome{Outcome: "selected", OptionID: o.OptionID}}, nil
			}
		}
	}
	return acpPermissionResponse{Outcome: acpPermissionOutcome{Outcome: "cancelled"}}, nil
}

// readFile reads a file for the agent, from the turn's directories only.
func (t *acpTurn) readFile(params json.RawMessage) (any, *acpError) {
	var r acpReadTextFileRequest
	if err := json.Unmarshal(params, &r); err != nil {
		return nil, &acpError{Code: acpErrInvalidParams, Message: err.Error()}
	}
	if !filepath.IsAbs(r.Path) {
		return nil, &acpError{Code: acpErrInvalidParams, Message: "the path must be absolute: " + r.Path}
	}
	path := filepath.Clean(r.Path)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &acpError{Code: acpErrResourceNotFound, Message: "no such file: " + path}
		}
		return nil, &acpError{Code: acpErrInternal, Message: err.Error()}
	}
	if !t.mayRead(resolved) {
		return nil, &acpError{Code: acpErrInternal, Message: path + " is outside the directories this chat may read: its working directory and the PR's checkout"}
	}
	info, err := os.Stat(resolved)
	switch {
	case err != nil:
		return nil, &acpError{Code: acpErrInternal, Message: err.Error()}
	case info.IsDir():
		return nil, &acpError{Code: acpErrInvalidParams, Message: path + " is a directory"}
	case info.Size() > acpMaxFileBytes:
		return nil, &acpError{Code: acpErrInternal, Message: fmt.Sprintf("%s is too large to read (%d MB)", path, info.Size()>>20)}
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, &acpError{Code: acpErrInternal, Message: err.Error()}
	}
	return acpReadTextFileResponse{Content: lineRange(string(data), r.Line, r.Limit)}, nil
}

// strconvQuote quotes s, or is "" for an empty s.
func strconvQuote(s string) string {
	if s == "" {
		return ""
	}
	return strconv.Quote(s)
}

// mayRead reports whether path is in one of the turn's directories.
func (t *acpTurn) mayRead(path string) bool {
	for _, dir := range t.dirs {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// lineRange is the limit lines of s from the 1-based line; nil means from the
// first, and to the end.
func lineRange(s string, line, limit *int) string {
	if line == nil && limit == nil {
		return s
	}
	lines := strings.SplitAfter(s, "\n")
	start := 0
	if line != nil && *line > 1 {
		start = *line - 1
	}
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit != nil && *limit >= 0 && start+*limit < end {
		end = start + *limit
	}
	return strings.Join(lines[start:end], "")
}

// The messages, as much of them as the chat reads and writes. ACP's schema:
// https://agentclientprotocol.com/protocol/schema

type acpInitializeRequest struct {
	ProtocolVersion    int                   `json:"protocolVersion"`
	ClientCapabilities acpClientCapabilities `json:"clientCapabilities"`
	ClientInfo         acpImplementation     `json:"clientInfo"`
}

type acpClientCapabilities struct {
	FS       acpFSCapabilities `json:"fs"`
	Terminal bool              `json:"terminal"`
}

type acpFSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type acpImplementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// serverVersion is the server's module version as built, "dev" when it says
// none.
func serverVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

type acpInitializeResponse struct {
	ProtocolVersion   int                  `json:"protocolVersion"`
	AgentCapabilities acpAgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []acpAuthMethod      `json:"authMethods"`
}

type acpAgentCapabilities struct {
	LoadSession         bool `json:"loadSession"`
	SessionCapabilities struct {
		// Present (as {}) when the agent offers each.
		Resume                *struct{} `json:"resume"`
		AdditionalDirectories *struct{} `json:"additionalDirectories"`
	} `json:"sessionCapabilities"`
}

type acpAuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type acpAuthenticateRequest struct {
	MethodID string `json:"methodId"`
}

type acpNewSessionRequest struct {
	Cwd                   string   `json:"cwd"`
	AdditionalDirectories []string `json:"additionalDirectories,omitempty"`
	McpServers            []any    `json:"mcpServers"`
}

type acpLoadSessionRequest struct {
	SessionID             string   `json:"sessionId"`
	Cwd                   string   `json:"cwd"`
	AdditionalDirectories []string `json:"additionalDirectories,omitempty"`
	McpServers            []any    `json:"mcpServers"`
}

// acpSessionState is what opening a session says about choosing its mode and
// model: config options (the current way), or the older modes and models.
type acpSessionState struct {
	Modes *struct {
		AvailableModes []struct {
			ID string `json:"id"`
		} `json:"availableModes"`
	} `json:"modes"`
	Models *struct {
		AvailableModels []struct {
			ModelID string `json:"modelId"`
		} `json:"availableModels"`
	} `json:"models"`
	ConfigOptions []acpConfigOption `json:"configOptions"`
}

type acpNewSessionResponse struct {
	SessionID string `json:"sessionId"`
	acpSessionState
}

type acpConfigOption struct {
	ID       string          `json:"id"`
	Category string          `json:"category"`
	Options  json.RawMessage `json:"options"`
}

// values are the option's choices. They come as a list, or as groups of them.
func (o acpConfigOption) values() []string {
	type choice struct {
		Value   string   `json:"value"`
		Options []choice `json:"options"`
	}
	var choices []choice
	if json.Unmarshal(o.Options, &choices) != nil {
		return nil
	}
	var values []string
	for _, c := range choices {
		if c.Value != "" {
			values = append(values, c.Value)
		}
		for _, inner := range c.Options {
			if inner.Value != "" {
				values = append(values, inner.Value)
			}
		}
	}
	return values
}

type acpSetConfigOptionRequest struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

type acpSetModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

type acpSetModelRequest struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

type acpSessionRef struct {
	SessionID string `json:"sessionId"`
}

type acpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func acpText(text string) acpContent { return acpContent{Type: "text", Text: text} }

type acpPromptRequest struct {
	SessionID string       `json:"sessionId"`
	Prompt    []acpContent `json:"prompt"`
}

type acpPromptResponse struct {
	StopReason string `json:"stopReason"`
}

type acpSessionNotification struct {
	SessionID string    `json:"sessionId"`
	Update    acpUpdate `json:"update"`
}

// acpUpdate is a session update: a chunk of the reply, or a tool call.
type acpUpdate struct {
	SessionUpdate string      `json:"sessionUpdate"`
	Content       *acpContent `json:"-"`
	ToolCallID    string      `json:"toolCallId"`
	Title         *string     `json:"title"`
	Kind          *string     `json:"kind"`
	Status        *string     `json:"status"`
	Locations     []struct {
		Path string `json:"path"`
	} `json:"locations"`
}

// UnmarshalJSON reads content as a chunk's content block; a tool call's is a
// list of its own, which the chat doesn't read.
func (u *acpUpdate) UnmarshalJSON(data []byte) error {
	type plain acpUpdate
	var p struct {
		plain
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*u = acpUpdate(p.plain)
	if len(p.Content) > 0 && p.Content[0] == '{' {
		var c acpContent
		if json.Unmarshal(p.Content, &c) == nil {
			u.Content = &c
		}
	}
	return nil
}

type acpPermissionRequest struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string  `json:"toolCallId"`
		Kind       *string `json:"kind"`
		Title      *string `json:"title"`
	} `json:"toolCall"`
	Options []struct {
		OptionID string `json:"optionId"`
		Kind     string `json:"kind"`
	} `json:"options"`
}

type acpPermissionResponse struct {
	Outcome acpPermissionOutcome `json:"outcome"`
}

type acpPermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type acpReadTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      *int   `json:"line"`
	Limit     *int   `json:"limit"`
}

type acpReadTextFileResponse struct {
	Content string `json:"content"`
}
