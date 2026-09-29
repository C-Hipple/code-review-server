// Package subprocess runs an external command to completion and captures what
// it wrote. It is the execution code plugins (server/plugins.go) and the AI
// features' command-backed provider (ai/providers.go) share, and nothing more:
// how a command is chosen, what arguments it gets and what its output means
// stay with each caller.
package subprocess

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// waitDelay bounds how long Run keeps waiting for a killed command's output
// pipes to close. A CLI that forked helpers can leave them holding stdout open
// after the command itself is gone; without a bound, a timed-out run would
// wait on them indefinitely.
const waitDelay = 5 * time.Second

// Command is one invocation.
type Command struct {
	Name string
	Args []string
	// Stdin is fed to the command's standard input. Empty means none.
	Stdin string
	// Env adds "KEY=value" entries to the environment the command inherits
	// from the server, replacing any the server's environment already has.
	Env []string
	// Timeout bounds the run on top of whatever deadline ctx carries. Zero
	// leaves it to ctx.
	Timeout time.Duration
}

// Output is what a command wrote before it exited or was killed.
type Output struct {
	Stdout string
	Stderr string
}

// Run executes c and waits for it to finish. The error is non-nil when the
// command could not start, exited non-zero, or was killed at its deadline;
// Output holds whatever it wrote in every case.
//
// A run that outlived its deadline returns an error wrapping
// context.DeadlineExceeded, so callers can tell a timeout from a failure with
// errors.Is rather than reading "signal: killed" out of the message.
func Run(ctx context.Context, c Command) (Output, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	if len(c.Env) > 0 {
		// exec keeps the last value of a duplicated key, so these win.
		cmd.Env = append(os.Environ(), c.Env...)
	}
	cmd.WaitDelay = waitDelay

	err := cmd.Run()
	out := Output{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil && ctx.Err() != nil {
		return out, fmt.Errorf("%w (%v)", ctx.Err(), err)
	}
	return out, err
}

// SplitCommand splits a configured command line into the program and its
// arguments the way a POSIX shell splits words: whitespace separates them,
// single quotes keep everything literal, double quotes keep whitespace (a
// backslash inside them escapes only ", \, $ and `), and a backslash outside
// quotes escapes the next character. Nothing else a shell does applies — no
// pipes, globs or variable expansion — since the command is executed directly.
func SplitCommand(line string) ([]string, error) {
	var (
		words   []string
		current strings.Builder
		inWord  bool
		quote   rune // 0, '\'' or '"'
		escaped bool
	)
	for _, r := range line {
		switch {
		case escaped && quote == '"':
			if !strings.ContainsRune("\"\\$`", r) {
				current.WriteRune('\\')
			}
			current.WriteRune(r)
			escaped = false
		case escaped:
			current.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == '\\':
			escaped = true
			inWord = true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote in %q", quote, line)
	}
	if escaped {
		return nil, fmt.Errorf("trailing backslash in %q", line)
	}
	if inWord {
		words = append(words, current.String())
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("command is empty")
	}
	return words, nil
}
