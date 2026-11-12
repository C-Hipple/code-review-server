package chat

import (
	"context"
	"crs/database"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealClaude runs two turns of a chat against the Claude Code CLI on this
// machine, which costs a little. It is skipped unless CRS_CHAT_REAL_CLAUDE is
// set (to the model to use, e.g. "haiku", or "1" for Claude's default):
//
//	CRS_CHAT_REAL_CLAUDE=haiku go test -run TestRealClaude -v ./chat/
func TestRealClaude(t *testing.T) {
	model := os.Getenv("CRS_CHAT_REAL_CLAUDE")
	if model == "" {
		t.Skip("set CRS_CHAT_REAL_CLAUDE to run a chat against the real claude CLI")
	}
	if model == "1" {
		model = ""
	}
	db, err := database.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := NewManager(NewClaudeBackend())
	if info := m.Backends()[0]; !info.Available {
		t.Skipf("claude is not available: %s", info.Reason)
	}
	dir := t.TempDir()
	m.Store = func() Store { return db }
	m.Dir = func() (string, error) { return dir, nil }
	m.LoadPR = func(context.Context, string, string, int) (PRContext, error) {
		pr := testPR
		pr.Diff = "diff --git a/greet.go b/greet.go\n--- a/greet.go\n+++ b/greet.go\n@@ -1,3 +1,5 @@\n package greet\n+\n+// Greet says hello to the codeword PAPAYA.\n+func Greet() string { return \"hello PAPAYA\" }\n"
		return pr, nil
	}
	idle := make(chan struct{}, 4)
	m.OnChange = func(_ database.ChatSession, status string) {
		if status == StatusIdle {
			idle <- struct{}{}
		}
	}
	wait := func() {
		t.Helper()
		select {
		case <-idle:
		case <-time.After(3 * time.Minute):
			t.Fatal("the agent never finished")
		}
	}

	session, err := m.Start(context.Background(), StartRequest{
		Owner: "acme", Repo: "widgets", Number: 42, Model: model,
		Message: "What codeword does the new function greet? Answer with the word alone.",
	})
	if err != nil {
		t.Fatal(err)
	}
	wait()
	v, _ := m.Get(session.ID)
	t.Logf("first turn: %s", dump(v.Messages))
	if last := v.Messages[len(v.Messages)-1]; last.Role != RoleAssistant || !strings.Contains(strings.ToUpper(last.Content), "PAPAYA") {
		t.Fatalf("the agent should have answered from the attached diff, got %+v", last)
	}
	if v.Session.BackendSessionID == "" || v.Session.CostUSD <= 0 {
		t.Errorf("the agent's session and cost should be recorded: %+v", v.Session)
	}

	if err := m.Send(context.Background(), session.ID,
		"Read the file pr.md in your working directory and tell me the PR author's login, alone."); err != nil {
		t.Fatal(err)
	}
	wait()
	v, _ = m.Get(session.ID)
	t.Logf("second turn: %s", dump(v.Messages))
	if last := v.Messages[len(v.Messages)-1]; last.Role != RoleAssistant || !strings.Contains(last.Content, "alice") {
		t.Fatalf("the resumed agent should have read pr.md, got %+v", last)
	}
}

func dump(msgs []database.ChatMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString("\n  [" + m.Role + "] " + strings.TrimSpace(m.Content))
		if m.Meta != "" && m.Role == RoleTool {
			b.WriteString(" " + m.Meta)
		}
	}
	return b.String()
}
