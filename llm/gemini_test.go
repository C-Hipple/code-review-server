package llm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testGeminiClient(srv *httptest.Server) *GeminiClient {
	return &GeminiClient{
		apiKey:  "test-key",
		model:   "test-model",
		baseURL: srv.URL,
		http:    srv.Client(),
	}
}

// wantCallError asserts err is a *CallError attributed to the given stage.
func wantCallError(t *testing.T, err error, stage string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var callErr *CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected a *CallError, got %T: %v", err, err)
	}
	if callErr.Stage != stage {
		t.Errorf("stage = %q, want %q", callErr.Stage, stage)
	}
}

func TestNewGeminiClientMissingKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	_, err := NewGeminiClient()
	wantCallError(t, err, StageClientInit)
}

func TestGeminiGenerate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/models/test-model:generateContent" {
			t.Errorf("unexpected path %q", got)
		}
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"REVIEW_EASE: easy\nmain.go\n"}]}}]}`))
	}))
	defer srv.Close()

	got, err := testGeminiClient(srv).Generate("prompt")
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if want := "REVIEW_EASE: easy\nmain.go\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestGeminiGenerateHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error": "quota exceeded"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := testGeminiClient(srv).Generate("prompt")
	wantCallError(t, err, StageHTTPStatus)
}

func TestGeminiGenerateDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	_, err := testGeminiClient(srv).Generate("prompt")
	wantCallError(t, err, StageDecode)
}

func TestGeminiGenerateEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"candidates":[]}`))
	}))
	defer srv.Close()

	_, err := testGeminiClient(srv).Generate("prompt")
	wantCallError(t, err, StageEmptyResponse)
}
