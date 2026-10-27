package workflows

import (
	"context"
	"crs/config"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Deliveries from Google Cloud Pub/Sub, when [Webhooks.PubSub] names a
// subscription. An organization that receives GitHub's webhooks with a service
// of its own and publishes each delivery to a topic can hand them to the
// server that way, with no port open to GitHub and no secret to share: the
// publisher has verified GitHub's signature, and the topic's IAM policy is who
// may publish. Each message is one delivery — its data the JSON body GitHub
// sent, its attributes the event type and delivery ID — and goes down the same
// queue as one received over HTTP (webhooks.go), so everything after that is
// the same.
//
// What it changes is when deliveries arrive. The subscription keeps what is
// published while nothing pulls, so a server that was stopped comes back to a
// backlog. That backlog is dropped, not applied: the first cycle after the
// subscriber comes up refreshes every PR anyway (beginWebhookCycle), and a
// delivery from before it could only roll back what that refresh fetched. A
// laptop that slept comes back to one too, and it is dropped the same way.
// Pub/Sub delivers at least once, so a delivery seen again is dropped too.

const (
	// pubsubMaxMessages is how many messages one pull asks for.
	pubsubMaxMessages = 100
	// pubsubSeenDeliveries is how many delivery IDs are remembered to drop a
	// delivery Pub/Sub hands over again.
	pubsubSeenDeliveries = 4096
)

// Variables so tests can shorten them.
var (
	// pubsubDownAfter is how long pulls may go without succeeding before the
	// subscriber stops counting as up and every PR is polled. The
	// subscription keeps what is published meanwhile, so a shorter outage
	// costs nothing but the wait.
	pubsubDownAfter = 2 * time.Minute
	// pubsubMinBackoff and pubsubMaxBackoff bound the wait before trying
	// again after a failed pull.
	pubsubMinBackoff = time.Second
	pubsubMaxBackoff = time.Minute
	// pubsubNow is the subscriber's clock: the wall clock, since the
	// monotonic one stands still while a laptop sleeps, and a sleep is as
	// much an outage as a dropped network.
	pubsubNow = func() time.Time { return time.Now().Round(0) }
)

// pubsubSettings is what a subscriber pulls with; a change to any of it
// starts a new one.
type pubsubSettings struct {
	subscription    string
	credentialsFile string
	eventTypeAttr   string
	deliveryIDAttr  string
	// emulator is PUBSUB_EMULATOR_HOST as of when the subscriber started.
	emulator string
}

func pubsubSettingsFrom(cfg config.Config) pubsubSettings {
	if !cfg.PubSubConfigured() {
		return pubsubSettings{}
	}
	return pubsubSettings{
		subscription:    cfg.PubSubSubscription(),
		credentialsFile: cfg.PubSubCredentialsFile(),
		eventTypeAttr:   cfg.PubSubEventTypeAttribute(),
		deliveryIDAttr:  cfg.PubSubDeliveryIDAttribute(),
		emulator:        strings.TrimSpace(os.Getenv(pubsubEmulatorEnv)),
	}
}

// pubsubRun is one subscriber, from when it is started until the config stops
// or replaces it.
type pubsubRun struct {
	settings pubsubSettings
	cancel   context.CancelFunc
	// started is when it was started, before the cycle the manager runs
	// next starts.
	started time.Time
	// since is when it started receiving deliveries: when the pull that first
	// reached the subscription was sent — after it started, or after it
	// stopped counting as up. Zero until then, and again once it has no
	// credentials, the subscription can't be pulled from, or pulls have been
	// failing for pubsubDownAfter. Guarded by pubsubSource.mu.
	since time.Time
	// done is closed once pullLoop has returned.
	done chan struct{}
	// seen remembers the delivery IDs it queued, and order the same oldest
	// first. Used by pullLoop alone.
	seen  map[string]bool
	order []string
}

// pubsubSource is the subscriber pulling deliveries, when one runs.
var pubsubSource struct {
	mu  sync.Mutex
	run *pubsubRun
}

// pubsubReceivingSince reports whether deliveries are being pulled from
// Pub/Sub, and since when.
func pubsubReceivingSince() (time.Time, bool) {
	pubsubSource.mu.Lock()
	defer pubsubSource.mu.Unlock()
	if r := pubsubSource.run; r != nil && !r.since.IsZero() {
		return r.since, true
	}
	return time.Time{}, false
}

// syncPubSubSubscriber starts, replaces or stops the subscriber to match cfg.
// It is called with the HTTP listener's sync (syncWebhookListener), before
// every cycle.
func syncPubSubSubscriber(cfg config.Config) {
	want := pubsubSettingsFrom(cfg)
	s := &pubsubSource
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil && s.run.settings == want {
		return
	}
	if s.run != nil {
		s.run.cancel()
		slog.Info("Stopped pulling GitHub webhook deliveries from Pub/Sub; every PR is polled again", "subscription", s.run.settings.subscription)
		s.run = nil
		activeCoverage.Store(nil)
	}
	if want == (pubsubSettings{}) {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	run := &pubsubRun{settings: want, cancel: cancel, started: pubsubNow(), done: make(chan struct{}), seen: map[string]bool{}}
	s.run = run
	startWebhookWorker()
	go run.pullLoop(ctx)

	repos := cfg.WebhookRepos()
	slog.Info("Pulling GitHub webhook deliveries from Pub/Sub", "subscription", want.subscription, "repos", repos)
	if len(repos) == 0 {
		slog.Warn("Webhooks cover no repositories, so every PR is still polled: list the ones whose deliveries the subscription receives in [Webhooks] Repos (\"owner/repo\", or \"owner/*\" for an organization)")
	}
}

// current reports whether r is still the subscriber the config asks for.
// Called with pubsubSource.mu held.
func (r *pubsubRun) current() bool {
	return pubsubSource.run == r
}

// markUp records that r reached the subscription with a pull sent at at, when
// it wasn't counting as up: deliveries published from then on are applied.
func (r *pubsubRun) markUp(at time.Time) {
	pubsubSource.mu.Lock()
	defer pubsubSource.mu.Unlock()
	if !r.current() || !r.since.IsZero() {
		return
	}
	r.since = at
	slog.Info("Receiving GitHub webhook deliveries from Pub/Sub", "subscription", r.settings.subscription)
}

// markDown stops r counting as up — until a pull succeeds again, every PR is
// polled — and reports whether it was.
func (r *pubsubRun) markDown() bool {
	pubsubSource.mu.Lock()
	defer pubsubSource.mu.Unlock()
	if !r.current() || r.since.IsZero() {
		return false
	}
	r.since = time.Time{}
	activeCoverage.Store(nil)
	return true
}

// receivingSince is when r started receiving, zero while it isn't.
func (r *pubsubRun) receivingSince() time.Time {
	pubsubSource.mu.Lock()
	defer pubsubSource.mu.Unlock()
	return r.since
}

// pullLoop pulls until ctx is cancelled, retrying a failed pull with backoff.
func (r *pubsubRun) pullLoop(ctx context.Context) {
	defer close(r.done)
	var client *pubsubClient
	backoff := pubsubMinBackoff
	// lastPulled is when the last successful pull had been handled, and
	// failingSince when the failures since began.
	var lastPulled, failingSince time.Time
	lastErr := ""
	// fail stops counting as up on an error retrying can't fix, or once
	// nothing has been pulled for pubsubDownAfter, and waits to try again. It
	// logs each distinct error once, not once per retry.
	fail := func(err error, permanent bool) {
		now := pubsubNow()
		if failingSince.IsZero() {
			failingSince = now
		}
		// Counted from the first failure, not the last success: a pull
		// that went unanswered has already used up pubsubPullTimeout.
		stalled := now.Sub(failingSince) >= pubsubDownAfter
		wentDown := (permanent || stalled) && r.markDown()
		switch msg := err.Error(); {
		case wentDown && !permanent:
			slog.Error("Pulls from Pub/Sub keep failing; every PR is polled until one succeeds", "subscription", r.settings.subscription,
				"failing_for", now.Sub(failingSince).Round(time.Second), "error", err)
		case msg == lastErr:
		case permanent:
			slog.Error("Can't pull GitHub webhook deliveries from Pub/Sub; every PR is polled until it can", "subscription", r.settings.subscription,
				"error", err, "retry_in", backoff)
		default:
			slog.Warn("Failed to pull GitHub webhook deliveries from Pub/Sub; retrying", "subscription", r.settings.subscription,
				"error", err, "retry_in", backoff)
		}
		lastErr = err.Error()
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, pubsubMaxBackoff)
	}

	for ctx.Err() == nil {
		if client == nil {
			c, err := newPubSubClient(ctx, r.settings)
			if err != nil {
				// Credentials are something only the user can fix.
				fail(err, true)
				continue
			}
			client = c
		}
		sent := pubsubNow()
		msgs, err := client.pull(ctx, pubsubMaxMessages)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			permanent := permanentPubSubError(err)
			if permanent || credentialsError(err) {
				// Read the credentials again next time: a refused pull or
				// token may be fixed by a fresh gcloud login or a new key.
				client = nil
			}
			fail(err, permanent)
			continue
		}
		now := pubsubNow()
		if !failingSince.IsZero() {
			slog.Info("Pulling GitHub webhook deliveries from Pub/Sub again", "subscription", r.settings.subscription, "failing_for", now.Sub(failingSince).Round(time.Second))
		}
		switch {
		// The first pull, with nothing failing before it: the subscription
		// kept everything published since the subscriber started, so that
		// is what deliveries count from — and a cycle that started after it
		// counts as the catch-up refresh, however long this pull took.
		case lastPulled.IsZero() && failingSince.IsZero():
			sent = r.started
		// Longer since the last pull than a pull can take, with too few
		// failures in between to say so: the machine slept. What was
		// published meanwhile is a backlog like a restart's, so it is
		// dropped and the next cycle refreshes every PR instead.
		case !lastPulled.IsZero() && now.Sub(lastPulled) >= pubsubDownAfter+pubsubPullTimeout && r.markDown():
			slog.Info("Pulling from Pub/Sub again after a gap; dropping what was published meanwhile and refreshing every PR",
				"subscription", r.settings.subscription, "gap", now.Sub(lastPulled).Round(time.Second))
			sent = now
		}
		failingSince, lastErr, backoff = time.Time{}, "", pubsubMinBackoff
		r.markUp(sent)
		r.handle(ctx, client, msgs)
		lastPulled = pubsubNow()
	}
}

// handle queues what a pull returned for the worker to apply, oldest first,
// then acknowledges every message it is done with: queued, dropped, or one
// that can never be applied. Only a message it couldn't queue before being
// stopped is handed back, for whatever pulls next.
func (r *pubsubRun) handle(ctx context.Context, client *pubsubClient, msgs []pubsubMessage) {
	if len(msgs) == 0 {
		return
	}
	slices.SortStableFunc(msgs, func(a, b pubsubMessage) int { return a.Message.PublishTime.Compare(b.Message.PublishTime) })
	// Zero only when r was replaced as it pulled: whatever replaced it
	// decides what to apply.
	since := r.receivingSince()
	var acks, releases []string
	for _, m := range msgs {
		if ctx.Err() != nil || since.IsZero() {
			releases = append(releases, m.AckID)
			continue
		}
		d, ok := r.delivery(m, since)
		if !ok {
			acks = append(acks, m.AckID)
			continue
		}
		select {
		case webhookQueue <- d:
			r.remember(d.id)
			acks = append(acks, m.AckID)
		case <-ctx.Done():
			releases = append(releases, m.AckID)
		}
	}
	// Acknowledging outlives a stop: the messages are queued either way.
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := client.acknowledge(ackCtx, acks); err != nil {
		slog.Warn("Failed to acknowledge Pub/Sub messages; they will be delivered again and dropped as seen", "subscription", r.settings.subscription, "count", len(acks), "error", err)
	}
	if err := client.release(ackCtx, releases); err != nil {
		slog.Debug("Failed to hand Pub/Sub messages back", "subscription", r.settings.subscription, "count", len(releases), "error", err)
	}
}

// delivery reads the delivery a message carries. It returns false for one
// there is nothing to apply from: published before the subscriber came up,
// seen already, not a delivery at all, or one parseWebhookDelivery skips.
func (r *pubsubRun) delivery(m pubsubMessage, since time.Time) (webhookDelivery, bool) {
	attrs := m.Message.Attributes
	eventType := strings.TrimSpace(attrs[r.settings.eventTypeAttr])
	deliveryID := strings.TrimSpace(attrs[r.settings.deliveryIDAttr])
	if deliveryID == "" {
		deliveryID = "pubsub-" + m.Message.MessageID
	}
	if m.Message.PublishTime.Before(since) {
		webhookActivity.ignored.Add(1)
		slog.Debug("Dropping a delivery published before the Pub/Sub subscriber came up", "event", eventType, "delivery", deliveryID, "published", m.Message.PublishTime)
		return webhookDelivery{}, false
	}
	if eventType == "" {
		webhookActivity.rejected.Add(1)
		slog.Warn("Dropping a Pub/Sub message that names no GitHub event", "subscription", r.settings.subscription, "attribute", r.settings.eventTypeAttr, "message", m.Message.MessageID)
		return webhookDelivery{}, false
	}
	if r.seenBefore(deliveryID) {
		webhookActivity.ignored.Add(1)
		slog.Debug("Dropping a delivery Pub/Sub handed over again", "event", eventType, "delivery", deliveryID)
		return webhookDelivery{}, false
	}
	d, ok, err := parseWebhookDelivery(eventType, deliveryID, m.Message.Data)
	if err != nil {
		webhookActivity.rejected.Add(1)
	}
	return d, ok
}

// seenBefore reports whether r already queued the delivery.
func (r *pubsubRun) seenBefore(id string) bool {
	return r.seen[id]
}

// remember notes that r queued the delivery, forgetting the oldest once it
// holds pubsubSeenDeliveries.
func (r *pubsubRun) remember(id string) {
	if r.seen[id] {
		return
	}
	r.seen[id] = true
	r.order = append(r.order, id)
	if len(r.order) > pubsubSeenDeliveries {
		delete(r.seen, r.order[0])
		r.order = r.order[1:]
	}
}
