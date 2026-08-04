package llm

import (
	"crs/config"
	"crs/utils"
	"strings"
	"testing"
)

// blockingClient holds each Generate call until release is closed, so a test
// can observe a call while it is still in flight, and counts every call it
// received.
type blockingClient struct {
	started chan struct{}
	release chan struct{}
	calls   chan struct{}
}

func newBlockingClient(maxCalls int) *blockingClient {
	return &blockingClient{
		started: make(chan struct{}, maxCalls),
		release: make(chan struct{}),
		calls:   make(chan struct{}, maxCalls),
	}
}

func (c *blockingClient) Provider() string { return "fake" }
func (c *blockingClient) Model() string    { return "fake-model" }
func (c *blockingClient) Generate(string) (string, error) {
	c.calls <- struct{}{}
	c.started <- struct{}{}
	<-c.release
	return "helper.go\nmain.go\n", nil
}

// failingClient fails the test if the LLM is contacted at all.
type failingClient struct{ t *testing.T }

func (c *failingClient) Provider() string { return "fake" }
func (c *failingClient) Model() string    { return "fake-model" }
func (c *failingClient) Generate(string) (string, error) {
	c.t.Error("LLM was called on a path that must not contact it")
	return "", nil
}

func TestCachedOrderedDiffFilesNeverCallsLLM(t *testing.T) {
	// A render asking for the ordering must be answered from cache or not at
	// all: it is on the critical path of opening a review.
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, ExperimentalLLMFileOrdering: true})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	useFakeClient(t, &failingClient{t: t}, nil)

	files := []*utils.DiffFile{{NewName: "main.go"}, {NewName: "helper.go"}}

	if _, ok := CachedOrderedDiffFiles(files, "code-review-server", 3, "sha-3"); ok {
		t.Fatal("expected a cache miss to report ok=false")
	}

	if err := db.UpsertDiffFileOrdering(3, "code-review-server", "sha-3", `["helper.go","main.go"]`); err != nil {
		t.Fatalf("failed to seed cache: %v", err)
	}

	ordered, ok := CachedOrderedDiffFiles(files, "code-review-server", 3, "sha-3")
	if !ok {
		t.Fatal("expected a cache hit to report ok=true")
	}
	if ordered[0].NewName != "helper.go" || ordered[1].NewName != "main.go" {
		t.Errorf("cached ordering not applied: got %q, %q", ordered[0].NewName, ordered[1].NewName)
	}
}

func TestEnsureDiffAnalysisCollapsesConcurrentCallsForSameRevision(t *testing.T) {
	// The render dispatch and the post-update hook both fire for a freshly
	// opened PR; only one of them should reach the LLM.
	t.Setenv("CRS_HOME", t.TempDir())
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, ExperimentalLLMFileOrdering: true})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	client := newBlockingClient(4)
	useFakeClient(t, client, nil)

	files := []*utils.DiffFile{{NewName: "main.go"}, {NewName: "helper.go"}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		EnsureDiffAnalysis(files, "code-review-server", 11, "sha-11", TriggerRender)
	}()

	// Wait until the first analysis is inside the LLM call, then fire the
	// second one for the same revision: it must return without calling out.
	<-client.started
	EnsureDiffAnalysis(files, "code-review-server", 11, "sha-11", TriggerPostUpdateHook)

	close(client.release)
	<-done

	if got := len(client.calls); got != 1 {
		t.Errorf("expected exactly 1 LLM call for the revision, got %d", got)
	}

	// The completed analysis is cached, so nothing after it needs the LLM.
	if ordering, err := db.GetDiffFileOrdering(11, "code-review-server", "sha-11"); err != nil || ordering == "" {
		t.Errorf("expected the completed analysis to be cached, got %q (err %v)", ordering, err)
	}
}

func TestCleanFileName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"server/server.go", "server/server.go"},
		{"  server/server.go  ", "server/server.go"},
		{"- server/server.go", "server/server.go"},
		{"* server/server.go", "server/server.go"},
		{"`server/server.go`", "server/server.go"},
		{"\"server/server.go\"", "server/server.go"},
		{"a/server/server.go", "server/server.go"},
		{"b/server/server.go", "server/server.go"},
		{"", ""},
		{"```", ""},
	}
	for i, tc := range tests {
		if got := cleanFileName(tc.in); got != tc.want {
			t.Errorf("[%d] cleanFileName(%q) = %q, want %q", i, tc.in, got, tc.want)
		}
	}
}

func TestReorderFilesByNames(t *testing.T) {
	files := []*utils.DiffFile{
		{NewName: "server/server_test.go"},
		{NewName: "server/server.go"},
		{NewName: "config/config.go"},
		{NewName: "styles/app.css"},
	}

	// LLM returns the desired reading order; basename-only and a/ prefix
	// variants must still match.
	names := []string{"server/server.go", "config.go", "a/styles/app.css", "server/server_test.go"}
	got := reorderFilesByNames(files, names)

	wantOrder := []string{
		"server/server.go",
		"config/config.go",
		"styles/app.css",
		"server/server_test.go",
	}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d files, want %d", len(got), len(wantOrder))
	}
	for i, w := range wantOrder {
		if got[i].NewName != w {
			t.Errorf("[%d] got %q, want %q", i, got[i].NewName, w)
		}
	}
}

func TestReorderFilesByNamesUnmentionedAppended(t *testing.T) {
	files := []*utils.DiffFile{
		{NewName: "a.go"},
		{NewName: "b.go"},
		{NewName: "c.go"},
	}
	// LLM only mentions one file; the rest keep original relative order.
	got := reorderFilesByNames(files, []string{"c.go"})

	wantOrder := []string{"c.go", "a.go", "b.go"}
	for i, w := range wantOrder {
		if got[i].NewName != w {
			t.Errorf("[%d] got %q, want %q", i, got[i].NewName, w)
		}
	}
}

func TestParseDiffAnalysisText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantEase string
		wantOrd  []string
	}{
		{
			name:     "ordering only",
			text:     "main.go\nhelper.go\nmain_test.go\n",
			wantEase: "",
			wantOrd:  []string{"main.go", "helper.go", "main_test.go"},
		},
		{
			name:     "ease line first",
			text:     "REVIEW_EASE: medium\nmain.go\nmain_test.go\n",
			wantEase: "medium",
			wantOrd:  []string{"main.go", "main_test.go"},
		},
		{
			name:     "ease line with decoration",
			text:     "  **REVIEW_EASE: Easy**  \nmain.go\n",
			wantEase: "easy",
			wantOrd:  []string{"main.go"},
		},
		{
			name:     "ease prefix is case-insensitive",
			text:     "Review_Ease: hard\nmain.go\n",
			wantEase: "hard",
			wantOrd:  []string{"main.go"},
		},
		{
			name:     "rating followed by commentary",
			text:     "REVIEW_EASE: medium — several interacting files\nmain.go\n",
			wantEase: "medium",
			wantOrd:  []string{"main.go"},
		},
		{
			name:     "invalid rating dropped",
			text:     "REVIEW_EASE: trivial\nmain.go\n",
			wantEase: "",
			wantOrd:  []string{"main.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDiffAnalysisText(tt.text)
			if got.ReviewEase != tt.wantEase {
				t.Errorf("ReviewEase = %q, want %q", got.ReviewEase, tt.wantEase)
			}
			if len(got.Ordering) != len(tt.wantOrd) {
				t.Fatalf("Ordering = %v, want %v", got.Ordering, tt.wantOrd)
			}
			for i, w := range tt.wantOrd {
				if got.Ordering[i] != w {
					t.Errorf("Ordering[%d] = %q, want %q", i, got.Ordering[i], w)
				}
			}
		})
	}
}

func TestNormalizeReviewEase(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"easy", "easy"},
		{" Medium ", "medium"},
		{"\"hard\"", "hard"},
		{"**easy**", "easy"},
		{"hard - tricky concurrency", "hard"},
		{"medium (many files touched)", "medium"},
		{"easy.", "easy"},
		{"hardly", ""},
		{"trivial", ""},
		{"", ""},
	}
	for i, tc := range tests {
		if got := normalizeReviewEase(tc.in); got != tc.want {
			t.Errorf("[%d] normalizeReviewEase(%q) = %q, want %q", i, tc.in, got, tc.want)
		}
	}
}

func TestBuildAnalysisPromptEaseInstruction(t *testing.T) {
	orderingOnly := buildAnalysisPrompt("- main.go\n", "diff text", true, false)
	if strings.Contains(orderingOnly, "REVIEW_EASE") {
		t.Error("ordering-only prompt must not mention REVIEW_EASE")
	}
	if !strings.Contains(orderingOnly, "ordering the files") {
		t.Error("ordering-only prompt must include the ordering instructions")
	}

	both := buildAnalysisPrompt("- main.go\n", "diff text", true, true)
	if !strings.Contains(both, "REVIEW_EASE: <rating>") {
		t.Error("combined prompt must instruct the REVIEW_EASE response line")
	}
	if !strings.Contains(both, "ordering the files") {
		t.Error("combined prompt must include the ordering instructions")
	}

	easeOnly := buildAnalysisPrompt("- main.go\n", "diff text", false, true)
	if !strings.Contains(easeOnly, "REVIEW_EASE: <rating>") {
		t.Error("ease-only prompt must instruct the REVIEW_EASE response line")
	}
	if strings.Contains(easeOnly, "ordering the files") || strings.Contains(easeOnly, "file paths") {
		t.Error("ease-only prompt must not ask for a file ordering")
	}
}
