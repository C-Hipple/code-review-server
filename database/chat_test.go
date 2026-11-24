package database

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestChatSessionRoundTrip(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateChatSession(ChatSession{
		ID: "c1", Owner: "Acme", Repo: "widgets", Number: 42,
		Backend: "claude", Model: "sonnet", HeadSHA: "sha-1", Title: "Is this safe?",
	}); err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}

	got, ok, err := db.GetChatSession("c1")
	if err != nil || !ok {
		t.Fatalf("GetChatSession = %v, %v", ok, err)
	}
	if got.Owner != "Acme" || got.Repo != "widgets" || got.Number != 42 || got.Backend != "claude" ||
		got.Model != "sonnet" || got.HeadSHA != "sha-1" || got.Title != "Is this safe?" {
		t.Errorf("unexpected session: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", got)
	}

	if _, ok, err := db.GetChatSession("missing"); ok || err != nil {
		t.Errorf("a missing chat should read as absent, got %v, %v", ok, err)
	}
}

func TestRecordChatTurnKeepsTheHandleAndAddsTheCost(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateChatSession(ChatSession{ID: "c1", Owner: "acme", Repo: "widgets", Number: 1, Backend: "claude"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordChatTurn("c1", "sess-1", 0.25); err != nil {
		t.Fatal(err)
	}
	// A turn that reported no handle (it failed before saying) leaves the old one.
	if err := db.RecordChatTurn("c1", "", 0.5); err != nil {
		t.Fatal(err)
	}
	got, _, _ := db.GetChatSession("c1")
	if got.BackendSessionID != "sess-1" || got.CostUSD != 0.75 {
		t.Errorf("after two turns: handle %q, cost %v", got.BackendSessionID, got.CostUSD)
	}
}

func TestChatMessagesInOrderAndDeletedWithTheirChat(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateChatSession(ChatSession{ID: "c1", Owner: "acme", Repo: "widgets", Number: 1, Backend: "claude"}); err != nil {
		t.Fatal(err)
	}
	first, err := db.AddChatMessage(ChatMessage{SessionID: "c1", Role: "user", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	toolID, err := db.AddChatMessage(ChatMessage{SessionID: "c1", Role: "tool", Content: "Read a.go", Meta: `{"status":"running"}`})
	if err != nil {
		t.Fatal(err)
	}
	if toolID <= first {
		t.Fatalf("message IDs should grow: %d then %d", first, toolID)
	}
	if err := db.UpdateChatMessage(toolID, "Read a.go", `{"status":"done"}`); err != nil {
		t.Fatal(err)
	}

	msgs, err := db.GetChatMessages("c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Content != "hi" || msgs[1].Meta != `{"status":"done"}` {
		t.Fatalf("unexpected messages: %+v", msgs)
	}

	if err := db.DeleteChatSession("c1"); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := db.GetChatMessages("c1"); len(msgs) != 0 {
		t.Errorf("messages outlived their chat: %+v", msgs)
	}
}

func TestListChatSessionsIgnoresCaseAndOtherPRs(t *testing.T) {
	db := newTestDB(t)
	for _, s := range []ChatSession{
		{ID: "old", Owner: "Acme", Repo: "Widgets", Number: 7, Backend: "claude"},
		{ID: "new", Owner: "acme", Repo: "widgets", Number: 7, Backend: "claude"},
		{ID: "other", Owner: "acme", Repo: "widgets", Number: 8, Backend: "claude"},
	} {
		if err := db.CreateChatSession(s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ListChatSessions("ACME", "widgets", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected the PR's two chats, got %+v", got)
	}
	// Created in the same second, so the later insert breaks the tie.
	if got[0].ID != "new" || got[1].ID != "old" {
		t.Errorf("expected the newest first, got %s then %s", got[0].ID, got[1].ID)
	}
}

func TestChatSessionsCountTurnsAndPreviewTheLastReply(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateChatSession(ChatSession{ID: "c1", Owner: "acme", Repo: "widgets", Number: 1, Backend: "claude"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := db.GetChatSession("c1")
	if got.Turns != 0 || got.Preview != "" {
		t.Errorf("an empty chat: %d turns, preview %q", got.Turns, got.Preview)
	}

	// Before any reply, the reviewer's message stands in.
	if _, err := db.AddChatMessage(ChatMessage{SessionID: "c1", Role: "user", Content: "Why?"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := db.GetChatSession("c1"); got.Turns != 1 || got.Preview != "Why?" {
		t.Errorf("after the question: %d turns, preview %q", got.Turns, got.Preview)
	}

	long := strings.Repeat("é", 400)
	for _, m := range []ChatMessage{
		{SessionID: "c1", Role: "assistant", Content: "Because."},
		{SessionID: "c1", Role: "user", Content: "And then?"},
		{SessionID: "c1", Role: "tool", Content: "pr.diff"},
		{SessionID: "c1", Role: "assistant", Content: long},
		{SessionID: "c1", Role: "notice", Content: "Stopped."},
	} {
		if _, err := db.AddChatMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	list, err := db.ListChatSessions("acme", "widgets", 1)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListChatSessions = %+v, %v", list, err)
	}
	if list[0].Turns != 2 {
		t.Errorf("turns = %d, want the reviewer's two messages", list[0].Turns)
	}
	if list[0].Preview != strings.Repeat("é", 300) {
		t.Errorf("the preview should be the last reply's first 300 characters, got %d: %q",
			len([]rune(list[0].Preview)), list[0].Preview)
	}
}

func TestRenameAndSetHeadLeaveTheChatsPlace(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateChatSession(ChatSession{ID: "c1", Owner: "acme", Repo: "widgets", Number: 1, Backend: "claude",
		Title: "First", HeadSHA: "sha-1"}); err != nil {
		t.Fatal(err)
	}
	before, _, _ := db.GetChatSession("c1")
	if err := db.RenameChatSession("c1", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatHeadSHA("c1", "sha-2"); err != nil {
		t.Fatal(err)
	}
	got, _, _ := db.GetChatSession("c1")
	if got.Title != "Renamed" || got.HeadSHA != "sha-2" {
		t.Errorf("after renaming and a new head: %+v", got)
	}
	if !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("a rename isn't activity: updated_at moved from %v to %v", before.UpdatedAt, got.UpdatedAt)
	}
}

func TestForkChatSessionCopiesTheConversation(t *testing.T) {
	db := newTestDB(t)
	if err := db.CreateChatSession(ChatSession{ID: "orig", Owner: "acme", Repo: "widgets", Number: 1, Backend: "claude",
		BackendSessionID: "agent-1", HeadSHA: "sha-1", Title: "Why?", CostUSD: 0.5}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []ChatMessage{
		{SessionID: "orig", Role: "user", Content: "Why?", Meta: `{"attached":["diff"]}`},
		{SessionID: "orig", Role: "tool", Content: "pr.diff", Meta: `{"status":"done"}`},
		{SessionID: "orig", Role: "assistant", Content: "Because."},
		{SessionID: "orig", Role: "error", Content: "boom"},
		{SessionID: "orig", Role: "user", Content: "Sure?"},
	} {
		if _, err := db.AddChatMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.ForkChatSession(ChatSession{ID: "fork", Owner: "acme", Repo: "widgets", Number: 1, Backend: "claude",
		Title: "Fork: Why?", ForkedFrom: "orig"}, []string{"user", "assistant", "tool"}); err != nil {
		t.Fatalf("ForkChatSession: %v", err)
	}
	fork, ok, err := db.GetChatSession("fork")
	if err != nil || !ok {
		t.Fatalf("GetChatSession(fork) = %v, %v", ok, err)
	}
	if fork.ForkedFrom != "orig" || fork.BackendSessionID != "" || fork.HeadSHA != "" || fork.CostUSD != 0 || fork.Turns != 2 {
		t.Errorf("a fork starts its own conversation: %+v", fork)
	}
	msgs, err := db.GetChatMessages("fork")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.Role+":"+m.Content)
	}
	if strings.Join(got, "|") != "user:Why?|tool:pr.diff|assistant:Because.|user:Sure?" {
		t.Errorf("forked messages = %q", got)
	}
	if msgs[0].Meta != `{"attached":["diff"]}` || msgs[0].CreatedAt.IsZero() {
		t.Errorf("a copied message keeps its meta and time: %+v", msgs[0])
	}
	// The original is untouched, and deleting it leaves the fork.
	if orig, _ := db.GetChatMessages("orig"); len(orig) != 5 {
		t.Errorf("the original has %d messages", len(orig))
	}
	if err := db.DeleteChatSession("orig"); err != nil {
		t.Fatal(err)
	}
	if msgs, _ := db.GetChatMessages("fork"); len(msgs) != 4 {
		t.Errorf("the fork lost messages with its original: %d left", len(msgs))
	}

	// A fork whose copy fails leaves nothing behind.
	if err := db.ForkChatSession(ChatSession{ID: "fork", Owner: "acme", Repo: "widgets", Number: 1, Backend: "claude",
		ForkedFrom: "orig"}, []string{"user"}); err == nil {
		t.Error("a second chat with the same ID should be refused")
	}
	if err := db.ForkChatSession(ChatSession{ID: "nameless", Owner: "acme", Repo: "widgets", Number: 1, Backend: "claude"}, nil); err == nil {
		t.Error("a fork must name what it copies")
	}
}

func TestChatSessionsGainForkedFromOnAnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	// The table as it was before forks.
	if _, err := db.conn.Exec(`DROP TABLE ChatSessions`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`CREATE TABLE ChatSessions (
		id TEXT PRIMARY KEY, owner TEXT NOT NULL, repo TEXT NOT NULL, pr_number INTEGER NOT NULL,
		backend TEXT NOT NULL, model TEXT NOT NULL DEFAULT '', backend_session_id TEXT NOT NULL DEFAULT '',
		head_sha TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', cost_usd REAL NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`INSERT INTO ChatSessions (id, owner, repo, pr_number, backend, title)
		VALUES ('c1', 'acme', 'widgets', 1, 'claude', 'Kept')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db, err = NewDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, ok, err := db.GetChatSession("c1")
	if err != nil || !ok || got.Title != "Kept" || got.ForkedFrom != "" {
		t.Errorf("an old chat after the migration = %+v, %v, %v", got, ok, err)
	}
}
