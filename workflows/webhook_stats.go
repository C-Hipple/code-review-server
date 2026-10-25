package workflows

import (
	"crs/git_tools"
	"log/slog"
	"sync"
	"sync/atomic"
)

// What it costs to keep the dashboard current is recorded once a cycle, in the
// cycle's APICallStats row and log lines, so polling and webhook-driven
// periods can be compared. Two things are measured beyond the cycle's own
// per-type counts: every request sent GitHub since the previous row, counted
// on the wire (git_tools.GetRequestCounts), and what webhook deliveries did in
// that time.

// webhookActivity counts what deliveries did since the last cycle row;
// takeWebhookActivity reads and resets it.
var webhookActivity struct {
	applied, ignored, rejected atomic.Int64
	// refetches is how many deliveries needed a PR fetched in full, and
	// apiCalls the API calls deliveries made, counted per call type as a
	// cycle's are.
	refetches, apiCalls atomic.Int64
	// sectionAdds and sectionDrops are the section rows deliveries added and
	// dropped by re-evaluating which sections a PR belongs in.
	sectionAdds, sectionDrops atomic.Int64
}

// webhookActivitySummary is a reading of webhookActivity.
type webhookActivitySummary struct {
	Applied, Ignored, Rejected int64
	Refetches, APICalls        int64
	SectionAdds, SectionDrops  int64
}

func (s webhookActivitySummary) empty() bool {
	return s == webhookActivitySummary{}
}

// takeWebhookActivity returns what deliveries did since it was last called,
// and starts counting again from zero.
func takeWebhookActivity() webhookActivitySummary {
	a := &webhookActivity
	return webhookActivitySummary{
		Applied:      a.applied.Swap(0),
		Ignored:      a.ignored.Swap(0),
		Rejected:     a.rejected.Swap(0),
		Refetches:    a.refetches.Swap(0),
		APICalls:     a.apiCalls.Swap(0),
		SectionAdds:  a.sectionAdds.Swap(0),
		SectionDrops: a.sectionDrops.Swap(0),
	}
}

// lastRowTraffic is the request counts as of the last cycle row.
var lastRowTraffic struct {
	mu     sync.Mutex
	counts git_tools.RequestCounts
}

// takeTrafficSinceLastRow returns the requests sent GitHub since it was last
// called — since the process started, the first time.
func takeTrafficSinceLastRow() git_tools.RequestCounts {
	now := git_tools.GetRequestCounts()
	lastRowTraffic.mu.Lock()
	defer lastRowTraffic.mu.Unlock()
	delta := now.Sub(lastRowTraffic.counts)
	lastRowTraffic.counts = now
	return delta
}

// logWebhookActivity reports what deliveries did since the last cycle. Quiet
// when there was nothing, which is always the case without webhooks.
func logWebhookActivity(mode string, a webhookActivitySummary) {
	if a.empty() {
		return
	}
	slog.Info("Webhook activity since the last cycle",
		"mode", mode,
		"deliveries_applied", a.Applied,
		"deliveries_ignored", a.Ignored,
		"deliveries_rejected", a.Rejected,
		"refetches", a.Refetches,
		"webhook_api_calls", a.APICalls,
		"section_adds", a.SectionAdds,
		"section_drops", a.SectionDrops,
	)
}
