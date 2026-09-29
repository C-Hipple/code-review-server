package pluginkit

import (
	"crs/config"
	"crs/openrouter"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBackendNamesMatchTheServer ties this package's names to the server's:
// pluginkit can't import config (it would link the server's database into
// every plugin), so the contract is written down twice.
func TestBackendNamesMatchTheServer(t *testing.T) {
	if ProviderEnv != config.PluginProviderEnv || ModelEnv != config.PluginModelEnv {
		t.Errorf("environment variables differ: %s/%s here, %s/%s in config",
			ProviderEnv, ModelEnv, config.PluginProviderEnv, config.PluginModelEnv)
	}
	if ProviderGemini != config.PluginProviderGemini || ProviderOpenRouter != config.PluginProviderOpenRouter {
		t.Error("provider names differ from config's")
	}
}

func TestModelFromEnv(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		wantModel  string
		wantErrHas string
	}{
		{"gemini by default", map[string]string{geminiKeyEnv: "k"}, "Gemini", ""},
		{"gemini by name", map[string]string{ProviderEnv: "gemini", geminiKeyEnv: "k"}, "Gemini", ""},
		{"gemini without a key", map[string]string{}, "", "GEMINI_API_KEY environment variable not set"},
		{"openrouter", map[string]string{ProviderEnv: "openrouter", ModelEnv: "openai/gpt-5", openrouter.APIKeyEnv: "k"}, "OpenRouter (openai/gpt-5)", ""},
		{"openrouter needs no gemini key", map[string]string{ProviderEnv: "openrouter", ModelEnv: "openai/gpt-5", openrouter.APIKeyEnv: "k", geminiKeyEnv: ""}, "OpenRouter (openai/gpt-5)", ""},
		{"openrouter without a model", map[string]string{ProviderEnv: "openrouter", openrouter.APIKeyEnv: "k"}, "", "CRS_LLM_MODEL not set"},
		{"openrouter without a key", map[string]string{ProviderEnv: "openrouter", ModelEnv: "openai/gpt-5"}, "", "OPENROUTER_API_KEY environment variable not set"},
		{"unknown provider", map[string]string{ProviderEnv: "command", geminiKeyEnv: "k"}, "", `unknown CRS_LLM_PROVIDER "command"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{ProviderEnv, ModelEnv, geminiKeyEnv, openrouter.APIKeyEnv} {
				t.Setenv(key, tt.env[key])
			}
			m, err := ModelFromEnv()
			if tt.wantErrHas != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrHas) {
					t.Errorf("err = %v, want one mentioning %q", err, tt.wantErrHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("ModelFromEnv: %v", err)
			}
			if m.String() != tt.wantModel {
				t.Errorf("model = %q, want %q", m.String(), tt.wantModel)
			}
		})
	}
}

func TestJSONSchemaKeepsTheOrderAndClosesObjects(t *testing.T) {
	schema := &Schema{
		Type: "OBJECT",
		Properties: map[string]*Schema{
			"summary":     {Type: "STRING", Description: "What the PR does."},
			"annotations": AnnotationsSchema(4),
		},
		PropertyOrdering: []string{"summary", "annotations"},
		Required:         []string{"summary", "annotations"},
	}
	encoded, err := json.Marshal(schema.JSONSchema())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(encoded)

	if strings.Contains(got, `"OBJECT"`) || strings.Contains(got, `"STRING"`) || strings.Contains(got, "propertyOrdering") {
		t.Errorf("schema kept Gemini's dialect: %s", got)
	}
	if strings.Index(got, `"summary"`) > strings.Index(got, `"annotations"`) {
		t.Errorf("summary should come before annotations, as PropertyOrdering says: %s", got)
	}
	if strings.Index(got, `"filename"`) > strings.Index(got, `"content"`) {
		t.Errorf("annotation fields lost their order: %s", got)
	}
	if strings.Count(got, `"additionalProperties":false`) != 2 {
		t.Errorf("both objects should be closed: %s", got)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("the rendered schema is not valid JSON: %v\n%s", err, got)
	}
	annotations := decoded["properties"].(map[string]any)["annotations"].(map[string]any)
	item := annotations["items"].(map[string]any)
	severity := item["properties"].(map[string]any)["severity"].(map[string]any)
	if annotations["type"] != "array" || item["type"] != "object" || severity["type"] != "string" || len(severity["enum"].([]any)) != 3 {
		t.Errorf("unexpected nested schema: %s", got)
	}
}

// openRouterRequest is the part of a chat completion request the tests read.
type openRouterRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Content string `json:"content"`
	} `json:"messages"`
	ResponseFormat *struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Schema map[string]any `json:"schema"`
		} `json:"json_schema"`
	} `json:"response_format"`
}

func TestModelGenerateThroughOpenRouter(t *testing.T) {
	var request openRouterRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request = openRouterRequest{}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decoding the request: %v", err)
		}
		w.Write([]byte(`{"choices": [{"message": {"content": "{\"summary\": \"ok\"}"}}]}`))
	}))
	defer srv.Close()
	m := &Model{provider: ProviderOpenRouter, openrouter: &openrouter.Client{
		APIKey: "k", Model: "openai/gpt-5", BaseURL: srv.URL, HTTP: srv.Client(),
	}}

	schema := &Schema{Type: "OBJECT", Properties: map[string]*Schema{"summary": {Type: "STRING"}}, Required: []string{"summary"}}
	reply, err := m.Generate("Summarize this.", schema)
	if err != nil || reply != `{"summary": "ok"}` {
		t.Fatalf("Generate = %q, %v", reply, err)
	}
	if request.Model != "openai/gpt-5" || len(request.Messages) != 1 {
		t.Fatalf("unexpected request: %+v", request)
	}
	prompt := request.Messages[0].Content
	if !strings.HasPrefix(prompt, "Summarize this.") || !strings.Contains(prompt, "Reply with only a JSON document") || !strings.Contains(prompt, `"summary"`) {
		t.Errorf("the prompt should carry the schema for models without structured outputs:\n%s", prompt)
	}
	if request.ResponseFormat == nil || request.ResponseFormat.Type != "json_schema" || request.ResponseFormat.JSONSchema.Schema["type"] != "object" {
		t.Errorf("unexpected response_format: %+v", request.ResponseFormat)
	}

	// Without a schema the prompt goes out as written.
	if _, err := m.Generate("Just talk.", nil); err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Content != "Just talk." || request.ResponseFormat != nil {
		t.Errorf("a call without a schema changed the request: %+v", request)
	}
}

func TestModelGenerateThroughGemini(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "test-key" {
			t.Errorf("request carried the wrong key: %s", r.URL)
		}
		var body geminiRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the request: %v", err)
		}
		if body.Contents[0].Parts[0].Text != "Summarize this." || body.GenerationConfig == nil || body.GenerationConfig.ResponseSchema.Type != "OBJECT" {
			t.Errorf("unexpected request: %+v", body)
		}
		w.Write([]byte(`{"candidates": [{"content": {"parts": [{"text": "done"}]}}]}`))
	}))
	defer srv.Close()
	m := &Model{provider: ProviderGemini, geminiEndpoint: srv.URL + "/generate?key=", geminiKey: "test-key"}

	reply, err := m.Generate("Summarize this.", &Schema{Type: "OBJECT"})
	if err != nil || reply != "done" {
		t.Errorf("Generate = %q, %v", reply, err)
	}
}
