package database

import "time"

// APICallStatsRow is one row of APICallStats: the GitHub API calls a single
// workflow cycle made, broken down by call type, together with the rate limit
// budget that was left when that cycle finished.
//
// Rows written before the rate limit columns were added carry -1 for
// RateLimitRemaining and RateLimitLimit, and a cycle whose post-cycle lookup
// failed with nothing cached from response headers can record 0. Readers should
// treat RateLimitLimit <= 0 as "budget unknown for this cycle" rather than
// plotting it as an exhausted budget.
type APICallStatsRow struct {
	ID                 int64     `json:"id"`
	RecordedAt         time.Time `json:"recorded_at"`
	PRList             int64     `json:"pr_list"`
	PRSpecific         int64     `json:"pr_specific"`
	Comments           int64     `json:"comments"`
	IssueComments      int64     `json:"issue_comments"`
	CIStatus           int64     `json:"ci_status"`
	Diff               int64     `json:"diff"`
	Reviews            int64     `json:"reviews"`
	CombinedStatus     int64     `json:"combined_status"`
	CheckRuns          int64     `json:"check_runs"`
	Commits            int64     `json:"commits"`
	ReviewThreads      int64     `json:"review_threads"`
	TeamReviews        int64     `json:"team_reviews"`
	Reactions          int64     `json:"reactions"`
	Total              int64     `json:"total"`
	RateLimitRemaining int       `json:"rate_limit_remaining"`
	RateLimitLimit     int       `json:"rate_limit_limit"`
	RateLimitResetAt   string    `json:"rate_limit_reset_at"`

	// Mode is the kind of cycle: CycleModePolling, CycleModeFullRefresh or
	// CycleModeWebhook. Empty on rows from before it was recorded, which were
	// all polling.
	Mode string `json:"mode"`
	// Requests, NotModified, SearchRequests and GraphQLRequests are what the
	// process sent GitHub between the previous row and this one, counted on
	// the wire: the cycle's own calls, including those the per-type counts
	// above don't cover, plus whatever webhook deliveries and the review view
	// asked for in between. Requests is the core REST budget's, NotModified how
	// many of them came back 304 and so weren't charged. -1 on rows from
	// before they were recorded.
	Requests        int64 `json:"requests"`
	NotModified     int64 `json:"not_modified"`
	SearchRequests  int64 `json:"search_requests"`
	GraphQLRequests int64 `json:"graphql_requests"`
	// WebhookDeliveries is how many deliveries were applied since the
	// previous row, and WebhookAPICalls how many API calls applying them took
	// (a push's full fetch, a new review thread's lookup), counted per call
	// type like the per-type counts above.
	WebhookDeliveries int64 `json:"webhook_deliveries"`
	WebhookAPICalls   int64 `json:"webhook_api_calls"`
	// GraphQLRemaining and GraphQLLimit are the GraphQL budget after the
	// cycle; -1 when unknown.
	GraphQLRemaining int `json:"graphql_remaining"`
	GraphQLLimit     int `json:"graphql_limit"`
}

// The kinds of cycle an APICallStats row records.
const (
	// CycleModePolling: webhooks aren't configured or nothing is listening,
	// so the cycle fetched every PR, as without them.
	CycleModePolling = "polling"
	// CycleModeFullRefresh: webhooks are on, but the cycle fetched every PR
	// regardless — the first after the listener came up, or the periodic one.
	CycleModeFullRefresh = "full_refresh"
	// CycleModeWebhook: the cycle left the PRs webhooks cover to them.
	CycleModeWebhook = "webhook"
)

// GetAPICallStatsSince returns the cycles recorded at or after the cutoff,
// oldest first, so a caller plotting them walks forward in time. Rows are
// ordered by recorded_at with id as the tie-break: cycles that finish inside
// the same second still come back in the order they were written.
func (db *DB) GetAPICallStatsSince(cutoff time.Time) ([]APICallStatsRow, error) {
	rows, err := db.conn.Query(
		`SELECT id, recorded_at, pr_list, pr_specific, comments, issue_comments,
			ci_status, diff, reviews, combined_status, check_runs, commits,
			review_threads, team_reviews, reactions, total,
			rate_limit_remaining, rate_limit_limit, rate_limit_reset_at,
			mode, requests, not_modified, search_requests, graphql_requests,
			webhook_deliveries, webhook_api_calls, graphql_remaining, graphql_limit
		 FROM APICallStats
		 WHERE recorded_at >= ?
		 ORDER BY recorded_at ASC, id ASC`,
		cutoff.UTC().Format(sqliteTimeLayout),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APICallStatsRow
	for rows.Next() {
		var r APICallStatsRow
		var recordedAt string
		if err := rows.Scan(
			&r.ID, &recordedAt, &r.PRList, &r.PRSpecific, &r.Comments, &r.IssueComments,
			&r.CIStatus, &r.Diff, &r.Reviews, &r.CombinedStatus, &r.CheckRuns, &r.Commits,
			&r.ReviewThreads, &r.TeamReviews, &r.Reactions, &r.Total,
			&r.RateLimitRemaining, &r.RateLimitLimit, &r.RateLimitResetAt,
			&r.Mode, &r.Requests, &r.NotModified, &r.SearchRequests, &r.GraphQLRequests,
			&r.WebhookDeliveries, &r.WebhookAPICalls, &r.GraphQLRemaining, &r.GraphQLLimit,
		); err != nil {
			return nil, err
		}
		r.RecordedAt = parseSQLiteTime(recordedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}
