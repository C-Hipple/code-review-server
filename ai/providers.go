package ai

import (
	"context"
	"crs/config"
	"crs/llm"
	"crs/subprocess"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Provider is the one-shot seam: one prompt in, one answer out. It generalizes
// llm.Client's Generate with a context, so the run's deadline reaches the call.
//
// Errors should be *llm.CallError values attributed to the stage that failed,
// so the call log can say where a run died.
type Provider interface {
	// Name identifies the backend, e.g. "gemini", "openrouter" or "command".
	Name() string
	// Model identifies what answers: a model name, or the command line.
	Model() string
	Generate(ctx context.Context, prompt string) (string, error)
}

// NewProvider builds the provider config chose: config.AIProviderGemini,
// config.AIProviderOpenRouter asking for the choice's Model, or
// config.AIProviderCommand running its Command. Failures are *llm.CallError
// values at stage client-init.
func NewProvider(choice config.AIProviderChoice) (Provider, error) {
	switch choice.Provider {
	case config.AIProviderGemini:
		client, err := llm.NewClient(llm.ProviderGemini)
		if err != nil {
			return nil, err
		}
		return &clientProvider{client: client}, nil
	case config.AIProviderOpenRouter:
		if strings.TrimSpace(choice.Model) == "" {
			return nil, &llm.CallError{Stage: llm.StageClientInit,
				Err: fmt.Errorf("the openrouter provider has no model: set AI.DefaultModel or the feature's Model")}
		}
		client, err := llm.NewOpenRouterClient(choice.Model)
		if err != nil {
			return nil, err
		}
		return &clientProvider{client: client}, nil
	case config.AIProviderCommand:
		if strings.TrimSpace(choice.Command) == "" {
			return nil, &llm.CallError{Stage: llm.StageClientInit,
				Err: fmt.Errorf("the command provider has no command: set AI.DefaultCommand or the feature's Command")}
		}
		argv, err := subprocess.SplitCommand(choice.Command)
		if err != nil {
			return nil, &llm.CallError{Stage: llm.StageClientInit, Err: err}
		}
		return &commandProvider{command: choice.Command, argv: argv}, nil
	default:
		return nil, &llm.CallError{Stage: llm.StageClientInit, Err: fmt.Errorf("unknown AI provider %q", choice.Provider)}
	}
}

// clientProvider adapts an llm.Client — Gemini or OpenRouter — to the
// Provider seam. The client itself is reused unchanged; only the deadline is
// added here.
type clientProvider struct {
	client llm.Client
}

func (p *clientProvider) Name() string  { return p.client.Provider() }
func (p *clientProvider) Model() string { return p.client.Model() }

// Generate returns when the client answers or the deadline passes, whichever
// comes first. A client that takes a context (llm.ContextClient) has its
// request cancelled at the deadline; any other's abandoned call still
// finishes in the background, bounded by the client's own HTTP timeout.
func (p *clientProvider) Generate(ctx context.Context, prompt string) (string, error) {
	if c, ok := p.client.(llm.ContextClient); ok {
		return c.GenerateContext(ctx, prompt)
	}
	type answer struct {
		text string
		err  error
	}
	done := make(chan answer, 1)
	go func() {
		text, err := p.client.Generate(prompt)
		done <- answer{text, err}
	}()
	select {
	case a := <-done:
		return a.text, a.err
	case <-ctx.Done():
		return "", &llm.CallError{Stage: llm.StageTimeout, Err: ctx.Err()}
	}
}

// commandProvider runs a CLI the user named — `claude -p`, `llm`, an in-house
// wrapper — as the model. The contract is the simplest one every such CLI can
// meet: the prompt arrives on stdin, the answer is whatever the command prints
// on stdout, and a non-zero exit is a failure.
type commandProvider struct {
	command string
	argv    []string
}

func (p *commandProvider) Name() string  { return config.AIProviderCommand }
func (p *commandProvider) Model() string { return p.command }

func (p *commandProvider) Generate(ctx context.Context, prompt string) (string, error) {
	out, err := subprocess.Run(ctx, subprocess.Command{Name: p.argv[0], Args: p.argv[1:], Stdin: prompt})
	if err != nil {
		stderr := strings.TrimSpace(out.Stderr)
		if len(stderr) > 500 {
			stderr = stderr[:500] + "..."
		}
		var exitErr *exec.ExitError
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return "", &llm.CallError{Stage: llm.StageTimeout, Err: fmt.Errorf("%s: %w", p.command, err)}
		case errors.As(err, &exitErr):
			return "", &llm.CallError{Stage: llm.StageExitStatus, Err: fmt.Errorf("%s: %w; stderr: %s", p.command, err, stderr)}
		default:
			// The command never ran: not on PATH, not executable.
			return "", &llm.CallError{Stage: llm.StageClientInit, Err: fmt.Errorf("%s: %w", p.command, err)}
		}
	}
	if strings.TrimSpace(out.Stdout) == "" {
		return "", &llm.CallError{Stage: llm.StageEmptyResponse, Err: fmt.Errorf("%s printed nothing on stdout", p.command)}
	}
	return out.Stdout, nil
}

// lazyProvider is the Provider a feature's Request carries. It builds the
// configured provider on first use — a run the deterministic rules settle on
// their own never needs one, and never fails for want of an API key — and
// records every call for the run's call log entry.
type lazyProvider struct {
	build func() (Provider, error)

	built    bool
	inner    Provider
	buildErr error

	calls         int
	responseBytes int
}

func (p *lazyProvider) provider() (Provider, error) {
	if !p.built {
		p.built = true
		p.inner, p.buildErr = p.build()
	}
	return p.inner, p.buildErr
}

// Name and Model describe the configured provider without building it, which
// would spend a failure on a call nobody made; before the first call they are
// empty.
func (p *lazyProvider) Name() string {
	if p.inner == nil {
		return ""
	}
	return p.inner.Name()
}

func (p *lazyProvider) Model() string {
	if p.inner == nil {
		return ""
	}
	return p.inner.Model()
}

func (p *lazyProvider) Generate(ctx context.Context, prompt string) (string, error) {
	inner, err := p.provider()
	if err != nil {
		return "", err
	}
	p.calls++
	text, err := inner.Generate(ctx, prompt)
	p.responseBytes += len(text)
	return text, err
}
