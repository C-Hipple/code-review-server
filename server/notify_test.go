package server

import (
	"bufio"
	"crs/config"
	"crs/workflows"
	"encoding/json"
	"fmt"
	"io"
	"net/rpc"
	"net/rpc/jsonrpc"
	"strings"
	"sync"
	"testing"
	"time"
)

// lines collects what is written to the returned writer, one line per
// message, the way a client reads the server's stdout.
func lines(t *testing.T) (io.Writer, <-chan string) {
	t.Helper()
	r, w := io.Pipe()
	out := make(chan string, 1024)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			out <- scanner.Text()
		}
	}()
	t.Cleanup(func() { w.Close() })
	return w, out
}

func nextLine(t *testing.T, out <-chan string, within time.Duration) string {
	t.Helper()
	select {
	case line, ok := <-out:
		if !ok {
			t.Fatal("output closed")
		}
		return line
	case <-time.After(within):
		t.Fatalf("nothing written within %s", within)
		return ""
	}
}

func expectSilence(t *testing.T, out <-chan string, d time.Duration) {
	t.Helper()
	select {
	case line := <-out:
		t.Fatalf("expected nothing written, got %s", line)
	case <-time.After(d):
	}
}

func parseHeartbeat(t *testing.T, line string) Heartbeat {
	t.Helper()
	var n map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &n); err != nil {
		t.Fatalf("not JSON: %s: %v", line, err)
	}
	if string(n["method"]) != `"`+HeartbeatMethod+`"` {
		t.Fatalf("method = %s, want %q", n["method"], HeartbeatMethod)
	}
	if id, ok := n["id"]; !ok || string(id) != "null" {
		t.Fatalf("a notification's id must be null: %s", line)
	}
	var params []json.RawMessage
	if err := json.Unmarshal(n["params"], &params); err != nil || len(params) != 1 {
		t.Fatalf("params must be a one-element array: %s", line)
	}
	var hb Heartbeat
	if err := json.Unmarshal(params[0], &hb); err != nil {
		t.Fatalf("params[0] is not a heartbeat: %s: %v", line, err)
	}
	return hb
}

func TestNotifierSendsHeartbeatsWhileSubscribed(t *testing.T) {
	w, out := lines(t)
	n := newNotifier(w)
	defer n.Unsubscribe()

	state := n.Subscribe(20 * time.Millisecond)
	if state.Seq != 0 {
		t.Errorf("Subscribe reply seq = %d before any heartbeat, want 0", state.Seq)
	}
	if _, err := time.Parse(time.RFC3339, state.StartedAt); err != nil {
		t.Errorf("started_at %q is not RFC 3339: %v", state.StartedAt, err)
	}

	for want := int64(1); want <= 3; want++ {
		hb := parseHeartbeat(t, nextLine(t, out, time.Second))
		if hb.Seq != want {
			t.Errorf("heartbeat seq = %d, want %d", hb.Seq, want)
		}
		if hb.StartedAt != state.StartedAt {
			t.Errorf("started_at = %q, want %q", hb.StartedAt, state.StartedAt)
		}
		if _, err := time.Parse(time.RFC3339, hb.Time); err != nil {
			t.Errorf("time %q is not RFC 3339: %v", hb.Time, err)
		}
	}
}

func TestNotifierIsSilentUnlessSubscribed(t *testing.T) {
	w, out := lines(t)
	n := newNotifier(w)

	if n.Notify("Server.Test", map[string]int{"x": 1}) {
		t.Error("Notify sent before Subscribe")
	}
	expectSilence(t, out, 50*time.Millisecond)

	n.Subscribe(10 * time.Millisecond)
	defer n.Unsubscribe()
	parseHeartbeat(t, nextLine(t, out, time.Second))
	n.Unsubscribe()
	// A heartbeat already written may still be in the pipe; nothing follows it.
	drain(out)
	expectSilence(t, out, 100*time.Millisecond)
	if n.Notify("Server.Test", nil) {
		t.Error("Notify sent after Unsubscribe")
	}
}

// drain discards whatever is already buffered.
func drain(out <-chan string) {
	for {
		select {
		case <-out:
		case <-time.After(20 * time.Millisecond):
			return
		}
	}
}

func TestNotifierNotifyWritesANotification(t *testing.T) {
	w, out := lines(t)
	n := newNotifier(w)
	n.Subscribe(time.Hour)
	defer n.Unsubscribe()

	if !n.Notify("Server.Test", map[string]string{"pr": "acme/widgets#42"}) {
		t.Fatal("Notify did not send while subscribed")
	}
	line := nextLine(t, out, time.Second)
	want := `{"method":"Server.Test","params":[{"pr":"acme/widgets#42"}],"id":null}`
	if line != want {
		t.Errorf("wrote %s\nwant  %s", line, want)
	}
}

func TestNotifierResubscribeReplacesTheInterval(t *testing.T) {
	w, out := lines(t)
	n := newNotifier(w)
	defer n.Unsubscribe()

	n.Subscribe(time.Hour)
	expectSilence(t, out, 50*time.Millisecond)

	// One loop at the new interval, not a second one beside the first: the
	// sequence has no gaps or repeats.
	n.Subscribe(10 * time.Millisecond)
	for want := int64(1); want <= 5; want++ {
		if hb := parseHeartbeat(t, nextLine(t, out, time.Second)); hb.Seq != want {
			t.Fatalf("heartbeat seq = %d, want %d", hb.Seq, want)
		}
	}
}

type failingWriter struct {
	mu     sync.Mutex
	writes int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	return 0, io.ErrClosedPipe
}

func TestNotifierStopsWhenTheClientCannotBeWritten(t *testing.T) {
	f := &failingWriter{}
	n := newNotifier(f)
	n.Subscribe(5 * time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	f.mu.Lock()
	writes := f.writes
	f.mu.Unlock()
	if writes != 1 {
		t.Errorf("tried to write %d times after the first write failed, want 1", writes)
	}
	if n.Notify("Server.Test", nil) {
		t.Error("still subscribed after a failed write")
	}
}

func TestHeartbeatInterval(t *testing.T) {
	tests := []struct {
		seconds int
		want    time.Duration
	}{
		{0, defaultHeartbeatInterval},
		{-3, defaultHeartbeatInterval},
		{1, time.Second},
		{30, 30 * time.Second},
		{3600, maxHeartbeatInterval},
	}
	for _, tt := range tests {
		if got := heartbeatInterval(tt.seconds); got != tt.want {
			t.Errorf("heartbeatInterval(%d) = %s, want %s", tt.seconds, got, tt.want)
		}
	}
}

func TestSubscribeRPCNeedsTheStdioConnection(t *testing.T) {
	saved := notifier
	notifier = nil
	defer func() { notifier = saved }()

	var reply SubscribeReply
	if err := (&RPCHandler{}).Subscribe(&SubscribeArgs{}, &reply); err == nil {
		t.Error("Subscribe succeeded with no connection to notify")
	}
	var unsub UnsubscribeReply
	if err := (&RPCHandler{}).Unsubscribe(&UnsubscribeArgs{}, &unsub); err != nil || !unsub.Okay {
		t.Errorf("Unsubscribe = %v, okay %v; want it to succeed", err, unsub.Okay)
	}
}

// Echo stands in for the RPCs that share the connection with notifications.
type Echo struct{}

func (Echo) Say(args *string, reply *string) error {
	*reply = *args
	return nil
}

// chunkedWriter passes each Write on in small pieces, as a writer that makes
// no promise about concurrent writes may.
type chunkedWriter struct {
	w io.Writer
}

func (c chunkedWriter) Write(p []byte) (int, error) {
	for i := 0; i < len(p); i += 512 {
		if _, err := c.w.Write(p[i:min(i+512, len(p))]); err != nil {
			return i, err
		}
	}
	return len(p), nil
}

// pipeConn is the server's stdio as one ReadWriteCloser.
type pipeConn struct {
	io.Reader
	io.Writer
}

func (pipeConn) Close() error { return nil }

// TestNotificationsShareTheConnectionWithReplies drives the real JSON-RPC
// codec: a client subscribes, then many large replies are written while
// heartbeats fire every millisecond. Every line must still be one whole
// message — a reply or a heartbeat — never the two spliced together.
func TestNotificationsShareTheConnectionWithReplies(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutW, out := lines(t)

	saved := notifier
	sw := &syncWriter{w: chunkedWriter{w: stdoutW}}
	notifier = newNotifier(sw)
	defer func() {
		notifier.Unsubscribe()
		notifier = saved
	}()

	srv := rpc.NewServer()
	if err := srv.Register(&RPCHandler{}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Register(Echo{}); err != nil {
		t.Fatal(err)
	}
	go srv.ServeCodec(jsonrpc.NewServerCodec(pipeConn{Reader: stdinR, Writer: sw}))
	defer stdinW.Close()

	send := func(id int, method string, params any) {
		req, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": []any{params}})
		if _, err := stdinW.Write(append(req, '\n')); err != nil {
			t.Fatal(err)
		}
	}

	send(1, "RPCHandler.Subscribe", SubscribeArgs{IntervalSeconds: 1})
	var reply struct {
		ID     int            `json:"id"`
		Result SubscribeReply `json:"result"`
		Error  any            `json:"error"`
	}
	if err := json.Unmarshal([]byte(nextLine(t, out, time.Second)), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.ID != 1 || reply.Error != nil || reply.Result.IntervalSeconds != 1 {
		t.Fatalf("Subscribe reply = %+v, want id 1 and a 1 s interval", reply)
	}

	// Faster than any client may ask for, to overlap with the replies.
	notifier.Subscribe(time.Millisecond)

	const calls = 200
	big := strings.Repeat("x", 64*1024)
	go func() {
		for i := range calls {
			send(100+i, "Echo.Say", fmt.Sprintf("%d:%s", i, big))
		}
	}()

	replies, heartbeats := 0, 0
	deadline := time.After(10 * time.Second)
	for replies < calls {
		var line string
		select {
		case line = <-out:
		case <-deadline:
			t.Fatalf("got %d of %d replies", replies, calls)
		}
		var msg struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Result string `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("a line is not one JSON message (%d bytes): %v", len(line), err)
		}
		if msg.Method == HeartbeatMethod {
			heartbeats++
			continue
		}
		if !strings.HasSuffix(msg.Result, big) {
			t.Fatalf("reply %v is not its call's echo", msg.ID)
		}
		replies++
	}
	if heartbeats == 0 {
		t.Error("no heartbeat was written while the replies were")
	}

	send(2, "RPCHandler.Unsubscribe", UnsubscribeArgs{})
	for {
		line := nextLine(t, out, time.Second)
		if strings.Contains(line, `"id":2`) {
			break
		}
	}
	expectSilence(t, out, 50*time.Millisecond)
}

func TestNotifyPRChangedSendsAPRUpdated(t *testing.T) {
	w, out := lines(t)
	saved := notifier
	notifier = newNotifier(w)
	t.Cleanup(func() {
		notifier.Unsubscribe()
		notifier = saved
	})
	config.SetC(config.Config{GithubUsername: "Me"})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	change := workflows.PRChange{
		Key:     workflows.PRKey{Owner: "acme", Repo: "widgets", Number: 42},
		Event:   "pull_request_review.submitted",
		Changes: []string{"reviews", "metadata"},
		HeadSHA: "abc123",
		Actor:   "rev",
		Summary: "rev approved",
	}
	// Nothing goes out before the client subscribes.
	NotifyPRChanged(change)
	expectSilence(t, out, 50*time.Millisecond)

	notifier.Subscribe(time.Hour)
	NotifyPRChanged(change)
	var n struct {
		Method string      `json:"method"`
		Params []PRUpdated `json:"params"`
		ID     any         `json:"id"`
	}
	line := nextLine(t, out, time.Second)
	if err := json.Unmarshal([]byte(line), &n); err != nil {
		t.Fatalf("not JSON: %s: %v", line, err)
	}
	if n.Method != PRUpdatedMethod || n.ID != nil || len(n.Params) != 1 {
		t.Fatalf("notification = %s", line)
	}
	got := n.Params[0]
	if got.Owner != "acme" || got.Repo != "widgets" || got.Number != 42 || got.Event != change.Event ||
		got.HeadSHA != "abc123" || got.Actor != "rev" || got.Summary != "rev approved" || got.Own || got.Time == "" ||
		strings.Join(got.Changes, ",") != "reviews,metadata" {
		t.Errorf("params = %+v", got)
	}

	// The user's own change says so, however the login is spelled.
	change.Actor = "me"
	NotifyPRChanged(change)
	if line := nextLine(t, out, time.Second); !strings.Contains(line, `"own":true`) {
		t.Errorf("the user's own change isn't marked own: %s", line)
	}
}

func TestPRUpdatedOwnNeedsAnActor(t *testing.T) {
	if prUpdated(workflows.PRChange{}, "", time.Now()).Own {
		t.Error("a change with no actor counted as the user's own when no login is configured")
	}
}
