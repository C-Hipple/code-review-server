package pluginkit

import (
	"context"
	"crs/openrouter"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

var readTool = ChatTool{
	Name:        "read",
	Description: "Read a file.",
	Parameters: &Schema{Type: "OBJECT", Properties: map[string]*Schema{
		"path": {Type: "STRING"},
	}, Required: []string{"path"}},
}

func TestChatThroughGemini(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the request: %v", err)
		}
		requests = append(requests, body)
		if len(requests) == 1 {
			w.Write([]byte(`{"candidates": [{"content": {"role": "model", "parts": [
				{"text": "pondering", "thought": true},
				{"functionCall": {"name": "read", "args": {"path": "a.md"}}, "thoughtSignature": "sig"}
			]}, "finishReason": "STOP"}]}`))
			return
		}
		// A content without a role is the model's.
		w.Write([]byte(`{"candidates": [{"content": {"parts": [{"text": "all "}, {"text": "done"}]}}]}`))
	}))
	defer srv.Close()
	m := &Model{provider: ProviderGemini, geminiEndpoint: srv.URL + "/generate?key=", geminiKey: "k"}

	chat := m.NewChat("be brief", []ChatTool{readTool, {Name: "finish", Description: "Finish."}})
	chat.AddUser("go")
	turn, err := chat.Next(context.Background(), "read")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if turn.Text != "" || len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Name != "read" || string(turn.ToolCalls[0].Args) != `{"path": "a.md"}` {
		t.Fatalf("unexpected turn: %+v", turn)
	}
	first := requests[0]
	if sys := first["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]; sys != "be brief" {
		t.Errorf("systemInstruction = %v", first["systemInstruction"])
	}
	decls := first["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)
	if len(decls) != 2 || decls[0].(map[string]any)["name"] != "read" {
		t.Errorf("functionDeclarations = %v", decls)
	}
	// A tool without parameters still declares an object.
	if params := decls[1].(map[string]any)["parameters"].(map[string]any); params["type"] != "OBJECT" {
		t.Errorf("finish parameters = %v", params)
	}
	config := first["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)
	if config["mode"] != "ANY" || config["allowedFunctionNames"].([]any)[0] != "read" {
		t.Errorf("toolConfig = %v", first["toolConfig"])
	}

	chat.AddToolResults([]ToolResult{{Call: turn.ToolCalls[0], Content: "the file"}})
	turn, err = chat.Next(context.Background(), "")
	if err != nil || turn.Text != "all done" || len(turn.ToolCalls) != 0 {
		t.Fatalf("second Next = %+v, %v", turn, err)
	}
	second := requests[1]
	if _, ok := second["toolConfig"]; ok {
		t.Error("an empty require should leave the calling mode to the model")
	}
	contents := second["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("contents = %v", contents)
	}
	model := contents[1].(map[string]any)
	if model["role"] != "model" || model["parts"].([]any)[1].(map[string]any)["thoughtSignature"] != "sig" {
		t.Errorf("the model's turn should go back as it came: %v", model)
	}
	response := contents[2].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if response["name"] != "read" || response["response"].(map[string]any)["result"] != "the file" {
		t.Errorf("functionResponse = %v", response)
	}

	// The next request carries the model's role-less turn with a role.
	chat.AddUser("again")
	chat.Next(context.Background(), "")
	if role := requests[2]["contents"].([]any)[3].(map[string]any)["role"]; role != "model" {
		t.Errorf("role = %v, want model", role)
	}
}

func TestChatThroughOpenRouter(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding the request: %v", err)
		}
		requests = append(requests, body)
		w.Write([]byte(`{"choices": [{"message": {"role": "assistant", "content": null, "tool_calls": [
			{"id": "c1", "type": "function", "function": {"name": "read", "arguments": "{\"path\":\"a.md\"}"}},
			{"id": "c2", "type": "function", "function": {"name": "finish", "arguments": ""}}
		]}}]}`))
	}))
	defer srv.Close()
	m := &Model{provider: ProviderOpenRouter, openrouter: &openrouter.Client{
		APIKey: "k", Model: "vendor/model", BaseURL: srv.URL, HTTP: srv.Client(),
	}}

	chat := m.NewChat("be brief", []ChatTool{readTool, {Name: "finish"}})
	chat.AddUser("go")
	turn, err := chat.Next(context.Background(), "")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(turn.ToolCalls) != 2 || turn.ToolCalls[0].ID != "c1" || string(turn.ToolCalls[0].Args) != `{"path":"a.md"}` ||
		string(turn.ToolCalls[1].Args) != "{}" {
		t.Fatalf("unexpected turn: %+v", turn)
	}
	tools := requests[0]["tools"].([]any)
	params := tools[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if params["type"] != "object" || params["additionalProperties"] != false {
		t.Errorf("parameters should be JSON Schema: %v", params)
	}

	chat.AddToolResults([]ToolResult{{Call: turn.ToolCalls[0], Content: "x"}, {Call: turn.ToolCalls[1], Content: "y"}})
	chat.Next(context.Background(), "finish")
	messages := requests[1]["messages"].([]any)
	if len(messages) != 5 || messages[0].(map[string]any)["role"] != "system" || messages[4].(map[string]any)["tool_call_id"] != "c2" {
		t.Errorf("messages = %v", messages)
	}
}
