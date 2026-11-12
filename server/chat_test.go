package server

import (
	"context"
	"crs/chat"
	"crs/database"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubChatBackend answers every turn with a fixed reply, after a read of the
// diff, and records what it was asked. With hold set, a turn waits for a
// value on it before answering.
type stubChatBackend struct {
	mu    sync.Mutex
	turns []chat.Turn
	hold  chan struct{}
}

func (b *stubChatBackend) ID() string { return "stub" }
func (b *stubChatBackend) Info() chat.BackendInfo {
	return chat.BackendInfo{ID: "stub", Name: "Stub agent", Available: true,
		Models: []chat.Model{{ID: "", Name: "Default"}, {ID: "big", Name: "Big"}}}
}

func (b *stubChatBackend) Turn(ctx context.Context, t chat.Turn, emit func(chat.Event)) (chat.TurnResult, error) {
	b.mu.Lock()
	b.turns = append(b.turns, t)
	b.mu.Unlock()
	emit(chat.Event{Kind: chat.EventSession, SessionID: t.SessionID})
	if b.hold != nil {
		select {
		case <-b.hold:
		case <-ctx.Done():
			return chat.TurnResult{SessionID: t.SessionID}, ctx.Err()
		}
	}
	emit(chat.Event{Kind: chat.EventToolUse, ToolUseID: "t1", Tool: "Read", Detail: chat.DiffFile})
	emit(chat.Event{Kind: chat.EventToolResult, ToolUseID: "t1"})
	emit(chat.Event{Kind: chat.EventText, Text: "It adds a **punctuation** field."})
	return chat.TurnResult{SessionID: t.SessionID, CostUSD: 0.01, Answered: true}, nil
}

func (b *stubChatBackend) recorded() []chat.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]chat.Turn(nil), b.turns...)
}

// useChatBackend swaps the chat manager the RPCs use for one running backend
// for the rest of the test.
func useChatBackend(t *testing.T, b chat.Backend) {
	t.Helper()
	m := newChatManager(b)
	m.OnChange = notifyChatChanged
	orig := chatManager
	chatManager = m
	t.Cleanup(func() { chatManager = orig })
}

// chatChange reads the next line written as a Server.ChatChanged.
func chatChange(t *testing.T, out <-chan string) ChatChanged {
	t.Helper()
	line := nextLine(t, out, 5*time.Second)
	var n struct {
		Method string        `json:"method"`
		Params []ChatChanged `json:"params"`
		ID     any           `json:"id"`
	}
	if err := json.Unmarshal([]byte(line), &n); err != nil {
		t.Fatalf("not JSON: %s: %v", line, err)
	}
	if n.Method != ChatChangedMethod || n.ID != nil || len(n.Params) != 1 {
		t.Fatalf("not a %s notification: %s", ChatChangedMethod, line)
	}
	return n.Params[0]
}

// untilIdle reads chat notifications until one says the chat went idle.
func untilIdle(t *testing.T, out <-chan string, id string) {
	t.Helper()
	for {
		c := chatChange(t, out)
		if c.ChatID != id {
			t.Fatalf("a notification for another chat: %+v", c)
		}
		if c.Status == chat.StatusIdle {
			return
		}
	}
}

func TestChatRPCs(t *testing.T) {
	db := aiTestSetup(t, nil)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	backend := &stubChatBackend{hold: make(chan struct{})}
	useChatBackend(t, backend)
	out := subscribedNotifier(t)
	h := &RPCHandler{}

	var backends ListChatBackendsReply
	if err := h.ListChatBackends(&ListChatBackendsArgs{}, &backends); err != nil {
		t.Fatal(err)
	}
	if len(backends.Backends) != 1 || backends.Backends[0].ID != "stub" || len(backends.Backends[0].Models) != 2 {
		t.Fatalf("backends = %+v", backends.Backends)
	}

	var started ChatReply
	if err := h.StartChat(&StartChatArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Model: "big",
		Message: "What does the new field do?"}, &started); err != nil {
		t.Fatalf("StartChat: %v", err)
	}
	if !started.Okay || started.Chat == nil {
		t.Fatalf("StartChat = %+v", started)
	}
	c := started.Chat
	if c.Status != chat.StatusRunning || c.Backend != "stub" || c.Model != "big" || c.HeadSHA != "sha-1" || c.CurrentSHA != "sha-1" {
		t.Errorf("a started chat = %+v", c.ChatSummary)
	}
	if len(c.Messages) != 1 || c.Messages[0].Role != chat.RoleUser || c.Messages[0].Content != "What does the new field do?" ||
		strings.Join(c.Messages[0].Attached, ",") != "description,diff" {
		t.Errorf("the first message should carry the PR: %+v", c.Messages)
	}
	if got := chatChange(t, out); got != (ChatChanged{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, ChatID: c.ID, Status: chat.StatusRunning}) {
		t.Errorf("first announcement = %+v", got)
	}

	// A second message while the agent is answering is refused, and says why.
	var busy ChatReply
	if err := h.SendChatMessage(&SendChatMessageArgs{ChatID: c.ID, Message: "Hello?"}, &busy); err != nil {
		t.Fatal(err)
	}
	if busy.Okay || !strings.Contains(busy.Message, "still answering") {
		t.Errorf("a message while busy = %+v", busy)
	}

	backend.hold <- struct{}{}
	untilIdle(t, out, c.ID)

	turns := backend.recorded()
	if len(turns) != 1 {
		t.Fatalf("expected one turn, got %d", len(turns))
	}
	first := turns[0]
	for _, want := range []string{`We're reviewing pull request acme/widgets#42: "Add greeting helper" by @alice`,
		"What does the new field do?", "## PR description", "+    punctuation: string;"} {
		if !strings.Contains(first.Prompt, want) {
			t.Errorf("the opening prompt lacks %q:\n%s", want, first.Prompt)
		}
	}
	if first.Resume || first.Model != "big" || !strings.Contains(first.SystemPrompt, "acme/widgets#42") {
		t.Errorf("first turn = %+v", first)
	}

	var got GetChatReply
	if err := h.GetChat(&GetChatArgs{ChatID: c.ID}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Chat.Status != chat.StatusIdle || got.Chat.Draft != "" || got.Chat.CostUSD != 0.01 {
		t.Errorf("an answered chat = %+v", got.Chat.ChatSummary)
	}
	var roles []string
	for _, m := range got.Chat.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,tool,assistant" {
		t.Fatalf("messages = %q", roles)
	}
	if tool := got.Chat.Messages[1]; tool.Tool != "Read" || tool.ToolStatus != chat.ToolDone || tool.Content != chat.DiffFile {
		t.Errorf("tool message = %+v", tool)
	}

	// The next message resumes the conversation.
	backend.hold = nil
	var sent ChatReply
	if err := h.SendChatMessage(&SendChatMessageArgs{ChatID: c.ID, Message: "Does it need a default?"}, &sent); err != nil {
		t.Fatal(err)
	}
	if !sent.Okay || len(sent.Chat.Messages) != 4 || len(sent.Chat.Messages[3].Attached) != 0 {
		t.Errorf("SendChatMessage = %+v", sent)
	}
	untilIdle(t, out, c.ID)
	if turns := backend.recorded(); len(turns) != 2 || !turns[1].Resume || turns[1].SessionID != first.SessionID ||
		turns[1].Prompt != "Does it need a default?" {
		t.Errorf("second turn = %+v", turns[len(turns)-1])
	}

	var list ListChatsReply
	if err := h.ListChats(&ListChatsArgs{Owner: "ACME", Repo: aiRepo, Number: aiNumber}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Chats) != 1 || list.Chats[0].ID != c.ID || list.Chats[0].Title != "What does the new field do?" {
		t.Errorf("ListChats = %+v", list.Chats)
	}

	var deleted DeleteChatReply
	if err := h.DeleteChat(&DeleteChatArgs{ChatID: c.ID}, &deleted); err != nil || !deleted.Okay {
		t.Fatalf("DeleteChat = %+v, %v", deleted, err)
	}
	if err := h.GetChat(&GetChatArgs{ChatID: c.ID}, &got); err == nil {
		t.Error("GetChat after DeleteChat should fail")
	}
}

func TestStartChatRefusesAnEmptyMessage(t *testing.T) {
	db := aiTestSetup(t, nil)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	useChatBackend(t, &stubChatBackend{})
	var reply ChatReply
	if err := (&RPCHandler{}).StartChat(&StartChatArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Message: "  "}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Okay || reply.Chat != nil || reply.Message == "" {
		t.Errorf("StartChat with no message = %+v", reply)
	}
}

func TestStopChatStopsARunningAgent(t *testing.T) {
	db := aiTestSetup(t, nil)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	useChatBackend(t, &stubChatBackend{hold: make(chan struct{})})
	out := subscribedNotifier(t)
	h := &RPCHandler{}

	var started ChatReply
	if err := h.StartChat(&StartChatArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Message: "Slow one"}, &started); err != nil || !started.Okay {
		t.Fatalf("StartChat = %+v, %v", started, err)
	}
	var stop StopChatReply
	if err := h.StopChat(&StopChatArgs{ChatID: started.Chat.ID}, &stop); err != nil || !stop.Stopped {
		t.Fatalf("StopChat = %+v, %v", stop, err)
	}
	untilIdle(t, out, started.Chat.ID)
	var got GetChatReply
	if err := h.GetChat(&GetChatArgs{ChatID: started.Chat.ID}, &got); err != nil {
		t.Fatal(err)
	}
	if n := len(got.Chat.Messages); n != 2 || got.Chat.Messages[1].Role != chat.RoleNotice {
		t.Errorf("a stopped chat should say so: %+v", got.Chat.Messages)
	}
	if err := h.StopChat(&StopChatArgs{ChatID: started.Chat.ID}, &stop); err != nil || stop.Stopped {
		t.Errorf("stopping an idle chat = %+v, %v", stop, err)
	}
}

func TestChatMessageJSONReadsTheMeta(t *testing.T) {
	got := chatMessageJSON(databaseMessage(`{"attached":["description","diff"],"diff_truncated":true,"head_sha":"abc"}`))
	if strings.Join(got.Attached, ",") != "description,diff" || !got.DiffTruncated || got.HeadSHA != "abc" {
		t.Errorf("user meta = %+v", got)
	}
	if got := chatMessageJSON(databaseMessage(`not json`)); got.Tool != "" || got.Content != "x" {
		t.Errorf("a broken meta should still give the message: %+v", got)
	}
}

func databaseMessage(meta string) database.ChatMessage {
	return database.ChatMessage{ID: 1, Role: chat.RoleUser, Content: "x", Meta: meta}
}
