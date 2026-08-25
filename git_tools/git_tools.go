package git_tools

import (
	"context"
	"crs/config"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v74/github"
	"golang.org/x/oauth2"
)

type PullRequest interface {
}

type PRFilter func([]*github.PullRequest) []*github.PullRequest

func GetPRs(client *github.Client, state string, owner string, repo string) ([]*github.PullRequest, error) {
	per_page := 100
	// Sort by recent activity, not creation date. We only ever look at the first
	// max_additional_calls+1 pages, so on a repo with more open PRs than that
	// window the ordering decides what is even eligible for filtering. A PR that
	// just had a review re-requested has a fresh updated_at but may have been
	// created months ago, and under the default created-desc ordering it fell off
	// the end of the window and never reached the filters at all.
	options := github.PullRequestListOptions{
		State:       state,
		Sort:        "updated",
		Direction:   "desc",
		ListOptions: github.ListOptions{PerPage: per_page, Page: 1},
	}
	// Use make so a successful fetch with 0 results returns a non-nil empty slice.
	// Callers use nil to detect fetch failure vs success (nil == failed, non-nil == succeeded).
	prs := make([]*github.PullRequest, 0)

	// TODO: Consider if I really want deep lookups.
	// Setting to 0 limits to 1 API call.
	max_additional_calls := 1
	i := 0

	for {
		new_prs, _, err := client.PullRequests.List(context.Background(), owner, repo, &options)
		if err != nil {
			slog.Error("Error listing PRs", "error", err)
			return nil, err
		}
		prs = append(prs, new_prs...)
		if len(new_prs) != per_page || i >= max_additional_calls {
			break
		}
		options.Page += 1
		i = i + 1
	}
	return prs, nil
}

func ParseRepoName(repoEntry string) (owner string, repo string, err error) {
	parts := strings.Split(repoEntry, "/")
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return "", "", fmt.Errorf("invalid repo entry: %s (expected 'owner/repo')", repoEntry)
}

func GetManyRepoPRs(client *github.Client, state string, repos []string) ([]*github.PullRequest, error) {
	var prs []*github.PullRequest
	for _, repoEntry := range repos {
		owner, repo, err := ParseRepoName(repoEntry)
		if err != nil {
			slog.Error("Skipping invalid repo entry", "entry", repoEntry, "error", err)
			continue
		}

		repo_prs, err := GetPRs(
			client,
			state,
			owner,
			repo,
		)
		if err != nil {
			return nil, err
		}
		prs = append(prs, repo_prs...)
	}
	return prs, nil
}

func GetSpecificPRs(client *github.Client, owner string, repo string, pr_numbers []int) ([]*github.PullRequest, error) {
	var prs []*github.PullRequest
	for _, number := range pr_numbers {
		pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, number)
		if err != nil {
			slog.Error("Error Getting PR", "owner", owner, "repo", repo, "number", number, "error", err)
			return nil, err
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

func GetPRDiff(client *github.Client, owner string, repo string, pr_number int) string {
	// Calls out to the
	diff, _, err := client.PullRequests.GetRaw(context.Background(), owner, repo, pr_number, github.RawOptions{
		Type: 1, // Diff format, not Patch format
	})
	if err != nil {
		return fmt.Sprintf("%s", err)
	}
	return diff

}

func GetPRComments(client *github.Client, owner string, repo string, number int) ([]*github.PullRequestComment, error) {
	comments, err := ListAllPRComments(context.Background(), client, owner, repo, number)
	if err != nil {
		// TODO: wump
		// unwrap in production
		return nil, err
	}
	return comments, nil
}

func ApplyPRFilters(prs []*github.PullRequest, filters []PRFilter) []*github.PullRequest {
	for _, filter := range filters {
		prs = filter(prs)
	}
	return prs
}

func FilterPRsByAuthor(prs []*github.PullRequest, author string) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		if strings.EqualFold(*pr.User.Login, author) {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func FilterPRsExcludeAuthor(prs []*github.PullRequest, author string) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		if !strings.EqualFold(*pr.User.Login, author) {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func FilterPRsByState(prs []*github.PullRequest, state string) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		if *pr.State == state {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func FilterPRsByLabel(prs []*github.PullRequest, label string) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		for _, pr_label := range pr.Labels {
			if *pr_label.Name == label {
				filtered = append(filtered, pr)
				break
			}
		}
	}
	return filtered
}

func FilterMyPRs(prs []*github.PullRequest) []*github.PullRequest {
	return FilterPRsByAuthor(prs, config.C().GithubUsername)
}

func FilterNotMyPRs(prs []*github.PullRequest) []*github.PullRequest {
	return FilterPRsExcludeAuthor(prs, config.C().GithubUsername)
}

func FilterIsDraft(prs []*github.PullRequest) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		if *pr.Draft {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func FilterNotDraft(prs []*github.PullRequest) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		if !*pr.Draft {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func MakeTeamFilters(teams []string) func([]*github.PullRequest) []*github.PullRequest {
	return func(prs []*github.PullRequest) []*github.PullRequest {
		filtered := []*github.PullRequest{}
		for _, pr := range prs {
			for _, team := range pr.RequestedTeams {
				if slices.Contains(teams, *team.Slug) {
					filtered = append(filtered, pr)
					break
				}
			}
		}
		return filtered
	}
}

// githubSemaphore limits concurrent in-flight GitHub API requests to 50
// to avoid hitting GitHub's secondary rate limits.
var githubSemaphore = make(chan struct{}, 50)

// RateLimitStatus provides visibility into current rate limit state
type RateLimitStatus struct {
	Remaining        int
	Limit            int
	ResetAt          time.Time
	TotalRequests    int64
	ThrottledCount   int64
	RateLimitedCount int64
}

// RateLimitManager tracks GitHub API rate limits and implements throttling/retry logic
type RateLimitManager struct {
	mu sync.RWMutex

	// Rate limit state from GitHub response headers
	remaining int
	limit     int
	resetAt   time.Time

	// Backoff state for handling rate limit errors
	consecutiveFailures int
	lastFailureTime     time.Time

	// Configuration
	minRemaining  int // Start throttling when remaining drops below this
	reserveBuffer int // Block requests when remaining <= this
	maxRetries    int // Max retries on 429/403 rate limit errors

	// Metrics
	totalRequests    atomic.Int64
	throttledCount   atomic.Int64
	rateLimitedCount atomic.Int64
}

// NewRateLimitManager creates a new rate limit manager with sensible defaults
func NewRateLimitManager() *RateLimitManager {
	return &RateLimitManager{
		minRemaining:  100, // Start throttling at 100 remaining requests
		reserveBuffer: 10,  // Block at 10 remaining (emergency reserve)
		maxRetries:    3,   // Retry up to 3 times on rate limit errors
		limit:         5000,
		remaining:     5000,
	}
}

// WaitIfNeeded blocks if we're at/near the rate limit
func (m *RateLimitManager) WaitIfNeeded(ctx context.Context) error {
	m.mu.RLock()
	remaining := m.remaining
	resetAt := m.resetAt
	m.mu.RUnlock()

	now := time.Now()

	// If we're at/past limit, block until reset
	if remaining <= m.reserveBuffer {
		if now.Before(resetAt) {
			wait := resetAt.Sub(now) + time.Second
			slog.Warn("Rate limit exhausted, waiting for reset", "remaining", remaining, "wait", wait)
			select {
			case <-time.After(wait):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	// If we're below threshold, implement adaptive throttling
	if remaining <= m.minRemaining && remaining > m.reserveBuffer {
		m.throttledCount.Add(1)
		// Distribute remaining requests evenly until reset
		timeUntilReset := resetAt.Sub(now)
		if timeUntilReset > 0 {
			delay := timeUntilReset / time.Duration(remaining-m.reserveBuffer)
			if delay > 0 && delay < 10*time.Second { // Cap max delay
				slog.Debug("Throttling request", "remaining", remaining, "delay", delay)
				select {
				case <-time.After(delay):
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}

	return nil
}

// UpdateFromHeaders updates rate limit state from GitHub response headers
func (m *RateLimitManager) UpdateFromHeaders(headers http.Header) {
	remaining, err := strconv.Atoi(headers.Get("X-RateLimit-Remaining"))
	if err != nil {
		return // Header not present or invalid
	}

	limit, _ := strconv.Atoi(headers.Get("X-RateLimit-Limit"))
	reset, _ := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64)

	m.mu.Lock()
	defer m.mu.Unlock()

	if limit > 0 {
		m.limit = limit
	}
	m.remaining = remaining
	if reset > 0 {
		m.resetAt = time.Unix(reset, 0)
	}
}

// RecordRateLimit records a rate limit error occurrence
func (m *RateLimitManager) RecordRateLimit() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.rateLimitedCount.Add(1)
	m.consecutiveFailures++
	m.lastFailureTime = time.Now()
}

// RecordSuccess resets consecutive failure count on successful request
func (m *RateLimitManager) RecordSuccess() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.consecutiveFailures = 0
}

// CalculateBackoff calculates exponential backoff duration
func (m *RateLimitManager) CalculateBackoff(attempt int) time.Duration {
	// Exponential backoff: 1s, 2s, 4s, 8s...
	base := time.Second
	backoff := base * (1 << attempt)
	// Cap at 1 minute
	if backoff > time.Minute {
		backoff = time.Minute
	}
	return backoff
}

// GetStatus returns current rate limit status for monitoring
func (m *RateLimitManager) GetStatus() RateLimitStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return RateLimitStatus{
		Remaining:        m.remaining,
		Limit:            m.limit,
		ResetAt:          m.resetAt,
		TotalRequests:    m.totalRequests.Load(),
		ThrottledCount:   m.throttledCount.Load(),
		RateLimitedCount: m.rateLimitedCount.Load(),
	}
}

// Global rate limit manager instance
var globalRateLimitManager = NewRateLimitManager()

type rateLimitedRoundTripper struct {
	next    http.RoundTripper
	manager *RateLimitManager
	// trackBudget controls whether this transport's responses update the
	// manager's remaining/limit/reset view. GitHub meters GraphQL against a
	// budget entirely separate from REST but reports it under the same
	// X-RateLimit-* header names, so letting GraphQL replies write into the
	// shared manager would overwrite the REST reading with an unrelated number —
	// and a nearly-exhausted REST quota would look healthy. GraphQL still waits
	// on the semaphore and the REST throttle, which only makes it more cautious.
	trackBudget bool
}

func (r *rateLimitedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.manager.totalRequests.Add(1)

	// 1. Pre-flight check: block/throttle if needed
	if err := r.manager.WaitIfNeeded(req.Context()); err != nil {
		return nil, err
	}

	// 2. Acquire semaphore slot (burst control)
	select {
	case githubSemaphore <- struct{}{}:
		defer func() { <-githubSemaphore }()
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}

	// 3. Log the API call
	slog.Info("GitHub API Call", "method", req.Method, "url", req.URL.String())

	// 4. Execute request with retry on 429/403 rate limit errors
	var resp *http.Response
	var err error
	for attempt := 0; attempt <= r.manager.maxRetries; attempt++ {
		resp, err = r.next.RoundTrip(req)
		if err != nil {
			return nil, err
		}

		// 5. Update rate limit state from response headers
		if r.trackBudget {
			r.manager.UpdateFromHeaders(resp.Header)
		}

		// 6. Handle rate limit errors (429 or 403 with rate limit message)
		if resp.StatusCode == 429 || (resp.StatusCode == 403 && isRateLimitError(resp)) {
			r.manager.RecordRateLimit()

			// Parse Retry-After header if present
			retryAfter := parseRetryAfter(resp.Header)
			if retryAfter == 0 {
				retryAfter = r.manager.CalculateBackoff(attempt)
			}

			// Must close and discard body before retry
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

			if attempt < r.manager.maxRetries {
				slog.Warn("Rate limited, retrying", "attempt", attempt+1, "backoff", retryAfter, "status", resp.StatusCode)
				select {
				case <-time.After(retryAfter):
					continue
				case <-req.Context().Done():
					return nil, req.Context().Err()
				}
			}

			return nil, fmt.Errorf("rate limited after %d retries (HTTP %d)", attempt+1, resp.StatusCode)
		}

		// Success
		r.manager.RecordSuccess()
		break
	}

	return resp, err
}

// isRateLimitError checks if a 403 response is due to rate limiting
func isRateLimitError(resp *http.Response) bool {
	// GitHub returns specific headers for rate limit 403s
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	// Could also check response body for rate limit message, but header check is sufficient
	return false
}

// parseRetryAfter parses the Retry-After header (seconds or HTTP-date)
func parseRetryAfter(headers http.Header) time.Duration {
	retryAfter := headers.Get("Retry-After")
	if retryAfter == "" {
		return 0
	}

	// Try parsing as seconds
	if seconds, err := strconv.Atoi(retryAfter); err == nil {
		return time.Duration(seconds) * time.Second
	}

	// Try parsing as HTTP-date
	if t, err := time.Parse(time.RFC1123, retryAfter); err == nil {
		return time.Until(t)
	}

	return 0
}

// newAuthedHTTPClient builds the token-authenticated, rate-limited HTTP client
// that both the REST client and the GraphQL client (see review_threads.go) use,
// so GraphQL calls share the same throttling and API-call logging as REST ones.
//
// trackBudget must be false for GraphQL — see rateLimitedRoundTripper.
//
// Returns an error rather than exiting when no token is configured; callers that
// cannot proceed without one (GetGithubClient) still exit.
func newAuthedHTTPClient(trackBudget bool) (*http.Client, error) {
	token := os.Getenv("CRS_GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("CRS_GITHUB_TOKEN is not set")
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(context.Background(), ts)
	next := tc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	tc.Transport = &rateLimitedRoundTripper{
		next:        next,
		manager:     globalRateLimitManager,
		trackBudget: trackBudget,
	}
	return tc, nil
}

func GetGithubClient() *github.Client {
	tc, err := newAuthedHTTPClient(true)
	if err != nil {
		slog.Error("Error! No Github Token!")
		os.Exit(1)
	}
	return github.NewClient(tc)
}

// GetRateLimitStatus returns current rate limit status for monitoring
func GetRateLimitStatus() RateLimitStatus {
	return globalRateLimitManager.GetStatus()
}

// GetRateLimitFromAPI fetches the authoritative rate limit status from GitHub's
// /rate_limit endpoint. Returns limit, remaining, and reset time for the core API.
func GetRateLimitFromAPI(client *github.Client) (limit, remaining int, resetAt time.Time, err error) {
	ctx := context.Background()
	rateLimits, _, apiErr := client.RateLimits(ctx)
	if apiErr != nil {
		return 0, 0, time.Time{}, apiErr
	}
	if rateLimits != nil && rateLimits.Core != nil {
		limit = rateLimits.Core.Limit
		remaining = rateLimits.Core.Remaining
		resetAt = rateLimits.Core.Reset.Time
	}
	return limit, remaining, resetAt, nil
}

func GetLeankitCardTitle(pr PullRequest) string {
	return "abc"
}

func CheckBodyURLNotYetSet(body string) bool {
	return strings.Contains(body, "[Card Title]")

}

func ReplaceURLInBody(body string, title string, url string) string {
	lines := strings.Split(body, "\n")
	output_lines := []string{}
	for _, line := range lines {
		if strings.Contains(line, "[Card Title]") {
			output_lines = append(output_lines, fmt.Sprintf("[%s](%s)", title, url))
		} else {
			output_lines = append(output_lines, line)
		}
	}
	return strings.Join(output_lines, "\n")
}

func UpdatePRBody(pr *github.PullRequest, new_body string) bool {
	client := GetGithubClient()
	ctx := context.Background()

	pr.Body = &new_body

	pr, _, err := client.PullRequests.Edit(ctx, *pr.Base.Repo.Owner.Login, *pr.Base.Repo.Name, *pr.Number, pr)
	if err != nil {
		slog.Error("Error editing PR", "error", err)
		return false
	}
	return true
}

func FilterPRsByAssignedTeam(prs []*github.PullRequest, target_team string) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		for _, team := range pr.RequestedTeams {
			if *team.Name == target_team {
				filtered = append(filtered, pr)
				continue
			}
		}
	}
	return filtered
}

func SubmitReview(client *github.Client, owner string, repo string, number int, review *github.PullRequestReviewRequest) error {
	ctx := context.Background()
	_, _, err := client.PullRequests.CreateReview(ctx, owner, repo, number, review)
	return err
}

// MergePR asks GitHub to merge the given PR using the supplied merge method
// ("merge", "squash", or "rebase"). We pass through whatever GitHub returns;
// the caller is responsible for interpreting Merged / Message fields rather
// than inferring success locally.
func MergePR(client *github.Client, owner string, repo string, number int, method string) (*github.PullRequestMergeResult, error) {
	ctx := context.Background()
	opts := &github.PullRequestOptions{MergeMethod: method}
	result, _, err := client.PullRequests.Merge(ctx, owner, repo, number, "", opts)
	return result, err
}

func SubmitReply(client *github.Client, owner string, repo string, number int, body string, replyToID int64) error {
	ctx := context.Background()
	comment := &github.PullRequestComment{
		Body:      &body,
		InReplyTo: &replyToID,
	}
	_, _, err := client.PullRequests.CreateComment(ctx, owner, repo, number, comment)
	return err
}

// GetCombinedStatus returns every commit status on a ref. The response is a
// struct wrapping a paginated Statuses list, so a repo with more contexts than
// one page would otherwise report CI as incomplete. Only Statuses accumulates;
// the summary fields come from the first page.
func GetCombinedStatus(client *github.Client, owner, repo, ref string) (*github.CombinedStatus, error) {
	ctx := context.Background()
	var combined *github.CombinedStatus
	opts := &github.ListOptions{PerPage: githubPageSize, Page: 1}
	for i := 0; i < githubMaxPages; i++ {
		status, resp, err := client.Repositories.GetCombinedStatus(ctx, owner, repo, ref, opts)
		if err != nil {
			return combined, err
		}
		if combined == nil {
			combined = status
		} else if status != nil {
			combined.Statuses = append(combined.Statuses, status.Statuses...)
		}
		if resp == nil || resp.NextPage == 0 {
			return combined, nil
		}
		opts.Page = resp.NextPage
	}
	slog.Warn("Paginated fetch hit the page cap; results are truncated",
		"endpoint", "commits/status", "max_pages", githubMaxPages, "ref", ref)
	return combined, nil
}

// GetCheckRuns returns every check run on a ref, for the same reason as
// GetCombinedStatus: a busy repo has more than one page of checks, and a
// truncated list silently misreports the PR's CI state.
func GetCheckRuns(client *github.Client, owner, repo, ref string) (*github.ListCheckRunsResults, error) {
	ctx := context.Background()
	var all *github.ListCheckRunsResults
	opts := &github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: githubPageSize, Page: 1}}
	for i := 0; i < githubMaxPages; i++ {
		checkRuns, resp, err := client.Checks.ListCheckRunsForRef(ctx, owner, repo, ref, opts)
		if err != nil {
			return all, err
		}
		if all == nil {
			all = checkRuns
		} else if checkRuns != nil {
			all.CheckRuns = append(all.CheckRuns, checkRuns.CheckRuns...)
		}
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = resp.NextPage
	}
	slog.Warn("Paginated fetch hit the page cap; results are truncated",
		"endpoint", "commits/check-runs", "max_pages", githubMaxPages, "ref", ref)
	return all, nil
}

func CreateWorktree(repoDir, branch, worktreePath string) error {
	// Ensure the worktree directory doesn't exist (git worktree add will fail if it does, but maybe we want to be clean)
	// Actually let git handle it.

	// git worktree add <path> <branch>
	cmd := exec.Command("git", "worktree", "add", worktreePath, branch)
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		if strings.Contains(outputStr, "already exists") {
			slog.Info("Worktree already exists, skipping creation", "repo", repoDir, "branch", branch, "path", worktreePath)
			return nil
		}
		slog.Error("Failed to create worktree", "repo", repoDir, "branch", branch, "path", worktreePath, "output", outputStr, "error", err)
		return fmt.Errorf("git worktree add failed: %s: %w", outputStr, err)
	}
	slog.Info("Created worktree", "repo", repoDir, "branch", branch, "path", worktreePath)
	return nil
}

func RemoveWorktree(repoDir, worktreePath string) error {
	// git worktree remove <path> --force
	cmd := exec.Command("git", "worktree", "remove", worktreePath, "--force")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If the worktree is already gone from disk, git might complain.
		// We might want to check if the path exists first, but git worktree prune might be needed.
		slog.Warn("Failed to remove worktree (might already be gone)", "repo", repoDir, "path", worktreePath, "output", string(output), "error", err)
		// We try to continue to prune anyway
	}

	// git worktree prune
	cmdPrune := exec.Command("git", "worktree", "prune")
	cmdPrune.Dir = repoDir
	if out, err := cmdPrune.CombinedOutput(); err != nil {
		slog.Warn("Failed to prune worktrees", "repo", repoDir, "output", string(out), "error", err)
	}

	// Ensure the directory is actually gone (if git failed to remove it for some reason but we want it gone)
	if _, err := os.Stat(worktreePath); err == nil {
		slog.Info("Worktree directory still exists, removing manually", "path", worktreePath)
		if err := os.RemoveAll(worktreePath); err != nil {
			return fmt.Errorf("failed to remove worktree directory: %w", err)
		}
	}

	return nil
}

type CIStatusInfo struct {
	Statuses      []string
	OverallStatus string
}

type CacheEntry struct {
	Value      interface{}
	Expiration time.Time
}

type DataCache struct {
	mu    sync.RWMutex
	store map[string]CacheEntry
}

func NewDataCache() *DataCache {
	return &DataCache{
		store: make(map[string]CacheEntry),
	}
}

func (c *DataCache) Set(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[key] = CacheEntry{
		Value:      value,
		Expiration: time.Now().Add(ttl),
	}
}

func (c *DataCache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, found := c.store[key]
	if !found {
		return nil, false
	}
	if time.Now().After(entry.Expiration) {
		return nil, false
	}
	return entry.Value, true
}

var GlobalCache = NewDataCache()

func GetCIStatus(owner string, repo string, branch string) CIStatusInfo {
	cacheKey := fmt.Sprintf("ci_status:%s/%s:%s", owner, repo, branch)
	if val, found := GlobalCache.Get(cacheKey); found {
		return val.(CIStatusInfo)
	}

	client := GetGithubClient()
	// branch typically comes as "username:branch_name" from GitHub API
	branchParts := strings.Split(branch, ":")
	apiBranch := branch
	if len(branchParts) > 1 {
		apiBranch = branchParts[1]
	}

	opts := github.ListWorkflowRunsOptions{Branch: apiBranch}
	runs, _, err := client.Actions.ListRepositoryWorkflowRuns(context.Background(), owner, repo, &opts)

	if err != nil {
		slog.Error("Error getting workflow runs", "error", err, "owner", owner, "repo", repo, "branch", apiBranch)
		return CIStatusInfo{Statuses: []string{}, OverallStatus: "TODO"}
	}

	var statuses []string
	hasFailure := false
	hasInProgress := false
	allSuccess := true

	processedRuns := ProcessWorkflowRuns(runs.WorkflowRuns)

	for _, run := range processedRuns {
		status := "<nil>" // completed, in_progress
		if run.Status != nil {
			status = "[" + *run.Status + "]"
			if *run.Status == "in_progress" {
				hasInProgress = true
			}
		}
		conclusion := " "
		if run.Conclusion != nil {
			if *run.Conclusion == "success" {
				conclusion = "✅"
				status = "" // We know the status if it was a success
			} else if *run.Conclusion == "failure" {
				conclusion = "❌"
				hasFailure = true
				allSuccess = false
			} else if *run.Conclusion != "success" {
				allSuccess = false // Any conclusion that is not success means not all are success
			}
		} else {
			// If conclusion is nil, it might still be in progress or pending
			allSuccess = false
			if run.Status != nil && *run.Status != "completed" {
				hasInProgress = true
			}
		}

		name := "Unknown Workflow Name"
		if run.Name != nil {
			name = *run.Name
		}

		item := fmt.Sprintf("[%s] %s %s", conclusion, status, name)
		statuses = append(statuses, item)
	}

	overallStatus := ""
	if hasFailure {
		overallStatus = "TODO"
	} else if hasInProgress {
		overallStatus = "WAITING"
	} else if allSuccess && len(processedRuns) > 0 {
		overallStatus = "DONE"
	} else {
		// Default to DONE if no failures and no in_progress, assuming non-success conclusions are also acceptable for DONE.
		overallStatus = "DONE"
	}

	info := CIStatusInfo{Statuses: statuses, OverallStatus: overallStatus}
	GlobalCache.Set(cacheKey, info, 5*time.Minute)
	return info
}

func ProcessWorkflowRuns(runs []*github.WorkflowRun) []*github.WorkflowRun {
	latestPerName := make(map[string]*github.WorkflowRun)
	for _, run := range runs {
		if run == nil || run.Name == nil {
			continue
		}
		latestByName, exists := latestPerName[*run.Name]
		if !exists || (run.CreatedAt != nil && (*run.CreatedAt).After(latestByName.CreatedAt.Time)) {
			latestPerName[*run.Name] = run
		}
	}

	var output []*github.WorkflowRun
	for _, run := range latestPerName {
		output = append(output, run)
	}
	return output
}

func FilterCIPassing(prs []*github.PullRequest) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		info := GetCIStatus(*pr.Base.Repo.Owner.Login, *pr.Head.Repo.Name, *pr.Head.Label)
		if info.OverallStatus == "DONE" {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func FilterCIFailing(prs []*github.PullRequest) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		info := GetCIStatus(*pr.Base.Repo.Owner.Login, *pr.Head.Repo.Name, *pr.Head.Label)
		if info.OverallStatus == "TODO" {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func FilterStale(prs []*github.PullRequest) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	threshold := time.Now().Add(-3 * 24 * time.Hour)
	for _, pr := range prs {
		if pr.UpdatedAt != nil && pr.UpdatedAt.Before(threshold) {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func FilterNotStale(prs []*github.PullRequest) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	threshold := time.Now().Add(-3 * 24 * time.Hour)
	for _, pr := range prs {
		if pr.UpdatedAt != nil && pr.UpdatedAt.After(threshold) {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

type InteractionState struct {
	LastMeTime             time.Time
	LastOthersTime         time.Time
	LastCommitTime         time.Time
	HasUnrespondedComments bool
	// MyReviewDismissed is true when the most recent review I submitted has been
	// dismissed (stale-review dismissal, a CODEOWNERS re-request, or a manual
	// dismissal). GitHub does not put me back in RequestedReviewers in that case,
	// so this is the only signal that I owe a re-review.
	MyReviewDismissed bool
}

func CalculateInteractionState(myLogin string, pr *github.PullRequest, reviews []*github.PullRequestReview, reviewComments []*github.PullRequestComment, issueComments []*github.IssueComment) InteractionState {
	state := InteractionState{}

	if pr == nil {
		return state
	}

	// Fetch Reviews
	//
	// myVerdictTime is when I last submitted an explicit verdict (APPROVED or
	// CHANGES_REQUESTED). A verdict is a holistic response to the whole PR:
	// anything others said before it was on my screen when I judged, so a
	// thread reply older than the verdict is not "waiting on me" — without
	// this, one "done!" reply from the author pins the PR in Waiting on Me
	// forever, straight through my approval. COMMENTED reviews don't count as
	// verdicts because GitHub wraps every standalone inline comment in an
	// implicit COMMENTED review — treating those as PR-wide responses would
	// let a drive-by comment in one thread clear every other thread. A later
	// dismissal rewrites the review's state to DISMISSED, so a dismissed
	// verdict stops clearing anything and MyReviewDismissed flags the PR
	// instead.
	var myLatestReview *github.PullRequestReview
	var myVerdictTime time.Time
	for _, r := range reviews {
		if r == nil || r.SubmittedAt == nil || r.User == nil || r.User.Login == nil {
			continue
		}
		if strings.EqualFold(*r.User.Login, myLogin) {
			if myLatestReview == nil || r.SubmittedAt.After(myLatestReview.SubmittedAt.Time) {
				myLatestReview = r
			}
			if (r.GetState() == "APPROVED" || r.GetState() == "CHANGES_REQUESTED") && r.SubmittedAt.After(myVerdictTime) {
				myVerdictTime = r.SubmittedAt.Time
			}
			if r.SubmittedAt.After(state.LastMeTime) {
				state.LastMeTime = r.SubmittedAt.Time
			}
		} else {
			if r.SubmittedAt.After(state.LastOthersTime) {
				state.LastOthersTime = r.SubmittedAt.Time
			}
		}
	}
	state.MyReviewDismissed = myLatestReview != nil && myLatestReview.GetState() == "DISMISSED"

	// Fetch Review Comments
	// Group comments by their "thread", remembering who spoke last and when.
	type lastComment struct {
		login string
		at    time.Time
	}
	threads := make(map[int64]lastComment)
	participation := make(map[int64]bool)

	for _, c := range reviewComments {
		if c == nil || c.ID == nil || c.User == nil || c.User.Login == nil {
			continue
		}

		rootID := *c.ID
		if c.InReplyTo != nil {
			rootID = *c.InReplyTo
		}

		var at time.Time
		if c.CreatedAt != nil {
			at = c.CreatedAt.Time
		}
		if prev, ok := threads[rootID]; !ok || !at.Before(prev.at) {
			threads[rootID] = lastComment{login: *c.User.Login, at: at}
		}

		if strings.EqualFold(*c.User.Login, myLogin) {
			participation[rootID] = true
			if c.CreatedAt != nil && c.CreatedAt.After(state.LastMeTime) {
				state.LastMeTime = c.CreatedAt.Time
			}
		} else {
			if c.CreatedAt != nil && c.CreatedAt.After(state.LastOthersTime) {
				state.LastOthersTime = c.CreatedAt.Time
			}
		}
	}

	// A thread is waiting on me only if someone else spoke last in it AND
	// that happened after my latest verdict (see myVerdictTime above).
	for rootID, last := range threads {
		if participation[rootID] && !strings.EqualFold(last.login, myLogin) && last.at.After(myVerdictTime) {
			state.HasUnrespondedComments = true
			break
		}
	}

	// Fetch Issue Comments
	//
	// The conversation tab is treated as one more thread, except the bar for
	// "I responded" is any later activity of mine (LastMeTime), not only a
	// verdict. Only conversation-tab comments count as the dangling reply:
	// this used to compare against LastOthersTime, which also moves on other
	// people's reviews — so a third reviewer approving after me would re-flag
	// any PR I had ever conversation-commented on.
	iParticipated := false
	var lastOtherIssueAt time.Time
	for _, c := range issueComments {
		if c == nil || c.User == nil || c.User.Login == nil {
			continue
		}
		if strings.EqualFold(*c.User.Login, myLogin) {
			iParticipated = true
			if c.CreatedAt != nil && c.CreatedAt.After(state.LastMeTime) {
				state.LastMeTime = c.CreatedAt.Time
			}
		} else {
			if c.CreatedAt != nil {
				if c.CreatedAt.After(state.LastOthersTime) {
					state.LastOthersTime = c.CreatedAt.Time
				}
				if c.CreatedAt.After(lastOtherIssueAt) {
					lastOtherIssueAt = c.CreatedAt.Time
				}
			}
		}
	}
	if iParticipated && lastOtherIssueAt.After(state.LastMeTime) {
		state.HasUnrespondedComments = true
	}

	return state
}

// githubPageSize / githubMaxPages bound every paginated fetch in this package.
// GitHub caps a PR's commit list at 250 entries, and a PR with more than 500
// comments is far past the point where any of this matters, so five pages is
// plenty while keeping the worst case at five calls per list.
const (
	githubPageSize = 100
	githubMaxPages = 5

	// interactionStateConcurrency bounds how many candidate PRs have their
	// interaction state fetched at once. Each GetInteractionState call fans
	// out four HTTP requests of its own, so this caps a filter pass at ~4×
	// this many in-flight calls instead of four per candidate PR — a burst
	// that trips GitHub's secondary rate limits on large repos.
	interactionStateConcurrency = 8
)

// ListAllPages walks the paginated endpoint fetch until it runs out of pages (or
// hits githubMaxPages), returning everything it collected. On error it returns
// the pages gathered so far alongside the error, so callers can still work with
// a partial history rather than falling back to nothing.
//
// Every GitHub list endpoint is paginated, defaults to 30 items per page, and
// returns oldest-first. A caller that passes nil options therefore gets the
// *start* of a PR's history and silently loses the newest entries — the newest
// review, the latest push — which is the opposite of what any caller wants.
// Route every list call through here rather than calling the client directly.
//
// label names the endpoint for the page-cap warning; truncating at githubMaxPages
// is the same silent-data-loss failure this helper exists to prevent, so it is
// logged rather than swallowed.
func ListAllPages[T any](label string, fetch func(opts *github.ListOptions) ([]T, *github.Response, error)) ([]T, error) {
	var all []T
	opts := &github.ListOptions{PerPage: githubPageSize, Page: 1}
	for i := 0; i < githubMaxPages; i++ {
		page, resp, err := fetch(opts)
		if err != nil {
			return all, err
		}
		all = append(all, page...)
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = resp.NextPage
	}
	slog.Warn("Paginated fetch hit the page cap; results are truncated",
		"endpoint", label, "max_pages", githubMaxPages, "per_page", githubPageSize, "collected", len(all))
	return all, nil
}

// ListAllPRReviews returns every review on a PR, not just the first page.
func ListAllPRReviews(ctx context.Context, client *github.Client, owner, repo string, number int) ([]*github.PullRequestReview, error) {
	return ListAllPages("pulls/reviews", func(opts *github.ListOptions) ([]*github.PullRequestReview, *github.Response, error) {
		return client.PullRequests.ListReviews(ctx, owner, repo, number, opts)
	})
}

// ListAllPRComments returns every review comment (comment on code) on a PR.
func ListAllPRComments(ctx context.Context, client *github.Client, owner, repo string, number int) ([]*github.PullRequestComment, error) {
	return ListAllPages("pulls/comments", func(opts *github.ListOptions) ([]*github.PullRequestComment, *github.Response, error) {
		return client.PullRequests.ListComments(ctx, owner, repo, number,
			&github.PullRequestListCommentsOptions{ListOptions: *opts})
	})
}

// ListAllIssueComments returns every conversation comment on a PR.
func ListAllIssueComments(ctx context.Context, client *github.Client, owner, repo string, number int) ([]*github.IssueComment, error) {
	return ListAllPages("issues/comments", func(opts *github.ListOptions) ([]*github.IssueComment, *github.Response, error) {
		return client.Issues.ListComments(ctx, owner, repo, number,
			&github.IssueListCommentsOptions{ListOptions: *opts})
	})
}

// ListAllPRCommits returns every commit on a PR, up to GitHub's own 250 cap.
func ListAllPRCommits(ctx context.Context, client *github.Client, owner, repo string, number int) ([]*github.RepositoryCommit, error) {
	return ListAllPages("pulls/commits", func(opts *github.ListOptions) ([]*github.RepositoryCommit, *github.Response, error) {
		return client.PullRequests.ListCommits(ctx, owner, repo, number, opts)
	})
}

// ListAllRequestedReviewers returns every pending reviewer on a PR. The endpoint
// returns a struct holding two slices rather than a single list, so it needs its
// own accumulation loop instead of ListAllPages.
func ListAllRequestedReviewers(ctx context.Context, client *github.Client, owner, repo string, number int) (*github.Reviewers, error) {
	all := &github.Reviewers{}
	opts := &github.ListOptions{PerPage: githubPageSize, Page: 1}
	for i := 0; i < githubMaxPages; i++ {
		page, resp, err := client.PullRequests.ListReviewers(ctx, owner, repo, number, opts)
		if err != nil {
			return all, err
		}
		if page != nil {
			all.Users = append(all.Users, page.Users...)
			all.Teams = append(all.Teams, page.Teams...)
		}
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = resp.NextPage
	}
	slog.Warn("Paginated fetch hit the page cap; results are truncated",
		"endpoint", "pulls/requested_reviewers", "max_pages", githubMaxPages,
		"users", len(all.Users), "teams", len(all.Teams))
	return all, nil
}

// latestCommitTime returns the newest commit timestamp in commits. It scans for
// the maximum rather than trusting the last element: pagination and rebases both
// hand back lists whose final entry is not the most recent commit, and an
// under-reported "last push" time makes a PR look like I have already responded
// to it.
func latestCommitTime(commits []*github.RepositoryCommit) time.Time {
	var latest time.Time
	for _, c := range commits {
		if c == nil || c.Commit == nil {
			continue
		}
		for _, ts := range []*github.CommitAuthor{c.Commit.Committer, c.Commit.Author} {
			if ts != nil && ts.Date != nil && ts.Date.After(latest) {
				latest = ts.Date.Time
			}
		}
	}
	return latest
}

func GetInteractionState(owner, repo string, pr *github.PullRequest) InteractionState {
	if pr == nil || pr.Number == nil {
		return InteractionState{}
	}

	cacheKey := fmt.Sprintf("interaction_state:%s/%s:%d", owner, repo, *pr.Number)
	if val, found := GlobalCache.Get(cacheKey); found {
		return val.(InteractionState)
	}

	client := GetGithubClient()
	// Add timeout to prevent hanging indefinitely
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	myLogin := config.C().GithubUsername

	// Use goroutines to fetch data in parallel instead of sequentially
	type result struct {
		reviews        []*github.PullRequestReview
		reviewComments []*github.PullRequestComment
		issueComments  []*github.IssueComment
		commits        []*github.RepositoryCommit
		err            error
	}

	resultChan := make(chan result, 4)

	// Every list below is paginated; see ListAllPages for why calling the client
	// directly loses the newest entries. These fetches share the timeout ctx above
	// rather than using the ListAll* helpers, which fetch on context.Background().

	// Fetch Reviews
	go func() {
		reviews, err := ListAllPages("pulls/reviews", func(opts *github.ListOptions) ([]*github.PullRequestReview, *github.Response, error) {
			return client.PullRequests.ListReviews(ctx, owner, repo, *pr.Number, opts)
		})
		if err != nil {
			slog.Warn("Error listing reviews", "owner", owner, "repo", repo, "number", *pr.Number, "error", err)
		}
		resultChan <- result{reviews: reviews, err: err}
	}()

	// Fetch Review Comments
	go func() {
		reviewComments, err := ListAllPages("pulls/comments", func(opts *github.ListOptions) ([]*github.PullRequestComment, *github.Response, error) {
			return client.PullRequests.ListComments(ctx, owner, repo, *pr.Number,
				&github.PullRequestListCommentsOptions{ListOptions: *opts})
		})
		if err != nil {
			slog.Warn("Error listing review comments", "owner", owner, "repo", repo, "number", *pr.Number, "error", err)
		}
		resultChan <- result{reviewComments: reviewComments, err: err}
	}()

	// Fetch Issue Comments
	go func() {
		issueComments, err := ListAllPages("issues/comments", func(opts *github.ListOptions) ([]*github.IssueComment, *github.Response, error) {
			return client.Issues.ListComments(ctx, owner, repo, *pr.Number,
				&github.IssueListCommentsOptions{ListOptions: *opts})
		})
		if err != nil {
			slog.Warn("Error listing issue comments", "owner", owner, "repo", repo, "number", *pr.Number, "error", err)
		}
		resultChan <- result{issueComments: issueComments, err: err}
	}()

	// Fetch Commits
	go func() {
		commits, err := ListAllPages("pulls/commits", func(opts *github.ListOptions) ([]*github.RepositoryCommit, *github.Response, error) {
			return client.PullRequests.ListCommits(ctx, owner, repo, *pr.Number, opts)
		})
		if err != nil {
			slog.Warn("Error listing commits", "owner", owner, "repo", repo, "number", *pr.Number, "error", err)
		}
		resultChan <- result{commits: commits, err: err}
	}()

	// Collect results
	var reviews []*github.PullRequestReview
	var reviewComments []*github.PullRequestComment
	var issueComments []*github.IssueComment
	var commits []*github.RepositoryCommit
	hasError := false

	for i := 0; i < 4; i++ {
		res := <-resultChan
		if res.err != nil {
			hasError = true
		}
		if res.reviews != nil {
			reviews = res.reviews
		}
		if res.reviewComments != nil {
			reviewComments = res.reviewComments
		}
		if res.issueComments != nil {
			issueComments = res.issueComments
		}
		if res.commits != nil {
			commits = res.commits
		}
	}

	state := CalculateInteractionState(myLogin, pr, reviews, reviewComments, issueComments)

	// Set LastCommitTime from commits
	state.LastCommitTime = latestCommitTime(commits)

	// Cache the result even if there were errors (with shorter TTL)
	// This prevents retry storms
	ttl := 10 * time.Minute
	if hasError {
		ttl = 2 * time.Minute // Shorter cache for partial/error states
	}
	GlobalCache.Set(cacheKey, state, ttl)
	return state
}

func GetMyTeams() []string {
	cacheKey := "my_teams"
	if val, found := GlobalCache.Get(cacheKey); found {
		return val.([]string)
	}

	client := GetGithubClient()
	ctx := context.Background()

	// List teams for the authenticated user
	// Note: go-github v48 might have different structure. Checking...
	teams, _, err := client.Teams.ListUserTeams(ctx, nil)
	if err != nil {
		slog.Error("Error fetching user teams", "error", err)
		return []string{}
	}

	slugs := []string{}
	for _, t := range teams {
		if t.Slug != nil {
			slugs = append(slugs, *t.Slug)
		}
	}

	GlobalCache.Set(cacheKey, slugs, 30*time.Minute)
	return slugs
}

// reviewRequestedFrom reports whether login has an open personal review request
// on the PR. GitHub logins are case-insensitive, so the comparison uses
// EqualFold: a casing mismatch between the configured GithubUsername and the
// login returned by the API would otherwise match nobody.
func reviewRequestedFrom(pr *github.PullRequest, login string) bool {
	for _, r := range pr.RequestedReviewers {
		if r != nil && r.Login != nil && strings.EqualFold(*r.Login, login) {
			return true
		}
	}
	return false
}

// FilterWaitingOnMe filters against the root-level GithubUsername. Workflows
// build their filters with MakeWaitingOnMeFilter so a per-workflow
// GithubUsername is honored and an unset one is caught at build time.
func FilterWaitingOnMe(prs []*github.PullRequest) []*github.PullRequest {
	return MakeWaitingOnMeFilter(config.C().GithubUsername)(prs)
}

// MakeWaitingOnMeFilter returns a filter matching PRs that need action from
// myLogin. An empty myLogin matches nothing — every check below is an identity
// comparison — so callers must reject that before building the filter.
func MakeWaitingOnMeFilter(myLogin string) PRFilter {
	return func(prs []*github.PullRequest) []*github.PullRequest {
		return filterWaitingOnMe(prs, myLogin)
	}
}

func filterWaitingOnMe(prs []*github.PullRequest, myLogin string) []*github.PullRequest {
	if myLogin == "" {
		// Without an identity every comparison in here is against "", so the
		// filter would quietly match nothing and leave the section empty with no
		// indication of why.
		slog.Error("FilterWaitingOnMe cannot match anything: GithubUsername is not configured")
		return []*github.PullRequest{}
	}

	// A PR is "Waiting on me" if:
	// 1. I have a pending review request on it, OR
	// 2. my last review was dismissed (so I owe a re-review), OR
	// 3. I have unresponded comments.
	//
	// Case 1 is deliberately unconditional. GitHub clears a review request
	// the moment the requested reviewer submits a review, and "re-request
	// review" puts them back into RequestedReviewers — so a login sitting in
	// RequestedReviewers *now* always means an outstanding request, however
	// recently that person reviewed or commented. This used to be gated on
	// "have I acted since the last push / last other action", which hid
	// exactly the re-review case: the prior review is newer than both the
	// last commit and the last comment, so every re-request was filtered out.
	//
	// Case 1 is also free: RequestedReviewers rides along on the PR list we
	// already fetched. Cases 2 and 3 need GetInteractionState's four fetches,
	// but both require some past review or comment of mine, so the
	// interaction set (two search calls, shared and cached — see
	// interaction_prefilter.go) rules them out for most PRs without fetching
	// anything. Only PRs that pass the prefilter pay for a state fetch.
	interacted := SearchMyOpenInteractions(myLogin)

	// Process the remaining PRs concurrently to avoid sequential API calls
	type prResult struct {
		pr           *github.PullRequest
		shouldFilter bool
		prefiltered  bool
	}

	resultChan := make(chan prResult)
	var wg sync.WaitGroup
	sem := make(chan struct{}, interactionStateConcurrency)

	for _, pr := range prs {
		if pr == nil || pr.Base == nil || pr.Base.Repo == nil || pr.Base.Repo.Owner == nil || pr.Base.Repo.Owner.Login == nil || pr.Base.Repo.Name == nil {
			continue
		}

		wg.Add(1)
		go func(pr *github.PullRequest) {
			defer wg.Done()

			// Case 1: open review request, no API calls needed.
			if reviewRequestedFrom(pr, myLogin) {
				slog.Debug("FilterWaitingOnMe",
					"repo", *pr.Base.Repo.Name, "number", *pr.Number,
					"included", true, "requested", true)
				resultChan <- prResult{pr: pr, shouldFilter: true}
				return
			}

			// Cases 2 and 3 are impossible without a past interaction of mine.
			if !interacted.MayHaveInteracted(pr) {
				resultChan <- prResult{pr: pr, shouldFilter: false, prefiltered: true}
				return
			}

			sem <- struct{}{}
			state := GetInteractionState(*pr.Base.Repo.Owner.Login, *pr.Base.Repo.Name, pr)
			<-sem

			shouldFilter := state.MyReviewDismissed || state.HasUnrespondedComments

			slog.Debug("FilterWaitingOnMe",
				"repo", *pr.Base.Repo.Name, "number", *pr.Number, "included", shouldFilter,
				"requested", false, "review_dismissed", state.MyReviewDismissed,
				"unresponded_comments", state.HasUnrespondedComments,
				"last_me", state.LastMeTime, "last_others", state.LastOthersTime,
				"last_commit", state.LastCommitTime)

			resultChan <- prResult{pr: pr, shouldFilter: shouldFilter}
		}(pr)
	}

	// Close channel when all goroutines complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	filtered := []*github.PullRequest{}
	prefiltered := 0
	for result := range resultChan {
		if result.shouldFilter {
			filtered = append(filtered, result.pr)
		}
		if result.prefiltered {
			prefiltered++
		}
	}

	// One line per cycle saying how many PRs survived. An empty section is
	// otherwise indistinguishable from a section nobody asked a review on.
	// prefilter_skipped says how many candidates were decided without any
	// per-PR API calls; ~0 on a big candidate list means the prefilter is
	// failing open (see the error log in SearchMyOpenInteractions).
	slog.Info("FilterWaitingOnMe", "user", myLogin, "candidates", len(prs),
		"matched", len(filtered), "prefilter_skipped", prefiltered)

	return filtered
}

// FilterWaitingOnAuthor filters against the root-level GithubUsername. See
// FilterWaitingOnMe for why workflows use the Make… variant instead.
func FilterWaitingOnAuthor(prs []*github.PullRequest) []*github.PullRequest {
	return MakeWaitingOnAuthorFilter(config.C().GithubUsername)(prs)
}

// MakeWaitingOnAuthorFilter returns a filter matching PRs where myLogin acted
// last and the author owes the next move. As with MakeWaitingOnMeFilter, an
// empty myLogin can only match nothing.
func MakeWaitingOnAuthorFilter(myLogin string) PRFilter {
	return func(prs []*github.PullRequest) []*github.PullRequest {
		return filterWaitingOnAuthor(prs, myLogin)
	}
}

func filterWaitingOnAuthor(prs []*github.PullRequest, myLogin string) []*github.PullRequest {
	if myLogin == "" {
		slog.Error("FilterWaitingOnAuthor cannot match anything: GithubUsername is not configured")
		return []*github.PullRequest{}
	}

	// "Waiting on the author" means I acted last and nothing new has come
	// back since. As in filterWaitingOnMe, two cheap checks decide most PRs
	// before paying for a GetInteractionState fetch:
	//
	//   - An open review request on me (free to check) outranks the
	//     timestamps: the ball is back in my court, not the author's. Without
	//     this a re-requested PR would show up in both the "Waiting on Me"
	//     and "Waiting on Author" sections.
	//   - "I acted last" requires that I acted at all: actedSinceOthers needs
	//     a nonzero LastMeTime, and only a review or comment of mine sets it.
	//     A PR outside the interaction set (see interaction_prefilter.go)
	//     cannot match, so its state fetch is skipped.
	interacted := SearchMyOpenInteractions(myLogin)

	// Process the remaining PRs concurrently to avoid sequential API calls
	type prResult struct {
		pr           *github.PullRequest
		shouldFilter bool
	}

	resultChan := make(chan prResult)
	var wg sync.WaitGroup
	sem := make(chan struct{}, interactionStateConcurrency)

	for _, pr := range prs {
		if pr == nil || pr.Base == nil || pr.Base.Repo == nil || pr.Base.Repo.Owner == nil || pr.Base.Repo.Owner.Login == nil || pr.Base.Repo.Name == nil {
			continue
		}

		wg.Add(1)
		go func(pr *github.PullRequest) {
			defer wg.Done()

			if reviewRequestedFrom(pr, myLogin) {
				resultChan <- prResult{pr: pr, shouldFilter: false}
				return
			}

			if !interacted.MayHaveInteracted(pr) {
				resultChan <- prResult{pr: pr, shouldFilter: false}
				return
			}

			sem <- struct{}{}
			state := GetInteractionState(*pr.Base.Repo.Owner.Login, *pr.Base.Repo.Name, pr)
			<-sem

			actedSinceOthers := !state.LastMeTime.IsZero() && state.LastMeTime.After(state.LastOthersTime) && state.LastMeTime.After(state.LastCommitTime)

			// A dismissal of my last review also puts the ball back in my
			// court (the open-request case was already handled above).
			resultChan <- prResult{pr: pr, shouldFilter: actedSinceOthers && !state.MyReviewDismissed}
		}(pr)
	}

	// Close channel when all goroutines complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	filtered := []*github.PullRequest{}
	for result := range resultChan {
		if result.shouldFilter {
			filtered = append(filtered, result.pr)
		}
	}

	return filtered
}

func MakeLabelFilter(targetLabel string) PRFilter {
	return func(prs []*github.PullRequest) []*github.PullRequest {
		filtered := []*github.PullRequest{}
		for _, pr := range prs {
			for _, label := range pr.Labels {
				if label.Name != nil && *label.Name == targetLabel {
					filtered = append(filtered, pr)
					break
				}
			}
		}
		return filtered
	}
}
func MakeAuthorFilter(authorLogin string) PRFilter {
	return func(prs []*github.PullRequest) []*github.PullRequest {
		return FilterPRsByAuthor(prs, authorLogin)
	}
}

func MakeExcludeAuthorFilter(authorLogin string) PRFilter {
	return func(prs []*github.PullRequest) []*github.PullRequest {
		return FilterPRsExcludeAuthor(prs, authorLogin)
	}
}

// GetFileContent fetches the content of a file from a GitHub repository at a given ref (branch, tag, or SHA).
func GetFileContent(client *github.Client, owner, repo, path, ref string) (string, error) {
	opts := &github.RepositoryContentGetOptions{Ref: ref}
	fileContent, _, _, err := client.Repositories.GetContents(context.Background(), owner, repo, path, opts)
	if err != nil {
		return "", fmt.Errorf("error fetching file content for %s at ref %s: %w", path, ref, err)
	}
	if fileContent == nil {
		return "", fmt.Errorf("path %s is a directory, not a file", path)
	}
	content, err := fileContent.GetContent()
	if err != nil {
		return "", fmt.Errorf("error decoding file content for %s: %w", path, err)
	}
	return content, nil
}
