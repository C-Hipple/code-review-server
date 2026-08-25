package workflows

import (
	"context"
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/go-github/v74/github"
)

// prUpdatedHook is called once per PR that a cycle newly added or re-fetched
// after a push, after that PR's aux data has been written to the DB caches.
// The server registers server.WarmPRAnalysis here so plugins and the LLM diff
// analysis are computed off the back of the workflow rather than when a
// reviewer opens the PR. It is a registered callback rather than a direct call
// because server imports this package, so the dependency cannot run both ways.
//
// Nil when nothing is registered — a workflow-only process (`--oneoff` without
// `--server`) exits as soon as the cycle finishes and would kill any hook work
// in flight, leaving plugin results stuck at "pending" for that head SHA.
var prUpdatedHook atomic.Pointer[func(owner, repo string, number int)]

// SetPRUpdatedHook registers the callback invoked for each PR a cycle added or
// updated. Call it during startup, before the manager runs. A nil fn clears
// any registered hook.
func SetPRUpdatedHook(fn func(owner, repo string, number int)) {
	if fn == nil {
		prUpdatedHook.Store(nil)
		return
	}
	prUpdatedHook.Store(&fn)
}

// apiCallCounter tracks GitHub API calls by type for a single RunOnce cycle.
type apiCallCounter struct {
	PRList         atomic.Int64
	PRSpecific     atomic.Int64
	Comments       atomic.Int64
	IssueComments  atomic.Int64
	CIStatus       atomic.Int64
	Diff           atomic.Int64
	Reviews        atomic.Int64
	CombinedStatus atomic.Int64
	CheckRuns      atomic.Int64
	Commits        atomic.Int64
	ReviewThreads  atomic.Int64
}

func (c *apiCallCounter) total() int64 {
	return c.PRList.Load() + c.PRSpecific.Load() + c.Comments.Load() +
		c.IssueComments.Load() + c.CIStatus.Load() + c.Diff.Load() +
		c.Reviews.Load() + c.CombinedStatus.Load() + c.CheckRuns.Load() +
		c.Commits.Load() + c.ReviewThreads.Load()
}

func (c *apiCallCounter) log() {
	slog.Info("GitHub API calls this cycle",
		"pr_list", c.PRList.Load(),
		"pr_specific", c.PRSpecific.Load(),
		"comments", c.Comments.Load(),
		"issue_comments", c.IssueComments.Load(),
		"ci_status", c.CIStatus.Load(),
		"diff", c.Diff.Load(),
		"reviews", c.Reviews.Load(),
		"combined_status", c.CombinedStatus.Load(),
		"check_runs", c.CheckRuns.Load(),
		"commits", c.Commits.Load(),
		"review_threads", c.ReviewThreads.Load(),
		"total", c.total(),
	)
}

// waitTimeout waits for the WaitGroup for the specified duration.
// It returns true if the wait timed out, false otherwise.
func waitTimeout(wg *sync.WaitGroup, timeout time.Duration) bool {
	c := make(chan struct{})
	go func() {
		defer close(c)
		wg.Wait()
	}()
	select {
	case <-c:
		return false
	case <-time.After(timeout):
		return true
	}
}

type ManagerService struct {
	Workflows     []Workflow
	workflow_chan chan FileChanges
	sleepTime     time.Duration
	oneoff        bool
}

func deduplicateChanges(changes []SerializedFileChange) []SerializedFileChange {
	// Dedup is per (identifier, workflow): a single workflow may emit at most
	// one canonical change per item per cycle, but different workflows that
	// touch the same item must each contribute (e.g. one Update from workflow
	// A and one Delete from workflow B both need to land so ownership is
	// tracked correctly).
	changesByIdentifier := make(map[string][]SerializedFileChange)
	for _, change := range changes {
		identifier := change.FileChange.Identifier + "\x00" + change.FileChange.WorkflowName
		changesByIdentifier[identifier] = append(changesByIdentifier[identifier], change)
	}

	finalChanges := []SerializedFileChange{}
	slog.Debug("Deduplicating changes", "count", len(changesByIdentifier))

	for identifier, itemChanges := range changesByIdentifier {
		var updateChange *SerializedFileChange
		var addChange *SerializedFileChange
		var deleteChange *SerializedFileChange

		for i, change := range itemChanges {
			switch change.FileChange.ChangeType {
			case "Addition":
				addChange = &itemChanges[i]
			case "Update":
				updateChange = &itemChanges[i]
			case "Delete":
				deleteChange = &itemChanges[i]
			}
		}

		if updateChange != nil {
			slog.Debug("Found update, discarding other changes", "identifier", identifier)
			finalChanges = append(finalChanges, *updateChange)
		} else if addChange != nil {
			slog.Debug("Found add, discarding delete", "identifier", identifier)
			finalChanges = append(finalChanges, *addChange)
		} else if deleteChange != nil {
			slog.Debug("Found delete", "identifier", identifier)
			finalChanges = append(finalChanges, *deleteChange)
		}
	}
	return finalChanges
}

func ListenChanges(channel chan FileChanges, wg *sync.WaitGroup) {
	changesMap := make(map[string][]SerializedFileChange)
	for fileChange := range channel {
		if fileChange.ChangeType == "No Change" {
			wg.Done()
			continue
		}
		fileChange.Report()
		key := fileChange.SectionName

		var lines []string
		if fileChange.ChangeType != "Delete" {
			lines = fileChange.GetLines(2)
		}

		changesMap[key] = append(changesMap[key], SerializedFileChange{
			FileChange: &fileChange,
			Lines:      lines,
		})
	}

	var serialziedChannel = make(chan SerializedFileChange)
	go ApplyChanges(serialziedChannel, wg)

	for _, changes := range changesMap {
		deduplicatedChanges := deduplicateChanges(changes)
		numDeduplicated := len(changes) - len(deduplicatedChanges)
		if numDeduplicated > 0 {
			slog.Debug("Deduplicated changes, adjusting WaitGroup", "count", numDeduplicated)
			for i := 0; i < numDeduplicated; i++ {
				wg.Done()
			}
		}
		for _, change := range deduplicatedChanges {
			serialziedChannel <- change
		}
	}
	close(serialziedChannel)
}

func ApplyChanges(channel chan SerializedFileChange, wg *sync.WaitGroup) {
	changeCount := 0
	for deserializedChange := range channel {
		db := config.C().DB

		if config.C().AutoWorktree {
			handleWorktreeChange(db, deserializedChange)
		}

		switch deserializedChange.FileChange.ChangeType {
		case "Addition", "Update":
			_, err := db.UpsertItemWithWorkflow(
				deserializedChange.FileChange.SectionID,
				deserializedChange.FileChange.Identifier,
				deserializedChange.FileChange.Status,
				deserializedChange.FileChange.Title,
				deserializedChange.FileChange.Details,
				deserializedChange.FileChange.Tags,
				deserializedChange.FileChange.TTL,
				deserializedChange.FileChange.WorkflowName,
			)
			if err != nil {
				slog.Error("Error upserting item", "error", err, "identifier", deserializedChange.FileChange.Identifier)
			} else {
				logItemAction(deserializedChange.FileChange,
					[]string{"status", "title", "details", "tags", "workflows"}, "")
			}
			if deserializedChange.FileChange.ChangeType == "Addition" && deserializedChange.FileChange.NotifyOnAdd {
				notifyPRAdded(deserializedChange.FileChange.SectionName, deserializedChange.FileChange.Title)
			}
		case "Delete":
			// "Delete" now means "this workflow no longer claims this item".
			// The row stays in place; PruneOrphanedItems removes anything
			// left with no remaining owners after the cycle completes.
			err := db.RemoveWorkflowFromItem(
				deserializedChange.FileChange.SectionID,
				deserializedChange.FileChange.Identifier,
				deserializedChange.FileChange.WorkflowName,
			)
			if err != nil {
				slog.Error("Error releasing workflow ownership", "error", err, "identifier", deserializedChange.FileChange.Identifier, "workflow", deserializedChange.FileChange.WorkflowName)
			} else {
				logItemAction(deserializedChange.FileChange, []string{"workflows"},
					"workflow released ownership; item pruned if no owners remain")
			}
		}
		changeCount++
		wg.Done()
	}
	slog.Info(fmt.Sprintf("Completed processing all DCR changes (%d total)", changeCount))
}

// logItemAction records an item write (Addition / Update / Delete) in
// WorkflowActionLog. Identifiers that aren't PR items are skipped rather than
// logged with empty coordinates.
func logItemAction(change *FileChanges, fields []string, detail string) {
	owner, repo, number, ok := parsePRIdentifier(change.Identifier)
	if !ok {
		return
	}
	logWorkflowAction(database.WorkflowAction{
		WorkflowName:  change.WorkflowName,
		Action:        change.ChangeType,
		Owner:         owner,
		Repo:          repo,
		PRNumber:      number,
		SHA:           change.HeadSHA,
		FieldsWritten: fields,
		SectionName:   change.SectionName,
		Detail:        detail,
	})
}

// parsePRIdentifier splits a workflow item identifier ("owner/repo-123") into
// its parts. The number is taken from the last "-" so owner and repo names that
// contain dashes ("C-hipple/code-review-server-207") parse correctly. The repo
// it returns is the short name, matching the cache-key convention used by the
// PR cache tables.
func parsePRIdentifier(identifier string) (string, string, int, bool) {
	dash := strings.LastIndex(identifier, "-")
	if dash < 0 {
		return "", "", 0, false
	}
	number, err := strconv.Atoi(identifier[dash+1:])
	if err != nil || number <= 0 {
		return "", "", 0, false
	}
	owner, repo, found := strings.Cut(identifier[:dash], "/")
	if !found || owner == "" || repo == "" {
		return "", "", 0, false
	}
	return owner, repo, number, true
}

func handleWorktreeChange(db *database.DB, change SerializedFileChange) {
	// Identifier is Repo-PRNumber for PRs
	parts := strings.Split(change.FileChange.Identifier, "-")
	if len(parts) < 2 {
		return
	}

	repoFull := parts[0]
	// Owner is part of RepoFull
	repoParts := strings.Split(repoFull, "/")
	if len(repoParts) < 2 {
		return
	}
	ownerName := repoParts[0]
	repoName := repoParts[1]
	prNumberStr := parts[1]

	var prNumber int
	fmt.Sscanf(prNumberStr, "%d", &prNumber)
	if prNumber == 0 {
		return
	}

	// We need head SHA and branch name. These are NOT in FileChanges.
	// We might need to fetch them from DB if they were cached.
	_, sha, _ := db.GetPullRequest(prNumber, repoFull)
	if sha == "" {
		// If we don't have it in DB, we can't easily manage worktree here without a refactor
		// to include more info in FileChanges or performing a GitHub API call.
		// For now, let's assume we can't do it if not in DB.
		return
	}

	// We also don't have HeadRef here.
	// This shows handleWorktreeChange was relying on PRToOrgBridge struct.
	// Let's try to get it from metadata cache.
	metadataJSON, _ := db.GetPRMetadataCache(ownerName, repoName, prNumber)
	if metadataJSON == "" {
		return
	}

	var metadata struct {
		HeadRef string `json:"head_ref"`
	}
	json.Unmarshal([]byte(metadataJSON), &metadata)
	branchName := metadata.HeadRef
	if branchName == "" {
		return
	}

	repoLocation := config.C().RepoLocation
	if strings.HasPrefix(repoLocation, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			repoLocation = strings.Replace(repoLocation, "~", home, 1)
		}
	}
	repoDir := filepath.Join(repoLocation, repoName)
	worktreeRoot := filepath.Join(repoLocation, fmt.Sprintf("%s_worktrees", repoName))
	worktreePath := filepath.Join(worktreeRoot, fmt.Sprintf("%d_%s", prNumber, branchName))

	// Check if repo exists
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		// Log debug if we can't find the repo, but don't error out loudly as it might be expected
		slog.Debug("Skipping worktree management, repo not found locally", "path", repoDir)
		return
	}

	if change.FileChange.ChangeType == "Addition" || change.FileChange.ChangeType == "Update" {
		// Create worktree
		slog.Info("Ensuring worktree exists", "pr", prNumber, "path", worktreePath)

		// Ensure worktree root exists
		if err := os.MkdirAll(worktreeRoot, 0755); err != nil {
			slog.Error("Failed to create worktree root directory", "path", worktreeRoot, "error", err)
			return
		}

		// Check if it's already in DB or exists on disk
		existingPath, err := db.GetWorktree(prNumber, repoName, ownerName)
		if err == nil && existingPath != "" {
			// Already tracked, maybe check if it still exists? For now assume it's good.
			// Actually, if branch changed, we might need to handle that, but let's assume one branch per PR for now.
			return
		}

		if err := git_tools.CreateWorktree(repoDir, branchName, worktreePath); err != nil {
			// If it fails, we log it but don't stop the workflow
			slog.Error("Failed to create worktree", "error", err)
		} else {
			if err := db.AddWorktree(prNumber, repoName, ownerName, worktreePath, branchName); err != nil {
				slog.Error("Failed to record worktree in DB", "error", err)
			}
		}

	} else if change.FileChange.ChangeType == "Delete" {
		// Remove worktree
		path, err := db.GetWorktree(prNumber, repoName, ownerName)
		if err != nil {
			slog.Error("Error checking for worktree", "error", err)
			return
		}
		if path != "" {
			slog.Info("Removing worktree", "pr", prNumber, "path", path)
			if err := git_tools.RemoveWorktree(repoDir, path); err != nil {
				slog.Error("Failed to remove worktree", "error", err)
			}
			if err := db.RemoveWorktreeRecord(prNumber, repoName, ownerName); err != nil {
				slog.Error("Failed to remove worktree record from DB", "error", err)
			}
		}
	}
}

func NewManagerService(workflows []Workflow, oneoff bool, sleepTime time.Duration) ManagerService {
	return ManagerService{
		Workflows:     workflows,
		workflow_chan: make(chan FileChanges),
		sleepTime:     sleepTime,
		oneoff:        oneoff,
	}
}

func (ms ManagerService) runWorkflow(workflow Workflow, prs []*github.PullRequest, workflow_chan chan FileChanges, file_change_wg *sync.WaitGroup) {
	// Helper which times the workflow run command.
	slog.Info("Starting Workflow", "workflow", workflow.GetName())
	start := time.Now()
	result, err := workflow.Run(prs, workflow_chan, file_change_wg)
	duration := time.Since(start)
	if err != nil {
		slog.Error("Errored in Workflow", "workflow", workflow.GetName(), "after", duration, "error", err)
	}
	slog.Info("Finishing Workflow", "workflow", workflow.GetName(), "took", duration, "result", result.Report())
}

// hasUnfetchedRequirements returns true if any PR data required by the workflow was
// not successfully fetched. A nil slice in repoStatePRs or a nil PR pointer in
// specificPRs indicates that the fetch either failed (e.g. 429 rate limit) or was
// never attempted. In either case the workflow must be skipped — running section
// matching against an empty list would delete all existing database items.
func hasUnfetchedRequirements(
	workflow Workflow,
	repoStatePRs map[string]map[string][]*github.PullRequest,
	specificPRs map[string]map[int]*github.PullRequest,
) bool {
	for _, req := range workflow.GetPRRequirements() {
		repoKey := fmt.Sprintf("%s/%s", req.Owner, req.Repo)
		if len(req.PRNumbers) > 0 {
			// Specific PRs: each entry starts nil and is set on success.
			// If any required PR is still nil, the fetch failed.
			for _, num := range req.PRNumbers {
				if specificPRs[repoKey][num] == nil {
					return true
				}
			}
		} else {
			// State-based PRs: GetPRs returns a non-nil slice on success (even
			// when there are 0 results). nil means the fetch failed or was skipped.
			if repoStatePRs[repoKey][req.State] == nil {
				return true
			}
		}
	}
	return false
}

func (ms ManagerService) RunOnce(file_change_wg *sync.WaitGroup) {
	client := git_tools.GetGithubClient()
	apiCalls := &apiCallCounter{}

	// Map to store fetched PRs: repo -> state -> PRs
	repoStatePRs := make(map[string]map[string][]*github.PullRequest)
	// Map to store specific PRs: repo -> PRNumber -> PR
	specificPRs := make(map[string]map[int]*github.PullRequest)

	// Collection phase
	for _, wf := range ms.Workflows {
		reqs := wf.GetPRRequirements()
		for _, req := range reqs {
			repoKey := fmt.Sprintf("%s/%s", req.Owner, req.Repo)
			if len(req.PRNumbers) > 0 {
				if specificPRs[repoKey] == nil {
					specificPRs[repoKey] = make(map[int]*github.PullRequest)
				}
				for _, num := range req.PRNumbers {
					specificPRs[repoKey][num] = nil // Mark for fetching
				}
			} else {
				if repoStatePRs[repoKey] == nil {
					repoStatePRs[repoKey] = make(map[string][]*github.PullRequest)
				}
				repoStatePRs[repoKey][req.State] = nil // Mark for fetching
			}
		}
	}

	// Fetching phase.
	// On success, repoStatePRs[repoKey][state] is set to a non-nil slice (possibly empty).
	// On failure it stays nil. This nil sentinel lets hasUnfetchedRequirements detect
	// errors without a separate error-tracking map.
	for repoKey, states := range repoStatePRs {
		owner, repo, _ := git_tools.ParseRepoName(repoKey)
		for state := range states {
			slog.Debug("Fetching PRs", "repo", repoKey, "state", state)
			prs, err := git_tools.GetPRs(client, state, owner, repo)
			apiCalls.PRList.Add(1)
			if err != nil {
				slog.Error("Failed to fetch PRs, will skip dependent workflows", "repo", repoKey, "state", state, "error", err)
				// repoStatePRs[repoKey][state] remains nil — signals failure to hasUnfetchedRequirements
				continue
			}
			repoStatePRs[repoKey][state] = prs
		}
	}

	for repoKey, prMap := range specificPRs {
		owner, repo, _ := git_tools.ParseRepoName(repoKey)
		numbers := []int{}
		for num := range prMap {
			numbers = append(numbers, num)
		}
		if len(numbers) > 0 {
			slog.Debug("Fetching specific PRs", "repo", repoKey, "count", len(numbers))
			prs, err := git_tools.GetSpecificPRs(client, owner, repo, numbers)
			apiCalls.PRSpecific.Add(1)
			if err != nil {
				slog.Error("Failed to fetch specific PRs, will skip dependent workflows", "repo", repoKey, "error", err)
				// specificPRs[repoKey][num] entries remain nil — signals failure
				continue
			}
			for _, pr := range prs {
				specificPRs[repoKey][*pr.Number] = pr
			}
		}
	}

	// Pre-fetch auxiliary data for all PRs
	auxDataStore := ms.prefetchAuxData(client, apiCalls, repoStatePRs, specificPRs)
	SetCurrentAuxDataStore(auxDataStore)
	defer SetCurrentAuxDataStore(nil)

	var wg sync.WaitGroup
	for _, workflow := range ms.Workflows {
		// Skip if any required PR data was not successfully fetched (e.g. rate limit).
		// Running section-matching against an empty list would delete all DB items.
		if hasUnfetchedRequirements(workflow, repoStatePRs, specificPRs) {
			slog.Warn("Skipping workflow due to PR fetch error; will retry next cycle", "workflow", workflow.GetName())
			continue
		}

		// Collect PRs for THIS workflow
		var workflowPRs []*github.PullRequest
		reqs := workflow.GetPRRequirements()
		for _, req := range reqs {
			repoKey := fmt.Sprintf("%s/%s", req.Owner, req.Repo)
			if len(req.PRNumbers) > 0 {
				for _, num := range req.PRNumbers {
					if pr := specificPRs[repoKey][num]; pr != nil {
						workflowPRs = append(workflowPRs, pr)
					}
				}
			} else {
				workflowPRs = append(workflowPRs, repoStatePRs[repoKey][req.State]...)
			}
		}

		wg.Add(1)
		go func(workflow Workflow, prs []*github.PullRequest) {
			defer wg.Done()
			ms.runWorkflow(workflow, prs, ms.workflow_chan, file_change_wg)
		}(workflow, workflowPRs)
	}
	if waitTimeout(&wg, 240*time.Second) {
		slog.Error("RunOnce waitgroup timed out waiting for workflows")
	} else {
		slog.Info("Completed RunOnce Waitgroup")
	}
	apiCalls.log()

	// Fetch authoritative rate limit status from GitHub's /rate_limit endpoint.
	rlLimit, rlRemaining, rlResetAt, rlErr := git_tools.GetRateLimitFromAPI(client)
	if rlErr != nil {
		slog.Warn("Failed to fetch rate limit from GitHub API, using header-based estimate", "error", rlErr)
		rlStatus := git_tools.GetRateLimitStatus()
		rlLimit = rlStatus.Limit
		rlRemaining = rlStatus.Remaining
		rlResetAt = rlStatus.ResetAt
	}
	slog.Info("GitHub rate limit post-cycle",
		"remaining", rlRemaining,
		"limit", rlLimit,
		"used_this_cycle", apiCalls.total(),
		"reset_at", rlResetAt,
	)

	rlResetAtStr := ""
	if !rlResetAt.IsZero() {
		rlResetAtStr = rlResetAt.UTC().Format(time.RFC3339)
	}

	db := config.C().DB
	if err := db.LogAPICallStats(
		apiCalls.PRList.Load(),
		apiCalls.PRSpecific.Load(),
		apiCalls.Comments.Load(),
		apiCalls.IssueComments.Load(),
		apiCalls.CIStatus.Load(),
		apiCalls.Diff.Load(),
		apiCalls.Reviews.Load(),
		apiCalls.CombinedStatus.Load(),
		apiCalls.CheckRuns.Load(),
		apiCalls.Commits.Load(),
		apiCalls.ReviewThreads.Load(),
		rlRemaining,
		rlLimit,
		rlResetAtStr,
	); err != nil {
		slog.Error("Failed to save API call stats to database", "error", err)
	}
}

func (ms *ManagerService) Run() {
	slog.Info("Starting Service")

	// Advisory lock to prevent multiple concurrent syncs (skip for oneoff mode)
	if !ms.oneoff {
		crsHome, err := os.UserHomeDir() // Fallback to home if getCRSHome fails but we use config.UserHomeDir elsewhere
		if err == nil {
			crsHome = filepath.Join(crsHome, ".crs")
			if err := os.MkdirAll(crsHome, 0755); err != nil {
				slog.Error("Failed to create CRS directory for lock file", "path", crsHome, "error", err)
			}
			lockPath := filepath.Join(crsHome, "codereviewserver_sync.lock")
			lockFile, err := os.Create(lockPath)
			if err == nil {
				err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
				if err != nil {
					slog.Warn("Another instance is already running background sync, skipping sync in this process.")
					lockFile.Close()
					return
				}
				defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
				defer lockFile.Close()
			}
		}
	}

	if ms.oneoff {
		var listener_wg sync.WaitGroup
		listener_wg.Add(1)
		go ListenChanges(ms.workflow_chan, &listener_wg)

		slog.Info("Running Once")
		ms.RunOnce(&listener_wg)
		close(ms.workflow_chan)
		listener_wg.Done()
		if waitTimeout(&listener_wg, 240*time.Second) {
			slog.Error("Listener waitgroup timed out waiting for changes to be applied")
		}
		pruneOrphanedItems()
		pruneWorkflowActionLog()
	} else {
		cycle_count := 0
		for {
			// Reload config and workflows before each cycle
			if err := config.Reload(); err != nil {
				slog.Error("Failed to reload config before cycle", "error", err)
			} else {
				// Re-generate workflows
				cfg := config.C()
				ms.Workflows = MatchWorkflows(cfg.RawWorkflows, &cfg.Repos, cfg.JiraDomain)
				ms.sleepTime = cfg.SleepDuration
				ms.Initialize()
			}

			// Throttle: skip this cycle if the last completed cycle was within half the sleep interval.
			// This prevents hammering the GitHub API when the server is restarted frequently.
			db := config.C().DB
			lastCycle, lastErr := db.GetLastWorkflowCycleTime()
			if lastErr != nil {
				slog.Warn("Failed to get last workflow cycle time", "error", lastErr)
			} else if !lastCycle.IsZero() {
				elapsed := time.Since(lastCycle)
				minInterval := ms.sleepTime / 2
				if elapsed < minInterval {
					slog.Info("Skipping cycle, last run was too recent",
						"last_run", lastCycle.Format(time.RFC3339),
						"elapsed", elapsed.Round(time.Second),
						"min_interval", minInterval)
					time.Sleep(ms.sleepTime)
					cycle_count++
					continue
				}
			}

			slog.Info("Cycle", "count", cycle_count, "sleepTime", ms.sleepTime)
			var cycle_wg sync.WaitGroup
			cycle_wg.Add(1)
			ms.workflow_chan = make(chan FileChanges)

			go ListenChanges(ms.workflow_chan, &cycle_wg)
			ms.RunOnce(&cycle_wg)
			close(ms.workflow_chan)
			cycle_wg.Done()

			if waitTimeout(&cycle_wg, 240*time.Second) {
				slog.Error("Cycle waitgroup timed out waiting for changes to be applied")
			}

			pruneOrphanedItems()
			pruneWorkflowActionLog()

			// Log cycle completion so the throttle check works on next server start.
			if err := db.LogWorkflowCycle(); err != nil {
				slog.Error("Failed to log workflow cycle completion", "error", err)
			}

			time.Sleep(ms.sleepTime)
			cycle_count++
		}
	}
	slog.Info("Exiting Service")
}

// pruneOrphanedItems deletes any items that are no longer claimed by any
// workflow. Run after every cycle once all per-workflow ownership updates have
// been applied.
func pruneOrphanedItems() {
	deleted, err := config.C().DB.PruneOrphanedItems()
	if err != nil {
		slog.Error("Failed to prune orphaned items", "error", err)
		return
	}
	if deleted > 0 {
		slog.Info("Pruned orphaned items", "count", deleted)
	}
}

// workflowActionLogRetention is how far back WorkflowActionLog rows are kept.
// Every cycle writes roughly two rows per PR it touches (one cache write, one
// item write), so on the default 10-minute cycle a 200-PR dashboard accumulates
// on the order of a few hundred thousand rows a week. That is well within what
// SQLite handles and far longer than any cache-miss investigation needs; lower
// it if the database size becomes a concern.
const workflowActionLogRetention = 7 * 24 * time.Hour

// pruneWorkflowActionLog drops action log rows past the retention window. Run
// after every cycle.
func pruneWorkflowActionLog() {
	deleted, err := config.C().DB.PruneWorkflowActionsBefore(time.Now().Add(-workflowActionLogRetention))
	if err != nil {
		slog.Error("Failed to prune workflow action log", "error", err)
		return
	}
	if deleted > 0 {
		slog.Info("Pruned workflow action log", "count", deleted)
	}
}

func (ms *ManagerService) Initialize() {
	// Ensure all required sections exist.
	// Does this sync since GetSection has creation side effect
	db := config.C().DB
	for _, wf := range ms.Workflows {
		db.GetOrCreateSection(wf.GetOrgSectionName(), config.C().SectionPriority[wf.GetOrgSectionName()])
	}
}

// prefetchConcurrency caps how many PRs have their aux data fetched at once.
// Each PR fans out to as many as six concurrent GitHub calls, so an unbounded
// loop over a repo's open PR list (up to 200, see git_tools.GetPRs) can put
// well over a thousand requests in flight simultaneously. GitHub answers that
// with secondary rate-limit errors, and every call that fails silently leaves
// its cache row unwritten — which is what a later "reviews: MISS" in
// ~/.crs/cache_miss.log looks like from the outside. Keeping a lid on the
// fan-out costs wall-clock time in the background cycle but makes the writes
// land.
const prefetchConcurrency = 8

// prefetchAuxData gathers auxiliary data for all PRs that need it
func (ms ManagerService) prefetchAuxData(client *github.Client,
	apiCalls *apiCallCounter,
	repoStatePRs map[string]map[string][]*github.PullRequest,
	specificPRs map[string]map[int]*github.PullRequest) *AuxDataStore {

	store := NewAuxDataStore()

	// Collect all PRs and merge their aux data requirements
	prRequirements := make(map[PRKey]AuxDataRequirement)
	prObjects := make(map[PRKey]*github.PullRequest)
	// Requirements are merged across workflows, so a single fetch can be on
	// behalf of several of them. Track every requester so the action log names
	// them all rather than picking one arbitrarily.
	prWorkflows := make(map[PRKey][]string)

	for _, wf := range ms.Workflows {
		for _, req := range wf.GetPRRequirements() {
			slog.Info("AuxDataRequirement", "workflow", wf.GetName(),
				"repo", fmt.Sprintf("%s/%s", req.Owner, req.Repo),
				"comments", req.AuxData.Comments, "ci_status", req.AuxData.CIStatus,
				"diff", req.AuxData.Diff, "reviews", req.AuxData.Reviews, "commits", req.AuxData.Commits)

			repoKey := fmt.Sprintf("%s/%s", req.Owner, req.Repo)

			var prs []*github.PullRequest
			if len(req.PRNumbers) > 0 {
				for _, num := range req.PRNumbers {
					if pr := specificPRs[repoKey][num]; pr != nil {
						prs = append(prs, pr)
					}
				}
			} else {
				prs = repoStatePRs[repoKey][req.State]
			}

			for _, pr := range prs {
				if pr == nil || pr.Number == nil {
					continue
				}
				key := PRKey{Owner: req.Owner, Repo: req.Repo, Number: *pr.Number}
				prObjects[key] = pr
				if !slices.Contains(prWorkflows[key], wf.GetName()) {
					prWorkflows[key] = append(prWorkflows[key], wf.GetName())
				}

				// Merge requirements (OR them together)
				existing := prRequirements[key]
				existing.Comments = existing.Comments || req.AuxData.Comments
				existing.CIStatus = existing.CIStatus || req.AuxData.CIStatus
				existing.Diff = existing.Diff || req.AuxData.Diff
				existing.Reviews = existing.Reviews || req.AuxData.Reviews
				existing.Commits = existing.Commits || req.AuxData.Commits
				existing.ReviewThreads = existing.ReviewThreads || req.AuxData.ReviewThreads
				// Always fetch reviews for open non-draft PRs so Details() can
				// display who has approved / requested changes / commented.
				if pr.State != nil && *pr.State == "open" && (pr.Draft == nil || !*pr.Draft) {
					existing.Reviews = true
				}
				prRequirements[key] = existing
			}
		}
	}

	warmed := applyCacheWarmRequirements(config.C().DB, prObjects, prRequirements)

	// Fetch aux data in parallel, but bounded (see prefetchConcurrency).
	var wg sync.WaitGroup
	sem := make(chan struct{}, prefetchConcurrency)
	for key, auxReq := range prRequirements {
		// Skip if no aux data is needed
		if !auxReq.Comments && !auxReq.CIStatus && !auxReq.Diff && !auxReq.Reviews &&
			!auxReq.Commits && !auxReq.ReviewThreads {
			continue
		}

		wg.Add(1)
		go func(key PRKey, auxReq AuxDataRequirement, pr *github.PullRequest, workflowName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			auxData := fetchAuxDataForPR(client, apiCalls, workflowName, key, auxReq, pr)
			store.Set(key, auxData)
		}(key, auxReq, prObjects[key], strings.Join(prWorkflows[key], ","))
	}
	wg.Wait()

	slog.Info("Pre-fetched auxiliary data", "pr_count", len(prRequirements))
	notifyPRsUpdated(warmed)
	return store
}

// notifyPRsUpdated fires the registered PR-updated hook for each PR this cycle
// warmed. It runs after the aux fetches have finished, so the hook finds the
// diff, comments and metadata it needs already in the DB.
//
// Each hook call gets its own goroutine and the cycle does not wait for them:
// the hooks fan out to plugin subprocesses and LLM calls, which are slow by
// nature and must not hold up the next workflow run.
func notifyPRsUpdated(warmed []PRKey) {
	hook := prUpdatedHook.Load()
	if hook == nil || len(warmed) == 0 {
		return
	}
	slog.Info("Dispatching post-update hooks for updated PRs", "pr_count", len(warmed))
	for _, key := range warmed {
		go (*hook)(key.Owner, key.Repo, key.Number)
	}
}

// applyCacheWarmRequirements turns on the aux fields a reviewer needs to open a
// PR for every PR that was newly added or just pushed to. Those fields all go
// stale on a push (the diff is keyed to the head SHA) and all of them are read
// by GetPRDetails, so if we only fetched what a section's filters strictly
// require, a freshly added or just-updated PR would be a guaranteed cache miss
// the moment the user opened it. The warm is gated on an actual change — we do
// NOT pre-fetch for unchanged PRs we may never look at.
//
// Reviews and comments are included even though prefetchAuxData separately
// forces Reviews on for open non-draft PRs: that override skips drafts and
// closed PRs, so without this pass those PRs never get a PRReviews row written
// by a workflow and every open of one logs a "reviews" cache miss.
//
// It returns the PRs it turned the warm on for. Those are exactly the ones
// whose plugin results and LLM analysis are stale too, so the caller fires the
// PR-updated hook for them once the fetches land.
func applyCacheWarmRequirements(db *database.DB,
	prObjects map[PRKey]*github.PullRequest,
	prRequirements map[PRKey]AuxDataRequirement) []PRKey {

	warmed := []PRKey{}
	for key, pr := range prObjects {
		if !prNeedsCacheWarm(db, key, pr) {
			continue
		}
		req := prRequirements[key]
		req.Diff = true
		req.Commits = true
		req.Reviews = true
		req.Comments = true
		// Resolution state is only ever read by the review view, so nothing else
		// would populate it; without warming it here the first person to open the
		// PR pays for the GraphQL call.
		req.ReviewThreads = true
		prRequirements[key] = req
		warmed = append(warmed, key)
	}
	return warmed
}

// prNeedsCacheWarm reports whether we should proactively warm the caches for a
// PR because it was just added or updated. It returns true when:
//   - the PR is brand new to us (no diff row cached yet),
//   - its head SHA changed since we last cached it (new commits pushed), or
//   - we only have a SHA-only placeholder row with no real diff body (e.g. an
//     earlier fetch failed).
//
// In all three cases the diff/commits a reviewer needs are missing or stale, so
// warming them now turns the next review open into a cache hit. For an unchanged
// PR whose current-SHA diff is already cached, it returns false so we don't pull
// data we may never use.
func prNeedsCacheWarm(db *database.DB, key PRKey, pr *github.PullRequest) bool {
	if pr == nil || pr.Head == nil || pr.Head.SHA == nil {
		return false
	}
	body, cachedSHA, err := db.GetPullRequest(key.Number, key.Repo)
	if err != nil {
		// On a lookup error, err on the side of warming.
		return true
	}
	return cachedSHA != *pr.Head.SHA || body == ""
}

// fetchAuxDataForPR fetches the requested auxiliary data for a single PR
// and persists it to the DB cache so that GetPRDetails can find it.
// workflowName identifies who asked for the fetch; it is recorded in
// WorkflowActionLog alongside the fields that actually landed.
func fetchAuxDataForPR(client *github.Client,
	apiCalls *apiCallCounter,
	workflowName string,
	key PRKey, req AuxDataRequirement, pr *github.PullRequest) *PRAuxData {

	auxData := &PRAuxData{}

	headSHA := ""
	if pr != nil && pr.Head != nil && pr.Head.SHA != nil {
		auxData.HeadSHA = *pr.Head.SHA
		headSHA = *pr.Head.SHA
	}
	if pr != nil && pr.Base != nil && pr.Base.SHA != nil {
		auxData.BaseSHA = *pr.Base.SHA
	}

	var wg sync.WaitGroup

	// Data collected by goroutines for DB caching
	var (
		allCommentsForDB []*github.PullRequestComment // PR + Issue comments for DB
		ghReviews        []*github.PullRequestReview
		ghCommits        []*github.RepositoryCommit
		combinedStatus   *github.CombinedStatus
		checkRunsResult  *github.ListCheckRunsResults
	)

	// 1. Comments (PR review comments + Issue comments)
	if req.Comments {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prComments, err := git_tools.GetPRComments(client, key.Owner, key.Repo, key.Number)
			apiCalls.Comments.Add(1)
			if err != nil {
				slog.Warn("Failed to fetch comments for pre-fetch", "pr", key.Number, "repo", key.Repo, "error", err)
				return
			}

			// Store PR review comments only for workflow display (issue comments
			// would cause nil panics in formatComments due to nil DiffHunk)
			auxData.Comments = prComments
			auxData.CommentsCount = len(prComments)

			// Also fetch issue comments for DB cache (GetPRDetails expects both)
			combined := make([]*github.PullRequestComment, len(prComments))
			copy(combined, prComments)
			apiCalls.IssueComments.Add(1)
			issueComments, err := git_tools.ListAllIssueComments(context.Background(), client, key.Owner, key.Repo, key.Number)
			if err == nil {
				for _, ic := range issueComments {
					combined = append(combined, convertIssueToPRComment(ic))
				}
			}
			sort.Slice(combined, func(i, j int) bool {
				if combined[i].CreatedAt == nil {
					return true
				}
				if combined[j].CreatedAt == nil {
					return false
				}
				return combined[i].CreatedAt.Before(combined[j].CreatedAt.Time)
			})
			allCommentsForDB = combined
		}()
	}

	// 2. CI Status for workflow display (uses Actions API)
	if req.CIStatus && pr != nil && pr.Head != nil && pr.Head.Label != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiCalls.CIStatus.Add(1)
			ciInfo := git_tools.GetCIStatus(key.Owner, key.Repo, *pr.Head.Label)
			auxData.CIStatus = &ciInfo
		}()
	}

	// 3. Diff
	if req.Diff {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			apiCalls.Diff.Add(1)
			diffResp, _, err := client.PullRequests.GetRaw(ctx, key.Owner, key.Repo, key.Number, github.RawOptions{Type: github.Diff})
			if err != nil {
				slog.Warn("Failed to fetch diff for pre-fetch", "pr", key.Number, "repo", key.Repo, "error", err)
				return
			}
			auxData.Diff = diffResp
			auxData.DiffLines = []string{"*** Diff\n", diffResp}
		}()
	}

	// 4. Reviews (for metadata: approved_by, changes_requested_by, etc.)
	if req.Reviews {
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiCalls.Reviews.Add(1)
			reviews, err := git_tools.ListAllPRReviews(context.Background(), client, key.Owner, key.Repo, key.Number)
			if err != nil {
				// Nothing gets written to PRReviews when this fails, so the next
				// time the UI opens this PR it logs a "reviews" cache miss.
				slog.Warn("Failed to fetch reviews for pre-fetch, PRReviews cache left unwritten",
					"pr", key.Number, "repo", key.Repo, "error", err)
				return
			}
			ghReviews = reviews
			auxData.Reviews = reviews
		}()
	}

	// 5. Commit Status + Check Runs (for metadata CI status)
	if headSHA != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiCalls.CombinedStatus.Add(1)
			status, err := git_tools.GetCombinedStatus(client, key.Owner, key.Repo, headSHA)
			if err == nil {
				combinedStatus = status
			}
			apiCalls.CheckRuns.Add(1)
			cr, err := git_tools.GetCheckRuns(client, key.Owner, key.Repo, headSHA)
			if err == nil {
				checkRunsResult = cr
			}
		}()
	}

	// 6. Commits
	if req.Commits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiCalls.Commits.Add(1)
			commits, err := git_tools.ListAllPRCommits(context.Background(), client, key.Owner, key.Repo, key.Number)
			if err != nil {
				slog.Warn("Failed to fetch commits for pre-fetch", "pr", key.Number, "error", err)
				return
			}
			ghCommits = commits
		}()
	}

	// 7. Review threads (GraphQL — resolution state has no REST equivalent).
	// Nothing in the workflow filters reads this; it is fetched purely so the
	// review view opens against a warm cache.
	if req.ReviewThreads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			apiCalls.ReviewThreads.Add(1)
			threads, err := git_tools.GetReviewThreads(key.Owner, key.Repo, key.Number)
			if err != nil {
				slog.Warn("Failed to fetch review threads for pre-fetch", "pr", key.Number, "repo", key.Repo, "error", err)
				return
			}
			auxData.ReviewThreads = threads
		}()
	}

	wg.Wait()

	// Persist all fetched data to DB so GetPRDetails finds it cached
	persistPRCacheData(workflowName, key, pr, auxData, allCommentsForDB, ghReviews, ghCommits, combinedStatus, checkRunsResult)

	return auxData
}

// convertIssueToPRComment converts a github.IssueComment to a PullRequestComment
// for unified storage in the DB cache.
func convertIssueToPRComment(ic *github.IssueComment) *github.PullRequestComment {
	return &github.PullRequestComment{
		ID:        ic.ID,
		Body:      ic.Body,
		User:      ic.User,
		CreatedAt: ic.CreatedAt,
		UpdatedAt: ic.UpdatedAt,
		URL:       ic.URL,
		HTMLURL:   ic.HTMLURL,
	}
}

// persistPRCacheData stores all pre-fetched PR data to the DB cache
// so that server.GetPRDetails can use it without making API calls.
//
// Every cache field that lands is recorded in WorkflowActionLog together with
// the workflow name and head SHA. A field that was requested but failed to
// fetch (or failed to write) is deliberately absent from that list, which is
// what makes the log useful when explaining a later cache miss.
func persistPRCacheData(workflowName string, key PRKey, pr *github.PullRequest,
	auxData *PRAuxData,
	allComments []*github.PullRequestComment,
	reviews []*github.PullRequestReview,
	commits []*github.RepositoryCommit,
	combinedStatus *github.CombinedStatus,
	checkRuns *github.ListCheckRunsResults) {

	db := config.C().DB
	var written []string

	// 1. Comments
	if allComments != nil {
		if j, err := json.Marshal(allComments); err == nil {
			if err := db.UpsertPRComments(key.Number, key.Repo, string(j)); err != nil {
				slog.Error("Failed to cache PR comments", "pr", key.Number, "repo", key.Repo, "error", err)
			} else {
				written = append(written, "comments")
			}
		}
	}

	// 2. Diff + SHA
	if auxData.Diff != "" {
		if err := db.UpsertPullRequest(key.Number, key.Repo, auxData.HeadSHA, auxData.BaseSHA, auxData.Diff); err != nil {
			slog.Error("Failed to cache PR diff", "pr", key.Number, "repo", key.Repo, "error", err)
		} else {
			written = append(written, "diff")
		}
	} else if auxData.HeadSHA != "" {
		// Store SHA even without diff so GetPRDetails can look up CI status
		if err := db.UpsertPullRequest(key.Number, key.Repo, auxData.HeadSHA, auxData.BaseSHA, ""); err != nil {
			slog.Error("Failed to cache PR SHA", "pr", key.Number, "repo", key.Repo, "error", err)
		} else {
			written = append(written, "sha")
		}
	}

	// 3. Reviews (stored in same JSON format as server.ReviewJSON)
	if reviews != nil {
		type reviewJSON struct {
			ID          int64     `json:"id"`
			User        string    `json:"user"`
			Body        string    `json:"body"`
			State       string    `json:"state"`
			SubmittedAt time.Time `json:"submitted_at"`
			HTMLURL     string    `json:"html_url"`
		}
		// Start from an empty (non-nil) slice so a PR with no reviews caches as
		// "[]" rather than "null". Readers unmarshal "null" back into a nil
		// slice, which is indistinguishable from "not loaded yet".
		rvs := []reviewJSON{}
		for _, r := range reviews {
			var submittedAt time.Time
			if r.SubmittedAt != nil {
				submittedAt = r.SubmittedAt.Time
			}
			rvs = append(rvs, reviewJSON{
				ID:          r.GetID(),
				User:        r.User.GetLogin(),
				Body:        r.GetBody(),
				State:       r.GetState(),
				SubmittedAt: submittedAt,
				HTMLURL:     r.GetHTMLURL(),
			})
		}
		if j, err := json.Marshal(rvs); err == nil {
			if err := db.UpsertPRReviews(key.Number, key.Repo, string(j)); err != nil {
				slog.Error("Failed to cache PR reviews", "pr", key.Number, "repo", key.Repo, "error", err)
			} else {
				written = append(written, "reviews")
			}
		}
	}

	// 4. CI Status (commit status + check runs, same format as server.CombinedPRStatus)
	if combinedStatus != nil || checkRuns != nil {
		combined := struct {
			Status    *github.CombinedStatus       `json:"status"`
			CheckRuns *github.ListCheckRunsResults `json:"check_runs"`
		}{Status: combinedStatus, CheckRuns: checkRuns}
		if j, err := json.Marshal(combined); err == nil && auxData.HeadSHA != "" {
			if err := db.UpsertCIStatus(key.Number, key.Repo, auxData.HeadSHA, string(j)); err != nil {
				slog.Error("Failed to cache CI status", "pr", key.Number, "repo", key.Repo, "error", err)
			} else {
				written = append(written, "ci_status")
			}
		}
	}

	// 5. Requested Reviewers (from PR object, no extra API call needed)
	if pr != nil {
		reviewers := &github.Reviewers{
			Users: pr.RequestedReviewers,
			Teams: pr.RequestedTeams,
		}
		if j, err := json.Marshal(reviewers); err == nil {
			if err := db.UpsertRequestedReviewers(key.Number, key.Repo, string(j)); err != nil {
				slog.Error("Failed to cache requested reviewers", "pr", key.Number, "repo", key.Repo, "error", err)
			} else {
				written = append(written, "requested_reviewers")
			}
		}
	}

	// 6. Commits (stored in same JSON format as server.CommitJSON)
	if commits != nil {
		type commitJSON struct {
			SHA     string `json:"sha"`
			Message string `json:"message"`
			Author  string `json:"author"`
			Date    string `json:"date"`
			URL     string `json:"url"`
		}
		var cms []commitJSON
		for _, c := range commits {
			cms = append(cms, commitJSON{
				SHA:     c.GetSHA(),
				Message: c.Commit.GetMessage(),
				Author:  c.Commit.Author.GetName(),
				Date:    c.Commit.Author.GetDate().Format(time.RFC3339),
				URL:     c.GetHTMLURL(),
			})
		}
		if j, err := json.Marshal(cms); err == nil {
			if err := db.UpsertPRCommits(key.Number, key.Repo, string(j)); err != nil {
				slog.Error("Failed to cache PR commits", "pr", key.Number, "repo", key.Repo, "error", err)
			} else {
				written = append(written, "commits")
			}
		}
	}

	// 6b. Review threads (resolution state; same JSON shape GetPRReviewThreads
	// reads). Nil means the fetch was not requested or failed, in which case the
	// previously cached row is left alone rather than being blanked.
	if auxData.ReviewThreads != nil {
		if j, err := json.Marshal(auxData.ReviewThreads); err == nil {
			if err := db.UpsertPRReviewThreads(key.Number, key.Repo, string(j)); err != nil {
				slog.Error("Failed to cache PR review threads", "pr", key.Number, "repo", key.Repo, "error", err)
			} else {
				written = append(written, "review_threads")
			}
		}
	}

	// 7. PR Metadata (constructed from PR object + fetched data)
	if pr != nil {
		if buildAndCacheMetadata(db, key, pr, reviews, combinedStatus, checkRuns) {
			written = append(written, "metadata")
		}
	}

	logWorkflowAction(database.WorkflowAction{
		WorkflowName:  workflowName,
		Action:        database.WorkflowActionCacheWrite,
		Owner:         key.Owner,
		Repo:          key.Repo,
		PRNumber:      key.Number,
		SHA:           auxData.HeadSHA,
		FieldsWritten: written,
		Detail:        describeAuxRequest(auxData, written),
	})
}

// describeAuxRequest notes anything worth knowing about a cache write beyond
// the field list, so a later cache-miss report can say *why* a field is absent
// rather than only that it is.
func describeAuxRequest(auxData *PRAuxData, written []string) string {
	if len(written) == 0 {
		return "no cache fields written (all requested fetches failed or returned nothing)"
	}
	if auxData.HeadSHA == "" {
		return "head SHA unknown; SHA-keyed caches (diff, CI status) could not be written"
	}
	return ""
}

// logWorkflowAction records one workflow action, downgrading failures to a warn:
// the log is diagnostic, and losing a row must never fail a workflow cycle.
func logWorkflowAction(action database.WorkflowAction) {
	db := config.C().DB
	if db == nil {
		return
	}
	if _, err := db.LogWorkflowAction(action); err != nil {
		slog.Warn("Failed to record workflow action",
			"workflow", action.WorkflowName, "action", action.Action,
			"pr", action.PRNumber, "repo", action.Repo, "error", err)
	}
}

// buildAndCacheMetadata constructs a PRMetadata-compatible JSON from the PR object
// and fetched review/CI data, then stores it in the metadata cache. It reports
// whether the metadata row was actually written, so the caller can log the
// field as written only when it landed.
func buildAndCacheMetadata(db *database.DB, key PRKey,
	pr *github.PullRequest,
	reviews []*github.PullRequestReview,
	combinedStatus *github.CombinedStatus,
	checkRuns *github.ListCheckRunsResults) bool {

	// Labels
	labels := []string{}
	for _, l := range pr.Labels {
		labels = append(labels, l.GetName())
	}

	// Assignees
	assignees := []string{}
	for _, u := range pr.Assignees {
		assignees = append(assignees, u.GetLogin())
	}

	// Requested Reviewers
	reviewerLogins := []string{}
	for _, r := range pr.RequestedReviewers {
		reviewerLogins = append(reviewerLogins, r.GetLogin())
	}
	teamLogins := []string{}
	for _, t := range pr.RequestedTeams {
		teamLogins = append(teamLogins, t.GetName())
	}

	// Process reviews for approval state
	approvedBy := []string{}
	changesRequestedBy := []string{}
	commentedBy := []string{}
	latestReviewState := make(map[string]string)
	for _, r := range reviews {
		latestReviewState[r.User.GetLogin()] = r.GetState()
	}
	for user, state := range latestReviewState {
		switch state {
		case "APPROVED":
			approvedBy = append(approvedBy, user)
		case "CHANGES_REQUESTED":
			changesRequestedBy = append(changesRequestedBy, user)
		case "COMMENTED":
			commentedBy = append(commentedBy, user)
		}
	}

	// CI Status string (matches GetPRDetails format)
	var ciStatus string
	var ciFailures []string
	if combinedStatus != nil || checkRuns != nil {
		total := 0
		success := 0
		overallState := "success"
		if combinedStatus != nil {
			if combinedStatus.GetState() != "success" && combinedStatus.GetState() != "" {
				overallState = combinedStatus.GetState()
			}
			total += combinedStatus.GetTotalCount()
			for _, s := range combinedStatus.Statuses {
				if s.GetState() == "success" {
					success++
				} else if s.GetState() == "failure" {
					ciFailures = append(ciFailures, fmt.Sprintf("%s: %s", s.GetContext(), s.GetDescription()))
				}
			}
		}
		if checkRuns != nil {
			total += checkRuns.GetTotal()
			for _, cr := range checkRuns.CheckRuns {
				if cr.GetConclusion() == "success" {
					success++
				} else if cr.GetConclusion() != "" && cr.GetConclusion() != "neutral" && cr.GetConclusion() != "skipped" {
					overallState = "failure"
					ciFailures = append(ciFailures, fmt.Sprintf("%s: %s", cr.GetName(), cr.GetConclusion()))
				}
			}
		}
		if total == 0 && overallState == "success" {
			overallState = "pending"
		}
		ciStatus = fmt.Sprintf("%s (%d/%d checks passed)", overallState, success, total)
	}

	milestone := ""
	if pr.Milestone != nil {
		milestone = pr.Milestone.GetTitle()
	}

	// WorktreePath from DB if it exists
	worktreePath, _ := db.GetWorktree(key.Number, key.Repo, key.Owner)

	// Look up existing release status from DB so it's preserved in the metadata cache
	existingReleaseStatus, _ := db.GetReleaseStatus(key.Owner, key.Repo, key.Number)

	// Construct metadata matching server.PRMetadata JSON field names
	metadata := struct {
		Number             int      `json:"number"`
		Title              string   `json:"title"`
		Author             string   `json:"author"`
		BaseRef            string   `json:"base_ref"`
		HeadRef            string   `json:"head_ref"`
		State              string   `json:"state"`
		Milestone          string   `json:"milestone"`
		Labels             []string `json:"labels"`
		Assignees          []string `json:"assignees"`
		Reviewers          []string `json:"reviewers"`
		RequestedTeams     []string `json:"requested_teams"`
		ApprovedBy         []string `json:"approved_by"`
		ChangesRequestedBy []string `json:"changes_requested_by"`
		CommentedBy        []string `json:"commented_by"`
		Draft              bool     `json:"draft"`
		CIStatus           string   `json:"ci_status"`
		CIFailures         []string `json:"ci_failures"`
		Body               string   `json:"body"`
		URL                string   `json:"url"`
		WorktreePath       string   `json:"worktree_path"`
		ReleaseStatus      string   `json:"release_status"`
	}{
		Number:             pr.GetNumber(),
		Title:              pr.GetTitle(),
		Author:             pr.User.GetLogin(),
		BaseRef:            pr.Base.GetRef(),
		HeadRef:            pr.Head.GetRef(),
		State:              pr.GetState(),
		Milestone:          milestone,
		Labels:             labels,
		Assignees:          assignees,
		Reviewers:          reviewerLogins,
		RequestedTeams:     teamLogins,
		ApprovedBy:         approvedBy,
		ChangesRequestedBy: changesRequestedBy,
		CommentedBy:        commentedBy,
		Draft:              pr.GetDraft(),
		CIStatus:           ciStatus,
		CIFailures:         ciFailures,
		Body:               pr.GetBody(),
		URL:                pr.GetHTMLURL(),
		WorktreePath:       worktreePath,
		ReleaseStatus:      existingReleaseStatus,
	}

	metadataWritten := false
	if j, err := json.Marshal(metadata); err == nil {
		if err := db.UpsertPRMetadataCache(key.Owner, key.Repo, key.Number, string(j)); err != nil {
			slog.Error("Failed to cache PR metadata", "pr", key.Number, "repo", key.Repo, "error", err)
		} else {
			metadataWritten = true
		}
	}

	// Run release check for closed PRs
	if pr.GetState() == "closed" {
		repoFullName := fmt.Sprintf("%s/%s", key.Owner, key.Repo)
		releaseCheckCmd := config.C().GetReleaseCheckCommand(repoFullName)
		if releaseCheckCmd != "" {
			mergeCommitSHA := pr.GetMergeCommitSHA()
			if mergeCommitSHA != "" {
				status, err := GetReleaseStatus(releaseCheckCmd, key.Owner, key.Repo, mergeCommitSHA)
				if err != nil {
					slog.Warn("Release check failed", "pr", key.Number, "repo", repoFullName, "error", err)
				} else {
					slog.Debug("Release check result", "pr", key.Number, "repo", repoFullName, "status", status)
					if err := db.UpsertReleaseStatus(key.Owner, key.Repo, key.Number, status); err != nil {
						slog.Error("Failed to store release status", "pr", key.Number, "repo", key.Repo, "error", err)
					}
				}
			}
		}
	}

	return metadataWritten
}
