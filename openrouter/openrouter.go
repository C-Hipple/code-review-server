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
	Messages       []message       `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type apiErrorBody struct {
	// Code is usually the HTTP status as a number, but OpenRouter passes some
	// upstream errors through with a string code.
	Code    any    `json:"code"`
	Message string `json:"message"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
			Refusal *string `json:"refusal"`
		} `json:"message"`
		FinishReason string        `json:"finish_reason"`
		Error        *apiErrorBody `json:"error"`
	} `json:"choices"`
	Error *apiErrorBody `json:"error"`
}

// Complete sends prompt as a single user message and returns the text of the
// first choice. A non-nil schema asks for a JSON reply matching it.
func (c *Client) Complete(ctx context.Context, prompt string, schema *JSONSchema) (string, error) {
	body := chatRequest{
		Model:    c.Model,
		Messages: []message{{Role: "user", Content: prompt}},
	}
	if schema != nil {
		body.ResponseFormat = &responseFormat{
			Type:       "json_schema",
			JSONSchema: jsonSchemaFormat{Name: schema.Name, Strict: true, Schema: schema.Schema},
		}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", appURL)
	req.Header.Set("X-Title", appTitle)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", &APIError{StatusCode: resp.StatusCode, Message: errorMessage(raw)}
	}

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("%w: %v", ErrDecode, err)
	}
	if parsed.Error != nil {
		return "", &APIError{StatusCode: resp.StatusCode, Message: parsed.Error.String()}
	}
	if len(parsed.Choices) == 0 {
		return "", ErrEmptyResponse
	}
	choice := parsed.Choices[0]
	if choice.Error != nil {
		return "", &APIError{StatusCode: resp.StatusCode, Message: choice.Error.String()}
	}
	if choice.Message.Content == nil || strings.TrimSpace(*choice.Message.Content) == "" {
		if choice.Message.Refusal != nil && *choice.Message.Refusal != "" {
			return "", fmt.Errorf("%w: the model refused: %s", ErrEmptyResponse, *choice.Message.Refusal)
		}
		return "", fmt.Errorf("%w (finish reason %q)", ErrEmptyResponse, choice.FinishReason)
	}
	return *choice.Message.Content, nil
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
