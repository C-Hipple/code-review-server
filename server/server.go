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

	"github.com/google/go-github/v48/github"
	// "strings"
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
	err := config.C.DB.QueryRow("SELECT COUNT(*) FROM sections").Scan(&count)
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

func (h *RPCHandler) GetAllReviews(args *GetReviewsArgs, reply *GetReviewsReply) error {
	renderer := NewOrgRenderer(config.C.DB)
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
	Okay     bool          `json:"okay"`
	Content  string        `json:"content"`
	Metadata *PRMetadata   `json:"metadata"`
	Diff     string        `json:"diff"`
	Comments []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews  []ReviewJSON  `json:"reviews"`
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
	reply.Okay = true
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
	rawComments, _ := config.C.DB.GetPRComments(number, repo)
	if rawComments != "" {
		commentsJSON = rawComments
	}

	// Extract SHA from DB
	_, sha, _ := config.C.DB.GetPullRequest(number, repo)

	// Run plugins in background
	metadataJSON, _ := json.Marshal(details.Metadata)
	go RunPlugins(owner, repo, number, sha, details.Diff, commentsJSON, string(metadataJSON))

	// Get the full formatted response for the UI.
	// We pass the already fetched details to avoid redundant API calls.
	content, _ := GetFullPRResponse(owner, repo, number, false, details)

	return details, content, nil
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
	ID       int64         `json:"id"`
	Content  string        `json:"content"`
	Metadata *PRMetadata   `json:"metadata"`
	Diff     string        `json:"diff"`
	Comments []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews  []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) AddComment(args *AddCommentArgs, reply *AddCommentReply) error {
	comment, err := config.C.DB.InsertLocalComment(args.Owner, args.Repo, args.Number, args.Filename, args.Position, &args.Body, args.ReplyToID)
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
	Okay     bool          `json:"okay"`
	Content  string        `json:"content"`
	Metadata *PRMetadata   `json:"metadata"`
	Diff     string        `json:"diff"`
	Comments []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews  []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) EditComment(args *EditCommentArgs, reply *EditCommentReply) error {
	err := config.C.DB.UpdateLocalComment(args.ID, args.Body)
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
	Okay     bool          `json:"okay"`
	Content  string        `json:"content"`
	Metadata *PRMetadata   `json:"metadata"`
	Diff     string        `json:"diff"`
	Comments []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews  []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) DeleteComment(args *DeleteCommentArgs, reply *DeleteCommentReply) error {
	err := config.C.DB.DeleteLocalComment(args.ID)
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
	reply.Comments = details.Comments
	reply.OutdatedComments = details.OutdatedComments
	reply.Reviews = details.Reviews
	return nil
}

type SetFeedbackArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
	Body   string
}

type SetFeedbackReply struct {
	ID       int64         `json:"id"`
	Content  string        `json:"content"`
	Metadata *PRMetadata   `json:"metadata"`
	Diff     string        `json:"diff"`
	Comments []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews  []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) SetFeedback(args *SetFeedbackArgs, reply *SetFeedbackReply) error {
	err := config.C.DB.InsertFeedback(args.Owner, args.Repo, args.Number, &args.Body)
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
	Okay     bool          `json:"okay"`
	Content  string        `json:"content"`
	Metadata *PRMetadata   `json:"metadata"`
	Diff     string        `json:"diff"`
	Comments []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews  []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) RemovePRComments(args *RemovePRCommentsArgs, reply *RemovePRCommentsReply) error {
	err := config.C.DB.DeleteLocalCommentsForPR(args.Owner, args.Repo, args.Number)
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
	Okay     bool          `json:"okay"`
	Content  string        `json:"content"`
	Metadata *PRMetadata   `json:"metadata"`
	Diff     string        `json:"diff"`
	Comments []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews  []ReviewJSON  `json:"reviews"`
}

func (h *RPCHandler) SubmitReview(args *SubmitReviewArgs, reply *SubmitReviewReply) error {
	// 1. Fetch Local Comments
	comments, err := config.C.DB.GetLocalCommentsForPR(args.Owner, args.Repo, args.Number)
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
	err = config.C.DB.DeleteLocalCommentsForPR(args.Owner, args.Repo, args.Number)
	if err != nil {
		h.Log.Error("Error deleting local comments after submission", "error", err)
	}

	// 5. Remove the item from all sections in the database
	// The identifier is constructed as RepoName + PRNumber (matching PRToOrgBridge.Identifier)
	identifier := fmt.Sprintf("%s%d", args.Repo, args.Number)
	err = config.C.DB.DeleteItemByIdentifier(identifier)
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
	Okay     bool          `json:"okay"`
	Content  string        `json:"content"`
	Metadata *PRMetadata   `json:"metadata"`
	Diff     string        `json:"diff"`
	Comments []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews  []ReviewJSON  `json:"reviews"`
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
	reply.Okay = true
	return nil
}

type ListPluginsArgs struct{}
type ListPluginsReply struct {
	Plugins []config.Plugin `json:"plugins"`
}

func (h *RPCHandler) ListPlugins(args *ListPluginsArgs, reply *ListPluginsReply) error {
	reply.Plugins = config.C.Plugins
	return nil
}

type CheckRepoExistsArgs struct {
	Repo string `json:"Repo"`
}

type CheckRepoExistsReply struct {
	Exists bool   `json:"Exists"`
	Path   string `json:"Path"`
}

func (h *RPCHandler) CheckRepoExists(args *CheckRepoExistsArgs, reply *CheckRepoExistsReply) error {
	repoLocation := config.C.RepoLocation
	if len(repoLocation) > 0 && repoLocation[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			h.Log.Error("Error getting user home directory", "error", err)
			return err
		}
		repoLocation = fmt.Sprintf("%s/%s", home, repoLocation[2:])
	}

	repoPath := fmt.Sprintf("%s/%s", repoLocation, args.Repo)
	// Clean path to remove double slashes if any
	repoPath = filepath.Clean(repoPath)

	info, err := os.Stat(repoPath)
	if err != nil {
		if os.IsNotExist(err) {
			reply.Exists = false
			return nil
		}
		h.Log.Error("Error checking repo existence", "error", err)
		return err
	}
	reply.Exists = info.IsDir()
	reply.Path = repoPath
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
	results, err := config.C.DB.GetPluginResults(args.Owner, args.Repo, args.Number)
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
