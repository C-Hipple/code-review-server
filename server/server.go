package server

import (
	"bytes"
	"context"
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"crs/utils"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

// reviewItemLess is the canonical ordering used for review items across the
// RPC surface: primary key is status, then most-recently-created first, with
// repo and PR number as tie-breakers.
func reviewItemLess(a, b ReviewItem) bool {
	si, sj := prStatusOrder(a), prStatusOrder(b)
	if si != sj {
		return si < sj
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	if a.Repo != b.Repo {
		return a.Repo < b.Repo
	}
	return a.Number < b.Number
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

	// Dispatch post-update hooks (plugins + experimental file ordering); each
	// hook runs in its own goroutine so this returns immediately.
	metadataJSON, _ := json.Marshal(details.Metadata)
	RunPostUpdatePRHooks(owner, repo, number, sha, details.Diff, commentsJSON, string(metadataJSON))

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

	// Find the current PR in the review list
	currentIdx := -1
	for i, item := range items {
		if item.Owner == args.Owner && item.Repo == args.Repo && item.Number == args.Number {
			currentIdx = i
			break
		}
	}

	if len(items) == 0 {
		return fmt.Errorf("no PRs in the review list")
	}

	var adjacentIdx int
	if currentIdx == -1 {
		// Current PR is no longer in the list (e.g. after submitting a review);
		// navigate to the first item instead of blocking the user.
		adjacentIdx = 0
	} else if len(items) == 1 {
		return fmt.Errorf("only one PR in the review list")
	} else {
		// Get adjacent index, wrapping around at both ends
		adjacentIdx = (currentIdx + 1) % len(items)
		if args.Previous {
			adjacentIdx = (currentIdx - 1 + len(items)) % len(items)
		}
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

type MergePRArgs struct {
	Owner       string `json:"Owner"`
	Repo        string `json:"Repo"`
	Number      int    `json:"Number"`
	MergeMethod string `json:"MergeMethod"` // Optional: "merge", "squash", or "rebase". Defaults to "squash" if empty.
}

type MergePRReply struct {
	Merged  bool   `json:"merged"`
	SHA     string `json:"sha"`
	Message string `json:"message"`
}

// MergePR asks GitHub to merge the given pull request and returns GitHub's
// verdict verbatim. The merge method defaults to "squash" if the caller does
// not specify one. We deliberately do not second-guess merge state here:
// clients should rely on `merged` and `message` from the response.
func (h *RPCHandler) MergePR(args *MergePRArgs, reply *MergePRReply) error {
	method := args.MergeMethod
	if method == "" {
		method = "squash"
	}

	client := git_tools.GetGithubClient()
	result, err := git_tools.MergePR(client, args.Owner, args.Repo, args.Number, method)
	if err != nil {
		h.Log.Error("Error merging PR", "owner", args.Owner, "repo", args.Repo, "number", args.Number, "method", method, "error", err)
		if result != nil {
			reply.Merged = result.GetMerged()
			reply.SHA = result.GetSHA()
			reply.Message = result.GetMessage()
		}
		return err
	}

	reply.Merged = result.GetMerged()
	reply.SHA = result.GetSHA()
	reply.Message = result.GetMessage()
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

// getFileContentLocal reads a file at a given git ref from the local clone
// using "git show <ref>:<path>". Returns an error if the repo isn't cloned
// locally or the ref/path doesn't exist.
func getFileContentLocal(repo, ref, filePath string) (string, error) {
	repoPath, err := GetLocalRepoPath(repo)
	if err != nil {
		return "", err
	}
	// Check the repo directory actually exists before shelling out.
	if _, err := os.Stat(repoPath); err != nil {
		return "", fmt.Errorf("local repo not found at %s: %w", repoPath, err)
	}
	cmd := exec.Command("git", "-C", repoPath, "show", fmt.Sprintf("%s:%s", ref, filePath))
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show %s:%s in %s failed: %w", ref, filePath, repoPath, err)
	}
	return string(out), nil
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

type GetHunkContextArgs struct {
	Owner    string `json:"Owner"`
	Repo     string `json:"Repo"`
	Number   int    `json:"Number"`
	Filename string `json:"Filename"` // File path within the repo
	Side     string `json:"Side"`     // "old" or "new" — which version of the file to read from
	// Anchor line number in the file (not the diff position).
	// For Direction "before": lines *above* this line are returned.
	// For Direction "after":  lines *below* this line are returned.
	AnchorLine int    `json:"AnchorLine"`
	Direction  string `json:"Direction"` // "before" or "after"
	Count      int    `json:"Count"`     // Number of extra lines to fetch (capped at 100)

	// Current hunk range — used to compute the updated hunk header after expansion.
	OrigStart  int    `json:"OrigStart"`  // Current @@ -OrigStart,OrigLength
	OrigLength int    `json:"OrigLength"`
	NewStart   int    `json:"NewStart"`   // Current @@ +NewStart,NewLength
	NewLength  int    `json:"NewLength"`
	HunkHeader string `json:"HunkHeader"` // Optional text after @@ (e.g. function name)
}

type GetHunkContextReply struct {
	Lines       []string `json:"lines"`        // The extra context lines
	StartLine   int      `json:"start_line"`   // 1-based line number of the first returned line
	EndLine     int      `json:"end_line"`     // 1-based line number of the last returned line
	RangeHeader string   `json:"range_header"` // Updated @@ -a,b +c,d @@ header for the expanded hunk
}

// GetHunkContext returns extra context lines before or after a hunk boundary,
// allowing clients to expand the visible context around a diff without visiting the full file.
func (h *RPCHandler) GetHunkContext(args *GetHunkContextArgs, reply *GetHunkContextReply) error {
	if args.Direction != "before" && args.Direction != "after" {
		return fmt.Errorf("Direction must be \"before\" or \"after\", got %q", args.Direction)
	}
	if args.Side != "old" && args.Side != "new" {
		return fmt.Errorf("Side must be \"old\" or \"new\", got %q", args.Side)
	}
	if args.AnchorLine < 1 {
		return fmt.Errorf("AnchorLine must be >= 1, got %d", args.AnchorLine)
	}
	count := args.Count
	if count <= 0 {
		count = 20
	}
	if count > 100 {
		count = 100
	}

	// Determine the ref to use.
	// For "new" side, use the PR's head SHA; for "old" side, use the base SHA.
	// Both are cached in the DB from workflow/renderer fetches.
	headSHA, baseSHA, _ := config.C().DB.GetPullRequestSHAs(args.Number, args.Repo)

	var ref string
	if args.Side == "new" {
		ref = headSHA
	} else {
		ref = baseSHA
	}
	// Fall back to GitHub API only if the cached SHA is missing.
	if ref == "" {
		client := git_tools.GetGithubClient()
		pr, _, err := client.PullRequests.Get(context.Background(), args.Owner, args.Repo, args.Number)
		if err != nil {
			return fmt.Errorf("error fetching PR: %w", err)
		}
		if args.Side == "new" {
			ref = pr.GetHead().GetSHA()
		} else {
			ref = pr.GetBase().GetSHA()
		}
	}

	// Try reading from the local repo first via "git show <ref>:<path>".
	// Falls back to the GitHub API only if the local repo isn't available.
	content, err := getFileContentLocal(args.Repo, ref, args.Filename)
	if err != nil {
		h.Log.Info("Local git show failed, falling back to GitHub API", "file", args.Filename, "error", err)
		client := git_tools.GetGithubClient()
		content, err = git_tools.GetFileContent(client, args.Owner, args.Repo, args.Filename, ref)
		if err != nil {
			h.Log.Error("Error fetching file content for hunk context", "file", args.Filename, "ref", ref, "error", err)
			return err
		}
	}

	fileLines := strings.Split(content, "\n")
	totalLines := len(fileLines)

	var startLine, endLine int // 1-based, inclusive
	if args.Direction == "before" {
		endLine = args.AnchorLine - 1
		startLine = endLine - count + 1
		if startLine < 1 {
			startLine = 1
		}
		if endLine < 1 {
			reply.Lines = []string{}
			reply.StartLine = 0
			reply.EndLine = 0
			return nil
		}
	} else { // "after"
		startLine = args.AnchorLine + 1
		endLine = startLine + count - 1
		if startLine > totalLines {
			reply.Lines = []string{}
			reply.StartLine = 0
			reply.EndLine = 0
			return nil
		}
		if endLine > totalLines {
			endLine = totalLines
		}
	}

	reply.Lines = fileLines[startLine-1 : endLine]
	reply.StartLine = startLine
	reply.EndLine = endLine

	// Compute the updated hunk header reflecting the expansion.
	// The extra lines are context (unchanged), so they increase both orig and new lengths equally.
	extraCount := endLine - startLine + 1
	origStart := args.OrigStart
	origLength := args.OrigLength
	newStart := args.NewStart
	newLength := args.NewLength

	if args.Direction == "before" {
		// Expanding upward: the hunk now starts earlier, length grows.
		origStart -= extraCount
		origLength += extraCount
		newStart -= extraCount
		newLength += extraCount
	} else {
		// Expanding downward: start stays the same, length grows.
		origLength += extraCount
		newLength += extraCount
	}

	headerSuffix := ""
	if args.HunkHeader != "" {
		headerSuffix = " " + args.HunkHeader
	}
	reply.RangeHeader = fmt.Sprintf("@@ -%d,%d +%d,%d @@%s", origStart, origLength, newStart, newLength, headerSuffix)

	return nil
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

// Experimental: LLM-based diff file ordering.
//
// When config.ExperimentalLLMFileOrdering is enabled, the files in a PR diff
// are ordered by an LLM so a reviewer can read the PR top-to-bottom: the
// integration / entry-point changes first, then implementation and helpers,
// then styling changes, then tests last. When the flag is off (the default),
// or if the LLM call fails for any reason, ordering falls back to
// sortFilesTestsLast.

const (
	llmOrderingModel       = "gemini-2.5-flash"
	llmOrderingMaxDiffSize = 200000
	llmOrderingTimeout     = 30 * time.Second
)

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

// orderDiffFiles returns the diff files in display order. It dispatches to the
// experimental LLM ordering when enabled, otherwise to the default sort that
// places test files last. LLM orderings are cached per PR SHA so the LLM is
// queried at most once per revision.
func orderDiffFiles(files []*utils.DiffFile, repo string, prNumber int, sha string) []*utils.DiffFile {
	if !config.C().ExperimentalLLMFileOrdering {
		return sortFilesTestsLast(files)
	}
	if len(files) < 2 {
		return files
	}

	if names := cachedFileOrdering(repo, prNumber, sha); names != nil {
		return reorderFilesByNames(files, names)
	}

	names, err := llmFileOrdering(files)
	if err != nil {
		slog.Warn("LLM diff file ordering failed, falling back to default sort", "error", err)
		return sortFilesTestsLast(files)
	}

	storeFileOrdering(repo, prNumber, sha, names)
	return reorderFilesByNames(files, names)
}

// cachedFileOrdering returns a previously cached LLM file ordering for the
// given PR SHA, or nil when there is no usable cache entry.
func cachedFileOrdering(repo string, prNumber int, sha string) []string {
	if sha == "" {
		return nil
	}
	db := config.C().DB
	if db == nil {
		return nil
	}
	stored, err := db.GetDiffFileOrdering(prNumber, repo, sha)
	if err != nil {
		slog.Warn("failed to read diff file ordering cache", "error", err)
		return nil
	}
	if stored == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(stored), &names); err != nil {
		slog.Warn("failed to decode cached diff file ordering", "error", err)
		return nil
	}
	return names
}

// storeFileOrdering persists an LLM file ordering keyed by PR SHA.
func storeFileOrdering(repo string, prNumber int, sha string, names []string) {
	if sha == "" {
		return
	}
	db := config.C().DB
	if db == nil {
		return
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		slog.Warn("failed to encode diff file ordering for cache", "error", err)
		return
	}
	if err := db.UpsertDiffFileOrdering(prNumber, repo, sha, string(encoded)); err != nil {
		slog.Warn("failed to write diff file ordering cache", "error", err)
	}
}

// diffFileName returns the path used to identify a diff file, preferring the
// new name and falling back to the original name for deleted files.
func diffFileName(file *utils.DiffFile) string {
	if file.NewName != "" {
		return file.NewName
	}
	return file.OrigName
}

// llmFileOrdering asks an LLM to order the diff files so the PR reads
// top-to-bottom from integration points through implementation to tests.
func llmFileOrdering(files []*utils.DiffFile) ([]string, error) {
	token := os.Getenv("GEMINI_API_KEY")
	if token == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY not set")
	}
	return requestFileOrdering(files, token)
}

// requestFileOrdering sends the diff to the LLM and returns the ordered list of
// file paths it responds with.
func requestFileOrdering(files []*utils.DiffFile, token string) ([]string, error) {
	var fileList strings.Builder
	for _, f := range files {
		fileList.WriteString("- " + diffFileName(f) + "\n")
	}

	diffText := buildDiffForLLM(files)
	if len(diffText) > llmOrderingMaxDiffSize {
		diffText = diffText[:llmOrderingMaxDiffSize] + "\n... (diff truncated)\n"
	}

	prompt := fmt.Sprintf(`You are ordering the files of a pull request diff so a reviewer can read the PR from top to bottom and understand it.

Order the files by this priority:
1. The most important and relevant changes first: the entry points where the change is integrated (call sites, public APIs, top-level wiring).
2. Then helper functions and implementation details: how the change actually works.
3. Then styling changes such as CSS: after code, but before tests.
4. Then test files, last.

A reviewer should be able to read top to bottom: starting where the change is integrated, then into how it works, then the tests.

Files in this diff:
%s
Full diff:
%s

Respond with ONLY the file paths, one per line, in the order they should be displayed. Use the exact file paths listed above. Do not include numbering, bullets, commentary, or code fences.`, fileList.String(), diffText)

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := "https://generativelanguage.googleapis.com/v1beta/models/" + llmOrderingModel + ":generateContent?key=" + token
	client := &http.Client{Timeout: llmOrderingTimeout}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Gemini API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var geminiResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return nil, err
	}
	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("no content in Gemini response")
	}

	var names []string
	for _, line := range strings.Split(geminiResp.Candidates[0].Content.Parts[0].Text, "\n") {
		if cleaned := cleanLLMFileName(line); cleaned != "" {
			names = append(names, cleaned)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("Gemini response contained no file paths")
	}
	return names, nil
}

// buildDiffForLLM renders the diff files into a plain-text form for the LLM,
// labelling each file so the model can refer back to exact paths.
func buildDiffForLLM(files []*utils.DiffFile) string {
	var b strings.Builder
	for _, f := range files {
		b.WriteString("=== FILE: " + diffFileName(f) + " ===\n")
		for _, hunk := range f.Hunks {
			b.WriteString(hunk.RangeHeader() + "\n")
			for _, line := range hunk.WholeRange.Lines {
				b.WriteString(line.Render())
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// cleanLLMFileName normalizes a single line of LLM output into a bare file
// path, stripping list markers, surrounding quotes/backticks, and a/ b/
// prefixes. It returns an empty string for lines that are not file paths.
func cleanLLMFileName(line string) string {
	s := strings.TrimSpace(line)
	s = strings.Trim(s, "`")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "- ")
	s = strings.TrimPrefix(s, "* ")
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`")
	s = strings.TrimPrefix(s, "a/")
	s = strings.TrimPrefix(s, "b/")
	return strings.TrimSpace(s)
}

// reorderFilesByNames reorders files to match the sequence of paths returned by
// the LLM. Files the LLM did not mention (or that could not be matched) keep
// their original relative order and are appended at the end.
func reorderFilesByNames(files []*utils.DiffFile, names []string) []*utils.DiffFile {
	used := make(map[*utils.DiffFile]bool)
	result := make([]*utils.DiffFile, 0, len(files))

	for _, name := range names {
		if f := matchDiffFile(files, name, used); f != nil {
			result = append(result, f)
			used[f] = true
		}
	}
	for _, f := range files {
		if !used[f] {
			result = append(result, f)
		}
	}
	return result
}

// matchDiffFile finds an unused diff file whose path matches name, trying an
// exact match first and then a basename match.
func matchDiffFile(files []*utils.DiffFile, name string, used map[*utils.DiffFile]bool) *utils.DiffFile {
	for _, f := range files {
		if !used[f] && diffFileName(f) == name {
			return f
		}
	}
	base := name[strings.LastIndex(name, "/")+1:]
	for _, f := range files {
		if used[f] {
			continue
		}
		fn := diffFileName(f)
		if fn[strings.LastIndex(fn, "/")+1:] == base {
			return f
		}
	}
	return nil
}
