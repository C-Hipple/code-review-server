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
	"sort"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/go-github/v48/github"
)

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

func deduplicateChanges(log *slog.Logger, changes []SerializedFileChange) []SerializedFileChange {
	changesByIdentifier := make(map[string][]SerializedFileChange)
	for _, change := range changes {
		identifier := change.FileChange.Identifier
		changesByIdentifier[identifier] = append(changesByIdentifier[identifier], change)
	}

	finalChanges := []SerializedFileChange{}
	log.Debug("Deduplicating changes", "count", len(changesByIdentifier))

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
			log.Debug("Found update, discarding other changes", "identifier", identifier)
			finalChanges = append(finalChanges, *updateChange)
		} else if addChange != nil {
			log.Debug("Found add, discarding delete", "identifier", identifier)
			finalChanges = append(finalChanges, *addChange)
		} else if deleteChange != nil {
			log.Debug("Found delete", "identifier", identifier)
			finalChanges = append(finalChanges, *deleteChange)
		}
	}
	return finalChanges
}

func ListenChanges(log *slog.Logger, channel chan FileChanges, wg *sync.WaitGroup) {
	changesMap := make(map[string][]SerializedFileChange)
	for fileChange := range channel {
		if fileChange.ChangeType == "No Change" {
			wg.Done()
			continue
		}
		fileChange.Report(log)
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
	go ApplyChanges(log, serialziedChannel, wg)

	for _, changes := range changesMap {
		deduplicatedChanges := deduplicateChanges(log, changes)
		numDeduplicated := len(changes) - len(deduplicatedChanges)
		if numDeduplicated > 0 {
			log.Debug("Deduplicated changes, adjusting WaitGroup", "count", numDeduplicated)
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

func ApplyChanges(log *slog.Logger, channel chan SerializedFileChange, wg *sync.WaitGroup) {
	changeCount := 0
	for deserializedChange := range channel {
		db := config.C().DB

		if config.C().AutoWorktree {
			handleWorktreeChange(log, db, deserializedChange)
		}

		switch deserializedChange.FileChange.ChangeType {
		case "Addition", "Update":
			_, err := db.UpsertItem(
				deserializedChange.FileChange.SectionID,
				deserializedChange.FileChange.Identifier,
				deserializedChange.FileChange.Status,
				deserializedChange.FileChange.Title,
				deserializedChange.FileChange.Details,
				deserializedChange.FileChange.Tags,
				deserializedChange.FileChange.TTL,
			)
			if err != nil {
				log.Error("Error upserting item", "error", err, "identifier", deserializedChange.FileChange.Identifier)
			}
		case "Delete":
			err := db.DeleteItem(deserializedChange.FileChange.SectionID, deserializedChange.FileChange.Identifier)
			if err != nil {
				log.Error("Error deleting item", "error", err, "identifier", deserializedChange.FileChange.Identifier)
			}
		}
		changeCount++
		wg.Done()
	}
	log.Info(fmt.Sprintf("Completed processing all DCR changes (%d total)", changeCount))
}

func handleWorktreeChange(log *slog.Logger, db *database.DB, change SerializedFileChange) {
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
		log.Debug("Skipping worktree management, repo not found locally", "path", repoDir)
		return
	}

	if change.FileChange.ChangeType == "Addition" || change.FileChange.ChangeType == "Update" {
		// Create worktree
		log.Info("Ensuring worktree exists", "pr", prNumber, "path", worktreePath)

		// Ensure worktree root exists
		if err := os.MkdirAll(worktreeRoot, 0755); err != nil {
			log.Error("Failed to create worktree root directory", "path", worktreeRoot, "error", err)
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
			log.Error("Failed to create worktree", "error", err)
		} else {
			if err := db.AddWorktree(prNumber, repoName, ownerName, worktreePath, branchName); err != nil {
				log.Error("Failed to record worktree in DB", "error", err)
			}
		}

	} else if change.FileChange.ChangeType == "Delete" {
		// Remove worktree
		path, err := db.GetWorktree(prNumber, repoName, ownerName)
		if err != nil {
			log.Error("Error checking for worktree", "error", err)
			return
		}
		if path != "" {
			log.Info("Removing worktree", "pr", prNumber, "path", path)
			if err := git_tools.RemoveWorktree(repoDir, path); err != nil {
				log.Error("Failed to remove worktree", "error", err)
			}
			if err := db.RemoveWorktreeRecord(prNumber, repoName, ownerName); err != nil {
				log.Error("Failed to remove worktree record from DB", "error", err)
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

func (ms ManagerService) runWorkflow(log *slog.Logger, workflow Workflow, prs []*github.PullRequest, workflow_chan chan FileChanges, file_change_wg *sync.WaitGroup) {
	// Helper which times the workflow run command.
	log.Info("Starting Workflow", "workflow", workflow.GetName())
	start := time.Now()
	result, err := workflow.Run(log, prs, workflow_chan, file_change_wg)
	duration := time.Since(start)
	if err != nil {
		log.Error("Errored in Workflow", "workflow", workflow.GetName(), "after", duration, "error", err)
	}
	log.Info("Finishing Workflow", "workflow", workflow.GetName(), "took", duration, "result", result.Report())
}

func (ms ManagerService) RunOnce(log *slog.Logger, file_change_wg *sync.WaitGroup) {
	client := git_tools.GetGithubClient()

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

	// Track which fetches failed so we can skip dependent workflows
	fetchErrors := make(map[string]bool) // key: "repoKey/state" or "repoKey/specific"

	// Fetching phase
	for repoKey, states := range repoStatePRs {
		owner, repo, _ := git_tools.ParseRepoName(repoKey)
		for state := range states {
			log.Debug("Fetching PRs", "repo", repoKey, "state", state)
			prs, err := git_tools.GetPRs(client, state, owner, repo)
			if err != nil {
				log.Error("Failed to fetch PRs", "repo", repoKey, "state", state, "error", err)
				fetchErrors[repoKey+"/"+state] = true
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
			log.Debug("Fetching specific PRs", "repo", repoKey, "count", len(numbers))
			prs, err := git_tools.GetSpecificPRs(client, owner, repo, numbers)
			if err != nil {
				log.Error("Failed to fetch specific PRs", "repo", repoKey, "error", err)
				fetchErrors[repoKey+"/specific"] = true
				continue
			}
			for _, pr := range prs {
				specificPRs[repoKey][*pr.Number] = pr
			}
		}
	}

	// Pre-fetch auxiliary data for all PRs
	auxDataStore := ms.prefetchAuxData(log, client, repoStatePRs, specificPRs)
	SetCurrentAuxDataStore(auxDataStore)
	defer SetCurrentAuxDataStore(nil)

	var wg sync.WaitGroup
	for _, workflow := range ms.Workflows {
		// Check if any of this workflow's PR requirements had a fetch error
		hasFetchError := false
		for _, req := range workflow.GetPRRequirements() {
			repoKey := fmt.Sprintf("%s/%s", req.Owner, req.Repo)
			if len(req.PRNumbers) > 0 {
				if fetchErrors[repoKey+"/specific"] {
					hasFetchError = true
					break
				}
			} else {
				if fetchErrors[repoKey+"/"+req.State] {
					hasFetchError = true
					break
				}
			}
		}
		if hasFetchError {
			log.Warn("Skipping workflow due to PR fetch error; will retry next cycle", "workflow", workflow.GetName())
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
			ms.runWorkflow(log, workflow, prs, ms.workflow_chan, file_change_wg)
		}(workflow, workflowPRs)
	}
	if waitTimeout(&wg, 240*time.Second) {
		log.Error("RunOnce waitgroup timed out waiting for workflows")
	} else {
		log.Info("Completed RunOnce Waitgroup")
	}
}

func (ms *ManagerService) Run(log *slog.Logger) {
	log.Info("Starting Service")

	// Advisory lock to prevent multiple concurrent syncs (skip for oneoff mode)
	if !ms.oneoff {
		crsHome, err := os.UserHomeDir() // Fallback to home if getCRSHome fails but we use config.UserHomeDir elsewhere
		if err == nil {
			crsHome = filepath.Join(crsHome, ".crs")
			if err := os.MkdirAll(crsHome, 0755); err != nil {
				log.Error("Failed to create CRS directory for lock file", "path", crsHome, "error", err)
			}
			lockPath := filepath.Join(crsHome, "codereviewserver_sync.lock")
			lockFile, err := os.Create(lockPath)
			if err == nil {
				err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
				if err != nil {
					log.Warn("Another instance is already running background sync, skipping sync in this process.")
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
		go ListenChanges(log, ms.workflow_chan, &listener_wg)

		log.Info("Running Once")
		ms.RunOnce(log, &listener_wg)
		close(ms.workflow_chan)
		listener_wg.Done()
		if waitTimeout(&listener_wg, 240*time.Second) {
			log.Error("Listener waitgroup timed out waiting for changes to be applied")
		}
	} else {
		cycle_count := 0
		for {
			// Reload config and workflows before each cycle
			if err := config.Reload(); err != nil {
				log.Error("Failed to reload config before cycle", "error", err)
			} else {
				// Re-generate workflows
				cfg := config.C()
				ms.Workflows = MatchWorkflows(cfg.RawWorkflows, &cfg.Repos, cfg.JiraDomain)
				ms.sleepTime = cfg.SleepDuration
				ms.Initialize()
			}

			log.Info("Cycle", "count", cycle_count, "sleepTime", ms.sleepTime)
			var cycle_wg sync.WaitGroup
			cycle_wg.Add(1)
			ms.workflow_chan = make(chan FileChanges)

			go ListenChanges(log, ms.workflow_chan, &cycle_wg)
			ms.RunOnce(log, &cycle_wg)
			close(ms.workflow_chan)
			cycle_wg.Done()

			if waitTimeout(&cycle_wg, 240*time.Second) {
				log.Error("Cycle waitgroup timed out waiting for changes to be applied")
			}
			// Render org files after each cycle
			time.Sleep(ms.sleepTime)
			cycle_count++
		}
	}
	log.Info("Exiting Service")
}

func (ms *ManagerService) Initialize() {
	// Ensure all required sections exist.
	// Does this sync since GetSection has creation side effect
	db := config.C().DB
	for _, wf := range ms.Workflows {
		db.GetOrCreateSection(wf.GetOrgSectionName(), config.C().SectionPriority[wf.GetOrgSectionName()])
	}
}

// prefetchAuxData gathers auxiliary data for all PRs that need it
func (ms ManagerService) prefetchAuxData(log *slog.Logger, client *github.Client,
	repoStatePRs map[string]map[string][]*github.PullRequest,
	specificPRs map[string]map[int]*github.PullRequest) *AuxDataStore {

	store := NewAuxDataStore()

	// Collect all PRs and merge their aux data requirements
	prRequirements := make(map[PRKey]AuxDataRequirement)
	prObjects := make(map[PRKey]*github.PullRequest)

	for _, wf := range ms.Workflows {
		for _, req := range wf.GetPRRequirements() {
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

				// Merge requirements (OR them together)
				existing := prRequirements[key]
				existing.Comments = existing.Comments || req.AuxData.Comments
				existing.CIStatus = existing.CIStatus || req.AuxData.CIStatus
				existing.Diff = existing.Diff || req.AuxData.Diff
				prRequirements[key] = existing
			}
		}
	}

	// Fetch aux data in parallel
	var wg sync.WaitGroup
	for key, auxReq := range prRequirements {
		// Skip if no aux data is needed
		if !auxReq.Comments && !auxReq.CIStatus && !auxReq.Diff {
			continue
		}

		wg.Add(1)
		go func(key PRKey, auxReq AuxDataRequirement, pr *github.PullRequest) {
			defer wg.Done()
			auxData := fetchAuxDataForPR(log, client, key, auxReq, pr)
			store.Set(key, auxData)
		}(key, auxReq, prObjects[key])
	}
	wg.Wait()

	log.Info("Pre-fetched auxiliary data", "pr_count", len(prRequirements))
	return store
}

// fetchAuxDataForPR fetches the requested auxiliary data for a single PR
// and persists it to the DB cache so that GetPRDetails can find it.
func fetchAuxDataForPR(log *slog.Logger, client *github.Client,
	key PRKey, req AuxDataRequirement, pr *github.PullRequest) *PRAuxData {

	auxData := &PRAuxData{}

	headSHA := ""
	if pr != nil && pr.Head != nil && pr.Head.SHA != nil {
		auxData.HeadSHA = *pr.Head.SHA
		headSHA = *pr.Head.SHA
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
			if err != nil {
				log.Warn("Failed to fetch comments for pre-fetch", "pr", key.Number, "repo", key.Repo, "error", err)
				return
			}

			// Store PR review comments only for workflow display (issue comments
			// would cause nil panics in formatComments due to nil DiffHunk)
			auxData.Comments = prComments
			auxData.CommentsCount = len(prComments)

			// Also fetch issue comments for DB cache (GetPRDetails expects both)
			combined := make([]*github.PullRequestComment, len(prComments))
			copy(combined, prComments)
			issueComments, _, err := client.Issues.ListComments(
				context.Background(), key.Owner, key.Repo, key.Number, nil)
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
				return combined[i].CreatedAt.Before(*combined[j].CreatedAt)
			})
			allCommentsForDB = combined
		}()
	}

	// 2. CI Status for workflow display (uses Actions API)
	if req.CIStatus && pr != nil && pr.Head != nil && pr.Head.Label != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
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
			diffResp, _, err := client.PullRequests.GetRaw(ctx, key.Owner, key.Repo, key.Number, github.RawOptions{Type: github.Diff})
			if err != nil {
				log.Warn("Failed to fetch diff for pre-fetch", "pr", key.Number, "repo", key.Repo, "error", err)
				return
			}
			auxData.Diff = diffResp
			auxData.DiffLines = []string{"*** Diff\n", diffResp}
		}()
	}

	// 4. Reviews (for metadata: approved_by, changes_requested_by, etc.)
	wg.Add(1)
	go func() {
		defer wg.Done()
		reviews, _, err := client.PullRequests.ListReviews(
			context.Background(), key.Owner, key.Repo, key.Number, nil)
		if err != nil {
			log.Warn("Failed to fetch reviews for pre-fetch", "pr", key.Number, "error", err)
			return
		}
		ghReviews = reviews
	}()

	// 5. Commit Status + Check Runs (for metadata CI status)
	if headSHA != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, err := git_tools.GetCombinedStatus(client, key.Owner, key.Repo, headSHA)
			if err == nil {
				combinedStatus = status
			}
			cr, err := git_tools.GetCheckRuns(client, key.Owner, key.Repo, headSHA)
			if err == nil {
				checkRunsResult = cr
			}
		}()
	}

	// 6. Commits
	wg.Add(1)
	go func() {
		defer wg.Done()
		commits, _, err := client.PullRequests.ListCommits(
			context.Background(), key.Owner, key.Repo, key.Number, nil)
		if err != nil {
			log.Warn("Failed to fetch commits for pre-fetch", "pr", key.Number, "error", err)
			return
		}
		ghCommits = commits
	}()

	wg.Wait()

	// Persist all fetched data to DB so GetPRDetails finds it cached
	persistPRCacheData(log, key, pr, auxData, allCommentsForDB, ghReviews, ghCommits, combinedStatus, checkRunsResult)

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
func persistPRCacheData(log *slog.Logger, key PRKey, pr *github.PullRequest,
	auxData *PRAuxData,
	allComments []*github.PullRequestComment,
	reviews []*github.PullRequestReview,
	commits []*github.RepositoryCommit,
	combinedStatus *github.CombinedStatus,
	checkRuns *github.ListCheckRunsResults) {

	db := config.C().DB

	// 1. Comments
	if allComments != nil {
		if j, err := json.Marshal(allComments); err == nil {
			db.UpsertPRComments(key.Number, key.Repo, string(j))
		}
	}

	// 2. Diff + SHA
	if auxData.Diff != "" {
		db.UpsertPullRequest(key.Number, key.Repo, auxData.HeadSHA, auxData.Diff)
	} else if auxData.HeadSHA != "" {
		// Store SHA even without diff so GetPRDetails can look up CI status
		db.UpsertPullRequest(key.Number, key.Repo, auxData.HeadSHA, "")
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
		var rvs []reviewJSON
		for _, r := range reviews {
			var submittedAt time.Time
			if r.SubmittedAt != nil {
				submittedAt = *r.SubmittedAt
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
			db.UpsertPRReviews(key.Number, key.Repo, string(j))
		}
	}

	// 4. CI Status (commit status + check runs, same format as server.CombinedPRStatus)
	if combinedStatus != nil || checkRuns != nil {
		combined := struct {
			Status    *github.CombinedStatus      `json:"status"`
			CheckRuns *github.ListCheckRunsResults `json:"check_runs"`
		}{Status: combinedStatus, CheckRuns: checkRuns}
		if j, err := json.Marshal(combined); err == nil && auxData.HeadSHA != "" {
			db.UpsertCIStatus(key.Number, key.Repo, auxData.HeadSHA, string(j))
		}
	}

	// 5. Requested Reviewers (from PR object, no extra API call needed)
	if pr != nil {
		reviewers := &github.Reviewers{
			Users: pr.RequestedReviewers,
			Teams: pr.RequestedTeams,
		}
		if j, err := json.Marshal(reviewers); err == nil {
			db.UpsertRequestedReviewers(key.Number, key.Repo, string(j))
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
			db.UpsertPRCommits(key.Number, key.Repo, string(j))
		}
	}

	// 7. PR Metadata (constructed from PR object + fetched data)
	if pr != nil {
		buildAndCacheMetadata(log, db, key, pr, reviews, combinedStatus, checkRuns)
	}
}

// buildAndCacheMetadata constructs a PRMetadata-compatible JSON from the PR object
// and fetched review/CI data, then stores it in the metadata cache.
func buildAndCacheMetadata(log *slog.Logger, db *database.DB, key PRKey,
	pr *github.PullRequest,
	reviews []*github.PullRequestReview,
	combinedStatus *github.CombinedStatus,
	checkRuns *github.ListCheckRunsResults) {

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
	}

	if j, err := json.Marshal(metadata); err == nil {
		db.UpsertPRMetadataCache(key.Owner, key.Repo, key.Number, string(j))
	}
}
