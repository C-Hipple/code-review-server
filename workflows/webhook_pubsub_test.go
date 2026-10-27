package workflows

import (
	"context"
	"crs/config"
	"crs/database"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
	"golang.org/x/oauth2"
)

const whSubscription = "projects/acme-ci/subscriptions/crs-github-events"

// fakePubSub stands in for Pub/Sub's REST API, as the emulator would, and for
// Google's token endpoint. A pull takes the next batch queued with publish,
// or, with none, answers with no messages after a moment, as Pub/Sub does
// once it stops waiting. While failWith is set it answers with that error
// instead, and while hang is set it never answers. The token endpoint issues
// an access token for a refresh token in goodTokens and refuses any other.
// Every request is recorded.
type fakePubSub struct {
	t       *testing.T
	url     string
	batches chan []map[string]any

	mu         sync.Mutex
	failWith   *pubsubError
	hang       bool
	held       int           // how many of the next pulls to hold
	holdFor    time.Duration // how long, or until the client gives up if 0
	goodTokens map[string]bool
	requests   []fakeRequest
}

// fakeRequest is one request the fake answered.
type fakeRequest struct {
	method string // the API method, or "token"
	header http.Header
	body   map[string]any
}

func newFakePubSub(t *testing.T) *fakePubSub {
	t.Helper()
	f := &fakePubSub{t: t, batches: make(chan []map[string]any, 8), goodTokens: map[string]bool{}}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	f.url = server.URL
	t.Setenv(pubsubEmulatorEnv, strings.TrimPrefix(server.URL, "http://"))
	return f
}

// withCredentials has the subscriber talk to the fake as it would to Google
// — at pubsubEndpoint, with an access token — rather than as an emulator.
func (f *fakePubSub) withCredentials(t *testing.T) {
	t.Helper()
	old := pubsubEndpoint
	pubsubEndpoint = f.url + "/v1/"
	t.Cleanup(func() { pubsubEndpoint = old })
	t.Setenv(pubsubEmulatorEnv, "")
}

func (f *fakePubSub) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		f.serveToken(w, r)
		return
	}
	path, method, _ := strings.Cut(r.URL.Path, ":")
	if path != "/v1/"+whSubscription {
		http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND","message":"Resource not found"}}`, http.StatusNotFound)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.requests = append(f.requests, fakeRequest{method: method, header: r.Header.Clone(), body: body})
	failWith, hang := f.failWith, f.hang
	var hold time.Duration
	if method == "pull" && f.held > 0 {
		f.held--
		hang, hold = f.holdFor == 0, f.holdFor
	}
	f.mu.Unlock()

	if method != "pull" {
		w.Write([]byte(`{}`))
		return
	}
	if n, _ := body["maxMessages"].(float64); n <= 0 {
		http.Error(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"max_messages must be positive"}}`, http.StatusBadRequest)
		return
	}
	switch {
	case hang:
		<-r.Context().Done()
		return
	case hold > 0:
		select {
		case <-time.After(hold):
			w.Write([]byte(`{}`))
		case <-r.Context().Done():
		}
		return
	case failWith != nil:
		w.WriteHeader(failWith.HTTPStatus)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code": failWith.HTTPStatus, "status": failWith.Status, "message": failWith.Message,
		}})
		return
	}
	select {
	case batch := <-f.batches:
		json.NewEncoder(w).Encode(map[string]any{"receivedMessages": batch})
	case <-time.After(20 * time.Millisecond):
		w.Write([]byte(`{}`))
	case <-r.Context().Done():
	}
}

// serveToken answers a refresh-token grant, as Google's token endpoint does.
func (f *fakePubSub) serveToken(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	refresh := r.PostForm.Get("refresh_token")
	f.mu.Lock()
	f.requests = append(f.requests, fakeRequest{method: "token", header: r.Header.Clone(), body: map[string]any{"refresh_token": refresh}})
	good := f.goodTokens[refresh]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !good {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"reauth related error (invalid_rapt)"}`))
		return
	}
	fmt.Fprintf(w, `{"access_token":"access-%s","token_type":"Bearer","expires_in":3600}`, refresh)
}

// publish queues a batch of messages for the next pull to return.
func (f *fakePubSub) publish(msgs ...map[string]any) {
	f.batches <- msgs
}

func (f *fakePubSub) fail(err *pubsubError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWith = err
}

func (f *fakePubSub) setHang(hang bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hang = hang
}

// holdPulls holds the next n pulls for d before answering them with nothing,
// or with d zero until the client gives up on them.
func (f *fakePubSub) holdPulls(n int, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held, f.holdFor = n, d
}

func (f *fakePubSub) allowToken(refresh string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goodTokens[refresh] = true
}

// calls returns the requests made to method so far.
func (f *fakePubSub) calls(method string) []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var calls []fakeRequest
	for _, req := range f.requests {
		if req.method == method {
			calls = append(calls, req)
		}
	}
	return calls
}

// ackIDs returns the IDs every call to method — acknowledge or
// modifyAckDeadline — named.
func (f *fakePubSub) ackIDs(method string) []string {
	var ids []string
	for _, req := range f.calls(method) {
		for _, id := range req.body["ackIds"].([]any) {
			ids = append(ids, id.(string))
		}
	}
	return ids
}

// pubsubMsg builds a received message the way the API encodes one.
func pubsubMsg(ackID, eventType, deliveryID string, published time.Time, data []byte) map[string]any {
	attrs := map[string]string{}
	if eventType != "" {
		attrs["event_type"] = eventType
	}
	if deliveryID != "" {
		attrs["delivery_id"] = deliveryID
	}
	return map[string]any{
		"ackId": ackID,
		"message": map[string]any{
			"data":        data, // encoding/json writes []byte as base64, as the API does
			"attributes":  attrs,
			"messageId":   "msg-" + ackID,
			"publishTime": published.UTC().Format(time.RFC3339Nano),
		},
	}
}

// newPubSubTestEnv is newWebhookTestEnv with a queue of the test's own and a
// fake Pub/Sub to pull from. The worker is started first: it takes deliveries
// from the queue that is current when it starts, which mustn't be the test's.
func newPubSubTestEnv(t *testing.T) (chan webhookDelivery, *fakePubSub) {
	t.Helper()
	newWebhookTestEnv(t)
	startWebhookWorker()
	takeWebhookActivity()
	t.Cleanup(func() {
		lastFullRefresh.mu.Lock()
		lastFullRefresh.at = time.Time{}
		lastFullRefresh.mu.Unlock()
		activeCoverage.Store(nil)
	})
	return withTestQueue(t), newFakePubSub(t)
}

// withFastPubSub shortens the subscriber's timings for the test. Call it
// before withPubSub, so they are put back only once the subscriber stops.
func withFastPubSub(t *testing.T, downAfter, pullTimeout time.Duration) {
	t.Helper()
	oldDown, oldPull, oldMin, oldMax := pubsubDownAfter, pubsubPullTimeout, pubsubMinBackoff, pubsubMaxBackoff
	pubsubDownAfter, pubsubPullTimeout, pubsubMinBackoff, pubsubMaxBackoff = downAfter, pullTimeout, 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		pubsubDownAfter, pubsubPullTimeout, pubsubMinBackoff, pubsubMaxBackoff = oldDown, oldPull, oldMin, oldMax
	})
}

// withPubSub configures webhooks to come from the fake's subscription, with
// whatever else edit sets, and starts pulling from it, stopping it — and
// waiting for it to stop — when the test ends.
func withPubSub(t *testing.T, edit ...func(*config.PubSubSettings)) *pubsubRun {
	t.Helper()
	cfg := config.C()
	cfg.Webhooks = config.WebhookSettings{
		Repos:  []string{whOwner + "/" + whRepo},
		PubSub: config.PubSubSettings{Subscription: whSubscription},
	}
	for _, e := range edit {
		e(&cfg.Webhooks.PubSub)
	}
	config.SetC(cfg)
	syncPubSubSubscriber(cfg)
	run := currentPubSubRun()
	if run == nil {
		t.Fatal("no subscriber started")
	}
	t.Cleanup(func() {
		syncPubSubSubscriber(config.Config{})
		waitStopped(t, run)
	})
	return run
}

func currentPubSubRun() *pubsubRun {
	pubsubSource.mu.Lock()
	defer pubsubSource.mu.Unlock()
	return pubsubSource.run
}

func waitStopped(t *testing.T, run *pubsubRun) {
	t.Helper()
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
		t.Error("subscriber still pulling after it was stopped")
	}
}

// eventually waits for cond, failing the test if it doesn't come true soon.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func receiving() bool {
	_, ok := pubsubReceivingSince()
	return ok
}

func reviewPayload(t *testing.T, reviewID int64) []byte {
	t.Helper()
	payload, err := json.Marshal(&github.PullRequestReviewEvent{
		Action:      github.Ptr("submitted"),
		Review:      &github.PullRequestReview{ID: github.Ptr(reviewID), User: &github.User{Login: github.Ptr("rev")}, State: github.Ptr("approved")},
		PullRequest: whPR(1, "head1"),
		Repo:        whRepository(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// queued takes n deliveries off the queue, failing the test if they don't
// come.
func queued(t *testing.T, queue chan webhookDelivery, n int) []webhookDelivery {
	t.Helper()
	var got []webhookDelivery
	for len(got) < n {
		select {
		case d := <-queue:
			got = append(got, d)
		case <-time.After(5 * time.Second):
			t.Fatalf("queued %d deliveries, want %d", len(got), n)
		}
	}
	return got
}

func deliveryIDs(ds []webhookDelivery) []string {
	var ids []string
	for _, d := range ds {
		ids = append(ids, d.id)
	}
	return ids
}

func sameIDs(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// A batch as a subscription might hand it over: the backlog from before the
// subscriber came up, a delivery Pub/Sub delivers twice, messages that aren't
// deliveries we can apply, and one that is. Only that one is queued; every
// message is acknowledged, since redelivering none of them would change what
// happens to it.
func TestPubSubQueuesOnlyCurrentNewDeliveries(t *testing.T) {
	queue, fake := newPubSubTestEnv(t)
	start := time.Now()
	run := withPubSub(t)

	eventually(t, "the first pull", receiving)
	since, _ := webhooksListeningSince()
	if since.Before(start) {
		t.Errorf("receiving since %v, before the subscriber started at %v", since, start)
	}
	if run.settings.eventTypeAttr != config.DefaultPubSubEventTypeAttribute {
		t.Errorf("event type attribute = %q", run.settings.eventTypeAttr)
	}

	now := time.Now()
	fake.publish(
		pubsubMsg("dup", "pull_request_review", "d-2", now.Add(time.Second), reviewPayload(t, 2)),
		pubsubMsg("new", "pull_request_review", "d-2", now, reviewPayload(t, 2)),
		pubsubMsg("old", "pull_request_review", "d-1", since.Add(-time.Hour), reviewPayload(t, 1)),
		pubsubMsg("no-event", "", "d-3", now, reviewPayload(t, 3)),
		pubsubMsg("unhandled", "workflow_job", "d-4", now, []byte(`{"action":"queued"}`)),
		pubsubMsg("ping", "ping", "d-5", now, []byte(`{"zen":"hi"}`)),
		pubsubMsg("malformed", "pull_request_review", "d-6", now, []byte(`{`)),
	)
	d := queued(t, queue, 1)[0]
	if d.eventType != "pull_request_review" || d.id != "d-2" {
		t.Errorf("queued %s %q, want the new review", d.eventType, d.id)
	}
	if e, ok := d.event.(*github.PullRequestReviewEvent); !ok || e.GetReview().GetID() != 2 {
		t.Errorf("queued event = %#v", d.event)
	}

	want := []string{"dup", "new", "old", "no-event", "unhandled", "ping", "malformed"}
	eventually(t, "the batch to be acknowledged", func() bool { return len(fake.ackIDs("acknowledge")) == len(want) })
	if got := fake.ackIDs("acknowledge"); !sameIDs(got, want) {
		t.Errorf("acknowledged %v, want %v", got, want)
	}
	select {
	case d := <-queue:
		t.Errorf("also queued %s %q", d.eventType, d.id)
	default:
	}
	activity := takeWebhookActivity()
	if activity.Ignored != 2 || activity.Rejected != 2 {
		t.Errorf("ignored %d (want the old and the repeated delivery), rejected %d (want the eventless and the malformed one)",
			activity.Ignored, activity.Rejected)
	}
}

// A relay that sends no delivery ID still has each delivery applied: the
// message's own ID stands in, so they aren't taken for repeats of each other.
func TestPubSubWithoutDeliveryIDs(t *testing.T) {
	queue, fake := newPubSubTestEnv(t)
	withPubSub(t)
	eventually(t, "the first pull", receiving)

	now := time.Now()
	fake.publish(
		pubsubMsg("a", "pull_request_review", "", now, reviewPayload(t, 1)),
		pubsubMsg("b", "pull_request_review", "", now, reviewPayload(t, 2)),
	)
	if got := deliveryIDs(queued(t, queue, 2)); !slices.Equal(got, []string{"pubsub-msg-a", "pubsub-msg-b"}) {
		t.Errorf("queued %v", got)
	}
}

// A subscription that can't be pulled from — missing, or the credentials not
// allowed to — leaves every PR polled, and the first pull that succeeds after
// is what deliveries count from.
func TestPubSubCountsAsReceivingOnlyWhilePullsSucceed(t *testing.T) {
	_, fake := newPubSubTestEnv(t)
	withFastPubSub(t, pubsubDownAfter, pubsubPullTimeout)
	fake.fail(&pubsubError{HTTPStatus: http.StatusForbidden, Status: "PERMISSION_DENIED", Message: "User not authorized to perform this action."})
	withPubSub(t)

	eventually(t, "a refused pull", func() bool { return len(fake.calls("pull")) > 0 })
	if receiving() {
		t.Fatal("receiving from a subscription that refuses every pull")
	}
	if mode := beginWebhookCycle(config.C(), time.Now()); mode != database.CycleModePolling {
		t.Errorf("mode = %q while pulls are refused, want polling", mode)
	}

	fixed := time.Now()
	fake.fail(nil)
	eventually(t, "a pull to succeed", receiving)
	if since, _ := webhooksListeningSince(); since.Before(fixed) {
		t.Errorf("receiving since %v, before pulls were allowed at %v", since, fixed)
	}
	if mode := beginWebhookCycle(config.C(), time.Now()); mode != database.CycleModeFullRefresh {
		t.Errorf("mode = %q for the first cycle after pulls succeeded, want a full refresh", mode)
	}
}

// startTrusted gets the subscriber receiving and a full refresh behind it, so
// webhooks are trusted for the test's repository, and returns since when.
func startTrusted(t *testing.T) time.Time {
	t.Helper()
	eventually(t, "the first pull", receiving)
	since, _ := pubsubReceivingSince()
	cfg := config.C()
	if mode := beginWebhookCycle(cfg, time.Now()); mode != database.CycleModeFullRefresh {
		t.Fatalf("mode = %q for the first cycle, want a full refresh", mode)
	}
	endWebhookCycle(cfg, time.Now(), database.CycleModeFullRefresh, true)
	if !webhookHandles(whKey(1)) {
		t.Fatal("webhooks not trusted after a full refresh")
	}
	return since
}

// Losing access while receiving stops it at once: every PR is polled again,
// and once pulls succeed again a full refresh catches up before webhooks are
// trusted.
func TestPubSubGoesDownWhenPullsAreRefused(t *testing.T) {
	_, fake := newPubSubTestEnv(t)
	withFastPubSub(t, pubsubDownAfter, pubsubPullTimeout)
	withPubSub(t)
	firstSince := startTrusted(t)

	fake.fail(&pubsubError{HTTPStatus: http.StatusForbidden, Status: "PERMISSION_DENIED", Message: "User not authorized to perform this action."})
	eventually(t, "the refused pull to stop it", func() bool { return !receiving() })
	if activeCoverage.Load() != nil {
		t.Error("webhooks still trusted after pulls were refused")
	}
	if mode := beginWebhookCycle(config.C(), time.Now()); mode != database.CycleModePolling {
		t.Errorf("mode = %q while pulls are refused, want polling", mode)
	}

	fake.fail(nil)
	eventually(t, "pulls to succeed again", receiving)
	if since, _ := pubsubReceivingSince(); !since.After(firstSince) {
		t.Errorf("receiving since %v again, want later than %v", since, firstSince)
	}
	if mode := beginWebhookCycle(config.C(), time.Now()); mode != database.CycleModeFullRefresh {
		t.Errorf("mode = %q once pulls succeed again, want a full refresh", mode)
	}
}

// Pulls that go unanswered — the network gone quiet — stop it counting as
// receiving once they have been failing for pubsubDownAfter: each fails only
// when it runs out of time, so that is within pubsubDownAfter and two pulls'
// time of the first going unanswered.
func TestPubSubGoesDownWhenPullsHang(t *testing.T) {
	_, fake := newPubSubTestEnv(t)
	withFastPubSub(t, 200*time.Millisecond, 150*time.Millisecond)
	withPubSub(t)
	startTrusted(t)

	hung := time.Now()
	fake.setHang(true)
	eventually(t, "hanging pulls to stop it", func() bool { return !receiving() })
	if took, within := time.Since(hung), pubsubDownAfter+3*pubsubPullTimeout; took > within {
		t.Errorf("went down %v after pulls began hanging, want within %v", took, within)
	}
	if activeCoverage.Load() != nil {
		t.Error("webhooks still trusted with nothing pulled")
	}
	fake.setHang(false)
	eventually(t, "pulls to succeed again", receiving)
}

// One pull going unanswered — a connection that died with a network change —
// costs only the wait: the next pull picks up where it left off, nothing is
// dropped, and webhooks stay trusted. The timings keep production's ratio, a
// pull's time limit as long as pubsubDownAfter.
func TestPubSubRidesOutOneUnansweredPull(t *testing.T) {
	queue, fake := newPubSubTestEnv(t)
	withFastPubSub(t, 200*time.Millisecond, 200*time.Millisecond)
	withPubSub(t)
	since := startTrusted(t)

	fake.holdPulls(1, 0)
	eventually(t, "the unanswered pull to be retried", func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return fake.held == 0
	})
	fake.publish(pubsubMsg("m", "pull_request_review", "d-1", time.Now(), reviewPayload(t, 1)))
	if got := deliveryIDs(queued(t, queue, 1)); !slices.Equal(got, []string{"d-1"}) {
		t.Errorf("queued %v", got)
	}
	if again, ok := pubsubReceivingSince(); !ok || !again.Equal(since) {
		t.Errorf("receiving = %v since %v, want still since %v", ok, again, since)
	}
	if !webhookHandles(whKey(1)) {
		t.Error("webhooks stopped being trusted over one unanswered pull")
	}
}

// A cycle that starts before the first pull comes back — a quiet
// subscription's pull is held for up to a minute — runs as a polling one, but
// it started after the subscriber did, which is what deliveries count from:
// it is the catch-up refresh, and the next cycle can leave PRs to webhooks.
func TestPubSubFirstCycleCountsAsTheCatchUp(t *testing.T) {
	_, fake := newPubSubTestEnv(t)
	fake.holdPulls(1, 300*time.Millisecond)
	withPubSub(t)

	cfg := config.C()
	start := time.Now()
	if mode := beginWebhookCycle(cfg, start); mode != database.CycleModePolling {
		t.Fatalf("mode = %q before the first pull came back, want polling", mode)
	}
	eventually(t, "the first pull", receiving)
	endWebhookCycle(cfg, start, database.CycleModePolling, true)
	if mode := beginWebhookCycle(cfg, time.Now()); mode != database.CycleModeWebhook {
		t.Errorf("mode = %q after the first complete cycle, want webhook", mode)
	}
}

// A laptop that slept comes back to what was published meanwhile. With no
// failed pull to say so — the monotonic clock stood still — the gap since the
// last pull does: it is dropped like a restart's backlog, and the next cycle
// refreshes every PR.
func TestPubSubTreatsASleepAsAnOutage(t *testing.T) {
	queue, fake := newPubSubTestEnv(t)
	var slept atomic.Int64
	oldNow := pubsubNow
	pubsubNow = func() time.Time { return time.Now().Round(0).Add(time.Duration(slept.Load())) }
	t.Cleanup(func() { pubsubNow = oldNow })
	withPubSub(t)
	firstSince := startTrusted(t)

	slept.Store(int64(time.Hour))
	eventually(t, "the gap to be noticed", func() bool {
		since, ok := pubsubReceivingSince()
		return ok && since.After(firstSince.Add(30*time.Minute))
	})
	if activeCoverage.Load() != nil {
		t.Error("webhooks still trusted after the gap")
	}
	if mode := beginWebhookCycle(config.C(), time.Now()); mode != database.CycleModeFullRefresh {
		t.Errorf("mode = %q after the gap, want a full refresh", mode)
	}

	// Published during the sleep, by the real clock: dropped.
	fake.publish(pubsubMsg("asleep", "pull_request_review", "d-1", time.Now(), reviewPayload(t, 1)))
	eventually(t, "the message to be acknowledged", func() bool { return len(fake.ackIDs("acknowledge")) == 1 })
	select {
	case d := <-queue:
		t.Errorf("queued %q, published while asleep", d.id)
	default:
	}
}

// When deliveries come faster than they're applied, the subscriber waits for
// room rather than dropping or handing them back, and queues them in the
// order they were published whatever order they were pulled in. Nothing is
// acknowledged until it is queued.
func TestPubSubWaitsForRoomInTheQueue(t *testing.T) {
	queue, fake := newPubSubTestEnv(t)
	withPubSub(t)
	eventually(t, "the first pull", receiving)

	fake.publish(newestFirst(t, 6)...)
	eventually(t, "the queue to fill", func() bool { return len(queue) == cap(queue) })
	time.Sleep(50 * time.Millisecond)
	if acked := fake.ackIDs("acknowledge"); len(acked) != 0 {
		t.Fatalf("acknowledged %v before they were queued", acked)
	}
	got := deliveryIDs(queued(t, queue, 6))
	if want := []string{"d-0", "d-1", "d-2", "d-3", "d-4", "d-5"}; !slices.Equal(got, want) {
		t.Errorf("queued %v, want publish order %v", got, want)
	}
	eventually(t, "all six to be acknowledged", func() bool { return len(fake.ackIDs("acknowledge")) == 6 })
}

// Stopped while waiting for room, the subscriber acknowledges what it queued
// and hands back what it couldn't, for whatever pulls next.
func TestPubSubStoppedWhileWaitingHandsBackTheRest(t *testing.T) {
	queue, fake := newPubSubTestEnv(t)
	run := withPubSub(t)
	eventually(t, "the first pull", receiving)

	fake.publish(newestFirst(t, 6)...)
	eventually(t, "the queue to fill", func() bool { return len(queue) == cap(queue) })
	syncPubSubSubscriber(config.Config{})
	waitStopped(t, run)
	if acked := fake.ackIDs("acknowledge"); !sameIDs(acked, []string{"m0", "m1", "m2", "m3"}) {
		t.Errorf("acknowledged %v, want the four queued", acked)
	}
	if released := fake.ackIDs("modifyAckDeadline"); !sameIDs(released, []string{"m4", "m5"}) {
		t.Errorf("handed back %v, want the two left over", released)
	}
	for _, req := range fake.calls("modifyAckDeadline") {
		if req.body["ackDeadlineSeconds"] != float64(0) {
			t.Errorf("handed back with ackDeadlineSeconds %v, want 0", req.body["ackDeadlineSeconds"])
		}
	}
}

// newestFirst builds n deliveries, m<i> carrying d-<i>, published in order
// but listed newest first.
func newestFirst(t *testing.T, n int) []map[string]any {
	t.Helper()
	now := time.Now()
	var msgs []map[string]any
	for i := n - 1; i >= 0; i-- {
		msgs = append(msgs, pubsubMsg(fmt.Sprintf("m%d", i), "pull_request_review", fmt.Sprintf("d-%d", i),
			now.Add(time.Duration(i)*time.Millisecond), reviewPayload(t, int64(i+1))))
	}
	return msgs
}

// Pulling with credentials, as from Google rather than the emulator: each
// call carries an access token and the quota project. A token Google refuses
// — an expired login — leaves it polling, and the credentials are read again
// on each try, so a fresh login is picked up without a restart.
func TestPubSubPullsWithCredentialsAndRereadsThem(t *testing.T) {
	_, fake := newPubSubTestEnv(t)
	fake.withCredentials(t)
	withFastPubSub(t, pubsubDownAfter, pubsubPullTimeout)
	t.Setenv(pubsubQuotaProjectEnv, "")
	creds := filepath.Join(t.TempDir(), "adc.json")
	login := func(refresh string) {
		t.Helper()
		data := fmt.Sprintf(`{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":%q,"token_uri":%q,"quota_project_id":"acme-ci"}`,
			refresh, fake.url+"/token")
		if err := os.WriteFile(creds, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	login("expired")
	fake.allowToken("fresh")
	withPubSub(t, func(p *config.PubSubSettings) { p.CredentialsFile = creds })

	eventually(t, "the refused token", func() bool { return len(fake.calls("token")) > 0 })
	if receiving() || len(fake.calls("pull")) > 0 {
		t.Fatal("pulled without a token")
	}
	login("fresh")
	eventually(t, "pulls with the fresh login", receiving)

	pull := fake.calls("pull")[0]
	for header, want := range map[string]string{
		"Authorization":       "Bearer access-fresh",
		"X-Goog-User-Project": "acme-ci",
		"X-Server-Timeout":    "60",
	} {
		if got := pull.header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestPubSubClientCalls(t *testing.T) {
	fake := newFakePubSub(t)
	client, err := newPubSubClient(context.Background(), pubsubSettings{subscription: whSubscription, emulator: os.Getenv(pubsubEmulatorEnv)})
	if err != nil {
		t.Fatal(err)
	}

	published := time.Date(2026, 10, 26, 9, 30, 0, 123456789, time.UTC)
	fake.publish(pubsubMsg("a1", "push", "d-1", published, []byte(`{"ref":"refs/heads/main"}`)))
	msgs, err := client.pull(context.Background(), 10)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("pulled %d messages", len(msgs))
	}
	m := msgs[0]
	if m.AckID != "a1" || string(m.Message.Data) != `{"ref":"refs/heads/main"}` || m.Message.Attributes["event_type"] != "push" ||
		!m.Message.PublishTime.Equal(published) || m.Message.MessageID != "msg-a1" {
		t.Errorf("message = %+v (data %q)", m, m.Message.Data)
	}
	if err := client.acknowledge(context.Background(), []string{"a1"}); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if err := client.release(context.Background(), []string{"a2"}); err != nil {
		t.Fatalf("release: %v", err)
	}
	if acked, released := fake.ackIDs("acknowledge"), fake.ackIDs("modifyAckDeadline"); !slices.Equal(acked, []string{"a1"}) || !slices.Equal(released, []string{"a2"}) {
		t.Errorf("acked %v, released %v", acked, released)
	}

	// Pub/Sub ending a pull at its X-Server-Timeout is no failure: nothing came.
	fake.fail(&pubsubError{HTTPStatus: http.StatusGatewayTimeout, Status: "DEADLINE_EXCEEDED", Message: "Deadline exceeded"})
	if msgs, err := client.pull(context.Background(), 10); err != nil || len(msgs) != 0 {
		t.Errorf("pull ended by the server = %v, %v; want nothing and no error", msgs, err)
	}

	// Pub/Sub's error envelope, and which errors retrying can't fix.
	fake.fail(&pubsubError{HTTPStatus: http.StatusNotFound, Status: "NOT_FOUND", Message: "Resource not found (resource=crs-github-events)."})
	_, err = client.pull(context.Background(), 10)
	var apiErr *pubsubError
	if !errors.As(err, &apiErr) || apiErr.Status != "NOT_FOUND" || !strings.Contains(apiErr.Message, "Resource not found") {
		t.Errorf("pull error = %v", err)
	}
	if !permanentPubSubError(err) {
		t.Error("a missing subscription counted as worth retrying")
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		if (&pubsubError{HTTPStatus: status}).permanent() {
			t.Errorf("HTTP %d counted as permanent", status)
		}
	}
	if permanentPubSubError(context.DeadlineExceeded) || credentialsError(context.DeadlineExceeded) {
		t.Error("a timed-out pull counted as permanent, or as the credentials' fault")
	}
	// A token the credentials can't get, through the client as a pull sees it.
	tokenErr := func(err error) error {
		client := &pubsubClient{
			http: &http.Client{Transport: &oauth2.Transport{Source: markedTokenSource{failingTokenSource{err}}}},
			base: fake.url + "/v1/" + whSubscription,
		}
		_, err = client.pull(context.Background(), 1)
		return err
	}
	refused := func(status int) error {
		return tokenErr(&oauth2.RetrieveError{Response: &http.Response{StatusCode: status}})
	}
	if err := refused(http.StatusBadRequest); !permanentPubSubError(err) || !credentialsError(err) {
		t.Errorf("a refused token (invalid_grant) counted as worth retrying as it is: %v", err)
	}
	if err := refused(http.StatusServiceUnavailable); permanentPubSubError(err) || !credentialsError(err) {
		t.Errorf("the token endpoint being down: permanent = %v, credentials = %v", permanentPubSubError(err), credentialsError(err))
	}
	// Impersonated credentials report a refusal only as text, which is still
	// worth reading the credentials again for.
	if err := tokenErr(errors.New("oauth2/google: unable to generate access token: status code 400")); !credentialsError(err) {
		t.Errorf("an impersonation failure not taken as the credentials': %v", err)
	}
}

// failingTokenSource never has a token.
type failingTokenSource struct{ err error }

func (s failingTokenSource) Token() (*oauth2.Token, error) { return nil, s.err }

func TestSubscriptionPathEscapesTheName(t *testing.T) {
	for name, want := range map[string]string{
		whSubscription:                        whSubscription,
		"projects/p/subscriptions/crs%events": "projects/p/subscriptions/crs%25events",
		"projects/p/subscriptions/a+b~c.d_e":  "projects/p/subscriptions/a+b~c.d_e",
	} {
		if got := subscriptionPath(name); got != want {
			t.Errorf("subscriptionPath(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSyncPubSubSubscriber(t *testing.T) {
	newPubSubTestEnv(t)
	run := withPubSub(t)
	cfg := config.C()

	// The same config leaves the subscriber be.
	syncWebhookListener(cfg)
	if currentPubSubRun() != run {
		t.Error("subscriber restarted for an unchanged config")
	}

	// A ListenAddress beside a subscription listens on nothing.
	cfg.Webhooks.ListenAddress = "127.0.0.1:0"
	cfg.Webhooks.Secret = whSecret
	syncWebhookListener(cfg)
	webhookListener.mu.Lock()
	listening := webhookListener.server != nil
	webhookListener.mu.Unlock()
	if listening {
		t.Error("listening for HTTP deliveries while pulling them from Pub/Sub")
	}

	// A new attribute name starts a new subscriber, and the old one stops.
	cfg.Webhooks.PubSub.EventTypeAttribute = "X-GitHub-Event"
	syncWebhookListener(cfg)
	next := currentPubSubRun()
	if next == run || next == nil || next.settings.eventTypeAttr != "X-GitHub-Event" {
		t.Fatalf("subscriber after the change = %+v", next)
	}
	waitStopped(t, run)

	// Dropping the subscription for an address switches to listening.
	cfg.Webhooks.PubSub = config.PubSubSettings{}
	syncWebhookListener(cfg)
	t.Cleanup(func() { syncWebhookListener(config.Config{}) })
	webhookListener.mu.Lock()
	listening = webhookListener.server != nil
	webhookListener.mu.Unlock()
	if currentPubSubRun() != nil || !listening {
		t.Errorf("subscriber stopped = %v, listening = %v, want both", currentPubSubRun() == nil, listening)
	}
	waitStopped(t, next)
}

func TestPubSubCredentialsFile(t *testing.T) {
	t.Setenv(pubsubQuotaProjectEnv, "")
	dir := t.TempDir()
	write := func(name, contents string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// What `gcloud auth application-default login` writes.
	adc := write("adc.json", `{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"token","quota_project_id":"acme-ci"}`)
	creds, err := pubsubCredentials(context.Background(), adc)
	if err != nil {
		t.Fatalf("authorized_user credentials: %v", err)
	}
	if got := quotaProject(creds); got != "acme-ci" {
		t.Errorf("quota project = %q, want the file's", got)
	}
	t.Setenv(pubsubQuotaProjectEnv, "acme-billing")
	if got := quotaProject(creds); got != "acme-billing" {
		t.Errorf("quota project = %q, want the environment's", got)
	}

	for name, contents := range map[string]string{
		"oauth-client.json": `{"installed":{"client_id":"id"}}`,
		"not-json.json":     `client_id=id`,
	} {
		if _, err := pubsubCredentials(context.Background(), write(name, contents)); err == nil {
			t.Errorf("%s accepted as credentials", name)
		}
	}
	if _, err := pubsubCredentials(context.Background(), filepath.Join(dir, "absent.json")); err == nil {
		t.Error("a missing credentials file accepted")
	}
}
