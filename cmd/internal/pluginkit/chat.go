package pluginkit

import (
	"bytes"
	"context"
	"crs/openrouter"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ChatTool is a function a plugin offers the model in a Chat. Parameters
// describes its arguments object; nil means it takes none.
type ChatTool struct {
	Name        string
	Description string
	Parameters  *Schema
}

// ToolCall is one call the model asked for. Args is the arguments object the
// model wrote, which may not match the tool's parameters, or parse at all.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolResult answers a ToolCall.
type ToolResult struct {
	Call    ToolCall
	Content string
}

// ChatTurn is the model's turn: text, tool calls, or both.
type ChatTurn struct {
	Text      string
	ToolCalls []ToolCall
}

// Chat is a multi-turn conversation in which the model may call the plugin's
// tools, through the backend's native tool calling: Gemini's function calls
// or OpenRouter's tool calls. It holds the transcript in the backend's own
// shape, so what a model hands back with its calls — Gemini's thought
// signatures, OpenRouter's reasoning — goes back to it untouched. The loop
// that decides what to do with a turn is the plugin's.
type Chat struct {
	model *Model
	tools []ChatTool

	// Gemini
	geminiSystem   string
	geminiContents []json.RawMessage

	// OpenRouter
	openrouterMessages []openrouter.Message
}

// NewChat starts a conversation framed by system, offering tools.
func (m *Model) NewChat(system string, tools []ChatTool) *Chat {
	c := &Chat{model: m, tools: tools}
	if m.provider == ProviderOpenRouter {
		if system != "" {
			c.openrouterMessages = append(c.openrouterMessages, openrouter.SystemMessage(system))
		}
	} else {
		c.geminiSystem = system
	}
	return c
}

// AddUser appends a message from the plugin.
func (c *Chat) AddUser(text string) {
	if c.model.provider == ProviderOpenRouter {
		c.openrouterMessages = append(c.openrouterMessages, openrouter.UserMessage(text))
		return
	}
	c.geminiContents = append(c.geminiContents, mustJSON(geminiChatContent{
		Role: "user", Parts: []geminiChatPart{{Text: text}},
	}))
}

// AddToolResults answers the calls of the model's last turn, one result each.
func (c *Chat) AddToolResults(results []ToolResult) {
	if c.model.provider == ProviderOpenRouter {
		for _, r := range results {
			c.openrouterMessages = append(c.openrouterMessages, openrouter.ToolMessage(r.Call.ID, r.Content))
		}
		return
	}
	content := geminiChatContent{Role: "user"}
	for _, r := range results {
		content.Parts = append(content.Parts, geminiChatPart{FunctionResponse: &geminiFunctionResponse{
			ID:       r.Call.ID,
			Name:     r.Call.Name,
			Response: map[string]string{"result": r.Content},
		}})
	}
	c.geminiContents = append(c.geminiContents, mustJSON(content))
}

// Next asks the model for its next turn and appends it to the conversation. A
// non-empty require names the tool the model must call; empty leaves it free
// to call any tool, or none.
func (c *Chat) Next(ctx context.Context, require string) (ChatTurn, error) {
	if c.model.provider == ProviderOpenRouter {
		return c.nextOpenRouter(ctx, require)
	}
	return c.nextGemini(ctx, require)
}

func (c *Chat) nextOpenRouter(ctx context.Context, require string) (ChatTurn, error) {
	tools := make([]openrouter.Tool, 0, len(c.tools))
	for _, t := range c.tools {
		tools = append(tools, openrouter.Tool{Name: t.Name, Description: t.Description, Parameters: toolParameters(t).JSONSchema()})
	}
	reply, err := c.model.openrouter.Chat(ctx, c.openrouterMessages, tools, require)
	if err != nil {
		return ChatTurn{}, err
	}
	c.openrouterMessages = append(c.openrouterMessages, reply.Message)
	turn := ChatTurn{Text: reply.Text}
	for _, call := range reply.ToolCalls {
		args := json.RawMessage(strings.TrimSpace(call.Function.Arguments))
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: call.ID, Name: call.Function.Name, Args: args})
	}
	return turn, nil
}

// toolParameters is the tool's arguments schema, an empty object for a tool
// that takes none: both backends want an object schema.
func toolParameters(t ChatTool) *Schema {
	if t.Parameters != nil {
		return t.Parameters
	}
	return &Schema{Type: "OBJECT", Properties: map[string]*Schema{}}
}

type geminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Response any    `json:"response"`
}

// geminiChatPart is the part of a content part a chat reads or writes. The
// model's own contents are kept raw, so fields this leaves out — thought
// signatures among them — still go back.
type geminiChatPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiChatContent struct {
	Role  string           `json:"role"`
	Parts []geminiChatPart `json:"parts"`
}

type geminiFunctionDeclaration struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Parameters  *Schema `json:"parameters,omitempty"`
}

type geminiChatRequest struct {
	SystemInstruction *geminiContent    `json:"systemInstruction,omitempty"`
	Contents          []json.RawMessage `json:"contents"`
	Tools             []struct {
		FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
	} `json:"tools,omitempty"`
	ToolConfig *geminiToolConfig `json:"toolConfig,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig struct {
		Mode                 string   `json:"mode"`
		AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
	} `json:"functionCallingConfig"`
}

type geminiChatResponse struct {
	Candidates []struct {
		Content      json.RawMessage `json:"content"`
		FinishReason string          `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
}

func (c *Chat) nextGemini(ctx context.Context, require string) (ChatTurn, error) {
	body := geminiChatRequest{Contents: c.geminiContents}
	if c.geminiSystem != "" {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: c.geminiSystem}}}
	}
	if len(c.tools) > 0 {
		decls := make([]geminiFunctionDeclaration, 0, len(c.tools))
		for _, t := range c.tools {
			decls = append(decls, geminiFunctionDeclaration{Name: t.Name, Description: t.Description, Parameters: toolParameters(t)})
		}
		body.Tools = append(body.Tools, struct {
			FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations"`
		}{decls})
	}
	if require != "" {
		body.ToolConfig = &geminiToolConfig{}
		body.ToolConfig.FunctionCallingConfig.Mode = "ANY"
		body.ToolConfig.FunctionCallingConfig.AllowedFunctionNames = []string{require}
	}

	data, err := json.Marshal(body)
	if err != nil {
		return ChatTurn{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.model.geminiEndpoint+c.model.geminiKey, bytes.NewReader(data))
	if err != nil {
		return ChatTurn{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ChatTurn{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return ChatTurn{}, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(raw))
	}

	var parsed geminiChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return ChatTurn{}, err
	}
	if len(parsed.Candidates) == 0 {
		if parsed.PromptFeedback != nil && parsed.PromptFeedback.BlockReason != "" {
			return ChatTurn{}, fmt.Errorf("the prompt was blocked: %s", parsed.PromptFeedback.BlockReason)
		}
		return ChatTurn{}, errors.New("no content in response")
	}
	candidate := parsed.Candidates[0]
	var content geminiChatContent
	if len(candidate.Content) > 0 {
		if err := json.Unmarshal(candidate.Content, &content); err != nil {
			return ChatTurn{}, err
		}
	}

	var turn ChatTurn
	var text []string
	for _, part := range content.Parts {
		switch {
		case part.FunctionCall != nil:
			args := part.FunctionCall.Args
			if len(args) == 0 || string(args) == "null" {
				args = json.RawMessage("{}")
			}
			// Older models give no ID; the response then goes back without
			// one, matched to its call by name and order.
			turn.ToolCalls = append(turn.ToolCalls, ToolCall{ID: part.FunctionCall.ID, Name: part.FunctionCall.Name, Args: args})
		case part.Text != "" && !part.Thought:
			text = append(text, part.Text)
		}
	}
	turn.Text = strings.Join(text, "")
	if strings.TrimSpace(turn.Text) == "" && len(turn.ToolCalls) == 0 {
		return ChatTurn{}, fmt.Errorf("no content in response (finish reason %q)", candidate.FinishReason)
	}

	// The model's turn goes back as it came; a content without a role is the
	// model's, and Gemini wants that said on the way back.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(candidate.Content, &raw); err != nil {
		return ChatTurn{}, err
	}
	if _, ok := raw["role"]; !ok {
		raw["role"] = json.RawMessage(`"model"`)
	}
	c.geminiContents = append(c.geminiContents, mustJSON(raw))
	return turn, nil
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
