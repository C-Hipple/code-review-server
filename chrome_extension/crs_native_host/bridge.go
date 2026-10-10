package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"
	"unicode/utf8"
)

const (
	// defaultShutdownGrace is how long a server whose stdin was closed gets
	// to exit before it is killed.
	defaultShutdownGrace = 5 * time.Second
	// flushTimeout bounds delivering the last queued frames at exit.
	flushTimeout = 5 * time.Second
)

// hostConfig is what run needs to start and bridge one server. main fills it
// from the environment; tests point it at a fake server.
type hostConfig struct {
	// ServerPath is the codereviewserver binary, already resolved.
	ServerPath string
	// ServerArgs are its arguments; nil means --server.
	ServerArgs []string
	// Dir is the server's working directory.
	Dir string
	// Env is the server's environment; nil inherits the host's.
	Env []string
	// ServerStderr receives the server's stderr, which is its log.
	ServerStderr io.Writer
	// Log records the host's own lifecycle.
	Log *log.Logger
	// LogPath and Version are reported to the extension in host events.
	LogPath string
	Version string
	// ShutdownGrace overrides defaultShutdownGrace.
	ShutdownGrace time.Duration
}

// run bridges Chrome, on stdin and stdout, to one codereviewserver until
// Chrome closes stdin, ctx is cancelled (a signal) or the server dies, and
// returns the host's exit code. Every path out stops the server: an exit
// that left it running would leave a server nobody can reach.
func run(ctx context.Context, stdin io.Reader, stdout io.Writer, cfg hostConfig) int {
	logger := cfg.Log
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	grace := cfg.ShutdownGrace
	if grace <= 0 {
		grace = defaultShutdownGrace
	}

	out := newFrameWriter(stdout, logger)
	defer out.close(flushTimeout)

	srv, err := startServer(cfg)
	if err != nil {
		msg := fmt.Sprintf("starting %s: %v", cfg.ServerPath, err)
		logger.Print(msg)
		out.sendEvent(hostEvent{Event: eventError, Message: msg, LogPath: cfg.LogPath})
		return 1
	}
	defer srv.stdout.Close()
	logger.Printf("started %s (pid %d)", cfg.ServerPath, srv.cmd.Process.Pid)
	out.sendEvent(hostEvent{
		Event:      eventReady,
		ServerPath: cfg.ServerPath,
		LogPath:    cfg.LogPath,
		Version:    cfg.Version,
	})

	responsesDone := make(chan struct{})
	go func() {
		defer close(responsesDone)
		if err := pumpResponses(srv.stdout, out, logger); err != nil && !errors.Is(err, os.ErrClosed) {
			logger.Printf("reading server stdout: %v", err)
		}
	}()
	requestsDone := make(chan error, 1)
	go func() { requestsDone <- pumpRequests(stdin, srv.stdin, logger) }()

	code := 0
	select {
	case err := <-requestsDone:
		if err != nil {
			logger.Printf("reading from Chrome: %v; shutting down", err)
			code = 1
		} else {
			logger.Print("Chrome closed the connection; shutting down")
		}
		srv.stop(grace, logger)
	case <-ctx.Done():
		logger.Print("signalled; shutting down")
		srv.stop(grace, logger)
	case <-srv.exited:
		// Forward what the server wrote before it died first: those are
		// answers to calls the extension is waiting on.
		srv.drain(responsesDone, logger)
		msg := srv.exitMessage()
		logger.Printf("server exited unexpectedly: %s", msg)
		out.sendEvent(hostEvent{Event: eventServerExited, Message: msg, LogPath: cfg.LogPath})
		return 1
	}
	srv.drain(responsesDone, logger)
	logger.Printf("server stopped (%s)", exitStatus(srv.waitErr))
	return code
}

// pumpRequests forwards each message from Chrome to the server as one line
// of JSON. It returns nil when Chrome closes stdin and an error when the
// stream breaks; either way the connection is over.
func pumpRequests(stdin io.Reader, server io.Writer, logger *log.Logger) error {
	for {
		msg, err := readFrame(stdin, maxIncomingFrame)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		line, err := requestLine(msg)
		if err != nil {
			logger.Printf("skipping a %d-byte message from Chrome: %v", len(msg), err)
			continue
		}
		label := describeRequest(msg)
		logger.Printf("request %s", label)
		if _, err := server.Write(line); err != nil {
			// The server is gone; run hears about it from Wait.
			logger.Printf("forwarding request %s: %v", label, err)
		}
	}
}

// requestLine compacts msg into the newline-terminated line the server's
// JSON-RPC codec reads. Compacting is what makes the newline a safe
// delimiter: no raw newline survives it.
func requestLine(msg []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, msg); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	if buf.Len() == 0 || buf.Bytes()[0] != '{' {
		return nil, errors.New("not a JSON object")
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// describeRequest names a request for the log by its method and id. The
// params are left out: they can hold tokens and comment text.
func describeRequest(msg []byte) string {
	var req struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(msg, &req)
	id := string(req.ID)
	if id == "" {
		id = "none"
	} else if len(id) > 64 {
		id = id[:64] + "…"
	}
	return fmt.Sprintf("%q id=%s", req.Method, id)
}

// pumpResponses reads the server's newline-delimited responses and queues
// each for Chrome. It returns nil at EOF. bufio.Reader rather than Scanner,
// because a response (a big diff) can be many megabytes.
func pumpResponses(r io.Reader, out *frameWriter, logger *log.Logger) error {
	br := bufio.NewReaderSize(r, 64<<10)
	fwd := &responseForwarder{out: out, log: logger}
	for {
		line, err := br.ReadBytes('\n')
		if msg := bytes.TrimSpace(line); len(msg) > 0 {
			fwd.forward(msg)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// responseForwarder queues server responses for Chrome: as is when one fits
// in a frame, as a crs_chunk stream when it doesn't.
type responseForwarder struct {
	out    *frameWriter
	log    *log.Logger
	stream int // the last chunk stream number used
}

func (f *responseForwarder) forward(line []byte) {
	line, err := checkResponse(line)
	if err != nil {
		f.log.Printf("skipping a %d-byte line from the server: %v", len(line), err)
		return
	}
	if len(line) <= maxVerbatimLine {
		f.out.send(line)
		return
	}
	f.stream++
	frames, err := chunkFrames(f.stream, line)
	if err != nil {
		f.log.Printf("dropping response id=%s: %v", responseID(line), err)
		return
	}
	f.log.Printf("response id=%s: %d bytes in %d chunks (stream %d)",
		responseID(line), len(line), len(frames), f.stream)
	f.out.send(frames...)
}

// checkResponse makes sure Chrome will accept line, since it drops the
// whole connection on a message that isn't valid JSON. Invalid UTF-8 is
// replaced rather than rejected (Go's encoder never writes it, but a reply
// that is otherwise fine beats a timeout); anything that isn't a JSON object,
// such as stray output on the server's stdout, is rejected.
func checkResponse(line []byte) ([]byte, error) {
	if !utf8.Valid(line) {
		line = bytes.ToValidUTF8(line, []byte("\uFFFD"))
	}
	if line[0] != '{' || !json.Valid(line) {
		return line, errors.New("not a JSON object")
	}
	return line, nil
}

// responseID reads a response's id for the log. Go's JSON-RPC codec writes
// it first, so this never decodes the (possibly huge) result.
func responseID(line []byte) string {
	dec := json.NewDecoder(bytes.NewReader(line))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "?"
	}
	if key, err := dec.Token(); err != nil || key != "id" {
		return "?"
	}
	var id json.RawMessage
	if err := dec.Decode(&id); err != nil {
		return "?"
	}
	return string(id)
}
