package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// acpConn is one running ACP agent: its process, and the JSON-RPC 2.0
// connection over the process's stdin and stdout, one message per line. The
// server is the protocol's client; the agent answers its requests and makes
// some of its own (reading a file, asking permission), which the turn
// attached answers.
type acpConn struct {
	// key is the command line, environment and mode the agent was started
	// with, so a config change starts a new one.
	key    string
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *tailBuffer

	writeMu sync.Mutex
	nextID  atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan acpResponse
	ended   bool
	endErr  error
	done    chan struct{}
	// stopping is set once the agent is being stopped, before it has exited.
	stopping atomic.Bool

	// dispatchMu is held while a message from the agent is handled, and while
	// the turn emits on its own goroutine, so a turn's events are never
	// emitted concurrently and none after it detaches.
	dispatchMu sync.Mutex
	handler    acpHandler

	// What initialize said, and the session open in this process. Only the
	// turn holding the connection touches them.
	caps        acpAgentCapabilities
	authMethods []acpAuthMethod
	sessionID   string
}

// acpHandler answers what the agent sends while a turn holds the connection.
type acpHandler interface {
	notify(method string, params json.RawMessage)
	request(method string, params json.RawMessage) (any, *acpError)
}

// acpMessage is any JSON-RPC message, as read.
type acpMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *acpError       `json:"error"`
}

// acpOutgoing is a message the server sends.
type acpOutgoing struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

// acpError is a JSON-RPC error.
type acpError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *acpError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = fmt.Sprintf("error %d", e.Code)
	}
	// The details: a string, or whatever the agent attached (a schema
	// validator's complaints), compacted.
	var data string
	if len(e.Data) > 0 && json.Unmarshal(e.Data, &data) != nil {
		var compact bytes.Buffer
		if json.Compact(&compact, e.Data) == nil && compact.String() != "null" {
			data = compact.String()
		}
	}
	if data = strings.TrimSpace(data); data != "" && !strings.Contains(msg, data) {
		if len(data) > 400 {
			data = cutAtLine(data, 400) + "…"
		}
		msg += ": " + data
	}
	return msg
}

// acpResponse is the answer to one of the server's requests.
type acpResponse struct {
	result json.RawMessage
	err    error
}

// startACP starts the agent in dir. It runs in a process group of its own, so
// stopping it also stops whatever it started (Claude Code under
// claude-code-acp, say).
func startACP(name string, argv, env []string, dir, key string) (*acpConn, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = claudeWaitDelay
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c := &acpConn{
		key: key, name: name, cmd: cmd, stdin: stdin, stderr: &tailBuffer{max: 8000},
		pending: map[int64]chan acpResponse{}, done: make(chan struct{}),
	}
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start %s: %w", filepath.Base(argv[0]), err)
	}
	running.add(c)
	go c.read(stdout)
	return c, nil
}

// running are the agents started and not yet exited, for StopAgents.
var running = &acpRunning{conns: map[*acpConn]struct{}{}}

type acpRunning struct {
	mu    sync.Mutex
	conns map[*acpConn]struct{}
}

func (r *acpRunning) add(c *acpConn) {
	r.mu.Lock()
	r.conns[c] = struct{}{}
	r.mu.Unlock()
}

func (r *acpRunning) remove(c *acpConn) {
	r.mu.Lock()
	delete(r.conns, c)
	r.mu.Unlock()
}

// StopAgents stops every ACP agent the server has running, a turn's or one
// kept for its chat's next, and returns once they have exited. The server
// calls it as it shuts down: an agent runs in a process group of its own, so
// neither a signal to the server's nor the server's exit reaches it.
func StopAgents() {
	running.mu.Lock()
	conns := make([]*acpConn, 0, len(running.conns))
	for c := range running.conns {
		conns = append(conns, c)
	}
	running.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.close()
		}()
	}
	wg.Wait()
}

// read handles what the agent writes until it exits. A line can be long — a
// tool result carries a whole file — so it is read without a length limit.
func (c *acpConn) read(stdout io.Reader) {
	br := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			break
		}
	}
	waitErr := c.cmd.Wait()
	// Whatever the agent started goes with it. Its process ID was only just
	// freed, so the group's ID can't have been taken by another yet.
	killGroup(c.cmd)
	c.end(waitErr)
}

func (c *acpConn) dispatch(line []byte) {
	var msg acpMessage
	if json.Unmarshal(line, &msg) != nil {
		return // not JSON-RPC: something the agent logged to stdout
	}
	hasID := len(msg.ID) > 0 && string(msg.ID) != "null"
	switch {
	case msg.Method != "" && hasID:
		c.dispatchMu.Lock()
		var (
			result any
			rpcErr *acpError
		)
		if c.handler != nil {
			result, rpcErr = c.handler.request(msg.Method, msg.Params)
		} else {
			rpcErr = &acpError{Code: acpErrInternal, Message: "the chat isn't running a turn"}
		}
		c.dispatchMu.Unlock()
		reply := acpOutgoing{JSONRPC: "2.0", ID: msg.ID, Error: rpcErr}
		if rpcErr == nil {
			reply.Result = result
			if result == nil {
				reply.Result = struct{}{}
			}
		}
		_ = c.write(reply)
	case msg.Method != "":
		c.dispatchMu.Lock()
		if c.handler != nil {
			c.handler.notify(msg.Method, msg.Params)
		}
		c.dispatchMu.Unlock()
	case hasID:
		id, err := strconv.ParseInt(string(msg.ID), 10, 64)
		if err != nil {
			return
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if !ok {
			return
		}
		if msg.Error != nil {
			ch <- acpResponse{err: msg.Error}
		} else {
			ch <- acpResponse{result: msg.Result}
		}
	}
}

// end records that the agent has exited, failing whatever still waits on it.
func (c *acpConn) end(waitErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended {
		return
	}
	c.ended = true
	running.remove(c)
	detail := lastLines(c.stderr.String(), 10, 1500)
	switch {
	case detail != "":
		c.endErr = fmt.Errorf("%s exited: %s", c.name, detail)
	case waitErr != nil:
		c.endErr = fmt.Errorf("%s exited: %v", c.name, waitErr)
	default:
		c.endErr = fmt.Errorf("%s exited", c.name)
	}
	for id, ch := range c.pending {
		ch <- acpResponse{err: c.endErr}
		delete(c.pending, id)
	}
	close(c.done)
}

// alive reports whether the agent is running and not being stopped.
func (c *acpConn) alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.ended && !c.stopping.Load()
}

func (c *acpConn) write(msg acpOutgoing) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

// start sends a request; its answer arrives on the channel.
func (c *acpConn) start(method string, params any) (<-chan acpResponse, error) {
	id := c.nextID.Add(1)
	ch := make(chan acpResponse, 1)
	c.mu.Lock()
	if c.ended {
		err := c.endErr
		c.mu.Unlock()
		return nil, err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(acpOutgoing{JSONRPC: "2.0", ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("writing to %s: %w", c.name, err)
	}
	return ch, nil
}

// call sends a request and waits for its answer, decoding it into result
// (which may be nil). The agent is stopped when ctx ends first: a request
// left hanging has no way back.
func (c *acpConn) call(ctx context.Context, method string, params, result any) error {
	ch, err := c.start(method, params)
	if err != nil {
		return err
	}
	select {
	case r := <-ch:
		return r.decode(result)
	case <-ctx.Done():
		c.kill()
		return ctx.Err()
	}
}

func (r acpResponse) decode(result any) error {
	if r.err != nil {
		return r.err
	}
	if result == nil || len(r.result) == 0 || string(r.result) == "null" {
		return nil
	}
	return json.Unmarshal(r.result, result)
}

// notify sends a notification.
func (c *acpConn) notify(method string, params any) error {
	return c.write(acpOutgoing{JSONRPC: "2.0", Method: method, Params: params})
}

// attach makes h the handler of what the agent sends; nil detaches. Once
// attach returns, the previous handler is never called again.
func (c *acpConn) attach(h acpHandler) {
	c.dispatchMu.Lock()
	c.handler = h
	c.dispatchMu.Unlock()
}

// locked runs f as if it were handling a message from the agent.
func (c *acpConn) locked(f func()) {
	c.dispatchMu.Lock()
	defer c.dispatchMu.Unlock()
	f()
}

// kill stops the agent at once.
func (c *acpConn) kill() {
	c.stopping.Store(true)
	_ = c.stdin.Close()
	c.killRunning()
}

// killRunning kills the agent's process group unless the agent has exited:
// its process ID, and so the group's, may belong to another process by now
// (read killed what it left behind as it exited).
func (c *acpConn) killRunning() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ended {
		killProcessGroup(c.cmd)
	}
}

// close stops the agent: its stdin is closed, which an ACP agent takes as the
// end, and it is killed when it hasn't exited after acpCloseGrace.
func (c *acpConn) close() {
	c.stopping.Store(true)
	_ = c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(acpCloseGrace):
		c.killRunning()
	}
}

// lastLines is the end of s — at most n lines and max bytes — trimmed.
func lastLines(s string, n, max int) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	s = strings.TrimSpace(strings.Join(lines, "\n"))
	if len(s) > max {
		s = "…" + s[len(s)-max:]
	}
	return s
}
