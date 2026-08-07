package server

import (
	"context"
	"crs/config"
	"crs/git_tools"
	"crs/llm"
	"crs/utils"
	"crs/workflows"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v74/github"
)

// testing mutable state
// RPC handler is recreated for each request, it's not stateful across requests
// simulate a db lol
var CurrentCount int

func RunServer() {
	server := rpc.NewServer()
	handler := &RPCHandler{}
	if err := server.Register(handler); err != nil {
		slog.Error("Error registering RPC handler", "error", err)
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

type RPCHandler struct{}

type HelloArgs struct{}
type HelloReply struct {
	Count   int
	Content string
}

func (h *RPCHandler) Hello(args *HelloArgs, reply *HelloReply) error {
	var count int
	err := config.C().DB.QueryRow("SELECT COUNT(*) FROM sections").Scan(&count)
	if err != nil {
		slog.Error("Error counting items", "error", err)
		return err
	}
	CurrentCount += count
	reply.Content = fmt.Sprintf("hello %d", CurrentCount)
	reply.Count = count

	return nil
}

// GetReviewsArgs lets a client opt in to the heavy per-PR detail subtrees in
// the rendered org content. Both default to false: the dashboard only needs
// headlines and metadata, and a client showing a full PR fetches it with
// GetPR, which returns the diff and comments as structured fields.
type GetReviewsArgs struct {
	IncludeDiff     bool `json:"IncludeDiff"`
	IncludeComments bool `json:"IncludeComments"`
}

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
		slog.Error("Error reloading config", "error", err)
	}

	renderer := NewOrgRenderer(config.C().DB)
	content, items, err := renderer.RenderAndGetItems(RenderOptions{
		IncludeDiff:     args.IncludeDiff,
		IncludeComments: args.IncludeComments,
	})
	if err != nil {
		slog.Error("Error rendering org files", "error", err)
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

// PRPayload is the shared body of every RPC reply that returns a PR's full
// state. Reply structs embed it so all methods expose the same fields, and
// populate() is the single place they get filled in and normalized.
type PRPayload struct {
	Okay             bool           `json:"okay"`
	Content          string         `json:"content"`
	Metadata         *PRMetadata    `json:"metadata"`
	Diff             string         `json:"diff"`
	Comments         []CommentJSON  `json:"comments"`
	OutdatedComments []CommentJSON  `json:"outdated_comments"`
	Reviews          []ReviewJSON   `json:"reviews"`
	Commits          []CommitJSON   `json:"commits"`
	Feedback         string         `json:"feedback"`
	Annotations      []PRAnnotation `json:"annotations"`
}

// populate fills the payload from fetched PR details, normalizing nil slices
// to empty ones so JSON clients always see [] instead of null.
func (p *PRPayload) populate(details *PRDetails, content string, owner, repo string, number int) {
	p.Okay = true
	p.Content = content
	p.Metadata = &details.Metadata
	p.Diff = details.Diff
	p.Comments = details.Comments
	p.OutdatedComments = details.OutdatedComments
	p.Reviews = details.Reviews
	p.Commits = details.Commits
	if p.Comments == nil {
		p.Comments = []CommentJSON{}
	}
	if p.OutdatedComments == nil {
		p.OutdatedComments = []CommentJSON{}
	}
	if p.Reviews == nil {
		p.Reviews = []ReviewJSON{}
	}
	if p.Commits == nil {
		p.Commits = []CommitJSON{}
	}

	feedback, err := config.C().DB.GetFeedback(owner, repo, number)
	if err != nil {
		slog.Warn("Error loading feedback for PR reply", "repo", repo, "pr", number, "error", err)
	}
	p.Feedback = feedback

	annotations, err := collectPluginAnnotations(owner, repo, number)
	if err != nil {
		slog.Warn("Error loading plugin annotations for PR reply", "repo", repo, "pr", number, "error", err)
	}
	if annotations == nil {
		annotations = []PRAnnotation{}
	}
	p.Annotations = annotations
}

type GetPRReply struct {
	PRPayload
}

func (h *RPCHandler) GetPR(args *GetPRstructArgs, reply *GetPRReply) error {
	details, content, err := h.fetchPR(args.Owner, args.Repo, args.Number, args.SkipCache)
	if err != nil {
		return err
	}
	ensurePostUpdateHooks(args.Owner, args.Repo, args.Number, details)
	reply.populate(details, content, args.Owner, args.Repo, args.Number)
	return nil
}

// fetchPR fetches PR details plus the fully formatted content string. It is a
// pure query: it reads caches (or GitHub on a miss) but never dispatches
// plugins or LLM analysis — callers that want those side effects invoke
// ensurePostUpdateHooks explicitly.
func (h *RPCHandler) fetchPR(owner, repo string, number int, skipCache bool) (*PRDetails, string, error) {
	fetchStart := time.Now()
	details, err := GetPRDetails(owner, repo, number, skipCache)
	if err != nil {
		slog.Error("Error fetching PR details", "error", err)
		return nil, "", err
	}
	fetchMS := time.Since(fetchStart).Milliseconds()

	// Get the full formatted response for the UI.
	// We pass the already fetched details to avoid redundant API calls.
	renderStart := time.Now()
	content, err := GetFullPRResponse(owner, repo, number, false, details)
	if err != nil {
		slog.Error("Error building formatted PR response", "repo", repo, "pr", number, "error", err)
	}

	// Timed because this is what a reviewer waits on when opening a PR: a slow
	// open shows up here as either fetch time (a cache miss going to GitHub) or
	// render time, rather than having to be guessed at from surrounding logs.
	slog.Info("Served PR", "repo", repo, "pr", number, "skip_cache", skipCache,
		"fetch_ms", fetchMS, "render_ms", time.Since(renderStart).Milliseconds())

	return details, content, nil
}

// hookDispatchTTL is how long a (PR, head SHA) pair is considered recently
// dispatched. Within this window repeated reads of the same PR revision skip
// re-dispatching the post-update hooks entirely; the per-plugin SHA check in
// executePlugin remains the durable guard against duplicate work.
const hookDispatchTTL = time.Minute

var recentHookDispatches sync.Map // "owner/repo#number@sha" -> time.Time

// shouldDispatchHooks debounces post-update hook dispatches per PR head SHA.
func shouldDispatchHooks(owner, repo string, number int, sha string) bool {
	key := fmt.Sprintf("%s/%s#%d@%s", owner, repo, number, sha)
	now := time.Now()
	if v, ok := recentHookDispatches.Load(key); ok {
		if last, ok := v.(time.Time); ok && now.Sub(last) < hookDispatchTTL {
			return false
		}
	}
	// Keys are per revision and every workflow cycle mints new ones, so drop
	// the expired entries rather than letting the map grow for the life of the
	// process. Anything past the TTL can no longer debounce anything.
	recentHookDispatches.Range(func(k, v any) bool {
		if last, ok := v.(time.Time); ok && now.Sub(last) >= hookDispatchTTL {
			recentHookDispatches.Delete(k)
		}
		return true
	})
	recentHookDispatches.Store(key, now)
	return true
}

// ensurePostUpdateHooks dispatches the async post-update hooks (plugins and
// the experimental LLM diff analysis) for a PR, reading the hooks' inputs from
// the DB caches. RPC methods that represent "the client is looking at fresh PR
// content" call this; local-comment mutations do not, since they never change
// the PR's head SHA. The workflow layer reaches it through WarmPRAnalysis.
func ensurePostUpdateHooks(owner, repo string, number int, details *PRDetails) {
	_, sha, err := config.C().DB.GetPullRequest(number, repo)
	if err != nil {
		slog.Warn("Error reading cached PR SHA for hook dispatch", "repo", repo, "pr", number, "error", err)
	}
	if !shouldDispatchHooks(owner, repo, number, sha) {
		return
	}

	commentsJSON := "[]"
	rawComments, err := config.C().DB.GetPRComments(number, repo)
	if err != nil {
		slog.Warn("Error reading cached PR comments for hook dispatch", "repo", repo, "pr", number, "error", err)
	}
	if rawComments != "" {
		commentsJSON = rawComments
	}

	metadataJSON, err := json.Marshal(details.Metadata)
	if err != nil {
		slog.Warn("Error marshaling PR metadata for hook dispatch", "repo", repo, "pr", number, "error", err)
		metadataJSON = []byte("{}")
	}

	// Each hook runs in its own goroutine so this returns immediately.
	RunPostUpdatePRHooks(owner, repo, number, sha, details.Diff, commentsJSON, string(metadataJSON), details.Metadata.HeadRef)
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
	// Adjacency only needs the ordered items; skip rendering the org text.
	items, err := renderer.GetAllReviewItems()
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
	slog.Info("GetAdjacentPR returning", "owner", adjacent.Owner, "repo", adjacent.Repo, "number", adjacent.Number)
	details, content, err := h.fetchPR(adjacent.Owner, adjacent.Repo, adjacent.Number, args.SkipCache)
	if err != nil {
		return err
	}
	ensurePostUpdateHooks(adjacent.Owner, adjacent.Repo, adjacent.Number, details)

	reply.AdjacentOwner = adjacent.Owner
	reply.AdjacentRepo = adjacent.Repo
	reply.AdjacentNumber = adjacent.Number
	reply.populate(details, content, adjacent.Owner, adjacent.Repo, adjacent.Number)
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
	PRPayload
	ID int64 `json:"id"`
}

func (h *RPCHandler) AddComment(args *AddCommentArgs, reply *AddCommentReply) error {
	comment, err := config.C().DB.InsertLocalComment(args.Owner, args.Repo, args.Number, args.Filename, args.Position, &args.Body, args.ReplyToID)
	if err != nil {
		slog.Error("Error inserting local comment", "error", err)
		return err
	}
	reply.ID = comment.ID

	details, content, err := h.fetchPR(args.Owner, args.Repo, args.Number, false)
	if err != nil {
		return err
	}
	reply.populate(details, content, args.Owner, args.Repo, args.Number)
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
	PRPayload
}

func (h *RPCHandler) EditComment(args *EditCommentArgs, reply *EditCommentReply) error {
	err := config.C().DB.UpdateLocalComment(args.ID, args.Body)
	if err != nil {
		slog.Error("Error updating local comment", "error", err)
		return err
	}

	details, content, err := h.fetchPR(args.Owner, args.Repo, args.Number, false)
	if err != nil {
		return err
	}
	reply.populate(details, content, args.Owner, args.Repo, args.Number)
	return nil
}

type DeleteCommentArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
	ID     int64  `json:"ID"`
}

type DeleteCommentReply struct {
	PRPayload
}

func (h *RPCHandler) DeleteComment(args *DeleteCommentArgs, reply *DeleteCommentReply) error {
	err := config.C().DB.DeleteLocalComment(args.ID)
	if err != nil {
		slog.Error("Error deleting local comment", "error", err)
		return err
	}

	details, content, err := h.fetchPR(args.Owner, args.Repo, args.Number, false)
	if err != nil {
		return err
	}
	reply.populate(details, content, args.Owner, args.Repo, args.Number)
	return nil
}

type SetFeedbackArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
	Body   string
}

type SetFeedbackReply struct {
	PRPayload
	ID int64 `json:"id"`
}

func (h *RPCHandler) SetFeedback(args *SetFeedbackArgs, reply *SetFeedbackReply) error {
	err := config.C().DB.InsertFeedback(args.Owner, args.Repo, args.Number, &args.Body)
	if err != nil {
		slog.Error("Error inserting feedback", "error", err)
		return err
	}

	details, content, err := h.fetchPR(args.Owner, args.Repo, args.Number, false)
	if err != nil {
		return err
	}
	reply.populate(details, content, args.Owner, args.Repo, args.Number)
	return nil
}

type RemovePRCommentsArgs struct {
	Repo   string `json:"Repo"`
	Owner  string `json:"Owner"`
	Number int    `json:"Number"`
}

type RemovePRCommentsReply struct {
	PRPayload
}

func (h *RPCHandler) RemovePRComments(args *RemovePRCommentsArgs, reply *RemovePRCommentsReply) error {
	err := config.C().DB.DeleteLocalCommentsForPR(args.Owner, args.Repo, args.Number)
	if err != nil {
		slog.Error("Error removing local comments", "error", err)
		return err
	}

	details, content, err := h.fetchPR(args.Owner, args.Repo, args.Number, false)
	if err != nil {
		return err
	}
	reply.populate(details, content, args.Owner, args.Repo, args.Number)
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
	PRPayload
}

func (h *RPCHandler) SubmitReview(args *SubmitReviewArgs, reply *SubmitReviewReply) error {
	// 1. Fetch Local Comments
	comments, err := config.C().DB.GetLocalCommentsForPR(args.Owner, args.Repo, args.Number)
	if err != nil {
		slog.Error("Error fetching local comments", "error", err)
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
				slog.Error("Error submitting reply", "error", err)
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
		slog.Error("Error submitting review to GitHub", "error", err)
		return err
	}

	// 4. Clean up Local Comments
	err = config.C().DB.DeleteLocalCommentsForPR(args.Owner, args.Repo, args.Number)
	if err != nil {
		slog.Error("Error deleting local comments after submission", "error", err)
	}

	// 5. Re-run the PR through the workflows that target its repo so the
	// dashboard reflects the review immediately instead of at the next cycle.
	// Every targeting workflow is consulted, not just the ones holding the PR:
	// a review can move a PR into a section ("waiting on author") as easily as
	// out of one ("needs review"). Failures are logged rather than returned —
	// the review itself already succeeded, and the next cycle re-syncs anyway.
	if err := workflows.ReprocessPR(args.Owner, args.Repo, args.Number); err != nil {
		slog.Error("Error reprocessing PR through workflows after review",
			"owner", args.Owner, "repo", args.Repo, "pr", args.Number, "error", err)
	}

	details, content, err := h.fetchPR(args.Owner, args.Repo, args.Number, true)
	if err != nil {
		return err
	}
	reply.populate(details, content, args.Owner, args.Repo, args.Number)
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
		slog.Error("Error merging PR", "owner", args.Owner, "repo", args.Repo, "number", args.Number, "method", method, "error", err)
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
	PRPayload
	Updated bool `json:"updated"`
}

func (h *RPCHandler) SyncPR(args *SyncPRArgs, reply *SyncPRReply) error {
	// Snapshot the cached state so we can tell the client whether the sync
	// actually pulled in anything new. The fresh fetch below overwrites these
	// cache entries.
	_, oldSHA, _ := config.C().DB.GetPullRequest(args.Number, args.Repo)
	oldCommentsJSON, _ := config.C().DB.GetPRComments(args.Number, args.Repo)

	details, content, err := h.fetchPR(args.Owner, args.Repo, args.Number, true)
	if err != nil {
		return err
	}
	ensurePostUpdateHooks(args.Owner, args.Repo, args.Number, details)

	_, newSHA, _ := config.C().DB.GetPullRequest(args.Number, args.Repo)
	newCommentsJSON, _ := config.C().DB.GetPRComments(args.Number, args.Repo)
	reply.Updated = syncDetectedChanges(oldSHA, newSHA, oldCommentsJSON, newCommentsJSON)

	reply.populate(details, content, args.Owner, args.Repo, args.Number)
	return nil
}

// syncDetectedChanges reports whether a sync brought in a new head SHA or new
// comments compared to the previously cached state.
func syncDetectedChanges(oldSHA, newSHA, oldCommentsJSON, newCommentsJSON string) bool {
	if oldSHA != newSHA {
		return true
	}
	oldIDs := cachedCommentIDs(oldCommentsJSON)
	for id := range cachedCommentIDs(newCommentsJSON) {
		if _, ok := oldIDs[id]; !ok {
			return true
		}
	}
	return false
}

// cachedCommentIDs extracts the GitHub comment IDs from a raw PRComments cache
// entry. Returns an empty set for empty or malformed JSON.
func cachedCommentIDs(commentsJSON string) map[int64]struct{} {
	ids := make(map[int64]struct{})
	if commentsJSON == "" {
		return ids
	}
	var comments []*github.PullRequestComment
	if err := json.Unmarshal([]byte(commentsJSON), &comments); err != nil {
		return ids
	}
	for _, c := range comments {
		if c != nil {
			ids[c.GetID()] = struct{}{}
		}
	}
	return ids
}

type ListPluginsArgs struct{}
type ListPluginsReply struct {
	Plugins []config.Plugin `json:"plugins"`
}

func (h *RPCHandler) ListPlugins(args *ListPluginsArgs, reply *ListPluginsReply) error {
	reply.Plugins = config.C().Plugins
	return nil
}

// --- Configuration ---
//
// Clients read and edit the server's TOML config (~/.config/codereviewserver.toml)
// through GetConfig and UpdateConfig. GetConfig also hands back the workflow
// type and filter registries so a client can build its pickers from what this
// server actually supports rather than hard-coding them.

// ConfigView is the client-facing view of the configuration file. SleepDuration
// is expressed in minutes, matching the TOML field rather than the
// time.Duration the server keeps in memory.
type ConfigView struct {
	Repos                       []string             `json:"Repos"`
	SleepDuration               int                  `json:"SleepDuration"`
	JiraDomain                  string               `json:"JiraDomain"`
	GithubUsername              string               `json:"GithubUsername"`
	RepoLocation                string               `json:"RepoLocation"`
	AutoWorktree                bool                 `json:"AutoWorktree"`
	DesktopNotifications        bool                 `json:"DesktopNotifications"`
	SectionPriority             map[string]int       `json:"SectionPriority"`
	SectionSorting              map[string]string    `json:"SectionSorting"`
	Workflows                   []config.RawWorkflow `json:"Workflows"`
	Plugins                     []config.Plugin      `json:"Plugins"`
	ExperimentalLLMFileOrdering bool                 `json:"ExperimentalLLMFileOrdering"`
	ExperimentalLLMReviewEase   bool                 `json:"ExperimentalLLMReviewEase"`
}

// newConfigView builds the view from a loaded config, normalizing nil maps and
// slices to empty ones so JSON clients always see {} / [] instead of null.
func newConfigView(cfg config.Config) ConfigView {
	view := ConfigView{
		Repos:                       cfg.Repos,
		SleepDuration:               int(cfg.SleepDuration.Minutes()),
		JiraDomain:                  cfg.JiraDomain,
		GithubUsername:              cfg.GithubUsername,
		RepoLocation:                cfg.RepoLocation,
		AutoWorktree:                cfg.AutoWorktree,
		DesktopNotifications:        cfg.DesktopNotifications,
		SectionPriority:             cfg.SectionPriority,
		SectionSorting:              cfg.SectionSorting,
		Workflows:                   cfg.RawWorkflows,
		Plugins:                     cfg.Plugins,
		ExperimentalLLMFileOrdering: cfg.ExperimentalLLMFileOrdering,
		ExperimentalLLMReviewEase:   cfg.ExperimentalLLMReviewEase,
	}
	if view.Repos == nil {
		view.Repos = []string{}
	}
	if view.SectionPriority == nil {
		view.SectionPriority = map[string]int{}
	}
	if view.SectionSorting == nil {
		view.SectionSorting = map[string]string{}
	}
	if view.Workflows == nil {
		view.Workflows = []config.RawWorkflow{}
	}
	if view.Plugins == nil {
		view.Plugins = []config.Plugin{}
	}
	return view
}

// ConfigPayload is the shared body of the config replies, so GetConfig and
// UpdateConfig hand back the same shape and a client can render either one.
type ConfigPayload struct {
	Okay    bool       `json:"okay"`
	Message string     `json:"message"`
	Path    string     `json:"path"`
	Config  ConfigView `json:"config"`
	// UsingDefaults is true when no file exists at Path and the server is
	// running the built-in defaults. Saving any change writes the file, at
	// which point what a client shows becomes what is on disk.
	UsingDefaults bool                         `json:"using_defaults"`
	WorkflowTypes []workflows.WorkflowTypeInfo `json:"workflow_types"`
	Filters       []workflows.FilterInfo       `json:"filters"`
}

func (p *ConfigPayload) populate(cfg config.Config) {
	p.Okay = true
	p.Config = newConfigView(cfg)
	p.UsingDefaults = cfg.UsingDefaults
	p.WorkflowTypes = workflows.WorkflowTypes()
	p.Filters = workflows.FilterTypes()

	path, err := config.ConfigPath()
	if err != nil {
		slog.Error("Error resolving config path", "error", err)
	}
	p.Path = path
}

type GetConfigArgs struct{}
type GetConfigReply struct {
	ConfigPayload
}

// GetConfig returns the current configuration, re-read from disk so clients
// see edits made outside the server.
func (h *RPCHandler) GetConfig(args *GetConfigArgs, reply *GetConfigReply) error {
	reloadErr := config.Reload()
	if reloadErr != nil {
		// A config file that no longer parses shouldn't hide the running
		// configuration; report the problem and return what's in memory.
		slog.Error("Error reloading config", "error", reloadErr)
	}
	reply.populate(config.C())
	if reloadErr != nil {
		reply.Okay = false
		reply.Message = fmt.Sprintf("Showing the configuration currently in memory; reloading from disk failed: %v", reloadErr)
	}
	return nil
}

// UpdateConfigArgs is a partial update: every field is optional and a field
// left out (null) keeps whatever is on disk. Sending Workflows replaces the
// whole list, which is how a client removes or reorders entries.
type UpdateConfigArgs struct {
	Repos                       *[]string             `json:"Repos"`
	SleepDuration               *int                  `json:"SleepDuration"`
	JiraDomain                  *string               `json:"JiraDomain"`
	GithubUsername              *string               `json:"GithubUsername"`
	RepoLocation                *string               `json:"RepoLocation"`
	AutoWorktree                *bool                 `json:"AutoWorktree"`
	DesktopNotifications        *bool                 `json:"DesktopNotifications"`
	SectionPriority             *map[string]int       `json:"SectionPriority"`
	SectionSorting              *map[string]string    `json:"SectionSorting"`
	Workflows                   *[]config.RawWorkflow `json:"Workflows"`
	ExperimentalLLMFileOrdering *bool                 `json:"ExperimentalLLMFileOrdering"`
	ExperimentalLLMReviewEase   *bool                 `json:"ExperimentalLLMReviewEase"`
}

// UpdateConfigReply carries the same body as GetConfig plus any validation
// problems. A rejected update is reported as okay=false with a populated
// errors list — not as an RPC error — so clients can attach each message to the
// field that caused it. The config in the reply is then the unchanged one still
// on disk.
type UpdateConfigReply struct {
	ConfigPayload
	Errors []config.ValidationError `json:"errors"`
}

// UpdateConfig validates a partial change against the configuration it would
// produce, writes the merged TOML file (keeping the previous contents in
// <path>.bak), and reloads the running config. Settings and keys the update
// doesn't mention are preserved; comments in the file are not.
//
// Nothing is written unless validation passes. The background workflow manager
// re-derives its workflows from the config at the top of each cycle, so a saved
// change takes effect on the next sync.
func (h *RPCHandler) UpdateConfig(args *UpdateConfigArgs, reply *UpdateConfigReply) error {
	reply.Errors = []config.ValidationError{}

	update := config.Update{
		Repos:                       normalizeRepos(args.Repos),
		SleepDuration:               args.SleepDuration,
		JiraDomain:                  args.JiraDomain,
		GithubUsername:              args.GithubUsername,
		RepoLocation:                args.RepoLocation,
		AutoWorktree:                args.AutoWorktree,
		DesktopNotifications:        args.DesktopNotifications,
		SectionPriority:             args.SectionPriority,
		SectionSorting:              args.SectionSorting,
		Workflows:                   normalizeWorkflows(args.Workflows),
		ExperimentalLLMFileOrdering: args.ExperimentalLLMFileOrdering,
		ExperimentalLLMReviewEase:   args.ExperimentalLLMReviewEase,
	}

	if update.IsEmpty() {
		reply.populate(config.C())
		reply.Message = "No changes requested"
		return nil
	}

	problems, err := config.Apply(update, validateConfig)
	if err != nil {
		slog.Error("Error applying config update", "error", err)
		return err
	}

	reply.populate(config.C())
	if len(problems) > 0 {
		reply.Okay = false
		reply.Errors = problems
		reply.Message = fmt.Sprintf("Configuration not saved: found %s", pluralize(len(problems), "problem", "problems"))
		return nil
	}
	reply.Message = fmt.Sprintf("Configuration saved to %s", reply.Path)
	return nil
}

// validateConfig runs both halves of validation: the root-level and
// always-required workflow fields owned by the config package, plus the
// workflow types and filters owned by the workflows package.
func validateConfig(cfg *config.Config) []config.ValidationError {
	problems := config.Validate(cfg)
	return append(problems, workflows.ValidateWorkflows(cfg.RawWorkflows, cfg.Repos)...)
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// normalizeRepos trims each entry and drops the blank ones, so a trailing empty
// line in a client's textarea doesn't become a validation error.
func normalizeRepos(repos *[]string) *[]string {
	if repos == nil {
		return nil
	}
	cleaned := trimList(*repos)
	return &cleaned
}

// normalizeWorkflows trims the string fields of each submitted workflow. Values
// arrive from text inputs, and a stray space in a section title would otherwise
// create a section distinct from the one the user meant.
func normalizeWorkflows(wfs *[]config.RawWorkflow) *[]config.RawWorkflow {
	if wfs == nil {
		return nil
	}
	cleaned := make([]config.RawWorkflow, 0, len(*wfs))
	for _, wf := range *wfs {
		wf.WorkflowType = strings.TrimSpace(wf.WorkflowType)
		wf.Name = strings.TrimSpace(wf.Name)
		wf.Owner = strings.TrimSpace(wf.Owner)
		wf.Repo = strings.TrimSpace(wf.Repo)
		wf.JiraEpic = strings.TrimSpace(wf.JiraEpic)
		wf.SectionTitle = strings.TrimSpace(wf.SectionTitle)
		wf.PRState = strings.TrimSpace(wf.PRState)
		wf.GithubUsername = strings.TrimSpace(wf.GithubUsername)
		wf.Repos = trimList(wf.Repos)
		wf.Filters = trimList(wf.Filters)
		wf.Teams = trimList(wf.Teams)
		cleaned = append(cleaned, wf)
	}
	return &cleaned
}

// trimList trims every entry of a string list and drops the ones left empty.
// Returns nil for an all-empty list so the field is omitted from the TOML file.
func trimList(values []string) []string {
	var cleaned []string
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return cleaned
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
	OrigStart  int    `json:"OrigStart"` // Current @@ -OrigStart,OrigLength
	OrigLength int    `json:"OrigLength"`
	NewStart   int    `json:"NewStart"` // Current @@ +NewStart,NewLength
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
		slog.Info("Local git show failed, falling back to GitHub API", "file", args.Filename, "error", err)
		client := git_tools.GetGithubClient()
		content, err = git_tools.GetFileContent(client, args.Owner, args.Repo, args.Filename, ref)
		if err != nil {
			slog.Error("Error fetching file content for hunk context", "file", args.Filename, "ref", ref, "error", err)
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
	Output map[string]PluginOutput `json:"output"`
}

// GetPluginOutput returns all stored plugin outputs for the given PR
func (h *RPCHandler) GetPluginOutput(args *GetPluginOutputArgs, reply *GetPluginOutputReply) error {
	results, err := config.C().DB.GetPluginResults(args.Owner, args.Repo, args.Number)
	if err != nil {
		slog.Error("Error fetching plugin results", "error", err)
		return err
	}

	// If no results are stored yet, make sure the plugins have at least been
	// triggered once for this PR.
	if len(results) == 0 {
		slog.Info("No plugin results found, triggering async run", "pr", args.Number)
		go func() {
			details, err := GetPRDetails(args.Owner, args.Repo, args.Number, false)
			if err != nil {
				slog.Error("Error fetching PR details for plugin trigger", "repo", args.Repo, "pr", args.Number, "error", err)
				return
			}
			ensurePostUpdateHooks(args.Owner, args.Repo, args.Number, details)
		}()
	}

	reply.Output = parsePluginOutputs(results)
	return nil
}

type RerunPluginsArgs struct {
	Owner   string   `json:"Owner"`
	Repo    string   `json:"Repo"`
	Number  int      `json:"Number"`
	Plugins []string `json:"Plugins"` // Optional: specific plugins to rerun. If empty, all plugins rerun.
}

type RerunPluginsReply struct {
	Okay    bool                    `json:"okay"`
	Message string                  `json:"message"`
	Output  map[string]PluginOutput `json:"output"`
}

// RerunPlugins forces reexecution of plugins for a given PR, bypassing SHA cache checks.
// If Plugins array is specified, only those plugins are rerun; otherwise all are rerun.
func (h *RPCHandler) RerunPlugins(args *RerunPluginsArgs, reply *RerunPluginsReply) error {
	// Clear plugin results to force rerun
	if len(args.Plugins) == 0 {
		// Clear all plugin results for this PR
		err := config.C().DB.DeletePluginResultsForPR(args.Owner, args.Repo, args.Number, "")
		if err != nil {
			slog.Error("Error clearing plugin results", "error", err)
			return err
		}
		slog.Info("Cleared all plugin results for PR, triggering rerun", "repo", args.Repo, "pr", args.Number)
	} else {
		// Clear only specified plugin results
		for _, pluginName := range args.Plugins {
			err := config.C().DB.DeletePluginResultsForPR(args.Owner, args.Repo, args.Number, pluginName)
			if err != nil {
				slog.Error("Error clearing plugin result", "plugin", pluginName, "error", err)
				return err
			}
		}
		slog.Info("Cleared specific plugin results for PR, triggering rerun", "repo", args.Repo, "pr", args.Number, "plugins", args.Plugins)
	}

	// Gather the plugin inputs and run them, all in the background: fetching
	// the details can reach GitHub on a cache miss, and the caller only needs
	// to know the rerun was accepted. Results arrive through GetPluginOutput.
	go func() {
		details, err := GetPRDetails(args.Owner, args.Repo, args.Number, false)
		if err != nil {
			slog.Error("Error fetching PR details for plugin rerun", "repo", args.Repo, "pr", args.Number, "error", err)
			return
		}

		commentsJSON := "[]"
		rawComments, _ := config.C().DB.GetPRComments(args.Number, args.Repo)
		if rawComments != "" {
			commentsJSON = rawComments
		}

		_, sha, _ := config.C().DB.GetPullRequest(args.Number, args.Repo)

		metadataJSON, _ := json.Marshal(details.Metadata)
		RunPluginsForce(args.Owner, args.Repo, args.Number, sha, details.Diff, commentsJSON, string(metadataJSON), details.Metadata.HeadRef, true, args.Plugins)
	}()

	reply.Okay = true
	if len(args.Plugins) == 0 {
		reply.Message = fmt.Sprintf("Rerunning all plugins for PR %d", args.Number)
	} else {
		reply.Message = fmt.Sprintf("Rerunning %d plugin(s) for PR %d", len(args.Plugins), args.Number)
	}

	// Return empty output for now (plugins running async)
	reply.Output = make(map[string]PluginOutput)
	return nil
}

// Experimental: LLM-based diff analysis (file ordering + review ease).
//
// When config.ExperimentalLLMFileOrdering is enabled, the files in a PR diff
// are ordered by an LLM so a reviewer can read the PR top-to-bottom: the
// integration / entry-point changes first, then implementation and helpers,
// then styling changes, then tests last. When the flag is off (the default),
// or if the LLM call fails for any reason, ordering falls back to
// sortFilesTestsLast.
//
// When config.ExperimentalLLMReviewEase is enabled, the same LLM call also
// rates how easy the PR is to review ("easy", "medium", or "hard"). The
// rating is cached alongside the file ordering and exposed as the
// review_ease field in PR metadata and review list items.
//
// The analysis itself — the provider client, prompt, response parsing,
// per-SHA caching, and the ~/.crs/llm_calls.log call log — lives in the llm
// package. This file only keeps the dispatch between the experimental LLM
// ordering and the default sort.

// orderDiffFiles returns the diff files in display order. It dispatches to the
// experimental LLM ordering when enabled, otherwise to the default sort that
// places test files last. LLM orderings are cached per PR SHA so the LLM is
// queried at most once per revision.
//
// Rendering never waits on the LLM. On a cache miss the analysis is kicked off
// in the background and this render falls back to the default sort; the
// ordering is in place for the next render of the same revision. Blocking here
// would put a multi-second network round trip in front of opening a review,
// which is precisely the delay this path must not have.
func orderDiffFiles(files []*utils.DiffFile, repo string, prNumber int, sha string) []*utils.DiffFile {
	if !config.C().ExperimentalLLMFileOrdering {
		return sortFilesTestsLast(files)
	}
	if len(files) < 2 {
		return files
	}
	if ordered, ok := llm.CachedOrderedDiffFiles(files, repo, prNumber, sha); ok {
		return ordered
	}
	go llm.EnsureDiffAnalysis(files, repo, prNumber, sha, llm.TriggerRender)
	return sortFilesTestsLast(files)
}
