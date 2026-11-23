package chat

import (
	"context"
	"crs/config"
	"crs/database"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealACP runs a chat against an ACP agent on this machine, which costs a
// little. It is skipped unless CRS_CHAT_REAL_ACP is set to the command that
// starts the agent speaking ACP; CRS_CHAT_REAL_ACP_MODEL picks its model:
//
//	CRS_CHAT_REAL_ACP=claude-code-acp CRS_CHAT_REAL_ACP_MODEL=haiku go test -run TestRealACP -v ./chat/
//	CRS_CHAT_REAL_ACP="gemini --experimental-acp" go test -run TestRealACP -v ./chat/
//	CRS_CHAT_REAL_ACP="opencode acp" go test -run TestRealACP -v ./chat/
//
// It checks that the agent answers from the attached diff, reads the working
// directory, can't write a file, and picks the conversation up again in a
// new process.
func TestRealACP(t *testing.T) {
	command := os.Getenv("CRS_CHAT_REAL_ACP")
	if command == "" {
		t.Skip("set CRS_CHAT_REAL_ACP to the command that starts an ACP agent")
	}
	model := os.Getenv("CRS_CHAT_REAL_ACP_MODEL")
	settings := config.ChatBackendSettings{Protocol: config.ChatProtocolACP, Command: command, DefaultModel: model}
	backend := NewACPBackend("real", func() config.ChatBackendSettings { return settings })
	if info := backend.Info(); !info.Available {
		t.Skipf("the agent is not available: %s", info.Reason)
	}
	t.Cleanup(backend.ReleaseAll)

	db, err := database.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := NewManager(Fixed{backend})
	dir := t.TempDir()
	m.Store = func() Store { return db }
	m.Dir = func() (string, error) { return dir, nil }
	m.LoadPR = func(context.Context, string, string, int) (PRContext, error) {
		pr := testPR
		pr.Diff = "diff --git a/greet.go b/greet.go\n--- a/greet.go\n+++ b/greet.go\n@@ -1,3 +1,5 @@\n package greet\n+\n+// Greet says hello to the codeword PAPAYA.\n+func Greet() string { return \"hello PAPAYA\" }\n"
		return pr, nil
	}
	idle := make(chan struct{}, 8)
	m.OnChange = func(_ database.ChatSession, status string) {
		if status == StatusIdle {
			idle <- struct{}{}
		}
	}
	turn := func(id, message string) View {
		t.Helper()
		if err := m.Send(context.Background(), id, message); err != nil {
			t.Fatal(err)
		}
		return waitView(t, m, id, idle)
	}

	session, err := m.Start(context.Background(), StartRequest{
		Owner: "acme", Repo: "widgets", Number: 42, Model: model,
		Message: "What codeword does the new function greet? Answer with the word alone.",
	})
	if err != nil {
		t.Fatal(err)
	}
	v := waitView(t, m, session.ID, idle)
	t.Logf("first turn: %s", dump(v.Messages))
	if last := v.Messages[len(v.Messages)-1]; last.Role != RoleAssistant || !strings.Contains(strings.ToUpper(last.Content), "PAPAYA") {
		t.Fatalf("the agent should have answered from the attached diff, got %+v", last)
	}
	if v.Session.BackendSessionID == "" {
		t.Errorf("the agent's session should be recorded: %+v", v.Session)
	}

	v = turn(session.ID, "Read the file pr.md in your working directory and tell me the PR author's login, alone.")
	t.Logf("second turn: %s", dump(v.Messages))
	if last := v.Messages[len(v.Messages)-1]; last.Role != RoleAssistant || !strings.Contains(last.Content, "alice") {
		t.Fatalf("the agent should have read pr.md, got %+v", last)
	}

	v = turn(session.ID, "Create a file named notes.txt in your working directory containing the word hi. Then say done.")
	t.Logf("third turn: %s", dump(v.Messages))
	if _, err := os.Stat(filepath.Join(dir, session.ID, "notes.txt")); err == nil {
		t.Fatal("the agent wrote a file in a read-only chat")
	}

	// A new process picks the conversation up again (or, for an agent that
	// can't, the Manager starts a new one carrying what was said).
	backend.ReleaseAll()
	deadline := time.Now().Add(10 * time.Second)
	for backend.running() > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	v = turn(session.ID, "Which codeword did you find in the diff earlier? Answer with the word alone.")
	t.Logf("fourth turn: %s", dump(v.Messages))
	if last := v.Messages[len(v.Messages)-1]; last.Role != RoleAssistant || !strings.Contains(strings.ToUpper(last.Content), "PAPAYA") {
		t.Fatalf("the agent should have remembered the conversation, got %+v", last)
	}
}

func waitView(t *testing.T, m *Manager, id string, idle <-chan struct{}) View {
	t.Helper()
	select {
	case <-idle:
	case <-time.After(4 * time.Minute):
		t.Fatal("the agent never finished")
	}
	v, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
