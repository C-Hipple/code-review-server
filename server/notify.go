package server

import (
	"crs/config"
	"crs/workflows"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
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
// There are two so far: the heartbeat, sent every interval while a client is
// subscribed, and Server.PRUpdated, sent when a GitHub webhook delivery has
// changed what the server holds for a PR. Notifier.Notify is the seam the next
// event goes through.

// HeartbeatMethod is the method a heartbeat notification names.
const HeartbeatMethod = "Server.Heartbeat"

// PRUpdatedMethod is the method a PR-updated notification names.
const PRUpdatedMethod = "Server.PRUpdated"

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

// PRUpdated is a PR-updated notification's params: a webhook delivery changed
// what the server holds for the PR, so a client showing it can fetch it again
// (GetPR, GetAllReviews) and find the change there.
type PRUpdated struct {
	// Owner, Repo and Number name the PR. Repo is the short name, as GetPR
	// takes it; Owner is spelled as GitHub spells it, which may differ in case
	// from the config.
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	// Event is the delivery's event and action
	// ("pull_request_review.submitted"), or "sections" when what changed is
	// the review list's rows for the PR rather than the PR.
	Event string `json:"event"`
	// Changes names what was rewritten: "head" (the head or base moved; the PR
	// was fetched again in full, diff included), "pr" (the PR's title,
	// description, state, labels, review requests), a cache field ("reviews",
	// "comments", "review_threads", "ci_status", "metadata", ...), or
	// "sections".
	Changes []string `json:"changes"`
	// HeadSHA is the PR's head as the delivery saw it; empty when the delivery
	// carries no PR.
	HeadSHA string `json:"head_sha,omitempty"`
	// Actor is who did it; empty for CI.
	Actor string `json:"actor,omitempty"`
	// Own is true when Actor is the configured GithubUsername: the change is
	// the user's own, made here or on GitHub.
	Own bool `json:"own"`
	// Summary says what happened in a few words ("alice approved", "Checks
	// failed: build"), without naming the PR; empty for a change not worth
	// announcing (a check starting, a row rewritten).
	Summary string `json:"summary,omitempty"`
	// Time is the server's clock when it was sent (RFC 3339).
	Time string `json:"time"`
}

// NotifyPRChanged tells a subscribed client that a webhook delivery changed a
// PR. It is the hook the workflow layer is wired to (see
// workflows.SetPRChangedHook).
func NotifyPRChanged(c workflows.PRChange) {
	if notifier == nil {
		return
	}
	notifier.Notify(PRUpdatedMethod, prUpdated(c, config.C().GithubUsername, time.Now()))
}

func prUpdated(c workflows.PRChange, me string, now time.Time) PRUpdated {
	return PRUpdated{
		Owner:   c.Key.Owner,
		Repo:    c.Key.Repo,
		Number:  c.Key.Number,
		Event:   c.Event,
		Changes: c.Changes,
		HeadSHA: c.HeadSHA,
		Actor:   c.Actor,
		Own:     c.Actor != "" && strings.EqualFold(c.Actor, me),
		Summary: c.Summary,
		Time:    now.Format(time.RFC3339),
	}
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
// Server.Heartbeat every interval, and whatever events the server sends — a
// Server.PRUpdated for each PR a webhook delivery changes.
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
