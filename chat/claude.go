package chat

import (
	"bufio"
	"context"
	"crs/config"
	"crs/subprocess"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// claudeDefaultCommand is the Claude Code CLI as installed.
const claudeDefaultCommand = "claude"

// claudeModels are the models offered when [Chat.Claude] lists none: Claude
// Code's aliases, which always name the latest of each.
var claudeModels = []string{"opus", "sonnet", "haiku"}

// The tools a chat's agent may use: reading and searching, nothing that
// changes a file, runs a command or reaches the network. In print mode a tool
// that isn't allowed is refused rather than asked about.
var (
	claudeAllowedTools    = []string{"Read", "Grep", "Glob"}
	claudeDisallowedTools = []string{"Bash", "Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch"}
)

// claudeWaitDelay bounds how long a killed claude's output pipes may stay
// open (its MCP servers can hold them), as in subprocess.
const claudeWaitDelay = 5 * time.Second

// ClaudeBackend drives the local Claude Code CLI: each turn is one
// `claude -p` run with streamed JSON output (--output-format stream-json),
// the prompt on stdin, in the chat's working directory. The first turn names
// the new conversation (--session-id), later ones resume it (--resume), so
// Claude Code keeps the transcript and the server keeps only its handle.
type ClaudeBackend struct {
	// Settings returns [Chat.Claude] as config has it now.
	Settings func() config.ChatBackendSettings
	// LookPath finds the program; tests replace it.
	LookPath func(string) (string, error)
}

// NewClaudeBackend is the Claude Code backend, reading its settings from the
// server's config.
func NewClaudeBackend() *ClaudeBackend {
	return &ClaudeBackend{
		Settings: func() config.ChatBackendSettings { return config.C().ChatBackend(config.ChatBackendClaude) },
		LookPath: exec.LookPath,
	}
}

func (b *ClaudeBackend) ID() string { return config.ChatBackendClaude }

func (b *ClaudeBackend) settings() config.ChatBackendSettings {
	if b.Settings == nil {
		return config.ChatBackendSettings{}
	}
	return b.Settings()
}

// argv is the configured command line, split like a shell would.
func (b *ClaudeBackend) argv() ([]string, error) {
	command := strings.TrimSpace(b.settings().Command)
	if command == "" {
		command = claudeDefaultCommand
	}
	return subprocess.SplitCommand(command)
}

func (b *ClaudeBackend) Info() BackendInfo {
	s := b.settings()
	info := BackendInfo{ID: b.ID(), Name: "Claude Code", DefaultModel: s.DefaultModel}
	info.Models = append(info.Models, Model{ID: "", Name: "Default"})
	models := s.Models
	if len(models) == 0 {
		models = claudeModels
	}
	for _, id := range models {
		if id = strings.TrimSpace(id); id != "" {
			info.Models = append(info.Models, Model{ID: id, Name: modelName(id)})
		}
	}

	switch argv, err := b.argv(); {
	case s.Disabled:
		info.Reason = "it is disabled in the config ([Chat.Claude] Disabled)"
	case err != nil:
		info.Reason = "its command can't be read: " + err.Error()
	default:
		lookPath := b.LookPath
		if lookPath == nil {
			lookPath = exec.LookPath
		}
		if _, err := lookPath(argv[0]); err != nil {
			info.Reason = fmt.Sprintf("%s isn't installed where the server can find it (set [Chat.Claude] Command to its path)", argv[0])
		} else {
			info.Available = true
		}
	}
	return info
}

// modelName labels a model ID for a picker: an alias capitalized, anything
// else as written.
func modelName(id string) string {
	if strings.ContainsAny(id, "-./:") || id == "" {
		return id
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// args are the arguments of one turn's run, after the configured command.
func (b *ClaudeBackend) args(t Turn) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--allowedTools", strings.Join(claudeAllowedTools, ","),
		"--disallowedTools", strings.Join(claudeDisallowedTools, ","),
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	if t.Resume {
		args = append(args, "--resume", t.SessionID)
	} else if t.SessionID != "" {
		args = append(args, "--session-id", t.SessionID)
	}
	if t.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", t.SystemPrompt)
	}
	for _, dir := range t.ReadDirs {
		args = append(args, "--add-dir", dir)
	}
	return args
}

func (b *ClaudeBackend) Turn(ctx context.Context, t Turn, emit func(Event)) (TurnResult, error) {
	argv, err := b.argv()
	if err != nil {
		return TurnResult{}, fmt.Errorf("the [Chat.Claude] Command can't be read: %w", err)
	}
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], b.args(t)...)...)
	cmd.Dir = t.WorkDir
	cmd.Stdin = strings.NewReader(t.Prompt)
	cmd.Env = claudeEnv(os.Environ())
	cmd.WaitDelay = claudeWaitDelay
	stderr := &tailBuffer{max: 4000}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return TurnResult{}, fmt.Errorf("could not start %s: %w", argv[0], err)
	}

	stream := newClaudeStream(emit)
	stream.dirs = append([]string{t.WorkDir}, t.ReadDirs...)
	readErr := stream.read(stdout)
	waitErr := cmd.Wait()
	res := stream.result

	switch {
	case ctx.Err() != nil:
		return res, ctx.Err()
	case stream.failure != "":
		return res, errors.New(stream.failure)
	case waitErr != nil:
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = waitErr.Error()
		}
		return res, fmt.Errorf("%s failed: %s", filepath.Base(argv[0]), detail)
	case readErr != nil:
		return res, fmt.Errorf("reading %s's output: %w", filepath.Base(argv[0]), readErr)
	case !stream.finished:
		return res, fmt.Errorf("%s exited without finishing its answer", filepath.Base(argv[0]))
	}
	return res, nil
}

// claudeEnv is the server's environment for a claude run, less the marker a
// Claude Code session sets for its own children: a server started from one
// would otherwise have its chats taken for nested sessions.
func claudeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDECODE=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// claudeStream reads `claude -p --output-format stream-json` output: one JSON
// object per line. Only the conversation's own messages count; a subagent's
// carry a parent_tool_use_id and are skipped.
type claudeStream struct {
	emit     func(Event)
	result   TurnResult
	finished bool
	// failure is the error a result line reported.
	failure string
	// tools are the names of the calls in flight, for their results.
	tools map[string]string
	// dirs are the directories paths in a tool call are shown relative to.
	dirs []string
}

func newClaudeStream(emit func(Event)) *claudeStream {
	return &claudeStream{emit: emit, tools: map[string]string{}}
}

// claudeLine is the part of a stream-json line the chat reads.
type claudeLine struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Message         *claudeMessage  `json:"message"`
	Event           json.RawMessage `json:"event"`
	// The result line's.
	IsError      bool     `json:"is_error"`
	Result       string   `json:"result"`
	TotalCostUSD float64  `json:"total_cost_usd"`
	Errors       []string `json:"errors"`
}

type claudeMessage struct {
	Content json.RawMessage `json:"content"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

type claudeStreamEvent struct {
	Type         string       `json:"type"`
	ContentBlock *claudeBlock `json:"content_block"`
	Delta        *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

// read consumes the stream to its end. A line can be long — a tool result
// carries a whole file — so it is read without a length limit.
func (s *claudeStream) read(r io.Reader) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			s.line(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (s *claudeStream) line(raw []byte) {
	var l claudeLine
	if json.Unmarshal(raw, &l) != nil {
		return // not a stream-json line: a warning the CLI printed
	}
	if l.ParentToolUseID != nil && *l.ParentToolUseID != "" {
		return
	}
	switch l.Type {
	case "system":
		if l.Subtype == "init" && l.SessionID != "" {
			s.result.SessionID = l.SessionID
			s.emit(Event{Kind: EventSession, SessionID: l.SessionID})
		}
	case "stream_event":
		var e claudeStreamEvent
		if json.Unmarshal(l.Event, &e) != nil {
			return
		}
		if e.Type == "content_block_delta" && e.Delta != nil && e.Delta.Type == "text_delta" && e.Delta.Text != "" {
			s.emit(Event{Kind: EventTextDelta, Text: e.Delta.Text})
		}
	case "assistant":
		for _, block := range blocks(l.Message) {
			switch block.Type {
			case "text":
				s.result.Answered = true
				s.emit(Event{Kind: EventText, Text: block.Text})
			case "tool_use":
				s.result.Answered = true
				s.tools[block.ID] = block.Name
				s.emit(Event{Kind: EventToolUse, ToolUseID: block.ID, Tool: block.Name, Detail: toolDetail(block.Name, block.Input, s.relative)})
			}
		}
	case "user":
		for _, block := range blocks(l.Message) {
			if block.Type != "tool_result" {
				continue
			}
			if _, ok := s.tools[block.ToolUseID]; !ok {
				continue
			}
			delete(s.tools, block.ToolUseID)
			s.emit(Event{Kind: EventToolResult, ToolUseID: block.ToolUseID, IsError: block.IsError})
		}
	case "result":
		s.finished = true
		if l.SessionID != "" {
			s.result.SessionID = l.SessionID
		}
		s.result.CostUSD = l.TotalCostUSD
		if l.IsError || (l.Subtype != "" && l.Subtype != "success") {
			s.failure = resultFailure(l)
		}
	}
}

// relative shows a path under one of the turn's directories relative to it:
// "pr.diff" rather than the chat's working directory spelled out.
func (s *claudeStream) relative(path string) string {
	for _, dir := range s.dirs {
		if dir == "" {
			continue
		}
		if rel, err := filepath.Rel(dir, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && filepath.IsAbs(path) {
			return rel
		}
	}
	return path
}

// blocks are a message's content blocks; a message whose content is a plain
// string has none worth reading here.
func blocks(m *claudeMessage) []claudeBlock {
	if m == nil {
		return nil
	}
	var out []claudeBlock
	if json.Unmarshal(m.Content, &out) != nil {
		return nil
	}
	return out
}

// resultFailure says why a turn's result line reported an error.
func resultFailure(l claudeLine) string {
	if text := strings.TrimSpace(l.Result); text != "" {
		return text
	}
	if len(l.Errors) > 0 {
		return strings.Join(l.Errors, "; ")
	}
	switch l.Subtype {
	case "error_max_turns":
		return "The agent stopped: it reached its turn limit."
	case "error_during_execution":
		return "The agent failed while answering."
	}
	return "The agent reported an error (" + l.Subtype + ")."
}

// toolDetail says in a few words what a tool call was for: the file read, the
// pattern searched for.
func toolDetail(tool string, input json.RawMessage, relative func(string) string) string {
	var fields map[string]any
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	str := func(key string) string {
		v, _ := fields[key].(string)
		if (key == "file_path" || key == "path") && relative != nil {
			v = relative(v)
		}
		return v
	}
	var detail string
	switch tool {
	case "Read":
		detail = str("file_path")
	case "Grep":
		detail = str("pattern")
		if p := str("path"); p != "" {
			detail += " in " + p
		}
	case "Glob":
		detail = str("pattern")
		if p := str("path"); p != "" {
			detail += " in " + p
		}
	default:
		for _, key := range []string{"file_path", "path", "pattern", "query", "command", "description", "url"} {
			if detail = str(key); detail != "" {
				break
			}
		}
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 200 {
		detail = cutAtLine(detail, 199) + "…"
	}
	return detail
}

// tailBuffer keeps the last max bytes written to it: what a failing command
// said last is what explains it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append([]byte(nil), t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
