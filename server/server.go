package server

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/go-github/v48/github"
)

// testing mutable state
// RPC handler is recreated for each request, it's not stateful across requests
// simulate a db lol
var CurrentCount int

func RunServer(log *slog.Logger) {
	server := rpc.NewServer()
	handler := &RPCHandler{Log: log}
	if err := server.Register(handler); err != nil {
		log.Error("Error registering RPC handler", "error", err)
		return
	}

	server.ServeCodec(jsonrpc.NewServerCodec(&Stdio{}))
}

type Stdio struct{}

func (s *Stdio) Read(p []byte) (n int, err error) {
	return os.Stdin.Read(p)
}

func (s *Stdio) Write(p []byte) (n int, err error) {
	return os.Stdout.Write(p)
}

func (s *Stdio) Close() error {
	return nil
}

type RPCHandler struct {
	Log *slog.Logger
}

type HelloArgs struct{}
type HelloReply struct {
	Count   int
	Content string
}

func (h *RPCHandler) Hello(args *HelloArgs, reply *HelloReply) error {
	var count int
	err := config.C().DB.QueryRow("SELECT COUNT(*) FROM sections").Scan(&count)
	if err != nil {
		h.Log.Error("Error counting items", "error", err)
		return err
	}
	CurrentCount += count
	reply.Content = fmt.Sprintf("hello %d", CurrentCount)
	reply.Count = count

	return nil
}

type GetReviewsArgs struct{}

type GetReviewsReply struct {
	Content string       `json:"content"` // Kept for simplicity on org-mode clients
	Items   []ReviewItem `json:"items"`
}

// prStatusOrder returns the sort order for a ReviewItem's status.
// Open PRs sort first (0), then draft (1), then merged (2), then closed (3).
func prStatusOrder(item ReviewItem) int {
	switch item.Status {
	case "TODO":
		return 0 // open
	case "WAITING":
		return 1 // draft
	case "DONE":
		if strings.Contains(item.Tags, "merged") {
			return 2 // merged
		}
		return 3 // closed
	default:
		return 4
	}
}

func (h *RPCHandler) GetAllReviews(args *GetReviewsArgs, reply *GetReviewsReply) error {
	if err := config.Reload(); err != nil {
		h.Log.Error("Error reloading config", "error", err)
	}

	renderer := NewOrgRenderer(config.C().DB)
	content, items, err := renderer.RenderAndGetItems()
	if err != nil {
		h.Log.Error("Error rendering org files", "error", err)
		return err
	}
	reply.Content = content
	if items == nil {
		reply.Items = []ReviewItem{}
	} else {
		sort.SliceStable(items, func(i, j int) bool {
			si, sj := prStatusOrder(items[i]), prStatusOrder(items[j])
			if si != sj {
				return si < sj
			}
			if items[i].Repo != items[j].Repo {
				return items[i].Repo < items[j].Repo
			}
			return items[i].Number < items[j].Number
		})
		reply.Items = items
	}
	return nil
}

type GetPRstructArgs struct {
	Repo      string `json:"Repo"`
	Owner     string `json:"Owner"`
	Number    int    `json:"Number"`
	SkipCache bool   `json:"SkipCache"`
}

type GetPRReply struct {
	Okay             bool          `json:"okay"`
	Content          string        `json:"content"`
	Metadata         *PRMetadata   `json:"metadata"`
	Diff             string        `json:"diff"`
	Comments         []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews          []ReviewJSON  `json:"reviews"`
	Commits          []CommitJSON  `json:"commits"`
	Feedback         string        `json:"feedback"`
}

func (h *RPCHandler) GetPR(args *GetPRstructArgs, reply *GetPRReply) error {
	details, content, err := h.fetchPRAndRunPlugins(args.Owner, args.Repo, args.Number, args.SkipCache)
	if err != nil {
		return err
	}

	reply.Content = content
	reply.Metadata = &details.Metadata
	reply.Diff = details.Diff
	reply.Comments = details.Comments
	reply.OutdatedComments = details.OutdatedComments
	reply.Reviews = details.Reviews
	reply.Commits = details.Commits
	reply.Okay = true

	feedback, _ := config.C().DB.GetFeedback(args.Owner, args.Repo, args.Number)
	reply.Feedback = feedback
	return nil
}

// fetchPRAndRunPlugins is a helper to centralize PR fetching, cache handling, and plugin triggering
func (h *RPCHandler) fetchPRAndRunPlugins(owner, repo string, number int, skipCache bool) (*PRDetails, string, error) {
	details, err := GetPRDetails(owner, repo, number, skipCache)
	if err != nil {
		h.Log.Error("Error fetching PR details", "error", err)
		return nil, "", err
	}

	// Trigger async plugin execution
	commentsJSON := "[]"
	rawComments, _ := config.C().DB.GetPRComments(number, repo)
	if rawComments != "" {
		commentsJSON = rawComments
	}

	// Extract SHA from DB
	_, sha, _ := config.C().DB.GetPullRequest(number, repo)

	// Run plugins in background
	metadataJSON, _ := json.Marshal(details.Metadata)
	go RunPlugins(owner, repo, number, sha, details.Diff, commentsJSON, string(metadataJSON))

	// Get the full formatted response for the UI.
	// We pass the already fetched details to avoid redundant API calls.
	content, _ := GetFullPRResponse(owner, repo, number, false, details)

	return details, content, nil
}

type GetAdjacentPRArgs struct {
	Repo      string `json:"Repo"`
	Owner     string `json:"Owner"`
	Number    int    `json:"Number"`
	SkipCache bool   `json:"SkipCache"`
	Previous  bool   `json:"Previous"` // true = previous PR, false = next PR
}

// GetAdjacentPRReply extends the standard PR reply with the adjacent PR's identity,
// so clients don't need to parse the GitHub URL to know where to navigate next.
type GetAdjacentPRReply struct {
	GetPRReply
	AdjacentOwner  string `json:"adjacent_owner"`
	AdjacentRepo   string `json:"adjacent_repo"`
	AdjacentNumber int    `json:"adjacent_number"`
}

func (h *RPCHandler) GetAdjacentPR(args *GetAdjacentPRArgs, reply *GetAdjacentPRReply) error {
	renderer := NewOrgRenderer(config.C().DB)
	_, items, err := renderer.RenderAndGetItems()
	if err != nil {
		return err
	}

	// Sort the same way as GetAllReviews
	sort.SliceStable(items, func(i, j int) bool {
		si, sj := prStatusOrder(items[i]), prStatusOrder(items[j])
		if si != sj {
			return si < sj
		}
		if items[i].Repo != items[j].Repo {
			return items[i].Repo < items[j].Repo
		}
		return items[i].Number < items[j].Number
	})

	// Find the current PR in the sorted list
	currentIdx := -1
	for i, item := range items {
		if item.Repo == args.Repo && item.Number == args.Number {
			currentIdx = i
			break
		}
	}

	if currentIdx == -1 {
		return fmt.Errorf("PR %s/%s#%d not found in reviews", args.Owner, args.Repo, args.Number)
	}

	// Get adjacent index, wrapping around at both ends
	adjacentIdx := (currentIdx + 1) % len(items)
	if args.Previous {
		adjacentIdx = (currentIdx - 1 + len(items)) % len(items)
	}

	adjacent := items[adjacentIdx]
	h.Log.Info("GetAdjacentPR returning", "owner", adjacent.Owner, "repo", adjacent.Repo, "number", adjacent.Number)
	details, content, err := h.fetchPRAndRunPlugins(adjacent.Owner, adjacent.Repo, adjacent.Number, args.SkipCache)
	if err != nil {
		return err
	}

	reply.AdjacentOwner = adjacent.Owner
	reply.AdjacentRepo = adjacent.Repo
	reply.AdjacentNumber = adjacent.Number
	reply.Content = content
	reply.Metadata = &details.Metadata
	reply.Diff = details.Diff
	reply.Comments = details.Comments
	reply.OutdatedComments = details.OutdatedComments
	reply.Reviews = details.Reviews
	reply.Commits = details.Commits
	reply.Okay = true

	feedback, _ := config.C().DB.GetFeedback(adjacent.Owner, adjacent.Repo, adjacent.Number)
	reply.Feedback = feedback
	return nil
}

type AddCommentArgs struct {
	Owner     string `json:"Owner"`
	Repo      string `json:"Repo"`
	Number    int    `json:"Number"`
	Filename  string
	Position  int64
	Body      string
	ReplyToID *int64
}

type AddCommentReply struct {
	ID               int64         `json:"id"`
	Content          string        `json:"content"`
	Metadata         *PRMetadata   `json:"metadata"`
	Diff             string        `json:"diff"`
	Comments         []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews          []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) AddComment(args *AddCommentArgs, reply *AddCommentReply) error {
	comment, err := config.C().DB.InsertLocalComment(args.Owner, args.Repo, args.Number, args.Filename, args.Position, &args.Body, args.ReplyToID)
	if err != nil {
		h.Log.Error("Error inserting local comment", "error", err)
		return err
	}
	reply.ID = comment.ID

	details, content, err := h.fetchPRAndRunPlugins(args.Owner, args.Repo, args.Number, false)
	if err != nil {
		return err
	}

	reply.Content = content
	reply.Metadata = &details.Metadata
	reply.Diff = details.Diff
	reply.Comments = details.Comments
	reply.OutdatedComments = details.OutdatedComments
	reply.Reviews = details.Reviews
	return nil
}

type EditCommentArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
	ID     int64  `json:"ID"`
	Body   string `json:"Body"`
}

type EditCommentReply struct {
	Okay             bool          `json:"okay"`
	Content          string        `json:"content"`
	Metadata         *PRMetadata   `json:"metadata"`
	Diff             string        `json:"diff"`
	Comments         []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews          []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) EditComment(args *EditCommentArgs, reply *EditCommentReply) error {
	err := config.C().DB.UpdateLocalComment(args.ID, args.Body)
	if err != nil {
		h.Log.Error("Error updating local comment", "error", err)
		return err
	}
	reply.Okay = true

	details, content, err := h.fetchPRAndRunPlugins(args.Owner, args.Repo, args.Number, false)
	if err != nil {
		return err
	}

	reply.Content = content
	reply.Metadata = &details.Metadata
	reply.Diff = details.Diff
	reply.Comments = details.Comments
	reply.OutdatedComments = details.OutdatedComments
	reply.Reviews = details.Reviews
	return nil
}

type DeleteCommentArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
	ID     int64  `json:"ID"`
}

type DeleteCommentReply struct {
	Okay             bool          `json:"okay"`
	Content          string        `json:"content"`
	Metadata         *PRMetadata   `json:"metadata"`
	Diff             string        `json:"diff"`
	Comments         []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews          []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) DeleteComment(args *DeleteCommentArgs, reply *DeleteCommentReply) error {
	err := config.C().DB.DeleteLocalComment(args.ID)
	if err != nil {
		h.Log.Error("Error deleting local comment", "error", err)
		return err
	}
	reply.Okay = true

	details, content, err := h.fetchPRAndRunPlugins(args.Owner, args.Repo, args.Number, false)
	if err != nil {
		return err
	}

	reply.Content = content
	reply.Metadata = &details.Metadata
	reply.Diff = details.Diff

	// Ensure empty slices are returned as [] not null in JSON
	if details.Comments == nil {
		reply.Comments = []CommentJSON{}
	} else {
		reply.Comments = details.Comments
	}
	if details.OutdatedComments == nil {
		reply.OutdatedComments = []CommentJSON{}
	} else {
		reply.OutdatedComments = details.OutdatedComments
	}
	if details.Reviews == nil {
		reply.Reviews = []ReviewJSON{}
	} else {
		reply.Reviews = details.Reviews
	}

	return nil
}

type SetFeedbackArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
	Body   string
}

type SetFeedbackReply struct {
	ID               int64         `json:"id"`
	Content          string        `json:"content"`
	Metadata         *PRMetadata   `json:"metadata"`
	Diff             string        `json:"diff"`
	Comments         []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews          []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) SetFeedback(args *SetFeedbackArgs, reply *SetFeedbackReply) error {
	err := config.C().DB.InsertFeedback(args.Owner, args.Repo, args.Number, &args.Body)
	if err != nil {
		h.Log.Error("Error inserting feedback", "error", err)
		return err
	}

	details, content, err := h.fetchPRAndRunPlugins(args.Owner, args.Repo, args.Number, false)
	if err != nil {
		return err
	}

	reply.Content = content
	reply.Metadata = &details.Metadata
	reply.Diff = details.Diff
	reply.Comments = details.Comments
	reply.OutdatedComments = details.OutdatedComments
	reply.Reviews = details.Reviews
	return nil
}

type RemovePRCommentsArgs struct {
	Repo   string `json:"Repo"`
	Owner  string `json:"Owner"`
	Number int    `json:"Number"`
}

type RemovePRCommentsReply struct {
	Okay             bool          `json:"okay"`
	Content          string        `json:"content"`
	Metadata         *PRMetadata   `json:"metadata"`
	Diff             string        `json:"diff"`
	Comments         []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews          []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) RemovePRComments(args *RemovePRCommentsArgs, reply *RemovePRCommentsReply) error {
	err := config.C().DB.DeleteLocalCommentsForPR(args.Owner, args.Repo, args.Number)
	if err != nil {
		h.Log.Error("Error removing local comments", "error", err)
		return err
	}
	reply.Okay = true

	details, content, err := h.fetchPRAndRunPlugins(args.Owner, args.Repo, args.Number, false)
	if err != nil {
		return err
	}

	reply.Content = content
	reply.Metadata = &details.Metadata
	reply.Diff = details.Diff
	reply.Comments = details.Comments
	reply.OutdatedComments = details.OutdatedComments
	reply.Reviews = details.Reviews
	return nil
}

type SubmitReviewArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
	Event  string `json:"Event"` // APPROVE, REQUEST_CHANGES, or COMMENT
	Body   string `json:"Body"`  // Top-level review body (optional)
}

type SubmitReviewReply struct {
	Okay             bool          `json:"okay"`
	Content          string        `json:"content"`
	Metadata         *PRMetadata   `json:"metadata"`
	Diff             string        `json:"diff"`
	Comments         []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews          []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) SubmitReview(args *SubmitReviewArgs, reply *SubmitReviewReply) error {
	// 1. Fetch Local Comments
	comments, err := config.C().DB.GetLocalCommentsForPR(args.Owner, args.Repo, args.Number)
	if err != nil {
		h.Log.Error("Error fetching local comments", "error", err)
		return err
	}

	// 2. Construct Review Request
	client := git_tools.GetGithubClient()
	var reviewComments []*github.DraftReviewComment
	for _, c := range comments {
		if c.Body == nil {
			continue
		}
		if c.ReplyToID != nil {
			err := git_tools.SubmitReply(client, args.Owner, args.Repo, args.Number, *c.Body, *c.ReplyToID)
			if err != nil {
				h.Log.Error("Error submitting reply", "error", err)
			}
		} else {
			// Top-level comments
			pos := int(c.Position)
			body := *c.Body
			reviewComments = append(reviewComments, &github.DraftReviewComment{
				Path:     &c.Filename,
				Position: &pos,
				Body:     &body,
			})
		}
	}

	reviewRequest := &github.PullRequestReviewRequest{
		Event:    &args.Event,
		Comments: reviewComments,
	}
	if args.Body != "" {
		reviewRequest.Body = &args.Body
	}

	// 3. Submit to GitHub
	err = git_tools.SubmitReview(client, args.Owner, args.Repo, args.Number, reviewRequest)
	if err != nil {
		h.Log.Error("Error submitting review to GitHub", "error", err)
		return err
	}

	// 4. Clean up Local Comments
	err = config.C().DB.DeleteLocalCommentsForPR(args.Owner, args.Repo, args.Number)
	if err != nil {
		h.Log.Error("Error deleting local comments after submission", "error", err)
	}

	// 5. Remove the item from all sections in the database
	// The identifier is constructed as RepoName + PRNumber (matching PRToOrgBridge.Identifier)
	identifier := fmt.Sprintf("%s%d", args.Repo, args.Number)
	err = config.C().DB.DeleteItemByIdentifier(identifier)
	if err != nil {
		h.Log.Error("Error removing item from sections after review", "identifier", identifier, "error", err)
	}

	reply.Okay = true

	details, content, err := h.fetchPRAndRunPlugins(args.Owner, args.Repo, args.Number, true)
	if err != nil {
		return err
	}

	reply.Content = content
	reply.Metadata = &details.Metadata
	reply.Diff = details.Diff
	reply.Comments = details.Comments
	reply.OutdatedComments = details.OutdatedComments
	reply.Reviews = details.Reviews
	return nil
}

type SyncPRArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
}

type SyncPRReply struct {
	Okay             bool          `json:"okay"`
	Content          string        `json:"content"`
	Metadata         *PRMetadata   `json:"metadata"`
	Diff             string        `json:"diff"`
	Comments         []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews          []ReviewJSON  `json:"reviews"`
	Commits          []CommitJSON  `json:"commits"`
	Feedback         string        `json:"feedback"`
}

func (h *RPCHandler) SyncPR(args *SyncPRArgs, reply *SyncPRReply) error {
	details, content, err := h.fetchPRAndRunPlugins(args.Owner, args.Repo, args.Number, true)
	if err != nil {
		return err
	}

	reply.Content = content
	reply.Metadata = &details.Metadata
	reply.Diff = details.Diff
	reply.Comments = details.Comments
	reply.OutdatedComments = details.OutdatedComments
	reply.Reviews = details.Reviews
	reply.Commits = details.Commits
	reply.Okay = true

	feedback, _ := config.C().DB.GetFeedback(args.Owner, args.Repo, args.Number)
	reply.Feedback = feedback
	return nil
}

type ListPluginsArgs struct{}
type ListPluginsReply struct {
	Plugins []config.Plugin `json:"plugins"`
}

func (h *RPCHandler) ListPlugins(args *ListPluginsArgs, reply *ListPluginsReply) error {
	reply.Plugins = config.C().Plugins
	return nil
}

type GetRateLimitStatusArgs struct{}
type GetRateLimitStatusReply struct {
	Remaining        int    `json:"remaining"`
	Limit            int    `json:"limit"`
	ResetAt          string `json:"reset_at"`
	TotalRequests    int64  `json:"total_requests"`
	ThrottledCount   int64  `json:"throttled_count"`
	RateLimitedCount int64  `json:"rate_limited_count"`
}

func (h *RPCHandler) GetRateLimitStatus(args *GetRateLimitStatusArgs, reply *GetRateLimitStatusReply) error {
	status := git_tools.GetRateLimitStatus()
	reply.Remaining = status.Remaining
	reply.Limit = status.Limit
	reply.ResetAt = status.ResetAt.Format("2006-01-02 15:04:05 MST")
	reply.TotalRequests = status.TotalRequests
	reply.ThrottledCount = status.ThrottledCount
	reply.RateLimitedCount = status.RateLimitedCount
	return nil
}

// GetLocalRepoPath returns the expected location of a local repository.
// It does NOT check if the directory actually exists.
func GetLocalRepoPath(repo string) (string, error) {
	repoLocation := config.C().RepoLocation
	if len(repoLocation) > 0 && repoLocation[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		repoLocation = fmt.Sprintf("%s/%s", home, repoLocation[2:])
	}

	repoPath := fmt.Sprintf("%s/%s", repoLocation, repo)
	print(repoPath)
	// Clean path to remove double slashes if any
	return filepath.Clean(repoPath), nil
}

type GetPluginOutputArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
}

type GetPluginOutputReply struct {
	Output map[string]database.PluginResult `json:"output"`
}

// GetPluginOutput returns all stored plugin outputs for the given PR
func (h *RPCHandler) GetPluginOutput(args *GetPluginOutputArgs, reply *GetPluginOutputReply) error {
	results, err := config.C().DB.GetPluginResults(args.Owner, args.Repo, args.Number)
	if err != nil {
		h.Log.Error("Error fetching plugin results", "error", err)
		return err
	}

	// If no results found, or if we want to ensure they are at least triggered,
	// we call fetchPRAndRunPlugins (which is async for the plugin part).
	if len(results) == 0 {
		h.Log.Info("No plugin results found, triggering async run", "pr", args.Number)
		go h.fetchPRAndRunPlugins(args.Owner, args.Repo, args.Number, false)
	}

	reply.Output = results
	return nil
}

type RerunPluginsArgs struct {
	Owner   string   `json:"Owner"`
	Repo    string   `json:"Repo"`
	Number  int      `json:"Number"`
	Plugins []string `json:"Plugins"` // Optional: specific plugins to rerun. If empty, all plugins rerun.
}

type RerunPluginsReply struct {
	Okay    bool                             `json:"okay"`
	Message string                           `json:"message"`
	Output  map[string]database.PluginResult `json:"output"`
}

// RerunPlugins forces reexecution of plugins for a given PR, bypassing SHA cache checks.
// If Plugins array is specified, only those plugins are rerun; otherwise all are rerun.
func (h *RPCHandler) RerunPlugins(args *RerunPluginsArgs, reply *RerunPluginsReply) error {
	// Clear plugin results to force rerun
	if len(args.Plugins) == 0 {
		// Clear all plugin results for this PR
		err := config.C().DB.DeletePluginResultsForPR(args.Owner, args.Repo, args.Number, "")
		if err != nil {
			h.Log.Error("Error clearing plugin results", "error", err)
			return err
		}
		h.Log.Info("Cleared all plugin results for PR, triggering rerun", "repo", args.Repo, "pr", args.Number)
	} else {
		// Clear only specified plugin results
		for _, pluginName := range args.Plugins {
			err := config.C().DB.DeletePluginResultsForPR(args.Owner, args.Repo, args.Number, pluginName)
			if err != nil {
				h.Log.Error("Error clearing plugin result", "plugin", pluginName, "error", err)
				return err
			}
		}
		h.Log.Info("Cleared specific plugin results for PR, triggering rerun", "repo", args.Repo, "pr", args.Number, "plugins", args.Plugins)
	}

	// Fetch PR details and trigger plugins
	details, err := GetPRDetails(args.Owner, args.Repo, args.Number, false)
	if err != nil {
		h.Log.Error("Error fetching PR details for plugin rerun", "error", err)
		return err
	}

	// Get PR metadata and diff
	commentsJSON := "[]"
	rawComments, _ := config.C().DB.GetPRComments(args.Number, args.Repo)
	if rawComments != "" {
		commentsJSON = rawComments
	}

	_, sha, _ := config.C().DB.GetPullRequest(args.Number, args.Repo)

	// Run plugins with force flag
	metadataJSON, _ := json.Marshal(details.Metadata)
	go RunPluginsForce(args.Owner, args.Repo, args.Number, sha, details.Diff, commentsJSON, string(metadataJSON), true, args.Plugins)

	reply.Okay = true
	if len(args.Plugins) == 0 {
		reply.Message = fmt.Sprintf("Rerunning all plugins for PR %d", args.Number)
	} else {
		reply.Message = fmt.Sprintf("Rerunning %d plugin(s) for PR %d", len(args.Plugins), args.Number)
	}

	// Return empty output for now (plugins running async)
	reply.Output = make(map[string]database.PluginResult)
	return nil
}
