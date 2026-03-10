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

	"github.com/google/go-github/v48/github"
	"golang.org/x/oauth2"
)

type PullRequest interface {
}

type PRFilter func([]*github.PullRequest) []*github.PullRequest

func GetPRs(client *github.Client, state string, owner string, repo string) ([]*github.PullRequest, error) {
	per_page := 100
	options := github.PullRequestListOptions{State: state, ListOptions: github.ListOptions{PerPage: per_page, Page: 1}}
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
	opts := github.PullRequestListCommentsOptions{}
	comments, _, err := client.PullRequests.ListComments(context.Background(), owner, repo, number, &opts)
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

func FilterMyReviewRequested(prs []*github.PullRequest) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		for _, reviewer := range pr.RequestedReviewers {
			if strings.EqualFold(*reviewer.Login, config.C().GithubUsername) {
				filtered = append(filtered, pr)
				break
			}
		}
	}
	return filtered
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
		r.manager.UpdateFromHeaders(resp.Header)

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

func GetGithubClient() *github.Client {
	ctx := context.Background()
	token := os.Getenv("CRS_GITHUB_TOKEN")
	if token == "" {
		slog.Error("Error! No Github Token!")
		os.Exit(1)
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	next := tc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	tc.Transport = &rateLimitedRoundTripper{
		next:    next,
		manager: globalRateLimitManager,
	}
	return github.NewClient(tc)
}

// GetRateLimitStatus returns current rate limit status for monitoring
func GetRateLimitStatus() RateLimitStatus {
	return globalRateLimitManager.GetStatus()
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

func SubmitReply(client *github.Client, owner string, repo string, number int, body string, replyToID int64) error {
	ctx := context.Background()
	comment := &github.PullRequestComment{
		Body:      &body,
		InReplyTo: &replyToID,
	}
	_, _, err := client.PullRequests.CreateComment(ctx, owner, repo, number, comment)
	return err
}

func GetCombinedStatus(client *github.Client, owner, repo, ref string) (*github.CombinedStatus, error) {
	ctx := context.Background()
	status, _, err := client.Repositories.GetCombinedStatus(ctx, owner, repo, ref, nil)
	return status, err
}

func GetCheckRuns(client *github.Client, owner, repo, ref string) (*github.ListCheckRunsResults, error) {
	ctx := context.Background()
	checkRuns, _, err := client.Checks.ListCheckRunsForRef(ctx, owner, repo, ref, nil)
	return checkRuns, err
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
}

func CalculateInteractionState(myLogin string, pr *github.PullRequest, reviews []*github.PullRequestReview, reviewComments []*github.PullRequestComment, issueComments []*github.IssueComment) InteractionState {
	state := InteractionState{}

	if pr == nil {
		return state
	}

	// Fetch Reviews
	for _, r := range reviews {
		if r == nil || r.SubmittedAt == nil || r.User == nil || r.User.Login == nil {
			continue
		}
		if *r.User.Login == myLogin {
			if r.SubmittedAt.After(state.LastMeTime) {
				state.LastMeTime = *r.SubmittedAt
			}
		} else {
			if r.SubmittedAt.After(state.LastOthersTime) {
				state.LastOthersTime = *r.SubmittedAt
			}
		}
	}

	// Fetch Review Comments
	// Group comments by their "thread"
	threads := make(map[int64]string) // Thread Root ID -> Last Commenter Login
	participation := make(map[int64]bool)

	for _, c := range reviewComments {
		if c == nil || c.ID == nil || c.User == nil || c.User.Login == nil {
			continue
		}

		rootID := *c.ID
		if c.InReplyTo != nil {
			rootID = *c.InReplyTo
		}

		threads[rootID] = *c.User.Login

		if *c.User.Login == myLogin {
			participation[rootID] = true
			if c.CreatedAt != nil && c.CreatedAt.After(state.LastMeTime) {
				state.LastMeTime = *c.CreatedAt
			}
		} else {
			if c.CreatedAt != nil && c.CreatedAt.After(state.LastOthersTime) {
				state.LastOthersTime = *c.CreatedAt
			}
		}
	}

	for rootID, lastCommenter := range threads {
		if participation[rootID] && lastCommenter != myLogin {
			state.HasUnrespondedComments = true
			break
		}
	}

	// Fetch Issue Comments
	iParticipated := false
	for _, c := range issueComments {
		if c == nil || c.User == nil || c.User.Login == nil {
			continue
		}
		if *c.User.Login == myLogin {
			iParticipated = true
			if c.CreatedAt != nil && c.CreatedAt.After(state.LastMeTime) {
				state.LastMeTime = *c.CreatedAt
			}
		} else {
			if c.CreatedAt != nil && c.CreatedAt.After(state.LastOthersTime) {
				state.LastOthersTime = *c.CreatedAt
			}
		}
	}
	if iParticipated && state.LastOthersTime.After(state.LastMeTime) {
		state.HasUnrespondedComments = true
	}

	return state
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

	// Fetch Reviews
	go func() {
		reviews, _, err := client.PullRequests.ListReviews(ctx, owner, repo, *pr.Number, nil)
		if err != nil {
			slog.Warn("Error listing reviews", "owner", owner, "repo", repo, "number", *pr.Number, "error", err)
		}
		resultChan <- result{reviews: reviews, err: err}
	}()

	// Fetch Review Comments
	go func() {
		reviewComments, _, err := client.PullRequests.ListComments(ctx, owner, repo, *pr.Number, nil)
		if err != nil {
			slog.Warn("Error listing review comments", "owner", owner, "repo", repo, "number", *pr.Number, "error", err)
		}
		resultChan <- result{reviewComments: reviewComments, err: err}
	}()

	// Fetch Issue Comments
	go func() {
		issueComments, _, err := client.Issues.ListComments(ctx, owner, repo, *pr.Number, nil)
		if err != nil {
			slog.Warn("Error listing issue comments", "owner", owner, "repo", repo, "number", *pr.Number, "error", err)
		}
		resultChan <- result{issueComments: issueComments, err: err}
	}()

	// Fetch Commits
	go func() {
		commits, _, err := client.PullRequests.ListCommits(ctx, owner, repo, *pr.Number, nil)
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
	if len(commits) > 0 {
		latestCommit := commits[len(commits)-1]
		if latestCommit.Commit != nil && latestCommit.Commit.Committer != nil && latestCommit.Commit.Committer.Date != nil {
			state.LastCommitTime = *latestCommit.Commit.Committer.Date
		}
	}

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

func FilterWaitingOnMe(prs []*github.PullRequest) []*github.PullRequest {
	myLogin := config.C().GithubUsername

	// Process PRs concurrently to avoid sequential API calls
	type prResult struct {
		pr           *github.PullRequest
		shouldFilter bool
	}

	resultChan := make(chan prResult)
	var wg sync.WaitGroup

	for _, pr := range prs {
		if pr == nil || pr.Base == nil || pr.Base.Repo == nil || pr.Base.Repo.Owner == nil || pr.Base.Repo.Owner.Login == nil || pr.Base.Repo.Name == nil {
			continue
		}

		wg.Add(1)
		go func(pr *github.PullRequest) {
			defer wg.Done()

			state := GetInteractionState(*pr.Base.Repo.Owner.Login, *pr.Base.Repo.Name, pr)

			// Is Personally Requested?
			isRequested := false
			for _, r := range pr.RequestedReviewers {
				if r != nil && r.Login != nil && *r.Login == myLogin {
					isRequested = true
					break
				}
			}

			// A PR is "Waiting on me" if:
			// 1. I am individually requested AND I haven't acted since the last push or last other action.
			// 2. OR I have unresponded comments.
			shouldFilter := false

			if isRequested {
				actedSinceOthers := !state.LastMeTime.IsZero() && state.LastMeTime.After(state.LastOthersTime) && state.LastMeTime.After(state.LastCommitTime)
				if !actedSinceOthers {
					shouldFilter = true
				}
			}

			if state.HasUnrespondedComments {
				shouldFilter = true
			}

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
	for result := range resultChan {
		if result.shouldFilter {
			filtered = append(filtered, result.pr)
		}
	}

	return filtered
}

func FilterWaitingOnAuthor(prs []*github.PullRequest) []*github.PullRequest {
	// Process PRs concurrently to avoid sequential API calls
	type prResult struct {
		pr           *github.PullRequest
		shouldFilter bool
	}

	resultChan := make(chan prResult)
	var wg sync.WaitGroup

	for _, pr := range prs {
		if pr == nil || pr.Base == nil || pr.Base.Repo == nil || pr.Base.Repo.Owner == nil || pr.Base.Repo.Owner.Login == nil || pr.Base.Repo.Name == nil {
			continue
		}

		wg.Add(1)
		go func(pr *github.PullRequest) {
			defer wg.Done()

			state := GetInteractionState(*pr.Base.Repo.Owner.Login, *pr.Base.Repo.Name, pr)
			actedSinceOthers := !state.LastMeTime.IsZero() && state.LastMeTime.After(state.LastOthersTime) && state.LastMeTime.After(state.LastCommitTime)

			resultChan <- prResult{pr: pr, shouldFilter: actedSinceOthers}
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
