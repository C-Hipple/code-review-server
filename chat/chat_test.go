package chat

import (
	"context"
	"crs/database"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testPR = PRContext{
	Owner: "acme", Repo: "widgets", Number: 42,
	Title: "Add a greeting", Author: "alice", URL: "https://github.com/acme/widgets/pull/42",
	BaseRef: "main", HeadRef: "greet", HeadSHA: "0123456789abcdef", State: "open",
	Body: "Says hello.",
	Diff: "diff --git a/greet.go b/greet.go\n+func Greet() {}\n",
}

type harness struct {
	m       *Manager
	db      *database.DB
	dir     string
	runs    func() []fakeRun
	mu      sync.Mutex
	changes []string
	idle    chan string
}

func newHarness(t *testing.T, mode string) *harness {
	t.Helper()
	db, err := database.NewDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	b := &ClaudeBackend{}
	h := &harness{db: db, dir: t.TempDir(), runs: useFakeClaude(t, b, mode), idle: make(chan string, 16)}
	h.m = NewManager(Fixed{b})
	h.m.Store = func() Store { return db }
	h.m.Dir = func() (string, error) { return h.dir, nil }
	h.m.LoadPR = func(_ context.Context, owner, repo string, number int) (PRContext, error) {
		pr := testPR
		pr.Owner, pr.Repo, pr.Number = owner, repo, number
		return pr, nil
	}
	h.m.OnChange = func(s database.ChatSession, status string) {
		h.mu.Lock()
		h.changes = append(h.changes, status)
		h.mu.Unlock()
		if status == StatusIdle {
			h.idle <- s.ID
		}
	}
	t.Cleanup(func() {
		// No turn may outlive its test: its fake would write to the next one's log.
		for _, s := range h.sessions(t) {
			h.m.Stop(s.ID)
		}
	})
	return h
}

func (h *harness) sessions(t *testing.T) []database.ChatSession {
	all, err := h.db.ListChatSessions(testPR.Owner, testPR.Repo, testPR.Number)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func (h *harness) waitIdle(t *testing.T, id string) {
	t.Helper()
	select {
	case got := <-h.idle:
		if got != id {
			t.Fatalf("chat %s went idle, expected %s", got, id)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("chat %s never went idle", id)
	}
}

func (h *harness) view(t *testing.T, id string) View {
	t.Helper()
	v, err := h.m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func roles(msgs []database.ChatMessage) string {
	var r []string
	for _, m := range msgs {
		r = append(r, m.Role)
	}
	return strings.Join(r, ",")
}

func TestStartAttachesThePRAndAnswers(t *testing.T) {
	h := newHarness(t, "ok")
	session, err := h.m.Start(context.Background(), StartRequest{
		Owner: "acme", Repo: "widgets", Number: 42, Model: "sonnet",
		Message: "  Is the greeting exported on purpose?  ",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if session.Title != "Is the greeting exported on purpose?" || session.Model != "sonnet" || session.HeadSHA != testPR.HeadSHA {
		t.Errorf("session = %+v", session)
	}
	h.waitIdle(t, session.ID)

	runs := h.runs()
	if len(runs) != 1 {
		t.Fatalf("expected one run, got %d", len(runs))
	}
	run := runs[0]
	stdin := run.Stdin
	header := strings.Index(stdin, `We're reviewing pull request acme/widgets#42: "Add a greeting" by @alice (greet → main)`)
	question := strings.Index(stdin, "Is the greeting exported on purpose?")
	body := strings.Index(stdin, "Says hello.")
	diff := strings.Index(stdin, "+func Greet() {}")
	if header != 0 || !(header < question && question < body && body < diff) {
		t.Errorf("the opening message should say what we're reviewing, then the question, the description and the diff, in that order:\n%s", stdin)
	}
	if run.flag("--session-id") == "" || run.flag("--resume") != "" || run.flag("--model") != "sonnet" {
		t.Errorf("first run args = %q", run.Args)
	}
	if !strings.Contains(run.flag("--append-system-prompt"), "acme/widgets#42") {
		t.Errorf("system prompt = %q", run.flag("--append-system-prompt"))
	}
	workDir := filepath.Join(h.dir, session.ID)
	if mustEval(t, run.Dir) != mustEval(t, workDir) {
		t.Errorf("ran in %s, want the chat's own directory %s", run.Dir, workDir)
	}
	if data, err := os.ReadFile(filepath.Join(workDir, DiffFile)); err != nil || string(data) != testPR.Diff {
		t.Errorf("%s = %q, %v", DiffFile, data, err)
	}
	if data, err := os.ReadFile(filepath.Join(workDir, DescriptionFile)); err != nil || !strings.Contains(string(data), "Says hello.") {
		t.Errorf("%s = %q, %v", DescriptionFile, data, err)
	}

	v := h.view(t, session.ID)
	if v.Running || v.Draft != "" {
		t.Errorf("a finished chat should be idle with no draft: %+v", v)
	}
	if got := roles(v.Messages); got != "user,tool,assistant" {
		t.Fatalf("messages = %s", got)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(v.Messages[0].Meta), &meta); err != nil || meta["head_sha"] != testPR.HeadSHA {
		t.Errorf("the first message should record what it attached, got %q", v.Messages[0].Meta)
	}
	if tool := v.Messages[1]; tool.Content != DiffFile || !strings.Contains(tool.Meta, `"status":"done"`) || !strings.Contains(tool.Meta, `"tool":"Read"`) {
		t.Errorf("tool message = %+v", tool)
	}
	if v.Messages[2].Content != "Looks fine." {
		t.Errorf("reply = %q", v.Messages[2].Content)
	}
	if v.Session.BackendSessionID != run.flag("--session-id") || v.Session.CostUSD != 0.02 {
		t.Errorf("the agent's session and cost should be recorded: %+v", v.Session)
	}

	h.mu.Lock()
	changes := append([]string(nil), h.changes...)
	h.mu.Unlock()
	if len(changes) < 3 || changes[0] != StatusRunning || changes[len(changes)-1] != StatusIdle {
		t.Errorf("changes = %q, want running ... idle", changes)
	}
}

func TestSendResumesTheConversation(t *testing.T) {
	h := newHarness(t, "ok")
	session, err := h.m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "First?"})
	if err != nil {
		t.Fatal(err)
	}
	if session.Backend != "claude" {
		t.Errorf("an unnamed backend should pick the first available, got %q", session.Backend)
	}
	h.waitIdle(t, session.ID)

	if err := h.m.Send(context.Background(), session.ID, "And second?"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.waitIdle(t, session.ID)

	runs := h.runs()
	if len(runs) != 2 {
		t.Fatalf("expected two runs, got %d", len(runs))
	}
	if runs[1].flag("--resume") != runs[0].flag("--session-id") || runs[1].flag("--session-id") != "" {
		t.Errorf("the second turn should resume the first's session: %q then %q", runs[0].Args, runs[1].Args)
	}
	if runs[1].Stdin != "And second?" {
		t.Errorf("a resumed turn sends only the message, got %q", runs[1].Stdin)
	}
	if got := roles(h.view(t, session.ID).Messages); got != "user,tool,assistant,user,tool,assistant" {
		t.Errorf("messages = %s", got)
	}
}

func TestALostConversationStartsOverWithItsHistory(t *testing.T) {
	h := newHarness(t, "fail-resume")
	session, err := h.m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "First question"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t, session.ID)
	first := h.view(t, session.ID).Session.BackendSessionID

	if err := h.m.Send(context.Background(), session.ID, "Second question"); err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t, session.ID)

	runs := h.runs()
	if len(runs) != 3 {
		t.Fatalf("expected the first turn, a failed resume and a fresh start; got %d runs", len(runs))
	}
	fresh := runs[2]
	if fresh.flag("--resume") != "" || fresh.flag("--session-id") == "" || fresh.flag("--session-id") == first {
		t.Errorf("the retry should start a new conversation: %q", fresh.Args)
	}
	for _, want := range []string{"This conversation started earlier", "**Reviewer:** First question", "**You:** Looks fine.", "The reviewer now says:\n\nSecond question", "+func Greet() {}"} {
		if !strings.Contains(fresh.Stdin, want) {
			t.Errorf("the fresh start should carry %q:\n%s", want, fresh.Stdin)
		}
	}
	if strings.Count(fresh.Stdin, "Second question") != 1 {
		t.Errorf("the new message belongs once, after the history:\n%s", fresh.Stdin)
	}
	v := h.view(t, session.ID)
	if got := roles(v.Messages); got != "user,tool,assistant,user,tool,assistant" {
		t.Errorf("a recovered resume shows no error, got %s", got)
	}
	if v.Session.BackendSessionID != fresh.flag("--session-id") {
		t.Errorf("the new conversation's handle should be kept, got %q", v.Session.BackendSessionID)
	}
}

func TestStopEndsARunningTurn(t *testing.T) {
	h := newHarness(t, "hang")
	session, err := h.m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "Take your time"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for h.view(t, session.ID).Draft != "Let me" {
		if time.Now().After(deadline) {
			t.Fatalf("the reply being written never showed: %+v", h.view(t, session.ID))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if v := h.view(t, session.ID); !v.Running || v.StartedAt.IsZero() {
		t.Errorf("a turn in flight should read as running: %+v", v)
	}
	if err := h.m.Send(context.Background(), session.ID, "Hurry"); !errors.Is(err, ErrBusy) {
		t.Errorf("Send while running = %v, want ErrBusy", err)
	}
	if !h.m.Stop(session.ID) {
		t.Fatal("Stop found nothing running")
	}
	h.waitIdle(t, session.ID)
	v := h.view(t, session.ID)
	if got := roles(v.Messages); got != "user,notice" || v.Messages[1].Content != "Stopped." {
		t.Errorf("messages = %s %+v", got, v.Messages)
	}
	if h.m.Stop(session.ID) {
		t.Error("Stop on an idle chat should report nothing to stop")
	}
}

func TestAFailedTurnSaysWhy(t *testing.T) {
	h := newHarness(t, "crash")
	session, err := h.m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "Hello?"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t, session.ID)
	v := h.view(t, session.ID)
	if got := roles(v.Messages); got != "user,error" || !strings.Contains(v.Messages[1].Content, "something broke") {
		t.Errorf("messages = %s %+v", got, v.Messages)
	}
	// It said its session before failing, so the next turn resumes it.
	if v.Session.BackendSessionID == "" {
		t.Error("the session the agent announced should be kept")
	}
}

func TestStartRefusals(t *testing.T) {
	h := newHarness(t, "ok")
	if _, err := h.m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "   "}); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty message: %v", err)
	}
	h.m.source = defaultSource{Fixed: Fixed{h.m.backends()[0]}, id: "codex"}
	if _, err := h.m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "hi"}); err == nil ||
		!strings.Contains(err.Error(), `"codex"`) {
		t.Errorf("a configured agent there is no backend for should be refused, got %v", err)
	}
	if err := h.m.Send(context.Background(), "nope", "hi"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Send to a missing chat: %v", err)
	}
	none := NewManager(Fixed{})
	if _, err := none.Start(context.Background(), StartRequest{Message: "hi"}); !errors.Is(err, ErrNoStore) {
		t.Errorf("no store: %v", err)
	}
}

// defaultSource is Fixed with a default of its own.
type defaultSource struct {
	Fixed
	id string
}

func (s defaultSource) Default() string { return s.id }

func TestDeleteRemovesTheChatAndItsDirectory(t *testing.T) {
	h := newHarness(t, "ok")
	session, err := h.m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t, session.ID)
	if err := h.m.Delete(session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.Get(session.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dir, session.ID)); !os.IsNotExist(err) {
		t.Errorf("the working directory should be gone: %v", err)
	}
	if list, _ := h.m.List("acme", "widgets", 42); len(list) != 0 {
		t.Errorf("List after Delete = %+v", list)
	}
}

func TestForkCarriesTheConversationIntoANewOne(t *testing.T) {
	h := newHarness(t, "ok")
	session, err := h.m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Model: "sonnet", Message: "First question"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t, session.ID)

	fork, err := h.m.Fork(session.ID, "")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	h.waitIdle(t, fork.ID) // announced, idle: nothing runs yet
	if fork.ID == session.ID || fork.ForkedFrom != session.ID || fork.Title != "Fork: First question" ||
		fork.Backend != "claude" || fork.Model != "sonnet" || fork.HeadSHA != "" || fork.BackendSessionID != "" {
		t.Errorf("fork = %+v", fork)
	}
	if len(h.runs()) != 1 {
		t.Errorf("forking ran the agent: %d runs", len(h.runs()))
	}
	v := h.view(t, fork.ID)
	if got := roles(v.Messages); got != "user,tool,assistant,notice" ||
		!strings.Contains(v.Messages[3].Content, "Forked from “First question”") {
		t.Errorf("the fork's messages = %s %+v", got, v.Messages)
	}

	if err := h.m.Send(context.Background(), fork.ID, "Second question"); err != nil {
		t.Fatalf("Send to the fork: %v", err)
	}
	h.waitIdle(t, fork.ID)
	runs := h.runs()
	if len(runs) != 2 {
		t.Fatalf("expected two runs, got %d", len(runs))
	}
	first, second := runs[0], runs[1]
	if second.flag("--resume") != "" || second.flag("--session-id") == "" || second.flag("--session-id") == first.flag("--session-id") {
		t.Errorf("the fork's first turn should open a conversation of its own: %q", second.Args)
	}
	for _, want := range []string{"This conversation started earlier", "**Reviewer:** First question", "**You:** Looks fine.",
		"The reviewer now says:\n\nSecond question", "+func Greet() {}"} {
		if !strings.Contains(second.Stdin, want) {
			t.Errorf("the fork's opening message should carry %q:\n%s", want, second.Stdin)
		}
	}
	if strings.Contains(second.Stdin, "Forked from") {
		t.Errorf("the fork's notice is for the reviewer, not the agent:\n%s", second.Stdin)
	}
	if mustEval(t, second.Dir) != mustEval(t, filepath.Join(h.dir, fork.ID)) {
		t.Errorf("the fork ran in %s, not its own directory", second.Dir)
	}

	v = h.view(t, fork.ID)
	if got := roles(v.Messages); got != "user,tool,assistant,notice,user,tool,assistant" {
		t.Errorf("the fork's messages after a turn = %s", got)
	}
	if v.Session.HeadSHA != testPR.HeadSHA || v.Session.BackendSessionID != second.flag("--session-id") || v.Session.Turns != 2 {
		t.Errorf("the fork after its first turn = %+v", v.Session)
	}
	if !strings.Contains(v.Messages[4].Meta, `"attached"`) {
		t.Errorf("the fork's first message attaches the PR: %+v", v.Messages[4])
	}
	if got := roles(h.view(t, session.ID).Messages); got != "user,tool,assistant" {
		t.Errorf("the original changed: %s", got)
	}

	// A model asked for wins over the original's.
	other, err := h.m.Fork(session.ID, "opus")
	if err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t, other.ID)
	if other.Model != "opus" {
		t.Errorf("a fork asking for opus got %q", other.Model)
	}
	if list, _ := h.m.List("acme", "widgets", 42); len(list) != 3 {
		t.Errorf("the PR should have the original and two forks, got %d", len(list))
	}
}

func TestForkRefusals(t *testing.T) {
	h := newHarness(t, "hang")
	session, err := h.m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "Take your time"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.Fork(session.ID, ""); !errors.Is(err, ErrBusy) {
		t.Errorf("forking a running chat = %v, want ErrBusy", err)
	}
	if _, err := h.m.Fork("nope", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("forking a missing chat = %v, want ErrNotFound", err)
	}
	h.m.Stop(session.ID)
	h.waitIdle(t, session.ID)
}

func TestRenameTitlesAChat(t *testing.T) {
	h := newHarness(t, "ok")
	session, err := h.m.Start(context.Background(), StartRequest{Owner: "acme", Repo: "widgets", Number: 42, Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t, session.ID)

	renamed, err := h.m.Rename(session.ID, "  The greeting,\n\tagain  ")
	if err != nil {
		t.Fatal(err)
	}
	h.waitIdle(t, session.ID)
	if renamed.Title != "The greeting, again" || h.view(t, session.ID).Session.Title != "The greeting, again" {
		t.Errorf("renamed = %+v", renamed)
	}
	if _, err := h.m.Rename(session.ID, " \n "); !errors.Is(err, ErrEmptyTitle) {
		t.Errorf("an empty title = %v, want ErrEmptyTitle", err)
	}
	if _, err := h.m.Rename("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("renaming a missing chat = %v, want ErrNotFound", err)
	}
}
