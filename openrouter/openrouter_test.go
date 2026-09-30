package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(srv *httptest.Server) *Client {
	return &Client{APIKey: "test-key", Model: "vendor/model", BaseURL: srv.URL, HTTP: srv.Client()}
}

// serve answers every request with status and body.
func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNewNeedsAModelAndAKey(t *testing.T) {
	t.Setenv(APIKeyEnv, "test-key")
	if _, err := New("  "); !errors.Is(err, ErrNoModel) {
		t.Errorf("New without a model: %v, want ErrNoModel", err)
	}
	c, err := New(" anthropic/claude-sonnet-4.5 ")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Model != "anthropic/claude-sonnet-4.5" || c.APIKey != "test-key" || c.BaseURL != DefaultBaseURL {
		t.Errorf("unexpected client: %+v", c)
	}

	t.Setenv(APIKeyEnv, "")
	if _, err := New("anthropic/claude-sonnet-4.5"); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("New without a key: %v, want ErrNoAPIKey", err)
	}
}

func TestCompleteSendsAChatCompletion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		if r.Header.Get("X-Title") != appTitle || r.Header.Get("HTTP-Referer") != appURL {
			t.Errorf("missing attribution headers: %v", r.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the request: %v", err)
		}
		if body["model"] != "vendor/model" {
			t.Errorf("model = %v", body["model"])
		}
		messages, _ := body["messages"].([]any)
		if len(messages) != 1 {
			t.Fatalf("messages = %v", body["messages"])
		}
		if m := messages[0].(map[string]any); m["role"] != "user" || m["content"] != "the prompt" {
			t.Errorf("message = %v", m)
		}
		if _, ok := body["response_format"]; ok {
			t.Error("a call without a schema should not ask for structured output")
		}
		w.Write([]byte(`{"model": "vendor/model", "choices": [{"message": {"role": "assistant", "content": "the answer"}, "finish_reason": "stop"}]}`))
	}))
	defer srv.Close()

	got, err := testClient(srv).Complete(context.Background(), "the prompt", nil)
	if err != nil || got != "the answer" {
		t.Errorf("Complete = %q, %v", got, err)
	}
}

func TestCompleteAsksForStructuredOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Name   string         `json:"name"`
					Strict bool           `json:"strict"`
					Schema map[string]any `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the request: %v", err)
		}
		f := body.ResponseFormat
		if f.Type != "json_schema" || f.JSONSchema.Name != "report" || !f.JSONSchema.Strict || f.JSONSchema.Schema["type"] != "object" {
			t.Errorf("unexpected response_format: %+v", f)
		}
		w.Write([]byte(`{"choices": [{"message": {"content": "{\"ok\": true}"}}]}`))
	}))
	defer srv.Close()

	schema := &JSONSchema{Name: "report", Schema: map[string]any{"type": "object"}}
	if got, err := testClient(srv).Complete(context.Background(), "p", schema); err != nil || got != `{"ok": true}` {
		t.Errorf("Complete = %q, %v", got, err)
	}
}

func TestCompleteReportsAPIErrors(t *testing.T) {
	var apiErr *APIError

	srv := serve(t, http.StatusPaymentRequired, `{"error": {"code": 402, "message": "Insufficient credits"}}`)
	_, err := testClient(srv).Complete(context.Background(), "p", nil)
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 402 || apiErr.Message != "Insufficient credits" {
		t.Errorf("402: got %v", err)
	}

	srv = serve(t, http.StatusBadGateway, `upstream exploded`)
	_, err = testClient(srv).Complete(context.Background(), "p", nil)
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 502 || apiErr.Message != "upstream exploded" {
		t.Errorf("502 with a plain body: got %v", err)
	}

	// An error can also arrive in the body of a 200, for the request or for
	// the choice.
	srv = serve(t, http.StatusOK, `{"error": {"code": "provider_error", "message": "model overloaded"}}`)
	_, err = testClient(srv).Complete(context.Background(), "p", nil)
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.Message, "model overloaded") {
		t.Errorf("error in a 200: got %v", err)
	}

	srv = serve(t, http.StatusOK, `{"choices": [{"message": {"content": ""}, "finish_reason": "error", "error": {"code": 502, "message": "upstream timeout"}}]}`)
	_, err = testClient(srv).Complete(context.Background(), "p", nil)
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.Message, "upstream timeout") {
		t.Errorf("error in a choice: got %v", err)
	}
}

func TestCompleteRejectsUnusableResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{"not json", `not json`, ErrDecode},
		{"no choices", `{"choices": []}`, ErrEmptyResponse},
		{"null content", `{"choices": [{"message": {"content": null}, "finish_reason": "length"}]}`, ErrEmptyResponse},
		{"blank content", `{"choices": [{"message": {"content": "  \n"}}]}`, ErrEmptyResponse},
		{"refusal", `{"choices": [{"message": {"content": null, "refusal": "I can't help with that"}}]}`, ErrEmptyResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := serve(t, http.StatusOK, tt.body)
			if _, err := testClient(srv).Complete(context.Background(), "p", nil); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestCompleteStopsAtTheDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := testClient(srv).Complete(ctx, "p", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want a deadline error", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("Complete outlived its deadline")
	}
}

func TestChatSendsToolsAndReturnsToolCalls(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the request: %v", err)
		}
		requests = append(requests, body)
		if len(requests) == 1 {
			w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": null, "reasoning": "think", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "read", "arguments": "{\"path\": \"a.md\"}"}}
			]}, "finish_reason": "tool_calls"}]}`))
			return
		}
		w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": "done"}, "finish_reason": "stop"}]}`))
	}))
	defer srv.Close()
	c := testClient(srv)

	tools := []Tool{{Name: "read", Description: "Read a file.", Parameters: map[string]any{"type": "object"}}}
	messages := []Message{SystemMessage("be brief"), UserMessage("go")}
	reply, err := c.Chat(context.Background(), messages, tools, "read")
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if reply.Text != "" || len(reply.ToolCalls) != 1 || reply.ToolCalls[0].ID != "call_1" ||
		reply.ToolCalls[0].Function.Name != "read" || reply.ToolCalls[0].Function.Arguments != `{"path": "a.md"}` {
		t.Fatalf("unexpected reply: %+v", reply)
	}
	first := requests[0]
	if tl, _ := first["tools"].([]any); len(tl) != 1 || tl[0].(map[string]any)["type"] != "function" ||
		tl[0].(map[string]any)["function"].(map[string]any)["name"] != "read" {
		t.Errorf("tools = %v", first["tools"])
	}
	if choice, _ := first["tool_choice"].(map[string]any); choice["function"].(map[string]any)["name"] != "read" {
		t.Errorf("tool_choice = %v", first["tool_choice"])
	}

	messages = append(messages, reply.Message, ToolMessage("call_1", "the file"))
	reply, err = c.Chat(context.Background(), messages, tools, "")
	if err != nil || reply.Text != "done" || len(reply.ToolCalls) != 0 {
		t.Fatalf("second Chat = %+v, %v", reply, err)
	}
	second := requests[1]
	if _, ok := second["tool_choice"]; ok {
		t.Error("an empty require should leave tool_choice to the model")
	}
	sent, _ := second["messages"].([]any)
	if len(sent) != 4 {
		t.Fatalf("messages = %v", second["messages"])
	}
	// The assistant turn goes back exactly as it came, reasoning included.
	if m := sent[2].(map[string]any); m["reasoning"] != "think" || m["tool_calls"] == nil {
		t.Errorf("assistant message = %v", m)
	}
	if m := sent[3].(map[string]any); m["role"] != "tool" || m["tool_call_id"] != "call_1" || m["content"] != "the file" {
		t.Errorf("tool message = %v", m)
	}
	if m := sent[0].(map[string]any); m["role"] != "system" || m["content"] != "be brief" {
		t.Errorf("system message = %v", m)
	}
}

func TestChatRejectsAnEmptyTurn(t *testing.T) {
	srv := serve(t, http.StatusOK, `{"choices": [{"message": {"role": "assistant", "content": ""}, "finish_reason": "length"}]}`)
	if _, err := testClient(srv).Chat(context.Background(), []Message{UserMessage("go")}, nil, ""); !errors.Is(err, ErrEmptyResponse) {
		t.Errorf("Chat = %v, want ErrEmptyResponse", err)
	}
}
