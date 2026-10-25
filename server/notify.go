package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Server → client notifications.
//
// The connection is JSON-RPC 1.0 over stdio, which lets either peer send the
// other a notification: a request whose id is null, which gets no reply. A
// client that calls Subscribe gets them on the stdout its replies arrive on,
// and tells the two apart by the `method` a notification carries. A client
// that never subscribes never sees one, so a client that reads exactly one
// line per call (the TUI) is unaffected.
//
// The one notification so far is the heartbeat, sent every interval while a
// client is subscribed; Notifier.Notify is the seam the next event goes
// through.

// HeartbeatMethod is the method a heartbeat notification names.
const HeartbeatMethod = "Server.Heartbeat"

const (
	defaultHeartbeatInterval = 10 * time.Second
	minHeartbeatInterval     = time.Second
	maxHeartbeatInterval     = 5 * time.Minute
)

// syncWriter serializes writes to the connection, so a reply and a
// notification written at once can't splice. net/rpc writes each reply with a
// single Write (encoding/json's Encoder.Encode buffers the whole message), and
// a notification is written the same way, so holding the lock per Write keeps
// every line whole, whatever the writer underneath does with a large one.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// Heartbeat is a heartbeat notification's params, and Subscribe's reply.
type Heartbeat struct {
	// Seq counts the heartbeats sent since the server started; Subscribe's
	// reply carries the last one sent (0 before the first).
	Seq int64 `json:"seq"`
	// Time is the server's clock when the heartbeat was sent (RFC 3339).
	Time string `json:"time"`
	// StartedAt is when the server started (RFC 3339). It changes only when
	// a client is talking to a new server process.
	StartedAt string `json:"started_at"`
	// IntervalSeconds is how long until the next heartbeat. A client that
	// hears nothing for a few intervals can take the server for gone.
	IntervalSeconds int `json:"interval_seconds"`
}

// notification is a JSON-RPC 1.0 notification as it goes on the wire.
type notification struct {
	Method string `json:"method"`
	Params [1]any `json:"params"`
	ID     any    `json:"id"` // always null
}

// Notifier sends notifications to the client while it is subscribed.
type Notifier struct {
	out       io.Writer
	startedAt time.Time

	// mu guards the fields below, and is held across each notification's
	// write so none is sent after Unsubscribe returns.
	mu       sync.Mutex
	seq      int64
	interval time.Duration
	// stop ends the running heartbeat loop; nil while unsubscribed.
	stop chan struct{}
}

func newNotifier(out io.Writer) *Notifier {
	return &Notifier{out: out, startedAt: time.Now()}
}

// notifier is the notifier for the stdio connection RunServer serves; nil
// otherwise (a one-off command, or a test that doesn't set one).
var notifier *Notifier

// Subscribe starts heartbeats every interval, or changes the interval of the
// ones already running, and returns the heartbeat state as it stands. The
// first heartbeat follows one interval after the call.
func (n *Notifier) Subscribe(interval time.Duration) Heartbeat {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stop != nil {
		close(n.stop)
	}
	n.stop = make(chan struct{})
	n.interval = interval
	go n.beat(n.stop, interval)
	return n.heartbeatLocked(time.Now())
}

// Unsubscribe stops notifications. None is written after it returns.
func (n *Notifier) Unsubscribe() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.unsubscribeLocked()
}

func (n *Notifier) unsubscribeLocked() {
	if n.stop != nil {
		close(n.stop)
		n.stop = nil
	}
}

// Notify sends method to the client with payload as its params, if the client
// is subscribed. It reports whether it was sent.
func (n *Notifier) Notify(method string, payload any) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stop == nil {
		return false
	}
	return n.sendLocked(method, payload)
}

func (n *Notifier) beat(stop chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			n.mu.Lock()
			// A tick that raced a Subscribe or Unsubscribe belongs to a loop
			// that has been replaced or stopped.
			if n.stop != stop {
				n.mu.Unlock()
				return
			}
			n.seq++
			n.sendLocked(HeartbeatMethod, n.heartbeatLocked(now))
			n.mu.Unlock()
		}
	}
}

func (n *Notifier) heartbeatLocked(now time.Time) Heartbeat {
	return Heartbeat{
		Seq:             n.seq,
		Time:            now.Format(time.RFC3339),
		StartedAt:       n.startedAt.Format(time.RFC3339),
		IntervalSeconds: int(n.interval / time.Second),
	}
}

// sendLocked writes one notification. A failed write means the client is gone
// or can't be written to, so notifications stop rather than fail every tick.
func (n *Notifier) sendLocked(method string, payload any) bool {
	line, err := json.Marshal(notification{Method: method, Params: [1]any{payload}})
	if err != nil {
		slog.Error("Could not encode a notification", "method", method, "error", err)
		return false
	}
	if _, err := n.out.Write(append(line, '\n')); err != nil {
		slog.Warn("Could not write a notification; unsubscribing the client", "method", method, "error", err)
		n.unsubscribeLocked()
		return false
	}
	return true
}

// SubscribeArgs asks for server notifications.
type SubscribeArgs struct {
	// IntervalSeconds is how often to send a heartbeat: 0 for the default
	// (10 s), otherwise clamped to 1 s – 5 min.
	IntervalSeconds int `json:"IntervalSeconds"`
}

// SubscribeReply is the heartbeat state at the moment of subscribing, with
// the interval the server settled on.
type SubscribeReply struct {
	Heartbeat
}

// Subscribe turns on server notifications for this connection: a
// Server.Heartbeat every interval, and whatever events the server sends.
// Calling it again changes the interval.
func (h *RPCHandler) Subscribe(args *SubscribeArgs, reply *SubscribeReply) error {
	if notifier == nil {
		return fmt.Errorf("notifications are only sent over the server's stdio connection")
	}
	interval := heartbeatInterval(args.IntervalSeconds)
	reply.Heartbeat = notifier.Subscribe(interval)
	slog.Info("Client subscribed to notifications", "heartbeat_interval", interval)
	return nil
}

func heartbeatInterval(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultHeartbeatInterval
	}
	return min(max(time.Duration(seconds)*time.Second, minHeartbeatInterval), maxHeartbeatInterval)
}

type UnsubscribeArgs struct{}

type UnsubscribeReply struct {
	Okay bool `json:"okay"`
}

// Unsubscribe turns server notifications off; none arrives after its reply.
func (h *RPCHandler) Unsubscribe(args *UnsubscribeArgs, reply *UnsubscribeReply) error {
	if notifier != nil {
		notifier.Unsubscribe()
		slog.Info("Client unsubscribed from notifications")
	}
	reply.Okay = true
	return nil
}
