// Package chat lets a reviewer talk to a local coding agent about the PR they
// are reviewing. A chat is a conversation with one agent (a Backend: Claude
// Code to start with), held by the server: the conversation and its messages
// are stored in the ChatSessions and ChatMessages tables, so a client can
// leave and come back to it, and each turn runs the agent's CLI headless in a
// working directory of the chat's own.
//
// The first message of a conversation tells the agent we're reviewing the PR,
// carries what the reviewer asked, then the PR's description and diff, so the
// agent starts with them in context; the same are written to files in the
// working directory, where it can read them again. Later turns resume the
// agent's own session, which keeps the full transcript. The agent can read
// and search files but not change anything or run commands.
//
// Nothing here waits on the agent: Start and Send store the reviewer's
// message and return, the turn runs in the background, and the Manager's
// OnChange announces each step so clients read the chat again (GetChat)
// rather than poll. Whether a turn is running is kept in memory, so a restart
// can never leave a chat claiming to be busy.
package chat

import (
	"context"
	"crs/database"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Backend is a chat agent the server can drive.
type Backend interface {
	// ID is the stable identifier clients and config use, e.g. "claude".
	ID() string
	// Info describes the backend as config sets it up right now.
	Info() BackendInfo
	// Turn runs one turn of a conversation: it sends t.Prompt, calls emit with
	// each step as the agent takes it, and returns when the agent has
	// answered, failed, or ctx ended. emit is called on Turn's goroutine.
	Turn(ctx context.Context, t Turn, emit func(Event)) (TurnResult, error)
}

// BackendInfo is what a client is told about a backend.
type BackendInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Available is false when the backend can't run here — its program isn't
	// installed, or config disables it — and Reason says why.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Models are the models to offer, the backend's own default first (its ID
	// empty). DefaultModel is the one to pick when a chat names none.
	Models       []Model `json:"models"`
	DefaultModel string  `json:"default_model"`
}

// Model is one model a backend can run.
type Model struct {
	// ID is what the backend is asked for; empty leaves the choice to it.
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Turn is one turn of a conversation, as a Backend runs it.
type Turn struct {
	// WorkDir is the chat's working directory, which holds DescriptionFile
	// and DiffFile. The agent's sessions are tied to it, so it never changes
	// for a chat.
	WorkDir string
	// ReadDirs are further directories the agent may read: a checkout of the
	// repository.
	ReadDirs []string
	Model    string
	Prompt   string
	// SystemPrompt is appended to the agent's own system prompt.
	SystemPrompt string
	// SessionID is the agent's handle on the conversation: the one to resume
	// when Resume is set, otherwise the one to give the new conversation.
	SessionID string
	Resume    bool
}

// EventKind says what a step of a turn was.
type EventKind string

const (
	// EventSession reports the agent's handle on the conversation, as soon as
	// the agent says it.
	EventSession EventKind = "session"
	// EventTextDelta is a piece of the reply being written.
	EventTextDelta EventKind = "text_delta"
	// EventText is a finished block of the reply.
	EventText EventKind = "text"
	// EventToolUse is the agent calling a tool.
	EventToolUse EventKind = "tool_use"
	// EventToolResult is a tool call's outcome.
	EventToolResult EventKind = "tool_result"
)

// Event is one step of a turn.
type Event struct {
	Kind      EventKind
	Text      string
	SessionID string
	// ToolUseID pairs a tool call with its result; Tool and Detail describe
	// the call ("Read", "server/chat.go").
	ToolUseID string
	Tool      string
	Detail    string
	IsError   bool
}

// TurnResult is what a finished turn reports.
type TurnResult struct {
	// SessionID is the agent's handle on the conversation, to resume next
	// turn; empty when it never said.
	SessionID string
	CostUSD   float64
	// Answered is true once the agent produced any reply or tool call, so a
	// failure after it is not a failure to resume.
	Answered bool
}

// Roles of a chat's messages.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
	RoleError     = "error"
	RoleNotice    = "notice"
)

// Statuses of a tool call.
const (
	ToolRunning = "running"
	ToolDone    = "done"
	ToolError   = "error"
)

// Statuses of a chat, as OnChange and clients see them.
const (
	StatusIdle    = "idle"
	StatusRunning = "running"
)

// DefaultTurnTimeout bounds one turn. An agent reading its way around a large
// change can take a while; the reviewer can stop it sooner.
const DefaultTurnTimeout = 20 * time.Minute

// notifyInterval is the most often a reply being written is announced.
const notifyInterval = 200 * time.Millisecond

// Store is where chats are kept; *database.DB is one.
type Store interface {
	CreateChatSession(s database.ChatSession) error
	GetChatSession(id string) (database.ChatSession, bool, error)
	ListChatSessions(owner, repo string, number int) ([]database.ChatSession, error)
	RecordChatTurn(id, backendSessionID string, costUSD float64) error
	DeleteChatSession(id string) error
	AddChatMessage(m database.ChatMessage) (int64, error)
	UpdateChatMessage(id int64, content, meta string) error
	GetChatMessages(sessionID string) ([]database.ChatMessage, error)
}

// Errors the Manager's methods return for a client to show.
var (
	ErrNotFound    = errors.New("there is no such chat")
	ErrBusy        = errors.New("the agent is still answering; wait for it, or stop it")
	ErrEmpty       = errors.New("the message is empty")
	ErrNoStore     = errors.New("chats need the server's database, which isn't open")
	ErrUnavailable = errors.New("that chat backend isn't available")
)

// Manager holds the chats: it starts them, runs their turns, and tracks which
// are running.
type Manager struct {
	backends []Backend

	// Store returns where chats are kept, nil when there is nowhere.
	Store func() Store
	// Dir returns the directory the chats' working directories go in.
	Dir func() (string, error)
	// LoadPR reads what a conversation attaches about a PR.
	LoadPR func(ctx context.Context, owner, repo string, number int) (PRContext, error)
	// OnChange, when set, is told each time what GetChat returns for a chat
	// changes: a message is added or updated, the reply being written grows,
	// a turn starts or ends. It is called on the turn's goroutine or the
	// caller's, and must not block.
	OnChange func(s database.ChatSession, status string)
	// TurnTimeout bounds each turn; zero means DefaultTurnTimeout.
	TurnTimeout time.Duration

	mu      sync.Mutex
	running map[string]*turnState
}

// turnState is a running turn, as GetChat reports it and StopChat ends it.
type turnState struct {
	cancel    context.CancelFunc
	startedAt time.Time
	draft     strings.Builder
	stopped   bool
}

// NewManager makes a manager for the given backends, in the order clients
// should offer them.
func NewManager(backends ...Backend) *Manager {
	return &Manager{backends: backends, running: map[string]*turnState{}}
}

// Backends describes every backend, in order.
func (m *Manager) Backends() []BackendInfo {
	infos := make([]BackendInfo, 0, len(m.backends))
	for _, b := range m.backends {
		infos = append(infos, b.Info())
	}
	return infos
}

func (m *Manager) backend(id string) (Backend, bool) {
	for _, b := range m.backends {
		if b.ID() == id {
			return b, true
		}
	}
	return nil, false
}

func (m *Manager) store() (Store, error) {
	if m.Store == nil {
		return nil, ErrNoStore
	}
	s := m.Store()
	if s == nil {
		return nil, ErrNoStore
	}
	return s, nil
}

// StartRequest asks for a new chat about a PR.
type StartRequest struct {
	Owner  string
	Repo   string
	Number int
	// Backend is the backend's ID; empty picks the first available one.
	Backend string
	// Model is the model to run; empty takes the backend's default.
	Model   string
	Message string
}

// Start creates a chat, stores the reviewer's first message, and starts the
// agent on it in the background. The chat it returns is already running.
func (m *Manager) Start(ctx context.Context, req StartRequest) (database.ChatSession, error) {
	message := strings.TrimSpace(req.Message)
	if message == "" {
		return database.ChatSession{}, ErrEmpty
	}
	store, err := m.store()
	if err != nil {
		return database.ChatSession{}, err
	}
	backend, info, err := m.pickBackend(req.Backend)
	if err != nil {
		return database.ChatSession{}, err
	}
	model := req.Model
	if model == "" {
		model = info.DefaultModel
	}
	if m.LoadPR == nil {
		return database.ChatSession{}, fmt.Errorf("the server can't read PRs for chats")
	}
	pr, err := m.LoadPR(ctx, req.Owner, req.Repo, req.Number)
	if err != nil {
		return database.ChatSession{}, fmt.Errorf("reading the PR: %w", err)
	}

	session := database.ChatSession{
		ID:      newID(),
		Owner:   req.Owner,
		Repo:    req.Repo,
		Number:  req.Number,
		Backend: backend.ID(),
		Model:   model,
		HeadSHA: pr.HeadSHA,
		Title:   titleFor(message),
	}
	if err := store.CreateChatSession(session); err != nil {
		return database.ChatSession{}, fmt.Errorf("storing the chat: %w", err)
	}
	if stored, ok, err := store.GetChatSession(session.ID); err == nil && ok {
		session = stored
	}
	if err := m.begin(store, session, backend, message, &pr); err != nil {
		return database.ChatSession{}, err
	}
	return session, nil
}

// pickBackend finds the backend a chat asked for, or the first available one.
func (m *Manager) pickBackend(id string) (Backend, BackendInfo, error) {
	if id == "" {
		for _, b := range m.backends {
			if info := b.Info(); info.Available {
				return b, info, nil
			}
		}
		return nil, BackendInfo{}, ErrUnavailable
	}
	b, ok := m.backend(id)
	if !ok {
		return nil, BackendInfo{}, fmt.Errorf("there is no chat backend named %q", id)
	}
	info := b.Info()
	if !info.Available {
		return nil, BackendInfo{}, fmt.Errorf("%s isn't available: %s", info.Name, info.Reason)
	}
	return b, info, nil
}

// Send stores the reviewer's next message in a chat and starts the agent on
// it in the background. A chat whose agent is still answering refuses it.
func (m *Manager) Send(ctx context.Context, id, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return ErrEmpty
	}
	store, err := m.store()
	if err != nil {
		return err
	}
	session, ok, err := store.GetChatSession(id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	backend, ok := m.backend(session.Backend)
	if !ok {
		return fmt.Errorf("this chat's backend %q is no longer offered", session.Backend)
	}
	if info := backend.Info(); !info.Available {
		return fmt.Errorf("%s isn't available: %s", info.Name, info.Reason)
	}
	// A conversation the agent never confirmed holding (its first turn failed
	// before it said) starts over, with the PR attached again.
	var pr *PRContext
	if session.BackendSessionID == "" {
		loaded, err := m.LoadPR(ctx, session.Owner, session.Repo, session.Number)
		if err != nil {
			return fmt.Errorf("reading the PR: %w", err)
		}
		pr = &loaded
	}
	return m.begin(store, session, backend, message, pr)
}

// begin claims the chat, stores the reviewer's message, and runs the turn on
// a goroutine of its own. pr is set when the turn opens a new conversation
// with the agent, which attaches the PR.
func (m *Manager) begin(store Store, session database.ChatSession, backend Backend, message string, pr *PRContext) error {
	timeout := m.TurnTimeout
	if timeout <= 0 {
		timeout = DefaultTurnTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	ts := &turnState{cancel: cancel, startedAt: time.Now()}

	m.mu.Lock()
	if _, busy := m.running[session.ID]; busy {
		m.mu.Unlock()
		cancel()
		return ErrBusy
	}
	m.running[session.ID] = ts
	m.mu.Unlock()

	// The history a new conversation is given is read before the message
	// joins it.
	var history []historyEntry
	if pr != nil {
		history = m.history(store, session.ID)
	}
	if _, err := store.AddChatMessage(database.ChatMessage{
		SessionID: session.ID, Role: RoleUser, Content: message, Meta: userMeta(pr),
	}); err != nil {
		m.finish(session.ID)
		cancel()
		return fmt.Errorf("storing the message: %w", err)
	}
	m.changed(session, StatusRunning)

	go func() {
		defer cancel()
		m.run(ctx, store, session, backend, ts, message, pr, history)
	}()
	return nil
}

// userMeta is the meta of the reviewer's message: what it attached.
func userMeta(pr *PRContext) string {
	if pr == nil {
		return ""
	}
	meta := map[string]any{"attached": []string{AttachedDescription, AttachedDiff}, "head_sha": pr.HeadSHA}
	if len(pr.Diff) > maxPromptDiffBytes {
		meta["diff_truncated"] = true
	}
	data, _ := json.Marshal(meta)
	return string(data)
}

// history is what was said in a chat so far, for a conversation that has to
// start over: the reviewer's messages and the agent's replies.
func (m *Manager) history(store Store, id string) []historyEntry {
	messages, err := store.GetChatMessages(id)
	if err != nil {
		slog.Warn("Could not read a chat's history", "chat", id, "error", err)
		return nil
	}
	var history []historyEntry
	for _, msg := range messages {
		switch msg.Role {
		case RoleUser:
			history = append(history, historyEntry{reviewer: true, text: msg.Content})
		case RoleAssistant:
			history = append(history, historyEntry{text: msg.Content})
		}
	}
	return history
}

// run is one turn, from the agent's start to the chat going idle again.
func (m *Manager) run(ctx context.Context, store Store, session database.ChatSession, backend Backend,
	ts *turnState, message string, pr *PRContext, history []historyEntry) {
	defer func() {
		m.finish(session.ID)
		m.changed(session, StatusIdle)
	}()

	workDir, err := m.workDir(session.ID)
	if err != nil {
		m.addMessage(store, session, RoleError, "Could not set up the chat's working directory: "+err.Error(), "")
		return
	}

	// The PR context this turn's system prompt describes: what pr carries
	// when the turn opens a conversation, else read again (cheaply, from the
	// caches) for its checkout. A failure there only loses the checkout.
	promptPR := pr
	if promptPR == nil && m.LoadPR != nil {
		if loaded, err := m.LoadPR(ctx, session.Owner, session.Repo, session.Number); err == nil {
			promptPR = &loaded
		} else {
			promptPR = &PRContext{Owner: session.Owner, Repo: session.Repo, Number: session.Number}
		}
	}

	result, err := m.turn(ctx, store, session, backend, ts, workDir, message, pr, promptPR, history)
	// The agent no longer has the conversation (its transcript went, or it
	// never got going): start a new one that carries what was said.
	if err != nil && pr == nil && !result.Answered && ctx.Err() == nil && m.LoadPR != nil {
		slog.Info("Could not resume a chat's conversation; starting a new one", "chat", session.ID, "error", err)
		if loaded, loadErr := m.LoadPR(ctx, session.Owner, session.Repo, session.Number); loadErr == nil {
			history := m.history(store, session.ID)
			if n := len(history); n > 0 && history[n-1].reviewer {
				history = history[:n-1] // the message being answered
			}
			result, err = m.turn(ctx, store, session, backend, ts, workDir, message, &loaded, &loaded, history)
		}
	}

	m.mu.Lock()
	stopped := ts.stopped
	m.mu.Unlock()
	switch {
	case stopped:
		m.addMessage(store, session, RoleNotice, "Stopped.", "")
	case err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		m.addMessage(store, session, RoleError, fmt.Sprintf("The agent took longer than %s and was stopped.", m.timeout()), "")
	case err != nil:
		m.addMessage(store, session, RoleError, err.Error(), "")
	}
	if err := store.RecordChatTurn(session.ID, result.SessionID, result.CostUSD); err != nil {
		slog.Warn("Could not record a chat turn", "chat", session.ID, "error", err)
	}
}

func (m *Manager) timeout() time.Duration {
	if m.TurnTimeout > 0 {
		return m.TurnTimeout
	}
	return DefaultTurnTimeout
}

// turn runs the backend once. pr is set when the turn opens a new
// conversation; promptPR is what its system prompt describes.
func (m *Manager) turn(ctx context.Context, store Store, session database.ChatSession, backend Backend,
	ts *turnState, workDir, message string, pr, promptPR *PRContext, history []historyEntry) (TurnResult, error) {
	t := Turn{
		WorkDir:   workDir,
		Model:     session.Model,
		Prompt:    message,
		SessionID: session.BackendSessionID,
		Resume:    true,
	}
	if promptPR != nil {
		t.SystemPrompt = systemPrompt(*promptPR)
		if promptPR.Checkout != "" {
			t.ReadDirs = []string{promptPR.Checkout}
		}
	}
	if pr != nil {
		if err := writeContextFiles(workDir, *pr); err != nil {
			return TurnResult{}, fmt.Errorf("writing the PR into the chat's working directory: %w", err)
		}
		t.Prompt, _ = openingPrompt(*pr, history, message)
		t.SessionID = newID()
		t.Resume = false
	}

	// Tool calls in flight, by the ID that pairs each with its result.
	tools := map[string]*toolCall{}
	var lastNotify time.Time
	emit := func(e Event) {
		switch e.Kind {
		case EventSession:
			if e.SessionID != "" {
				// Kept at once, so a turn that dies later can still be resumed.
				if err := store.RecordChatTurn(session.ID, e.SessionID, 0); err != nil {
					slog.Warn("Could not record a chat's agent session", "chat", session.ID, "error", err)
				}
			}
			return
		case EventTextDelta:
			m.mu.Lock()
			ts.draft.WriteString(e.Text)
			m.mu.Unlock()
			if time.Since(lastNotify) < notifyInterval {
				return
			}
		case EventText:
			m.mu.Lock()
			ts.draft.Reset()
			m.mu.Unlock()
			if strings.TrimSpace(e.Text) == "" {
				return
			}
			m.addMessage(store, session, RoleAssistant, e.Text, "")
		case EventToolUse:
			call := &toolCall{tool: e.Tool, useID: e.ToolUseID, detail: e.Detail}
			id, err := store.AddChatMessage(database.ChatMessage{
				SessionID: session.ID, Role: RoleTool, Content: call.detail, Meta: call.meta(ToolRunning),
			})
			if err != nil {
				slog.Warn("Could not store a chat's tool call", "chat", session.ID, "error", err)
				return
			}
			call.id = id
			tools[e.ToolUseID] = call
		case EventToolResult:
			call, ok := tools[e.ToolUseID]
			if !ok {
				return
			}
			status := ToolDone
			if e.IsError {
				status = ToolError
			}
			call.settle(store, session.ID, status)
			delete(tools, e.ToolUseID)
		}
		lastNotify = time.Now()
		m.changed(session, StatusRunning)
	}

	result, err := backend.Turn(ctx, t, emit)
	m.mu.Lock()
	ts.draft.Reset()
	m.mu.Unlock()
	// A call the agent never saw the end of was cut off with the turn.
	for _, call := range tools {
		call.settle(store, session.ID, ToolError)
	}
	if result.SessionID == "" && result.Answered {
		result.SessionID = t.SessionID
	}
	return result, err
}

// toolCall is a tool call stored as a chat message, until its result is in.
type toolCall struct {
	id     int64
	tool   string
	useID  string
	detail string
}

func (c *toolCall) meta(status string) string {
	data, _ := json.Marshal(map[string]string{"tool": c.tool, "tool_use_id": c.useID, "status": status})
	return string(data)
}

// settle records how the call ended.
func (c *toolCall) settle(store Store, chatID, status string) {
	if err := store.UpdateChatMessage(c.id, c.detail, c.meta(status)); err != nil {
		slog.Warn("Could not update a chat's tool call", "chat", chatID, "error", err)
	}
}

func (m *Manager) addMessage(store Store, session database.ChatSession, role, content, meta string) {
	if _, err := store.AddChatMessage(database.ChatMessage{
		SessionID: session.ID, Role: role, Content: content, Meta: meta,
	}); err != nil {
		slog.Warn("Could not store a chat message", "chat", session.ID, "role", role, "error", err)
		return
	}
	m.changed(session, StatusRunning)
}

// finish releases the chat's claim on a running turn.
func (m *Manager) finish(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.running, id)
}

func (m *Manager) changed(session database.ChatSession, status string) {
	if m.OnChange != nil {
		m.OnChange(session, status)
	}
}

// workDir is the chat's working directory, made when missing.
func (m *Manager) workDir(id string) (string, error) {
	if m.Dir == nil {
		return "", fmt.Errorf("no directory is configured for chats")
	}
	root, err := m.Dir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// writeContextFiles writes the PR into a chat's working directory.
func writeContextFiles(dir string, pr PRContext) error {
	if err := os.WriteFile(filepath.Join(dir, DescriptionFile), []byte(descriptionFile(pr)), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, DiffFile), []byte(pr.Diff), 0o644)
}

// View is a chat as a client reads it.
type View struct {
	Session  database.ChatSession
	Messages []database.ChatMessage
	Running  bool
	// Draft is the part of the reply the agent is writing right now, not yet
	// stored; empty between replies.
	Draft string
	// StartedAt is when the running turn started; zero when none is.
	StartedAt time.Time
}

// Get reads a chat: its messages, and what its running turn has written so
// far.
func (m *Manager) Get(id string) (View, error) {
	store, err := m.store()
	if err != nil {
		return View{}, err
	}
	session, ok, err := store.GetChatSession(id)
	if err != nil {
		return View{}, err
	}
	if !ok {
		return View{}, ErrNotFound
	}
	messages, err := store.GetChatMessages(id)
	if err != nil {
		return View{}, err
	}
	view := View{Session: session, Messages: messages}
	m.mu.Lock()
	if ts, ok := m.running[id]; ok {
		view.Running = true
		view.Draft = ts.draft.String()
		view.StartedAt = ts.startedAt
	}
	m.mu.Unlock()
	return view, nil
}

// List returns a PR's chats, the most recently active first.
func (m *Manager) List(owner, repo string, number int) ([]database.ChatSession, error) {
	store, err := m.store()
	if err != nil {
		return nil, err
	}
	return store.ListChatSessions(owner, repo, number)
}

// Stop ends the chat's running turn, if it has one, and reports whether it
// did. The turn notes that it was stopped once the agent has exited.
func (m *Manager) Stop(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ts, ok := m.running[id]
	if !ok {
		return false
	}
	ts.stopped = true
	ts.cancel()
	return true
}

// Running reports whether a turn is running for the chat.
func (m *Manager) Running(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[id]
	return ok
}

// Delete removes a chat and its messages, and its working directory. A
// running chat is stopped first.
func (m *Manager) Delete(id string) error {
	store, err := m.store()
	if err != nil {
		return err
	}
	session, ok, err := store.GetChatSession(id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	m.Stop(id)
	if err := store.DeleteChatSession(id); err != nil {
		return err
	}
	if m.Dir != nil {
		if root, err := m.Dir(); err == nil {
			if err := os.RemoveAll(filepath.Join(root, id)); err != nil {
				slog.Warn("Could not remove a chat's working directory", "chat", id, "error", err)
			}
		}
	}
	m.changed(session, StatusIdle)
	return nil
}

// newID is a random UUID (version 4), the form Claude Code takes for a
// session ID and a fine one for a chat's.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand doesn't fail on the platforms the server runs on
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
