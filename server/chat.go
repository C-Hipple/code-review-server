package server

import (
	"context"
	"crs/chat"
	"crs/config"
	"crs/database"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
)

// --- Review chat ---
//
// A reviewer can chat with a local agent (Claude Code, to start with) about
// the PR they're reviewing; the chat package runs it. The server holds the
// conversations: StartChat opens one and SendChatMessage continues it, each
// returning as soon as the reviewer's message is stored while the agent
// answers in the background, and GetChat reads a chat back. A subscribed
// client hears Server.ChatChanged as each step lands and reads GetChat again,
// as it does for plugin and AI output.

// chatManager runs the chats for the RPCs. Tests swap it.
var chatManager = newChatManager(chat.NewClaudeBackend())

// newChatManager is the chat manager for the given backends, keeping chats in
// the server's database and reading PRs from its caches.
func newChatManager(backends ...chat.Backend) *chat.Manager {
	m := chat.NewManager(backends...)
	m.Store = func() chat.Store {
		if db := config.C().DB; db != nil {
			return db
		}
		return nil
	}
	m.Dir = chatsDir
	m.LoadPR = loadChatPR
	return m
}

// chatsDir is where the chats' working directories go: $CRS_HOME/chats.
func chatsDir() (string, error) {
	home, err := config.GetCRSHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "chats"), nil
}

// loadChatPR reads what a chat attaches about its PR from the same caches
// GetPR serves (reaching GitHub only on a miss), and the checkout the agent may read: the PR's worktree when
// there is one, else the local clone.
func loadChatPR(_ context.Context, owner, repo string, number int) (chat.PRContext, error) {
	details, err := GetPRDetails(owner, repo, number, false)
	if err != nil {
		return chat.PRContext{}, err
	}
	md := details.Metadata
	pr := chat.PRContext{
		Owner:   owner,
		Repo:    repo,
		Number:  number,
		Title:   md.Title,
		Author:  md.Author,
		URL:     md.URL,
		BaseRef: md.BaseRef,
		HeadRef: md.HeadRef,
		State:   md.State,
		Body:    md.Body,
		Diff:    details.Diff,
	}
	// The diff as GitHub sent it, which GetPRDetails has just made sure is
	// cached: an agent reads a plain unified diff best, and pr.diff should
	// be one. The rendered one stands in when there is none.
	if db := config.C().DB; db != nil {
		raw, sha, err := db.GetPullRequest(number, repo)
		if err == nil {
			pr.HeadSHA = sha
			if strings.TrimSpace(raw) != "" {
				pr.Diff = raw
			}
		}
	}
	switch {
	case md.WorktreePath != "":
		pr.Checkout, pr.AtHead = md.WorktreePath, true
	case md.RepoPath != "":
		pr.Checkout = md.RepoPath
	}
	return pr, nil
}

// ChatChangedMethod is the method a notification names when what GetChat
// returns for a chat changes: a message lands, the reply being written grows,
// a turn starts or ends, the chat is deleted.
const ChatChangedMethod = "Server.ChatChanged"

// ChatChanged is a Server.ChatChanged notification's params. Like the other
// change notifications it names what changed, not the messages: a client
// showing the chat reads GetChat again.
type ChatChanged struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	ChatID string `json:"chat_id"`
	// Status is "running" while the agent is answering and "idle" once it is
	// done (or stopped, or the chat deleted).
	Status string `json:"status"`
}

// notifyChatChanged tells a subscribed client that a chat changed. It is the
// chat manager's OnChange in server mode.
func notifyChatChanged(s database.ChatSession, status string) {
	n := notifier
	if n == nil {
		return
	}
	n.Notify(ChatChangedMethod, ChatChanged{Owner: s.Owner, Repo: s.Repo, Number: s.Number, ChatID: s.ID, Status: status})
}

// ChatSummary is a chat as a list shows it.
type ChatSummary struct {
	ID      string `json:"id"`
	Owner   string `json:"owner"`
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	Backend string `json:"backend"`
	// Model is the model asked for; empty means the backend's default.
	Model string `json:"model"`
	Title string `json:"title"`
	// HeadSHA is the PR head whose description and diff the chat attached.
	HeadSHA string  `json:"head_sha"`
	CostUSD float64 `json:"cost_usd"`
	// Status is "running" while the agent is answering, else "idle".
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ChatMessageJSON is one message of a chat.
type ChatMessageJSON struct {
	ID int64 `json:"id"`
	// Role is "user" (the reviewer), "assistant" (the agent's reply, in
	// Markdown), "tool" (a tool the agent used; Content says on what),
	// "error" or "notice".
	Role      string `json:"role"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	// Tool and ToolStatus describe a tool message: the tool's name, and
	// "running", "done" or "error".
	Tool       string `json:"tool,omitempty"`
	ToolStatus string `json:"tool_status,omitempty"`
	// Attached lists what a reviewer's message carried to the agent beside
	// their words ("description", "diff"), as the first message of a
	// conversation does; DiffTruncated says the diff was cut to fit, and
	// HeadSHA is the head they came from.
	Attached      []string `json:"attached,omitempty"`
	DiffTruncated bool     `json:"diff_truncated,omitempty"`
	HeadSHA       string   `json:"head_sha,omitempty"`
}

// ChatJSON is a chat with its messages.
type ChatJSON struct {
	ChatSummary
	Messages []ChatMessageJSON `json:"messages"`
	// Draft is the reply the agent is writing right now, not yet a message;
	// empty between replies.
	Draft string `json:"draft"`
	// StartedAt is when the running turn started (RFC 3339); empty when idle.
	StartedAt string `json:"started_at,omitempty"`
	// CurrentSHA is the PR's head now, as the caches have it; a HeadSHA that
	// differs means the PR has moved on since the chat attached it.
	CurrentSHA string `json:"current_sha"`
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func chatSummary(s database.ChatSession) ChatSummary {
	status := chat.StatusIdle
	if chatManager.Running(s.ID) {
		status = chat.StatusRunning
	}
	return ChatSummary{
		ID:        s.ID,
		Owner:     s.Owner,
		Repo:      s.Repo,
		Number:    s.Number,
		Backend:   s.Backend,
		Model:     s.Model,
		Title:     s.Title,
		HeadSHA:   s.HeadSHA,
		CostUSD:   s.CostUSD,
		Status:    status,
		CreatedAt: formatTime(s.CreatedAt),
		UpdatedAt: formatTime(s.UpdatedAt),
	}
}

// chatMessageJSON reads a stored message's meta into its fields.
func chatMessageJSON(m database.ChatMessage) ChatMessageJSON {
	out := ChatMessageJSON{ID: m.ID, Role: m.Role, Content: m.Content, CreatedAt: formatTime(m.CreatedAt)}
	if m.Meta == "" {
		return out
	}
	var meta struct {
		Tool          string   `json:"tool"`
		Status        string   `json:"status"`
		Attached      []string `json:"attached"`
		DiffTruncated bool     `json:"diff_truncated"`
		HeadSHA       string   `json:"head_sha"`
	}
	if err := json.Unmarshal([]byte(m.Meta), &meta); err != nil {
		return out
	}
	out.Tool, out.ToolStatus = meta.Tool, meta.Status
	out.Attached, out.DiffTruncated, out.HeadSHA = meta.Attached, meta.DiffTruncated, meta.HeadSHA
	return out
}

func chatJSON(v chat.View) ChatJSON {
	out := ChatJSON{
		ChatSummary: chatSummary(v.Session),
		Messages:    make([]ChatMessageJSON, 0, len(v.Messages)),
		Draft:       v.Draft,
		StartedAt:   formatTime(v.StartedAt),
	}
	if v.Running {
		out.Status = chat.StatusRunning
	}
	out.CurrentSHA, _ = currentAIInputs(v.Session.Repo, v.Session.Number)
	for _, m := range v.Messages {
		out.Messages = append(out.Messages, chatMessageJSON(m))
	}
	return out
}

type ListChatBackendsArgs struct{}

type ListChatBackendsReply struct {
	// Backends lists every chat backend, available or not, in the order to
	// offer them.
	Backends []chat.BackendInfo `json:"backends"`
}

// ListChatBackends lists the agents a chat can run, and the models each
// offers.
func (h *RPCHandler) ListChatBackends(args *ListChatBackendsArgs, reply *ListChatBackendsReply) error {
	reply.Backends = chatManager.Backends()
	return nil
}

type ListChatsArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
}

type ListChatsReply struct {
	// Chats are the PR's chats, the most recently active first.
	Chats []ChatSummary `json:"chats"`
}

// ListChats lists a PR's chats, without their messages.
func (h *RPCHandler) ListChats(args *ListChatsArgs, reply *ListChatsReply) error {
	sessions, err := chatManager.List(args.Owner, args.Repo, args.Number)
	if err != nil {
		return err
	}
	reply.Chats = make([]ChatSummary, 0, len(sessions))
	for _, s := range sessions {
		reply.Chats = append(reply.Chats, chatSummary(s))
	}
	return nil
}

type StartChatArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
	// Backend is a backend's ID; empty picks the first available.
	Backend string `json:"Backend"`
	// Model is one of the backend's models; empty takes its default.
	Model   string `json:"Model"`
	Message string `json:"Message"`
}

// ChatReply answers StartChat and SendChatMessage.
type ChatReply struct {
	// Okay is false when the message was refused (empty, the agent still
	// answering, the backend unavailable); Message says why.
	Okay    bool   `json:"okay"`
	Message string `json:"message"`
	// Chat is the chat right after the message was stored, already running;
	// null when the message was refused before a chat existed.
	Chat *ChatJSON `json:"chat"`
}

// StartChat opens a chat about a PR with the reviewer's first message. The
// agent is told we're reviewing the PR and given the message, then the PR's
// description and diff; it answers in the background.
func (h *RPCHandler) StartChat(args *StartChatArgs, reply *ChatReply) error {
	session, err := chatManager.Start(context.Background(), chat.StartRequest{
		Owner: args.Owner, Repo: args.Repo, Number: args.Number,
		Backend: args.Backend, Model: args.Model, Message: args.Message,
	})
	if err != nil {
		reply.Message = err.Error()
		slog.Warn("Could not start a chat", "repo", args.Repo, "pr", args.Number, "error", err)
		return nil
	}
	reply.Okay = true
	return fillChatReply(session.ID, reply)
}

type SendChatMessageArgs struct {
	ChatID  string `json:"ChatID"`
	Message string `json:"Message"`
}

// SendChatMessage continues a chat with the reviewer's next message, which
// the agent answers in the background. A chat whose agent is still answering
// refuses it.
func (h *RPCHandler) SendChatMessage(args *SendChatMessageArgs, reply *ChatReply) error {
	err := chatManager.Send(context.Background(), args.ChatID, args.Message)
	if errors.Is(err, chat.ErrNotFound) {
		return fmt.Errorf("there is no chat %q", args.ChatID)
	}
	if err != nil {
		reply.Message = err.Error()
	} else {
		reply.Okay = true
	}
	return fillChatReply(args.ChatID, reply)
}

func fillChatReply(id string, reply *ChatReply) error {
	v, err := chatManager.Get(id)
	if err != nil {
		return err
	}
	c := chatJSON(v)
	reply.Chat = &c
	return nil
}

type GetChatArgs struct {
	ChatID string `json:"ChatID"`
}

type GetChatReply struct {
	Chat ChatJSON `json:"chat"`
}

// GetChat reads a chat: its messages, and the reply being written while the
// agent is answering. It is a pure read.
func (h *RPCHandler) GetChat(args *GetChatArgs, reply *GetChatReply) error {
	v, err := chatManager.Get(args.ChatID)
	if errors.Is(err, chat.ErrNotFound) {
		return fmt.Errorf("there is no chat %q", args.ChatID)
	}
	if err != nil {
		return err
	}
	reply.Chat = chatJSON(v)
	return nil
}

type StopChatArgs struct {
	ChatID string `json:"ChatID"`
}

type StopChatReply struct {
	// Stopped is false when the chat had nothing running.
	Stopped bool `json:"stopped"`
}

// StopChat stops the agent answering in a chat. The chat notes it once the
// agent has exited, and goes idle.
func (h *RPCHandler) StopChat(args *StopChatArgs, reply *StopChatReply) error {
	reply.Stopped = chatManager.Stop(args.ChatID)
	return nil
}

type DeleteChatArgs struct {
	ChatID string `json:"ChatID"`
}

type DeleteChatReply struct {
	Okay bool `json:"okay"`
}

// DeleteChat removes a chat and its messages, stopping it first if it is
// running.
func (h *RPCHandler) DeleteChat(args *DeleteChatArgs, reply *DeleteChatReply) error {
	if err := chatManager.Delete(args.ChatID); err != nil {
		if errors.Is(err, chat.ErrNotFound) {
			return fmt.Errorf("there is no chat %q", args.ChatID)
		}
		return err
	}
	reply.Okay = true
	return nil
}
