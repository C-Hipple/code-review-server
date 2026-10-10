package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServerEnv makes the test binary act as codereviewserver (see
// TestHelperProcess); its value picks the behavior.
const fakeServerEnv = "CRS_NATIVE_HOST_FAKE_SERVER"

// fakeMarkerEnv names a file the fake server writes when it sees stdin
// close, which is how the tests tell a clean shutdown from a kill.
const fakeMarkerEnv = "CRS_NATIVE_HOST_FAKE_MARKER"

// bigText is the result of the fake's Fake.Big method: far over
// maxVerbatimLine, with 2-, 3- and 4-byte runes that chunk boundaries fall
// inside, and characters Go's encoder escapes.
var bigText = strings.Repeat("ünïcödé 日本語 🎉 \"q\" \\ <a&b> \u2028 ", 60_000)

// TestHelperProcess is not a test: run as the server by the bridge tests, it
// stands in for `codereviewserver --server`, speaking newline-delimited
// JSON-RPC on stdin/stdout the way net/rpc/jsonrpc does.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(fakeServerEnv)
	if mode == "" {
		return
	}
	os.Exit(fakeServer(mode))
}

func fakeServer(mode string) int {
	if !slices.Contains(os.Args, "--server") {
		fmt.Fprintln(os.Stderr, "fake server: started without --server")
		return 2
	}
	fmt.Fprintln(os.Stderr, "fake server: listening")
	// Stray output on stdout must not reach Chrome.
	fmt.Println("fake server: not a JSON-RPC response")

	in := bufio.NewReader(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			if mode == "ignore-eof" {
				time.Sleep(time.Minute)
				return 0
			}
			if marker := os.Getenv(fakeMarkerEnv); marker != "" {
				_ = os.WriteFile(marker, []byte("stdin closed\n"), 0o600)
			}
			return 0
		}
		var req struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			fmt.Fprintf(os.Stderr, "fake server: bad request line %q: %v\n", line, err)
			return 2
		}
		// Field order as net/rpc/jsonrpc's serverResponse writes it.
		resp := struct {
			ID     json.RawMessage `json:"id"`
			Result any             `json:"result"`
			Error  any             `json:"error"`
		}{ID: req.ID}
		switch req.Method {
		case "Fake.Echo":
			resp.Result = map[string]string{"line": string(line)}
		case "Fake.Big":
			resp.Result = map[string]string{"text": bigText}
		case "Fake.Exit":
			fmt.Fprintln(os.Stderr, "fatal: the config is broken")
			return 3
		default:
			resp.Error = "unknown method " + req.Method
		}
		if err := out.Encode(resp); err != nil {
			return 2
		}
	}
}

// syncBuffer collects the host's log and the fake server's stderr, which
// are written from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// hostHarness runs the bridge in-process with Chrome's side played by pipes.
type hostHarness struct {
	t      *testing.T
	chrome *io.PipeWriter // the host's stdin
	frames chan []byte    // the host's stdout, one frame each
	done   chan struct{}
	code   int
	cancel context.CancelFunc
	logs   *syncBuffer
	marker string
	cfg    hostConfig
}

const testLogPath = "/test/native_host.log"

func startHost(t *testing.T, mode string, configure func(*hostConfig)) *hostHarness {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	h := &hostHarness{
		t:      t,
		chrome: stdinW,
		frames: make(chan []byte, 64),
		done:   make(chan struct{}),
		cancel: cancel,
		logs:   &syncBuffer{},
		marker: filepath.Join(dir, "stdin-closed"),
	}
	h.cfg = hostConfig{
		ServerPath:   exe,
		ServerArgs:   []string{"-test.run=^TestHelperProcess$", "--", "--server"},
		Dir:          dir,
		Env:          append(os.Environ(), fakeServerEnv+"="+mode, fakeMarkerEnv+"="+h.marker),
		ServerStderr: h.logs,
		Log:          log.New(h.logs, "host: ", 0),
		LogPath:      testLogPath,
		Version:      "test",
	}
	if configure != nil {
		configure(&h.cfg)
	}

	go func() {
		h.code = run(ctx, stdinR, stdoutW, h.cfg)
		stdoutW.Close()
		close(h.done)
	}()
	go func() {
		defer close(h.frames)
		for {
			// The Chrome-side limit doubles as a check on every frame's size.
			frame, err := readFrame(stdoutR, maxOutgoingFrame)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					fmt.Fprintf(h.logs, "test: reading host stdout: %v\n", err)
				}
				return
			}
			h.frames <- frame
		}
	}()
	t.Cleanup(func() {
		cancel()
		stdinW.Close()
		select {
		case <-h.done:
		case <-time.After(15 * time.Second):
			t.Error("host did not exit")
		}
		if t.Failed() {
			t.Logf("host log and server stderr:\n%s", h.logs.String())
		}
	})
	return h
}

func (h *hostHarness) send(msg string) {
	h.t.Helper()
	if err := writeFrame(h.chrome, []byte(msg)); err != nil {
		h.t.Fatalf("sending %s: %v", msg, err)
	}
}

func (h *hostHarness) next() []byte {
	h.t.Helper()
	select {
	case frame, ok := <-h.frames:
		if !ok {
			h.t.Fatal("host closed stdout")
		}
		return frame
	case <-time.After(15 * time.Second):
		h.t.Fatal("timed out waiting for a frame")
	}
	return nil
}

func (h *hostHarness) nextEvent() hostEvent {
	h.t.Helper()
	frame := h.next()
	var msg struct {
		Host *hostEvent `json:"crs_host"`
	}
	if err := json.Unmarshal(frame, &msg); err != nil || msg.Host == nil {
		h.t.Fatalf("want a crs_host event, got %.200s (%v)", frame, err)
	}
	return *msg.Host
}

func (h *hostHarness) wait() int {
	h.t.Helper()
	select {
	case <-h.done:
		return h.code
	case <-time.After(15 * time.Second):
		h.t.Fatal("host did not exit")
	}
	return -1
}

// rest returns the frames the host wrote after the last one read, once it
// has exited.
func (h *hostHarness) rest() [][]byte {
	h.t.Helper()
	var frames [][]byte
	for frame := range h.frames {
		frames = append(frames, frame)
	}
	return frames
}

func (h *hostHarness) serverSawEOF() bool {
	_, err := os.Stat(h.marker)
	return err == nil
}

type testResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  any             `json:"error"`
}

func decodeResponse(t *testing.T, data []byte) testResponse {
	t.Helper()
	var resp testResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("response %.200s: %v", data, err)
	}
	return resp
}

func TestBridgeEndToEnd(t *testing.T) {
	h := startHost(t, "echo", nil)

	ready := h.next()
	serverPath, _ := json.Marshal(h.cfg.ServerPath)
	wantReady := fmt.Sprintf(`{"crs_host":{"event":"ready","server_path":%s,"log_path":%q,"version":"test"}}`,
		serverPath, testLogPath)
	if string(ready) != wantReady {
		t.Fatalf("ready event:\n got %s\nwant %s", ready, wantReady)
	}

	// A request is compacted to one line before it reaches the server.
	h.send("{ \"method\": \"Fake.Echo\",\n  \"params\": [ { \"Owner\": \"o\", \"Body\": \"a\\nb <i>\" } ],\n  \"id\": 1 }")
	resp := decodeResponse(t, h.next())
	var echo struct{ Line string }
	if err := json.Unmarshal(resp.Result, &echo); err != nil || resp.ID != 1 {
		t.Fatalf("echo response id %d result %s: %v", resp.ID, resp.Result, err)
	}
	if want := `{"method":"Fake.Echo","params":[{"Owner":"o","Body":"a\nb <i>"}],"id":1}` + "\n"; echo.Line != want {
		t.Errorf("server received %q, want %q", echo.Line, want)
	}

	// Messages that aren't JSON objects are dropped; the bridge carries on.
	h.send(`[1,2]`)
	h.send(`not json`)
	h.send(`{"method":"Fake.Echo","params":[{}],"id":2}`)
	if resp := decodeResponse(t, h.next()); resp.ID != 2 {
		t.Fatalf("want the response to id 2 next, got id %d", resp.ID)
	}

	// A response over maxVerbatimLine arrives as one contiguous chunk stream.
	h.send(`{"method":"Fake.Big","params":[{}],"id":3}`)
	var (
		joined strings.Builder
		stream int
		total  = -1
	)
	for seq := 0; seq != total; seq++ {
		frame := h.next()
		var msg struct {
			Chunk *chunk `json:"crs_chunk"`
		}
		if err := json.Unmarshal(frame, &msg); err != nil || msg.Chunk == nil {
			t.Fatalf("frame %d of the stream is not a chunk: %.200s (%v)", seq, frame, err)
		}
		c := msg.Chunk
		if seq == 0 {
			stream, total = c.Stream, c.Total
			if total < 2 {
				t.Fatalf("big response sent in %d chunk(s)", total)
			}
		}
		if c.Stream != stream || c.Seq != seq || c.Total != total {
			t.Fatalf("chunk out of order: stream %d seq %d total %d, want stream %d seq %d total %d",
				c.Stream, c.Seq, c.Total, stream, seq, total)
		}
		joined.WriteString(c.Data)
	}
	if joined.Len() <= maxVerbatimLine {
		t.Errorf("reassembled response is only %d bytes", joined.Len())
	}
	big := decodeResponse(t, []byte(joined.String()))
	var text struct{ Text string }
	if err := json.Unmarshal(big.Result, &text); err != nil || big.ID != 3 {
		t.Fatalf("big response id %d: %v", big.ID, err)
	}
	if text.Text != bigText {
		t.Error("reassembled text differs from what the server sent")
	}

	// Small responses still pass through after a chunked one.
	h.send(`{"method":"Fake.Echo","params":[{}],"id":4}`)
	if resp := decodeResponse(t, h.next()); resp.ID != 4 {
		t.Fatalf("want id 4, got %d", resp.ID)
	}

	// Chrome closing stdin stops the server through its own stdin.
	start := time.Now()
	h.chrome.Close()
	if code := h.wait(); code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
	if elapsed := time.Since(start); elapsed >= defaultShutdownGrace {
		t.Errorf("shutdown took %v; the server should exit on its own", elapsed)
	}
	if !h.serverSawEOF() {
		t.Error("the server never saw its stdin close")
	}
	if extra := h.rest(); len(extra) > 0 {
		t.Errorf("unexpected frames after shutdown: %q", extra)
	}

	logs := h.logs.String()
	for _, want := range []string{
		`request "Fake.Echo" id=1`,
		`request "Fake.Big" id=3`,
		"chunks (stream 1)",
		"skipping a 5-byte message from Chrome",
		"skipping a 36-byte line from the server",
		"Chrome closed the connection",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q", want)
		}
	}
	if strings.Contains(logs, "Owner") {
		t.Error("the log contains request params")
	}
}

func TestBridgeReportsServerExit(t *testing.T) {
	h := startHost(t, "echo", nil)
	if ev := h.nextEvent(); ev.Event != eventReady {
		t.Fatalf("first event %q, want ready", ev.Event)
	}
	h.send(`{"method":"Fake.Exit","params":[{}],"id":1}`)
	ev := h.nextEvent()
	if ev.Event != eventServerExited || ev.LogPath != testLogPath {
		t.Fatalf("got %+v, want server_exited", ev)
	}
	if !strings.HasPrefix(ev.Message, "exit status 3") || !strings.Contains(ev.Message, "fatal: the config is broken") {
		t.Errorf("message %q should give the status and the server's last words", ev.Message)
	}
	if code := h.wait(); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}

func TestBridgeStopsServerOnSignal(t *testing.T) {
	h := startHost(t, "echo", nil)
	h.nextEvent()
	h.cancel()
	if code := h.wait(); code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
	if !h.serverSawEOF() {
		t.Error("the server was not stopped through its stdin")
	}
}

func TestBridgeKillsServerThatIgnoresEOF(t *testing.T) {
	h := startHost(t, "ignore-eof", func(cfg *hostConfig) {
		cfg.ShutdownGrace = 200 * time.Millisecond
	})
	h.nextEvent()
	start := time.Now()
	h.chrome.Close()
	if code := h.wait(); code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("shutdown took %v", elapsed)
	}
	if !strings.Contains(h.logs.String(), "killing it") {
		t.Error("log doesn't record the kill")
	}
}

func TestBridgeReportsStartFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-server")
	h := startHost(t, "echo", func(cfg *hostConfig) { cfg.ServerPath = missing })
	ev := h.nextEvent()
	if ev.Event != eventError || !strings.Contains(ev.Message, missing) || ev.LogPath != testLogPath {
		t.Errorf("got %+v, want an error event naming %s", ev, missing)
	}
	if code := h.wait(); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}

func TestBridgeRejectsOversizedFrameFromChrome(t *testing.T) {
	h := startHost(t, "echo", nil)
	h.nextEvent()
	var header [4]byte
	binary.NativeEndian.PutUint32(header[:], maxIncomingFrame+1)
	if _, err := h.chrome.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	if code := h.wait(); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if !h.serverSawEOF() {
		t.Error("the server was not stopped through its stdin")
	}
}
