package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// drainTimeout bounds reading what an exited server left in its stdout
	// pipe, and exec's copying of its stderr. Something the server started
	// could hold either pipe open after the server itself is gone.
	drainTimeout = 2 * time.Second
	// stderrTailBytes is how much of the server's stderr is kept for the
	// server_exited message: enough for the last few log lines, which is
	// where the reason it exited is.
	stderrTailBytes = 4 << 10
	// stderrTailLines is how many of those lines the message carries.
	stderrTailLines = 5
)

// serverProcess is one running codereviewserver.
type serverProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *os.File
	stderr *tailBuffer
	// exited is closed once the process has exited; waitErr is its status.
	exited  chan struct{}
	waitErr error
}

func startServer(cfg hostConfig) (*serverProcess, error) {
	args := cfg.ServerArgs
	if args == nil {
		args = []string{"--server"}
	}
	cmd := exec.Command(cfg.ServerPath, args...)
	cmd.Dir = cfg.Dir
	cmd.Env = cfg.Env
	tail := &tailBuffer{limit: stderrTailBytes}
	cmd.Stderr = tail
	if cfg.ServerStderr != nil {
		cmd.Stderr = io.MultiWriter(discardErrors{cfg.ServerStderr}, tail)
	}
	cmd.WaitDelay = drainTimeout

	// Our own pipe rather than StdoutPipe: Wait closes a StdoutPipe as soon
	// as the process exits, which could cut off responses still in it, and
	// Wait is how the host notices the server dying.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	cmd.Stdout = stdoutW
	stdin, err := cmd.StdinPipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return nil, fmt.Errorf("creating stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return nil, err
	}
	// The server has its own copy of the write end; ours would keep the read
	// end from ever reaching EOF.
	stdoutW.Close()

	p := &serverProcess{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdoutR,
		stderr: tail,
		exited: make(chan struct{}),
	}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

// stop asks the server to exit the way every client does, by closing its
// stdin, and kills it if it is still running after grace.
func (p *serverProcess) stop(grace time.Duration, logger *log.Logger) {
	p.stdin.Close()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.exited:
		return
	case <-timer.C:
	}
	logger.Printf("server still running %v after its stdin closed; killing it", grace)
	if err := p.cmd.Process.Kill(); err != nil {
		logger.Printf("killing server: %v", err)
	}
	<-p.exited
}

// drain waits for the response pump (done) to forward whatever the exited
// server left in its stdout pipe. The wait is bounded; closing our end of
// the pipe then ends the pump.
func (p *serverProcess) drain(done <-chan struct{}, logger *log.Logger) {
	if waitFor(done, drainTimeout) {
		return
	}
	logger.Printf("server stdout still open %v after it exited; closing it", drainTimeout)
	p.stdout.Close()
	waitFor(done, drainTimeout)
}

// exitMessage describes how the server exited, with the last lines it
// logged, for the server_exited event.
func (p *serverProcess) exitMessage() string {
	msg := exitStatus(p.waitErr)
	if tail := lastLines(p.stderr.String(), stderrTailLines); tail != "" {
		msg += "; last server output:\n" + tail
	}
	return msg
}

func exitStatus(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

func lastLines(s string, n int) string {
	// The tail buffer can start mid-rune and mid-line; neither is worth
	// showing.
	s = strings.ToValidUTF8(s, "")
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func waitFor(ch <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	// Trimming only past twice the limit keeps the copying amortized.
	if len(t.buf) > 2*t.limit {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.limit:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.buf
	if len(b) > t.limit {
		b = b[len(b)-t.limit:]
	}
	return string(b)
}

// discardErrors reports every write as successful. A failing log write (a
// full disk) would otherwise stop exec copying the server's stderr, and the
// server would block on its next log line once the pipe filled.
type discardErrors struct{ w io.Writer }

func (d discardErrors) Write(p []byte) (int, error) {
	_, _ = d.w.Write(p)
	return len(p), nil
}
