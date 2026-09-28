package subprocess

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesStdoutAndStderrSeparately(t *testing.T) {
	out, err := Run(context.Background(), Command{
		Name: "sh",
		Args: []string{"-c", "echo out; echo err >&2"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Stdout != "out\n" || out.Stderr != "err\n" {
		t.Errorf("got stdout %q stderr %q", out.Stdout, out.Stderr)
	}
}

func TestRunFeedsStdin(t *testing.T) {
	out, err := Run(context.Background(), Command{Name: "cat", Stdin: "the prompt\n"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Stdout != "the prompt\n" {
		t.Errorf("stdout = %q, want the stdin echoed back", out.Stdout)
	}
}

func TestRunReportsNonZeroExitWithOutput(t *testing.T) {
	out, err := Run(context.Background(), Command{
		Name: "sh",
		Args: []string{"-c", "echo partial; echo broken >&2; exit 3"},
	})
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("expected exit status 3, got %v", err)
	}
	if out.Stdout != "partial\n" || out.Stderr != "broken\n" {
		t.Errorf("output lost on failure: %+v", out)
	}
}

func TestRunTimeoutIsDistinguishable(t *testing.T) {
	start := time.Now()
	_, err := Run(context.Background(), Command{
		Name:    "sleep",
		Args:    []string{"10"},
		Timeout: 100 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a deadline error, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("timed-out command took %v to return", time.Since(start))
	}
}

func TestRunMissingCommand(t *testing.T) {
	_, err := Run(context.Background(), Command{Name: "definitely-not-a-real-command-crs"})
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("expected exec.ErrNotFound, got %v", err)
	}
}

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"claude -p", []string{"claude", "-p"}},
		{"  llm   -m  gpt  ", []string{"llm", "-m", "gpt"}},
		{`agent --system "be brief, please"`, []string{"agent", "--system", "be brief, please"}},
		{`agent 'single $quoted \n'`, []string{"agent", `single $quoted \n`}},
		{`agent "say \"hi\" \n"`, []string{"agent", `say "hi" \n`}},
		{`agent a\ b`, []string{"agent", "a b"}},
		{`agent ""`, []string{"agent", ""}},
		{"/usr/local/bin/tool", []string{"/usr/local/bin/tool"}},
	}
	for _, tt := range tests {
		got, err := SplitCommand(tt.in)
		if err != nil {
			t.Errorf("SplitCommand(%q): %v", tt.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("SplitCommand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSplitCommandRejectsMalformedLines(t *testing.T) {
	for _, in := range []string{"", "   ", `agent "open`, `agent 'open`, `agent trailing\`} {
		if got, err := SplitCommand(in); err == nil {
			t.Errorf("SplitCommand(%q) = %q, want an error", in, got)
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Errorf("SplitCommand(%q) returned an empty error", in)
		}
	}
}
