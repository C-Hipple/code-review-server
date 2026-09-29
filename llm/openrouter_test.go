package llm

import (
	"context"
	"crs/openrouter"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testOpenRouterClient(t *testing.T, status int, body string) *OpenRouterClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &OpenRouterClient{client: &openrouter.Client{
		APIKey: "test-key", Model: "vendor/model", BaseURL: srv.URL, HTTP: srv.Client(),
	}}
}

func TestNewOpenRouterClient(t *testing.T) {
	t.Setenv(openrouter.APIKeyEnv, "")
	_, err := NewOpenRouterClient("vendor/model")
	wantCallError(t, err, StageClientInit)

	t.Setenv(openrouter.APIKeyEnv, "test-key")
	_, err = NewOpenRouterClient("")
	wantCallError(t, err, StageClientInit)

	c, err := NewOpenRouterClient("vendor/model")
	if err != nil {
		t.Fatalf("NewOpenRouterClient: %v", err)
	}
	if c.Provider() != "openrouter" || c.Model() != "vendor/model" {
		t.Errorf("client = %s (%s)", c.Provider(), c.Model())
	}
}

func TestOpenRouterGenerate(t *testing.T) {
	c := testOpenRouterClient(t, http.StatusOK, `{"choices": [{"message": {"content": "REVIEW_EASE: easy\nmain.go\n"}}]}`)
	got, err := c.Generate("prompt")
	if err != nil || got != "REVIEW_EASE: easy\nmain.go\n" {
		t.Errorf("Generate = %q, %v", got, err)
	}
}

func TestOpenRouterGenerateStages(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		stage  string
	}{
		{"http status", http.StatusTooManyRequests, `{"error": {"code": 429, "message": "rate limited"}}`, StageHTTPStatus},
		{"error in a 200", http.StatusOK, `{"error": {"code": 502, "message": "upstream down"}}`, StageHTTPStatus},
		{"decode", http.StatusOK, `not json`, StageDecode},
		{"empty response", http.StatusOK, `{"choices": []}`, StageEmptyResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := testOpenRouterClient(t, tt.status, tt.body).Generate("prompt")
			wantCallError(t, err, tt.stage)
		})
	}

	unreachable := &OpenRouterClient{client: &openrouter.Client{
		APIKey: "k", Model: "m", BaseURL: "http://127.0.0.1:1", HTTP: &http.Client{},
	}}
	_, err := unreachable.Generate("prompt")
	wantCallError(t, err, StageRequest)
}

func TestOpenRouterGenerateContextTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c := &OpenRouterClient{client: &openrouter.Client{APIKey: "k", Model: "m", BaseURL: srv.URL, HTTP: srv.Client()}}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.GenerateContext(ctx, "prompt")
	wantCallError(t, err, StageTimeout)
}
