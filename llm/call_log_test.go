package llm

import (
	"crs/config"
	"crs/database"
	"crs/utils"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestDB(t *testing.T) *database.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := database.NewDB(dbPath)
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}
	return db
}

// readCallLog returns the contents of llm_calls.log under the given CRS home.
func readCallLog(t *testing.T, crsHome string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(crsHome, callLogName))
	if err != nil {
		t.Fatalf("failed to read call log: %v", err)
	}
	return string(data)
}

// fakeClient is a Client stub whose Generate returns a fixed response.
type fakeClient struct {
	text string
	err  error
}

func (f *fakeClient) Provider() string { return "fake" }
func (f *fakeClient) Model() string    { return "fake-model" }
func (f *fakeClient) Generate(prompt string) (string, error) {
	return f.text, f.err
}

// useFakeClient makes analyzeDiff use the given stub for the duration of the
// test.
func useFakeClient(t *testing.T, c Client, err error) {
	t.Helper()
	orig := newClient
	newClient = func() (Client, error) { return c, err }
	t.Cleanup(func() { newClient = orig })
}

func TestCallLogMissingAPIKey(t *testing.T) {
	crsHome := t.TempDir()
	t.Setenv("CRS_HOME", crsHome)
	t.Setenv("GEMINI_API_KEY", "")

	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, ExperimentalLLMReviewEase: true})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	files := []*utils.DiffFile{{NewName: "main.go"}, {NewName: "helper.go"}}
	EnsureDiffAnalysis(files, "code-review-server", 42, "sha-42", TriggerPostUpdateHook)

	log := readCallLog(t, crsHome)
	for _, want := range []string{
		"=== LLM Call Report ===",
		"PR:       #42  (repo: code-review-server, sha: sha-42)",
		"Trigger:  post-update hook",
		"Purpose:  review-ease",
		fmt.Sprintf("FAILURE at stage %q", StageClientInit),
		"GEMINI_API_KEY not set",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("call log missing %q; log:\n%s", want, log)
		}
	}
}

func TestCallLogPartialParseKeepsOrderingAndLogsProblem(t *testing.T) {
	crsHome := t.TempDir()
	t.Setenv("CRS_HOME", crsHome)

	db := setupTestDB(t)
	config.SetC(config.Config{
		DB:                          db,
		ExperimentalLLMFileOrdering: true,
		ExperimentalLLMReviewEase:   true,
	})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	// The model answers with a usable ordering but no REVIEW_EASE line —
	// the exact failure mode that leaves PRs without a difficulty tag.
	useFakeClient(t, &fakeClient{text: "helper.go\nmain.go\n"}, nil)

	files := []*utils.DiffFile{{NewName: "main.go"}, {NewName: "helper.go"}}
	EnsureDiffAnalysis(files, "code-review-server", 7, "sha-7", TriggerPostUpdateHook)

	// The ordering must still have been cached; the rating must not have been.
	if ordering, err := db.GetDiffFileOrdering(7, "code-review-server", "sha-7"); err != nil || ordering == "" {
		t.Errorf("expected cached ordering, got %q (err %v)", ordering, err)
	}
	if ease, _ := db.GetReviewEase(7, "code-review-server", "sha-7"); ease != "" {
		t.Errorf("expected no cached review ease, got %q", ease)
	}

	log := readCallLog(t, crsHome)
	for _, want := range []string{
		"Outcome:  PARTIAL",
		"Problem:  response contained no usable review-ease rating",
		"LLM:      fake (fake-model)",
		"Purpose:  file-ordering + review-ease",
		"Response (for parse debugging):",
		"  | helper.go",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("call log missing %q; log:\n%s", want, log)
		}
	}
}

func TestCallLogSuccess(t *testing.T) {
	crsHome := t.TempDir()
	t.Setenv("CRS_HOME", crsHome)

	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, ExperimentalLLMReviewEase: true})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	useFakeClient(t, &fakeClient{text: "REVIEW_EASE: hard\n"}, nil)

	files := []*utils.DiffFile{{NewName: "main.go"}}
	EnsureDiffAnalysis(files, "code-review-server", 9, "sha-9", TriggerPostUpdateHook)

	if ease, _ := db.GetReviewEase(9, "code-review-server", "sha-9"); ease != "hard" {
		t.Errorf("expected cached review ease %q, got %q", "hard", ease)
	}

	log := readCallLog(t, crsHome)
	for _, want := range []string{
		"Outcome:  SUCCESS",
		"review-ease hard",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("call log missing %q; log:\n%s", want, log)
		}
	}
}
