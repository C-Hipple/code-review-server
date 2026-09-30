// Package openrouter calls OpenRouter's chat completions API, which puts
// models from many vendors — Anthropic, OpenAI, Google, Meta, Mistral and
// more — behind one OpenAI-compatible endpoint and one API key.
//
// It is the one HTTP client for OpenRouter in the module: the AI features
// reach it through llm.OpenRouterClient, and the bundled plugins through
// cmd/internal/pluginkit. It imports nothing else from the module, so a
// plugin binary that links it doesn't also link the server's config and
// database.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// APIKeyEnv is the environment variable the API key is read from.
	APIKeyEnv = "OPENROUTER_API_KEY"
	// DefaultBaseURL is OpenRouter's API root.
	DefaultBaseURL = "https://openrouter.ai/api/v1"
	// DefaultTimeout bounds one HTTP call. It is generous because the models
	// behind OpenRouter include slow reasoning ones and a PR's diff makes for
	// a long prompt; callers with a tighter deadline pass it in the context.
	DefaultTimeout = 3 * time.Minute

	// appURL and appTitle attribute requests to this project on OpenRouter's
	// dashboard (the HTTP-Referer and X-Title headers it documents). They name
	// the software, never the user.
	appURL   = "https://github.com/C-Hipple/code-review-server"
	appTitle = "code-review-server"
)

var (
	// ErrNoAPIKey means OPENROUTER_API_KEY is not set.
	ErrNoAPIKey = errors.New(APIKeyEnv + " not set")
	// ErrNoModel means no model was named. OpenRouter has no default model,
	// and this package pins none: which model to pay for is the user's call.
	ErrNoModel = errors.New("no OpenRouter model configured")
	// ErrDecode wraps a response body that isn't the JSON OpenRouter sends.
	ErrDecode = errors.New("malformed OpenRouter response")
	// ErrEmptyResponse means the response was well formed but carried no text.
	ErrEmptyResponse = errors.New("no content in OpenRouter response")
)

// APIError is an error OpenRouter answered with: a non-200 status, or an
// error object in the body of a 200.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("OpenRouter API request failed with status %d: %s", e.StatusCode, e.Message)
}

// Client calls one model through OpenRouter.
type Client struct {
	APIKey string
	// Model is OpenRouter's name for it, e.g. "anthropic/claude-sonnet-4.5".
	Model   string
	BaseURL string
	HTTP    *http.Client
}

// New builds a client for model with the key in OPENROUTER_API_KEY.
func New(model string) (*Client, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, ErrNoModel
	}
	apiKey := strings.TrimSpace(os.Getenv(APIKeyEnv))
	if apiKey == "" {
		return nil, ErrNoAPIKey
	}
	return &Client{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: DefaultTimeout},
	}, nil
}

// JSONSchema asks for a reply that is a JSON document matching Schema
// (OpenRouter's structured outputs). A model that doesn't support structured
// outputs ignores it, so callers still have to cope with a reply that isn't
// JSON.
type JSONSchema struct {
	Name   string
	Schema any
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type jsonSchemaFormat struct {
	Name   string `json:"name"`
	Strict bool   `json:"strict"`
	Schema any    `json:"schema"`
}

type responseFormat struct {
	Type       string           `json:"type"`
	JSONSchema jsonSchemaFormat `json:"json_schema"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []any           `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Tools          []toolSpec      `json:"tools,omitempty"`
	ToolChoice     any             `json:"tool_choice,omitempty"`
}

type apiErrorBody struct {
	// Code is usually the HTTP status as a number, but OpenRouter passes some
	// upstream errors through with a string code.
	Code    any    `json:"code"`
	Message string `json:"message"`
}

// replyMessage is the part of a choice's message this package reads.
type replyMessage struct {
	Content   *string    `json:"content"`
	Refusal   *string    `json:"refusal"`
	ToolCalls []ToolCall `json:"tool_calls"`
}

type chatResponse struct {
	Choices []struct {
		// Message is kept raw so a chat can send it back exactly as it came.
		Message      json.RawMessage `json:"message"`
		FinishReason string          `json:"finish_reason"`
		Error        *apiErrorBody   `json:"error"`
	} `json:"choices"`
	Error *apiErrorBody `json:"error"`
}

// Complete sends prompt as a single user message and returns the text of the
// first choice. A non-nil schema asks for a JSON reply matching it.
func (c *Client) Complete(ctx context.Context, prompt string, schema *JSONSchema) (string, error) {
	body := chatRequest{
		Model:    c.Model,
		Messages: []any{message{Role: "user", Content: prompt}},
	}
	if schema != nil {
		body.ResponseFormat = &responseFormat{
			Type:       "json_schema",
			JSONSchema: jsonSchemaFormat{Name: schema.Name, Strict: true, Schema: schema.Schema},
		}
	}
	reply, _, finishReason, err := c.send(ctx, body)
	if err != nil {
		return "", err
	}
	if reply.Content == nil || strings.TrimSpace(*reply.Content) == "" {
		if reply.Refusal != nil && *reply.Refusal != "" {
			return "", fmt.Errorf("%w: the model refused: %s", ErrEmptyResponse, *reply.Refusal)
		}
		return "", fmt.Errorf("%w (finish reason %q)", ErrEmptyResponse, finishReason)
	}
	return *reply.Content, nil
}

// Tool is a function the model may call during a Chat. Parameters is the JSON
// Schema of its arguments object.
type Tool struct {
	Name        string
	Description string
	Parameters  any
}

type toolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type toolSpec struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

// ToolCall is one call the model asked for. Arguments is the JSON object of
// its arguments, as a string: the model wrote it, so it may not parse.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Message is one entry of a Chat's conversation. Build them with
// SystemMessage, UserMessage and ToolMessage; an assistant message is the one
// Chat returned, sent back as is.
type Message struct {
	raw json.RawMessage
}

func (m Message) MarshalJSON() ([]byte, error) { return m.raw, nil }

func newMessage(v any) Message {
	raw, _ := json.Marshal(v) // plain strings: cannot fail
	return Message{raw: raw}
}

// SystemMessage carries instructions that frame the whole conversation.
func SystemMessage(text string) Message { return newMessage(message{Role: "system", Content: text}) }

// UserMessage is a turn from the caller.
func UserMessage(text string) Message { return newMessage(message{Role: "user", Content: text}) }

// ToolMessage answers the tool call with id.
func ToolMessage(id, content string) Message {
	return newMessage(struct {
		Role       string `json:"role"`
		ToolCallID string `json:"tool_call_id"`
		Content    string `json:"content"`
	}{"tool", id, content})
}

// ChatReply is the model's turn in a Chat.
type ChatReply struct {
	// Message is the reply to append to the conversation. It is the message
	// exactly as OpenRouter sent it, so whatever a model needs back to carry
	// on — its reasoning, say — goes back too.
	Message   Message
	Text      string
	ToolCalls []ToolCall
}

// Chat sends a conversation with the tools the model may call and returns its
// next turn: text, tool calls, or both. A non-empty require names the tool the
// model must call this turn; empty leaves the choice to the model.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool, require string) (ChatReply, error) {
	body := chatRequest{Model: c.Model}
	for _, m := range messages {
		body.Messages = append(body.Messages, m)
	}
	for _, t := range tools {
		body.Tools = append(body.Tools, toolSpec{Type: "function", Function: toolFunction(t)})
	}
	if require != "" {
		body.ToolChoice = map[string]any{"type": "function", "function": map[string]string{"name": require}}
	}
	reply, raw, finishReason, err := c.send(ctx, body)
	if err != nil {
		return ChatReply{}, err
	}
	out := ChatReply{Message: Message{raw: raw}, ToolCalls: reply.ToolCalls}
	if reply.Content != nil {
		out.Text = *reply.Content
	}
	if strings.TrimSpace(out.Text) == "" && len(out.ToolCalls) == 0 {
		if reply.Refusal != nil && *reply.Refusal != "" {
			return ChatReply{}, fmt.Errorf("%w: the model refused: %s", ErrEmptyResponse, *reply.Refusal)
		}
		return ChatReply{}, fmt.Errorf("%w (finish reason %q)", ErrEmptyResponse, finishReason)
	}
	return out, nil
}

// send posts a chat completion and returns the first choice's message, both
// parsed and raw, with its finish reason.
func (c *Client) send(ctx context.Context, body chatRequest) (replyMessage, json.RawMessage, string, error) {
	var reply replyMessage
	data, err := json.Marshal(body)
	if err != nil {
		return reply, nil, "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return reply, nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", appURL)
	req.Header.Set("X-Title", appTitle)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return reply, nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return reply, nil, "", &APIError{StatusCode: resp.StatusCode, Message: errorMessage(raw)}
	}

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return reply, nil, "", fmt.Errorf("%w: %v", ErrDecode, err)
	}
	if parsed.Error != nil {
		return reply, nil, "", &APIError{StatusCode: resp.StatusCode, Message: parsed.Error.String()}
	}
	if len(parsed.Choices) == 0 {
		return reply, nil, "", ErrEmptyResponse
	}
	choice := parsed.Choices[0]
	if choice.Error != nil {
		return reply, nil, "", &APIError{StatusCode: resp.StatusCode, Message: choice.Error.String()}
	}
	if len(choice.Message) == 0 || string(choice.Message) == "null" {
		return reply, nil, "", fmt.Errorf("%w (finish reason %q)", ErrEmptyResponse, choice.FinishReason)
	}
	if err := json.Unmarshal(choice.Message, &reply); err != nil {
		return reply, nil, "", fmt.Errorf("%w: %v", ErrDecode, err)
	}
	return reply, choice.Message, choice.FinishReason, nil
}

func (e *apiErrorBody) String() string {
	if e.Code == nil {
		return e.Message
	}
	return fmt.Sprintf("%v: %s", e.Code, e.Message)
}

// errorMessage pulls the message out of an error response, falling back to
// the raw body when it isn't OpenRouter's usual {"error": {...}} shape.
func errorMessage(raw []byte) string {
	var parsed struct {
		Error *apiErrorBody `json:"error"`
	}
	if json.Unmarshal(raw, &parsed) == nil && parsed.Error != nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	return strings.TrimSpace(string(raw))
}
