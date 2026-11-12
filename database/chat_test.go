package database

import "testing"

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
