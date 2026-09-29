// Package llm is the server's plain text-generation layer: the Client
// interface a backend implements, the Gemini backend (gemini.go), the stages
// a call can fail at, and the call log every model call and AI feature run is
// appended to (~/.crs/llm_calls.log, see call_log.go).
//
// The features that call a model live in the ai package, which reaches Gemini
// through this package's Client.
package llm

import (
	"fmt"
)

// Client is the interface to an LLM text-generation backend. It exists so
// the provider can be swapped out (or faked in tests) without touching the
// callers.
type Client interface {
	// Provider identifies the backend, e.g. "gemini".
	Provider() string
	// Model identifies the specific model, e.g. "gemini-flash-latest".
	Model() string
	// Generate sends a prompt and returns the model's text response. Errors
	// should be *CallError values so the call log can attribute the failure
	// to a stage.
	Generate(prompt string) (string, error)
}

// ProviderGemini names the Gemini backend (gemini.go), the one HTTP backend
// this package ships.
const ProviderGemini = "gemini"

// NewClient is the provider factory: it builds the named text-generation
// backend. Backends that are not HTTP APIs — the ai package's command-backed
// provider, which shells out to a CLI agent — are built by their own package
// and only share the Client shape.
func NewClient(provider string) (Client, error) {
	switch provider {
	case ProviderGemini:
		return NewGeminiClient()
	default:
		return nil, &CallError{Stage: StageClientInit, Err: fmt.Errorf("unknown LLM provider %q", provider)}
	}
}

// Stages an LLM call can fail at, recorded in the call log so a failure can
// be attributed to the step that produced it.
const (
	StageClientInit    = "client-init"    // building the client (e.g. missing API key)
	StageRequest       = "request"        // sending the request (network error, timeout)
	StageHTTPStatus    = "http-status"    // non-200 response from the API
	StageDecode        = "decode"         // malformed response body
	StageEmptyResponse = "empty-response" // well-formed response with no content
	StageParse         = "parse"          // response text lacked what we asked for
	StageExitStatus    = "exit-status"    // a command-backed provider exited non-zero
	StageTimeout       = "timeout"        // the call outlived its deadline
)

// CallError is an error from an LLM call attributed to the stage it failed
// at.
type CallError struct {
	Stage string
	Err   error
}

func (e *CallError) Error() string { return e.Err.Error() }
func (e *CallError) Unwrap() error { return e.Err }
