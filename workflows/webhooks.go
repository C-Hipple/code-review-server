package workflows

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v74/github"
)

// GitHub webhooks, when [Webhooks] is configured, take over keeping the PRs we
// already track up to date, so a workflow cycle no longer has to re-fetch each
// of them to find out whether anything changed. Deliveries are applied straight
// to the DB caches (webhook_events.go); a cycle still lists every PR — that is
// how it finds new ones and drops gone ones — but reads the aux data of a PR
// whose caches are current from those caches (webhook_cache.go) instead of
// GitHub, and fetches in full only the PRs it is adding or whose head moved.
//
// Only PRs in the repositories [Webhooks] Repos covers are left to webhooks; a
// PR anywhere else is polled as before. Every cycle is a polling cycle until
// the listener is up, again for the first cycle after it comes up (anything
// that changed while nothing was listening is missed for good), and once per
// FullRefreshInterval after that, in case a delivery never arrived.

// webhookWorkflowName is who the action log says wrote a cache when a webhook
// delivery did.
const webhookWorkflowName = "webhook"

const (
	// webhookQueueSize bounds the deliveries waiting to be applied. GitHub
	// gives up on a delivery after ten seconds, so they are queued and
	// acknowledged at once rather than applied while it waits; a full queue
	// answers 503, which GitHub records as a failed delivery to redeliver.
	webhookQueueSize = 512
	// maxWebhookPayload is GitHub's own cap on a delivery's size.
	maxWebhookPayload = 25 << 20
)

// handledWebhookEvents are the event types applyWebhookEvent acts on. Anything
// else a webhook sends is acknowledged and dropped without being parsed.
var handledWebhookEvents = map[string]bool{
	"pull_request":                true,
	"pull_request_review":         true,
	"pull_request_review_comment": true,
	"pull_request_review_thread":  true,
	"issue_comment":               true,
	"check_run":                   true,
	"status":                      true,
	"push":                        true,
}

type webhookDelivery struct {
	eventType string
	id        string
	event     any
}

// describe names the delivery for logs and the action log.
func (d webhookDelivery) describe(action string) string {
	name := d.eventType
	if action != "" {
		name += "." + action
	}
	if d.id == "" {
		return name
	}
	return fmt.Sprintf("%s (delivery %s)", name, d.id)
}

var (
	webhookQueue      = make(chan webhookDelivery, webhookQueueSize)
	webhookWorkerOnce sync.Once
)

// webhookListener is the HTTP server receiving deliveries, when one is up.
var webhookListener struct {
	mu     sync.Mutex
	server *http.Server
	addr   string
	since  time.Time
	// warnedNoSecret keeps a missing secret to one warning rather than one a
	// cycle.
	warnedNoSecret bool
}

// webhooksListeningSince reports whether deliveries are being received, and
// since when.
func webhooksListeningSince() (time.Time, bool) {
	webhookListener.mu.Lock()
	defer webhookListener.mu.Unlock()
	return webhookListener.since, webhookListener.server != nil
}

// syncWebhookListener starts, moves or stops the listener to match cfg. The
// manager calls it before every cycle, after reloading the config, so editing
// [Webhooks] takes effect without a restart. When the listener can't start —
// no secret, the port taken — it logs why and every PR goes on being polled;
// the next cycle tries again.
func syncWebhookListener(cfg config.Config) {
	want := ""
	if cfg.WebhooksConfigured() {
		want = strings.TrimSpace(cfg.Webhooks.ListenAddress)
	}

	l := &webhookListener
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.server != nil && l.addr == want {
		return
	}
	if l.server != nil {
		l.server.Close()
		slog.Info("Stopped listening for GitHub webhooks; every PR is polled again", "addr", l.addr)
		l.server, l.addr, l.since = nil, "", time.Time{}
		activeCoverage.Store(nil)
	}
	if want == "" {
		if strings.TrimSpace(cfg.Webhooks.ListenAddress) != "" && !l.warnedNoSecret {
			slog.Warn("[Webhooks] sets a ListenAddress but there is no secret to verify deliveries with, so nothing listens and every PR is polled",
				"set", "Webhooks.Secret or "+config.WebhookSecretEnv)
			l.warnedNoSecret = true
		}
		return
	}
	l.warnedNoSecret = false

	ln, err := net.Listen("tcp", want)
	if err != nil {
		slog.Error("Failed to listen for GitHub webhooks; every PR is polled until it can", "addr", want, "error", err)
		return
	}
	srv := &http.Server{
		Handler:           http.HandlerFunc(serveWebhook),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	l.server, l.addr, l.since = srv, want, time.Now()
	webhookWorkerOnce.Do(func() {
		go runWebhookWorker()
		go runReevaluations()
	})
	git_tools.SetPRStateSource(webhookPRState{})
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			return
		}
		slog.Error("GitHub webhook listener stopped; every PR is polled until it restarts", "error", err)
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.server == srv {
			l.server, l.addr, l.since = nil, "", time.Time{}
			activeCoverage.Store(nil)
		}
	}()

	repos := cfg.WebhookRepos()
	slog.Info("Listening for GitHub webhooks", "addr", ln.Addr().String(), "path", cfg.WebhookPath(), "repos", repos)
	if len(repos) == 0 {
		slog.Warn("Webhooks cover no repositories, so every PR is still polled: list the ones whose webhooks deliver here in [Webhooks] Repos (\"owner/repo\", or \"owner/*\" for an organization webhook)")
	}
}

// serveWebhook receives one delivery: it checks the signature against the
// configured secret, acknowledges the delivery, and queues it for
// runWebhookWorker. Nothing a delivery says is acted on unless it is signed.
func serveWebhook(w http.ResponseWriter, r *http.Request) {
	cfg := config.C()
	if r.URL.Path != cfg.WebhookPath() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	secret := cfg.WebhookSecret()
	if secret == "" {
		http.Error(w, "webhooks are not configured", http.StatusServiceUnavailable)
		return
	}

	deliveryID := github.DeliveryID(r)
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookPayload)
	payload, err := github.ValidatePayload(r, []byte(secret))
	if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
		webhookActivity.rejected.Add(1)
		slog.Warn("Rejected a webhook delivery over the size limit", "delivery", deliveryID, "limit", tooLarge.Limit)
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		webhookActivity.rejected.Add(1)
		slog.Warn("Rejected a webhook delivery that failed signature validation", "delivery", deliveryID, "remote", r.RemoteAddr, "error", err)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	eventType := github.WebHookType(r)
	if eventType == "ping" {
		slog.Info("GitHub webhook ping received", "delivery", deliveryID)
		fmt.Fprintln(w, "pong")
		return
	}
	if !handledWebhookEvents[eventType] {
		slog.Debug("Ignoring webhook event", "event", eventType, "delivery", deliveryID)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	event, err := github.ParseWebHook(eventType, payload)
	if err != nil {
		webhookActivity.rejected.Add(1)
		slog.Warn("Failed to parse webhook delivery", "event", eventType, "delivery", deliveryID, "error", err)
		http.Error(w, "malformed payload", http.StatusBadRequest)
		return
	}

	select {
	case webhookQueue <- webhookDelivery{eventType: eventType, id: deliveryID, event: event}:
		w.WriteHeader(http.StatusAccepted)
	default:
		webhookActivity.rejected.Add(1)
		slog.Error("Webhook queue is full; dropping a delivery GitHub can redeliver", "event", eventType, "delivery", deliveryID)
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}
}

// runWebhookWorker applies queued deliveries one at a time, in arrival order.
func runWebhookWorker() {
	for d := range webhookQueue {
		applyWebhookDelivery(d)
	}
}

// applyWebhookDelivery applies one delivery, keeping a panic on a malformed
// payload from taking the worker down with it.
func applyWebhookDelivery(d webhookDelivery) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("Panic applying a webhook delivery", "event", d.eventType, "delivery", d.id, "panic", p)
		}
	}()
	applyWebhookEvent(d)
}

// webhookCoverage is the set of repositories [Webhooks] Repos names.
type webhookCoverage struct {
	repos  map[string]bool // lowercased "owner/repo"
	owners map[string]bool // lowercased owner, from "owner/*"
}

func newWebhookCoverage(entries []string) *webhookCoverage {
	cov := &webhookCoverage{repos: map[string]bool{}, owners: map[string]bool{}}
	for _, entry := range entries {
		owner, repo, ok := strings.Cut(strings.ToLower(strings.TrimSpace(entry)), "/")
		if !ok || owner == "" || repo == "" {
			continue
		}
		if repo == "*" {
			cov.owners[owner] = true
		} else {
			cov.repos[owner+"/"+repo] = true
		}
	}
	return cov
}

func (c *webhookCoverage) covers(owner, repo string) bool {
	if c == nil {
		return false
	}
	owner, repo = strings.ToLower(owner), strings.ToLower(repo)
	return c.owners[owner] || c.repos[owner+"/"+repo]
}

// activeCoverage is the coverage webhooks are trusted for right now, or nil
// while they aren't: nothing is listening, nothing has caught up since the
// listener came up, or a cycle is refreshing every PR. It is set when a
// webhook cycle begins or a full refresh completes, and holds between cycles,
// so a delivery applied then can be acted on as a webhook cycle would.
var activeCoverage atomic.Pointer[webhookCoverage]

// webhookHandles reports whether the PR's updates are left to webhooks rather
// than fetched: its repository is covered and webhooks are trusted.
func webhookHandles(key PRKey) bool {
	return activeCoverage.Load().covers(key.Owner, key.Repo)
}

// lastFullRefresh is when the last cycle that polled every PR, while the
// listener was up, started.
var lastFullRefresh struct {
	mu sync.Mutex
	at time.Time
}

// beginWebhookCycle decides what kind of cycle is starting now and returns it
// as a database.CycleMode*. It leaves covered PRs to webhooks unless nothing is
// listening (polling), or nothing has caught up since the listener came up or
// the periodic full refresh is due (a full refresh) — both of which fetch
// every PR.
func beginWebhookCycle(cfg config.Config, now time.Time) string {
	activeCoverage.Store(nil)
	since, listening := webhooksListeningSince()
	if !listening || !cfg.WebhooksConfigured() {
		return database.CycleModePolling
	}
	lastFullRefresh.mu.Lock()
	last := lastFullRefresh.at
	lastFullRefresh.mu.Unlock()
	switch {
	case last.Before(since):
		slog.Info("Refreshing every PR to catch up on anything that changed before webhooks were being received")
		return database.CycleModeFullRefresh
	case now.Sub(last) >= cfg.WebhookFullRefreshInterval():
		slog.Info("Refreshing every PR, as webhooks' periodic full refresh", "last", last.Format(time.RFC3339))
		return database.CycleModeFullRefresh
	}
	repos := cfg.WebhookRepos()
	activeCoverage.Store(newWebhookCoverage(repos))
	slog.Info("Webhook cycle: PRs in covered repositories are listed but not re-fetched", "repos", repos)
	return database.CycleModeWebhook
}

// endWebhookCycle closes the cycle beginWebhookCycle opened. A webhook cycle
// leaves webhooks trusted. A full refresh that started while the listener was
// up, and refreshed everything it meant to, is what webhook cycles count from,
// so it starts trusting them; one that didn't finish leaves them untrusted
// until one does.
func endWebhookCycle(cfg config.Config, start time.Time, mode string, complete bool) {
	if mode == database.CycleModeWebhook {
		return
	}
	since, listening := webhooksListeningSince()
	if mode == database.CycleModePolling || !complete || !listening || start.Before(since) {
		return
	}
	lastFullRefresh.mu.Lock()
	lastFullRefresh.at = start
	lastFullRefresh.mu.Unlock()
	activeCoverage.Store(newWebhookCoverage(cfg.WebhookRepos()))
	pruneWebhookMemory(start)
}

// Whether a PR merges cleanly changes when its base branch moves, which says
// nothing to the PR itself, so a webhook cycle can't simply trust the answer
// it has. It re-asks GitHub about a covered PR only when the PR's head moved,
// when the answer on record is "unknown", or when a push delivery moved the
// PR's base branch after the answer was recorded.
var (
	// baseMoves maps a lowercased "owner/repo:branch" to its last push.
	baseMoves sync.Map
	// mergeabilityCheckedAt maps a PRRef to when its answer was recorded.
	mergeabilityCheckedAt sync.Map
)

func baseMoveKey(owner, repo, branch string) string {
	return strings.ToLower(owner+"/"+repo) + ":" + branch
}

// recordBaseMoved notes a push to branch, for every PR merging into it.
func recordBaseMoved(owner, repo, branch string, at time.Time) {
	baseMoves.Store(baseMoveKey(owner, repo, branch), at)
}

// markMergeabilityChecked notes that GitHub just answered for ref.
func markMergeabilityChecked(ref git_tools.PRRef, at time.Time) {
	mergeabilityCheckedAt.Store(ref, at)
}

// mergeabilityCurrent reports whether the mergeability on record for pr still
// stands, so a webhook cycle needn't ask GitHub again.
func mergeabilityCurrent(db *database.DB, ref git_tools.PRRef, pr *github.PullRequest) bool {
	state, headSHA, err := db.GetPRMergeability(ref.Number, ref.Repo)
	if err != nil || headSHA != pr.GetHead().GetSHA() {
		return false
	}
	if state != git_tools.MergeabilityMergeable && state != git_tools.MergeabilityConflicting {
		return false
	}
	v, ok := mergeabilityCheckedAt.Load(ref)
	if !ok {
		return false
	}
	checked := v.(time.Time)
	if moved, ok := baseMoves.Load(baseMoveKey(ref.Owner, ref.Repo, pr.GetBase().GetRef())); ok && !moved.(time.Time).Before(checked) {
		return false
	}
	return true
}

// pruneWebhookMemory forgets what a full refresh starting at start made
// irrelevant: it re-asks about every PR, so earlier pushes no longer matter,
// and a PR it didn't ask about — or list — is no longer listed.
func pruneWebhookMemory(start time.Time) {
	pruneRememberedPRs(start)
	for _, m := range []*sync.Map{&baseMoves, &mergeabilityCheckedAt} {
		m.Range(func(k, v any) bool {
			if v.(time.Time).Before(start) {
				m.Delete(k)
			}
			return true
		})
	}
}
