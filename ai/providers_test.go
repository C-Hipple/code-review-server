package ai

import (
	"context"
	"crs/config"
	"crs/llm"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func wantStage(t *testing.T, err error, stage string) {
	t.Helper()
	var callErr *llm.CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("expected a *llm.CallError at stage %q, got %T: %v", stage, err, err)
	}
	if callErr.Stage != stage {
		t.Errorf("stage = %q, want %q (%v)", callErr.Stage, stage, err)
	}
}

// script writes an executable shell script and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	return path
}

func TestCommandProviderSendsThePromptOnStdin(t *testing.T) {
	// The contract every CLI agent can meet: prompt in on stdin, answer out
	// on stdout. Arguments from the configured command line pass through.
	cmd := script(t, `printf '%s|' "$1"; tr a-z A-Z`)
	p, err := NewProvider(config.AIProviderCommand, cmd+` "--flag with space"`)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if p.Name() != "command" || p.Model() != cmd+` "--flag with space"` {
		t.Errorf("name %q model %q", p.Name(), p.Model())
	}
	got, err := p.Generate(context.Background(), "judge these")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "--flag with space|JUDGE THESE" {
		t.Errorf("got %q", got)
	}
}

func TestCommandProviderFailureStages(t *testing.T) {
	ctx := context.Background()

	failing, _ := NewProvider(config.AIProviderCommand, script(t, `echo "rate limited" >&2; exit 2`))
	_, err := failing.Generate(ctx, "p")
	wantStage(t, err, llm.StageExitStatus)

	silent, _ := NewProvider(config.AIProviderCommand, script(t, `cat >/dev/null`))
	_, err = silent.Generate(ctx, "p")
	wantStage(t, err, llm.StageEmptyResponse)

	missing, _ := NewProvider(config.AIProviderCommand, "definitely-not-an-agent-crs -p")
	_, err = missing.Generate(ctx, "p")
	wantStage(t, err, llm.StageClientInit)

	slow, _ := NewProvider(config.AIProviderCommand, script(t, `exec sleep 10`))
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err = slow.Generate(short, "p")
	wantStage(t, err, llm.StageTimeout)
}

func TestNewProviderRejectsWhatItCannotBuild(t *testing.T) {
	_, err := NewProvider(config.AIProviderCommand, "  ")
	wantStage(t, err, llm.StageClientInit)

	_, err = NewProvider("openai", "")
	wantStage(t, err, llm.StageClientInit)

	t.Setenv("GEMINI_API_KEY", "")
	_, err = NewProvider(config.AIProviderGemini, "")
	wantStage(t, err, llm.StageClientInit)

	t.Setenv("GEMINI_API_KEY", "test-key")
	p, err := NewProvider(config.AIProviderGemini, "")
	if err != nil || p.Name() != "gemini" {
		t.Errorf("gemini provider: %v, %v", p, err)
	}
}

// slowClient is an llm.Client that answers after a delay.
type slowClient struct{ delay time.Duration }

func (c slowClient) Provider() string { return "slow" }
func (c slowClient) Model() string    { return "slow-model" }
func (c slowClient) Generate(prompt string) (string, error) {
	time.Sleep(c.delay)
	return "late: " + prompt, nil
}

func TestClientProviderAddsTheDeadlineToAnLLMClient(t *testing.T) {
	p := &clientProvider{client: slowClient{delay: 20 * time.Millisecond}}
	if got, err := p.Generate(context.Background(), "hi"); err != nil || got != "late: hi" {
		t.Errorf("Generate = %q, %v", got, err)
	}

	p = &clientProvider{client: slowClient{delay: 5 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := p.Generate(ctx, "hi")
	wantStage(t, err, llm.StageTimeout)
	if time.Since(start) > 2*time.Second {
		t.Error("Generate outlived its deadline")
	}
}

func TestLazyProviderBuildsOnceAndCounts(t *testing.T) {
	builds := 0
	p := &lazyProvider{build: func() (Provider, error) {
		builds++
		return &scriptedProvider{t: t, answers: []string{"one", "three"}}, nil
	}}
	if p.Name() != "" {
		t.Error("an unbuilt provider has no name yet")
	}
	for _, want := range []string{"one", "three"} {
		if got, err := p.Generate(context.Background(), "p"); err != nil || got != want {
			t.Errorf("Generate = %q, %v", got, err)
		}
	}
	if builds != 1 || p.calls != 2 || p.responseBytes != len("one")+len("three") || p.Name() != "fake" {
		t.Errorf("builds %d calls %d bytes %d name %q", builds, p.calls, p.responseBytes, p.Name())
	}

	broken := &lazyProvider{build: func() (Provider, error) { return nil, errors.New("no key") }}
	for i := 0; i < 2; i++ {
		if _, err := broken.Generate(context.Background(), "p"); err == nil {
			t.Error("expected the build error")
		}
	}
	if broken.calls != 0 {
		t.Errorf("a call that never reached a model was counted: %d", broken.calls)
	}
}
