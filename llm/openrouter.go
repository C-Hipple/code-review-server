package llm

import (
	"context"
	"crs/openrouter"
	"errors"
)

// OpenRouter is the second HTTP backend: one API key reaches models from
// many vendors, named the way OpenRouter names them
// ("anthropic/claude-sonnet-4.5", "google/gemini-2.5-flash"). The HTTP call
// itself lives in the openrouter package, which the bundled plugins share.

// ProviderOpenRouter names the OpenRouter backend.
const ProviderOpenRouter = "openrouter"

// ContextClient is implemented by clients whose calls take a context, so a
// caller's deadline cancels the request itself instead of abandoning it.
type ContextClient interface {
	Client
	GenerateContext(ctx context.Context, prompt string) (string, error)
}

// OpenRouterClient implements ContextClient on top of OpenRouter's chat
// completions API.
type OpenRouterClient struct {
	client *openrouter.Client
}

// NewOpenRouterClient builds a client for model with the key in
// OPENROUTER_API_KEY. OpenRouter has no default model, so an empty one fails
// like a missing key does, at stage client-init.
func NewOpenRouterClient(model string) (*OpenRouterClient, error) {
	client, err := openrouter.New(model)
	if err != nil {
		return nil, &CallError{Stage: StageClientInit, Err: err}
	}
	return &OpenRouterClient{client: client}, nil
}

func (c *OpenRouterClient) Provider() string { return ProviderOpenRouter }

func (c *OpenRouterClient) Model() string { return c.client.Model }

func (c *OpenRouterClient) Generate(prompt string) (string, error) {
	return c.GenerateContext(context.Background(), prompt)
}

// GenerateContext sends the prompt and returns the model's answer. Failures
// come back as *CallError values attributed to the stage that failed.
func (c *OpenRouterClient) GenerateContext(ctx context.Context, prompt string) (string, error) {
	text, err := c.client.Complete(ctx, prompt, nil)
	if err == nil {
		return text, nil
	}
	var apiErr *openrouter.APIError
	switch {
	case ctx.Err() != nil:
		return "", &CallError{Stage: StageTimeout, Err: err}
	case errors.As(err, &apiErr):
		return "", &CallError{Stage: StageHTTPStatus, Err: err}
	case errors.Is(err, openrouter.ErrDecode):
		return "", &CallError{Stage: StageDecode, Err: err}
	case errors.Is(err, openrouter.ErrEmptyResponse):
		return "", &CallError{Stage: StageEmptyResponse, Err: err}
	default:
		return "", &CallError{Stage: StageRequest, Err: err}
	}
}
