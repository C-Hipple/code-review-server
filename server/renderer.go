package server

import (
	"context"
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"crs/org"
	"crs/utils"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v74/github"
)

// SortNewestFirst sorts items so that the most recently created appear first.
const SortNewestFirst = "newest_first"

// SortOldestFirst sorts items so that the oldest created appear first.
const SortOldestFirst = "oldest_first"

type OrgRenderer struct {
	db         *database.DB
	serializer org.OrgSerializer
}

func NewOrgRenderer(db *database.DB) *OrgRenderer {
	// TODO: Figure out if we still want different org serializers
	serializer := org.BaseOrgSerializer{}
	return &OrgRenderer{
		db:         db,
		serializer: serializer,
	}
}

// sectionEntry pairs a stored item with its parsed ReviewItem so the two stay
// aligned while a section is sorted.
type sectionEntry struct {
	item   *database.Item
	review ReviewItem
}

// sectionView is one section in final display order, produced by
// collectSections. Every render entry point serializes from these.
type sectionView struct {
	section *database.Section
	entries []sectionEntry
}

// collectSections is the single pipeline behind all renderer entry points:
// it loads every section and item, parses items into ReviewItems, and sorts
// each section (per-section configured sorting, defaulting to the canonical
// reviewItemLess ordering). Callers serialize the result to org text, JSON
// items, or both.
func (r *OrgRenderer) collectSections() ([]sectionView, error) {
	sections, err := r.db.GetAllSections()
	if err != nil {
		return nil, err
	}

	// Sort sections by Priority then Name
	sort.Slice(sections, func(i, j int) bool {
		if sections[i].Priority != sections[j].Priority {
			return sections[i].Priority < sections[j].Priority
		}
		return sections[i].SectionName < sections[j].SectionName
	})

	// Fetch all items at once to avoid N+1 queries
	allItems, err := r.db.GetAllItems()
	if err != nil {
		return nil, err
	}
	itemsBySection := make(map[int64][]*database.Item)
	for _, item := range allItems {
		itemsBySection[item.SectionID] = append(itemsBySection[item.SectionID], item)
	}

	sectionSorting := config.C().SectionSorting
	views := make([]sectionView, 0, len(sections))
	for _, section := range sections {
		items := itemsBySection[section.ID]
		entries := make([]sectionEntry, len(items))
		for i, item := range items {
			entries[i] = sectionEntry{
				item:   item,
				review: r.parseItemToReviewItem(item, section.SectionName, section.Priority),
			}
		}
		sortMethod, configured := sectionSorting[section.SectionName]
		sortEntries(entries, sortMethod, configured)
		views = append(views, sectionView{section: section, entries: entries})
	}
	return views, nil
}

// sortEntries orders a section's entries in-place. A configured per-section
// method (newest_first/oldest_first) wins; with no configuration the entries
// fall back to the canonical reviewItemLess ordering. An unrecognized
// configured method leaves the stored order untouched.
func sortEntries(entries []sectionEntry, sortMethod string, configured bool) {
	switch {
	case configured && sortMethod == SortNewestFirst:
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].item.CreatedAt.After(entries[j].item.CreatedAt)
		})
	case configured && sortMethod == SortOldestFirst:
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].item.CreatedAt.Before(entries[j].item.CreatedAt)
		})
	case !configured:
		sort.SliceStable(entries, func(i, j int) bool {
			return reviewItemLess(entries[i].review, entries[j].review)
		})
	}
}

// RenderOptions controls which of an item's heavy detail subtrees are
// serialized into the org text. The zero value renders the compact dashboard
// form — headline, metadata and CI status — which is all a list view needs;
// clients that display a full PR fetch it with GetPR instead. Diffs and
// comment threads dominate the payload (they were ~98% of it), so they are
// opt-in rather than opt-out.
type RenderOptions struct {
	IncludeDiff     bool
	IncludeComments bool
}

// FullRenderOptions renders every detail subtree, for callers that want the
// complete org export rather than the dashboard list.
func FullRenderOptions() RenderOptions {
	return RenderOptions{IncludeDiff: true, IncludeComments: true}
}

// renderSectionViews serializes the pipeline output to the org-mode text form.
func (r *OrgRenderer) renderSectionViews(views []sectionView, opts RenderOptions) string {
	var content strings.Builder
	for _, view := range views {
		items := make([]*database.Item, len(view.entries))
		for i, e := range view.entries {
			items[i] = e.item
		}

		content.WriteString(r.buildSectionHeader(view.section, items))
		content.WriteString("\n")

		for _, item := range items {
			for _, line := range r.buildItemLines(item, 2, opts) {
				content.WriteString(line)
				if !strings.HasSuffix(line, "\n") {
					content.WriteString("\n")
				}
			}
		}
		// Add blank line between sections
		content.WriteString("\n")
	}
	return content.String()
}

// reviewItemsFromViews flattens the pipeline output to the JSON items form.
func reviewItemsFromViews(views []sectionView) []ReviewItem {
	var reviewItems []ReviewItem
	for _, view := range views {
		for _, e := range view.entries {
			reviewItems = append(reviewItems, e.review)
		}
	}
	return reviewItems
}

// RenderAllSectionsToString renders the complete org export, including diffs
// and comment threads.
func (r *OrgRenderer) RenderAllSectionsToString() (string, error) {
	views, err := r.collectSections()
	if err != nil {
		return "", err
	}
	return r.renderSectionViews(views, FullRenderOptions()), nil
}

func (r *OrgRenderer) RenderAndGetItems(opts RenderOptions) (string, []ReviewItem, error) {
	views, err := r.collectSections()
	if err != nil {
		return "", nil, err
	}
	return r.renderSectionViews(views, opts), reviewItemsFromViews(views), nil
}

// ReviewItem represents a single PR review item with structured metadata
type ReviewItem struct {
	Section       string `json:"section"`
	Priority      int    `json:"section_priority"`
	Status        string `json:"status"`
	Tags          string `json:"tags"`
	Title         string `json:"title"`
	Owner         string `json:"owner"`
	Repo          string `json:"repo"`
	Number        int    `json:"number"`
	Author        string `json:"author"`
	URL           string `json:"url"`
	ReleaseStatus string `json:"release_status"`
	// ReviewEase is the LLM rating of how easy the PR is to review ("easy",
	// "medium", or "hard"). Empty unless ExperimentalLLMReviewEase is enabled
	// and a rating has been computed.
	ReviewEase string    `json:"review_ease"`
	CreatedAt  time.Time `json:"created_at"`
}

// GetAllReviewItems returns structured review items from all sections
func (r *OrgRenderer) GetAllReviewItems() ([]ReviewItem, error) {
	views, err := r.collectSections()
	if err != nil {
		return nil, err
	}
	return reviewItemsFromViews(views), nil
}

// parseItemToReviewItem extracts structured metadata from an item's details
func (r *OrgRenderer) parseItemToReviewItem(item *database.Item, sectionName string, priority int) ReviewItem {
	details, err := item.GetDetails()
	if err != nil {
		details = []string{}
	}

	reviewItem := ReviewItem{
		Section:   sectionName,
		Priority:  priority,
		Status:    item.Status,
		Tags:      item.Tags,
		Title:     item.Title,
		CreatedAt: item.CreatedAt,
	}

	for _, line := range details {
		line = strings.TrimSpace(line)

		// PR number is typically the first line and just a number
		if reviewItem.Number == 0 {
			var num int
			if _, err := fmt.Sscanf(line, "%d", &num); err == nil && num > 0 {
				reviewItem.Number = num
				continue
			}
		}

		// Parse Repo: owner/repo
		if strings.HasPrefix(line, "Repo:") {
			repoStr := strings.TrimSpace(strings.TrimPrefix(line, "Repo:"))
			parts := strings.Split(repoStr, "/")
			if len(parts) >= 2 {
				reviewItem.Owner = parts[0]
				reviewItem.Repo = parts[1]
			} else if len(parts) == 1 {
				reviewItem.Repo = parts[0]
			}
			continue
		}

		// Parse Author: username or Author: username (Full Name)
		if strings.HasPrefix(line, "Author:") {
			reviewItem.Author = strings.TrimSpace(strings.TrimPrefix(line, "Author:"))
			continue
		}

		// Parse URL (usually starts with https://github.com)
		if strings.HasPrefix(line, "https://") {
			reviewItem.URL = line
			continue
		}
	}

	// Look up release status from DB if we have owner/repo/number
	if reviewItem.Owner != "" && reviewItem.Repo != "" && reviewItem.Number > 0 {
		if status, err := r.db.GetReleaseStatus(reviewItem.Owner, reviewItem.Repo, reviewItem.Number); err == nil && status != "" {
			reviewItem.ReleaseStatus = status
		}
	}

	// Look up the LLM review-ease rating when the feature is enabled
	if config.C().ExperimentalLLMReviewEase && reviewItem.Repo != "" && reviewItem.Number > 0 {
		if ease, err := r.db.GetLatestReviewEase(reviewItem.Number, reviewItem.Repo); err == nil && ease != "" {
			reviewItem.ReviewEase = ease
		}
	}

	return reviewItem
}

func (r *OrgRenderer) RenderFile(filename, orgFileDir string) error {
	content, err := r.RenderAllSectionsToString()
	if err != nil {
		return err
	}

	// Write to file
	orgFilePath := orgFileDir
	if strings.HasPrefix(orgFilePath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		orgFilePath = filepath.Join(home, orgFilePath[2:])
	}
	orgFilePath = filepath.Join(orgFilePath, filename)

	return os.WriteFile(orgFilePath, []byte(content), 0644)
}

func (r *OrgRenderer) buildSectionHeader(section *database.Section, items []*database.Item) string {
	doneCount := 0
	for _, item := range items {
		if item.Status == "DONE" || item.Status == "CANCELLED" {
			doneCount++
		}
	}

	status := "TODO"
	if doneCount == len(items) && len(items) > 0 {
		status = "DONE"
	}

	indentStars := strings.Repeat("*", 1)
	ratio := fmt.Sprintf("[%d/%d]", doneCount, len(items))

	return fmt.Sprintf("%s %s %s %s", indentStars, status, section.SectionName, ratio)
}

// detailSubtree returns the heading text of the subtree a detail element
// opens (e.g. "Diff", "Comments [4]"), or "" when the element is ordinary
// content and therefore inherits the enclosing subtree's keep/skip decision.
// Detail elements are stored one-per-subtree-chunk, so a diff's body is a
// single element and never mistaken for a heading.
func detailSubtree(detail string) string {
	if !strings.HasPrefix(detail, "*") {
		return ""
	}
	rest := strings.TrimLeft(detail, "*")
	if !strings.HasPrefix(rest, " ") {
		return ""
	}
	heading, _, _ := strings.Cut(rest, "\n")
	return strings.TrimSpace(heading)
}

func (r *OrgRenderer) buildItemLines(item *database.Item, indentLevel int, opts RenderOptions) []string {
	details, err := item.GetDetails()
	if err != nil {
		slog.Error("Error getting item details", "error", err, "item_id", item.ID)
		details = []string{}
	}

	tags, err := item.GetTags()
	if err != nil {
		slog.Error("Error getting item tags", "error", err, "item_id", item.ID)
		tags = []string{}
	}

	// Append the LLM review-ease rating as a headline tag when enabled
	if config.C().ExperimentalLLMReviewEase {
		if repo, number := itemPRRef(item); repo != "" && number > 0 {
			if ease, err := r.db.GetLatestReviewEase(number, repo); err == nil {
				if tag := reviewEaseOrgTag(ease); tag != "" {
					tags = append(tags, tag)
				}
			}
		}
	}

	// Build the title line
	indentStars := strings.Repeat("*", indentLevel)
	titleLine := fmt.Sprintf("%s %s %s", indentStars, item.Status, item.Title)

	// Add tags
	if len(tags) > 0 {
		tagStr := ":" + strings.Join(tags, ":") + ":"
		titleLine += "\t\t" + tagStr
	}

	// Walk the detail elements, dropping whole subtrees the caller did not
	// ask for. keep carries across non-heading elements so a subtree's body
	// follows its heading in or out.
	lines := []string{titleLine + "\n"}
	keep := true
	for _, d := range details {
		if heading := detailSubtree(d); heading != "" {
			switch {
			case strings.HasPrefix(heading, "BODY"):
				keep = false
			case strings.HasPrefix(heading, "Diff"):
				keep = opts.IncludeDiff
			case strings.HasPrefix(heading, "Comments"):
				keep = opts.IncludeComments
			default:
				keep = true
			}
		}
		if keep {
			lines = append(lines, d)
		}
	}

	return lines
}

// itemPRRef extracts the PR number and short repo name from an item's detail
// lines (the same encoding parseItemToReviewItem reads). It returns ("", 0)
// when the item does not reference a PR.
func itemPRRef(item *database.Item) (repo string, number int) {
	details, err := item.GetDetails()
	if err != nil {
		return "", 0
	}
	for _, line := range details {
		line = strings.TrimSpace(line)
		if number == 0 {
			var num int
			if _, err := fmt.Sscanf(line, "%d", &num); err == nil && num > 0 {
				number = num
				continue
			}
		}
		if strings.HasPrefix(line, "Repo:") {
			repoStr := strings.TrimSpace(strings.TrimPrefix(line, "Repo:"))
			parts := strings.Split(repoStr, "/")
			repo = parts[len(parts)-1]
		}
	}
	return repo, number
}

// reviewEaseOrgTag converts a stored review-ease rating into an org headline
// tag. It centralizes the rating -> tag mapping so a future change to the
// rating scheme (e.g. a numeric score) only needs to be handled here. It
// returns an empty string when there is no usable rating, in which case no
// tag should be added.
func reviewEaseOrgTag(ease string) string {
	switch ease {
	case "easy", "medium", "hard":
		return ease
	}
	return ""
}

func renderPullRequest(diff string, comments []PRComment) string {
	var output strings.Builder
	output.WriteString(diff)
	for _, comment := range comments {
		output.WriteString(formatComment(comment))
	}
	return output.String()
}

func formatComment(comment PRComment) string {
	var formatted strings.Builder
	formatted.WriteString("Reviewed By: " + comment.GetLogin() + "\n")
	formatted.WriteString(comment.GetBody())
	formatted.WriteString("\n------------------\n")
	return formatted.String()
}

// Comments can either be from Github which are submitted
// or LocalComments which are not yet submitted.
type PRComment interface {
	GetLogin() string
	GetBody() string
	GetID() string
	GetPosition() string
	GetInReplyTo() int64
	GetPath() string
	GetCreatedAt() time.Time
	IsOutdated() bool
	GetCommitID() string
	GetDiffHunk() string
	// GetReviewID is the ID of the review this comment was submitted with, or 0
	// for standalone issue comments and local (unsubmitted) comments.
	GetReviewID() int64
	GetHTMLURL() string
}

type CommentJSON struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	Path      string    `json:"path"`
	Position  string    `json:"position"`
	InReplyTo int64     `json:"in_reply_to"`
	CreatedAt time.Time `json:"created_at"`
	Outdated  bool      `json:"outdated"`
	DiffHunk  string    `json:"diff_hunk"`
	// ReviewID ties a review comment back to the review that submitted it, so
	// clients can show "alice requested changes with these four comments" rather
	// than a flat list. 0 for issue comments and unsubmitted local comments.
	ReviewID int64  `json:"review_id"`
	HTMLURL  string `json:"html_url"`
	// The remaining fields come from GitHub's GraphQL reviewThreads connection
	// (see git_tools.GetReviewThreads) and are zero when that fetch was skipped
	// or failed — resolution state has no REST equivalent.
	ThreadID   string `json:"thread_id"`
	Resolved   bool   `json:"resolved"`
	ResolvedBy string `json:"resolved_by"`
}

type ReviewJSON struct {
	ID          int64     `json:"id"`
	User        string    `json:"user"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
	HTMLURL     string    `json:"html_url"`
}

type CommitJSON struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	URL     string `json:"url"`
}

type PRMetadata struct {
	Number             int      `json:"number"`
	Title              string   `json:"title"`
	Author             string   `json:"author"`
	BaseRef            string   `json:"base_ref"`
	HeadRef            string   `json:"head_ref"`
	State              string   `json:"state"`
	Milestone          string   `json:"milestone"`
	Labels             []string `json:"labels"`
	Assignees          []string `json:"assignees"`
	Reviewers          []string `json:"reviewers"`            // Requested individual reviewers
	RequestedTeams     []string `json:"requested_teams"`      // Requested team reviewers
	ApprovedBy         []string `json:"approved_by"`          // Logins of users who approved
	ChangesRequestedBy []string `json:"changes_requested_by"` // Logins of users who requested changes
	CommentedBy        []string `json:"commented_by"`         // Logins of users who commented (non-approval/non-request)
	Draft              bool     `json:"draft"`
	CIStatus           string   `json:"ci_status"`
	CIFailures         []string `json:"ci_failures"`
	Body               string   `json:"body"`
	URL                string   `json:"url"`
	RepoPath           string   `json:"repo_path"`
	WorktreePath       string   `json:"worktree_path"`
	ReleaseStatus      string   `json:"release_status"`
	// ReviewEase is the LLM rating of how easy the PR is to review ("easy",
	// "medium", or "hard"). Empty unless ExperimentalLLMReviewEase is enabled
	// and a rating has been computed.
	ReviewEase   string `json:"review_ease"`
	ChangedFiles int    `json:"changed_files"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
}

type PRDetails struct {
	Metadata         PRMetadata    `json:"metadata"`
	Diff             string        `json:"diff"`
	Comments         []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews          []ReviewJSON  `json:"reviews"`
	Commits          []CommitJSON  `json:"commits"`
}

// GitHubPRComment wraps *github.PullRequestComment to implement PRComment interface
type GitHubPRComment struct {
	*github.PullRequestComment
}

// GetLogin returns the login of the comment author
func (c *GitHubPRComment) GetLogin() string {
	if c.User != nil {
		return c.User.GetLogin()
	}
	return ""
}

// GetBody returns the comment body
func (c *GitHubPRComment) GetBody() string {
	return c.PullRequestComment.GetBody()
}

// GetID returns the comment ID as a string
func (c *GitHubPRComment) GetID() string {
	return strconv.FormatInt(c.PullRequestComment.GetID(), 10)
}

// GetPosition returns the comment position as a string
func (c *GitHubPRComment) GetPosition() string {
	if c.Position != nil {
		return strconv.Itoa(*c.Position)
	}
	return ""
}

// GetInReplyTo returns the ID of the comment this is replying to
func (c *GitHubPRComment) GetInReplyTo() int64 {
	return c.PullRequestComment.GetInReplyTo()
}

// GetPath returns the file path of the comment
func (c *GitHubPRComment) GetPath() string {
	if c.Path != nil {
		return *c.Path
	}
	return ""
}

// GetCreatedAt returns the creation time of the comment
func (c *GitHubPRComment) GetCreatedAt() time.Time {
	if c.CreatedAt != nil {
		return c.CreatedAt.Time
	}
	return time.Time{}
}

func (c *GitHubPRComment) IsOutdated() bool {
	// A comment is outdated if it targetted a line (OriginalPosition/Line != nil)
	// but is no longer attached to the current diff.
	// We check for Position == nil OR Line == nil to handle cases where the API
	// returns a valid Position (in the hunk) but no mapped Line in the file.
	return (c.Position == nil || c.Line == nil) && (c.OriginalPosition != nil || c.OriginalLine != nil)
}

func (c *GitHubPRComment) GetCommitID() string {
	if c.CommitID != nil {
		return *c.CommitID
	}
	return ""
}

func (c *GitHubPRComment) GetDiffHunk() string {
	if c.DiffHunk != nil {
		return *c.DiffHunk
	}
	return ""
}

func (c *GitHubPRComment) GetReviewID() int64 {
	return c.PullRequestComment.GetPullRequestReviewID()
}

func (c *GitHubPRComment) GetHTMLURL() string {
	return c.PullRequestComment.GetHTMLURL()
}

// LocalPRComment wraps database.LocalComment to implement PRComment interface
type LocalPRComment struct {
	*database.LocalComment
}

// GetLogin returns an empty string for local comments (no author)
func (c *LocalPRComment) GetLogin() string {
	return "local"
}

// GetBody returns the comment body
func (c *LocalPRComment) GetBody() string {
	if c.Body != nil {
		return *c.Body
	}
	return ""
}

// GetID returns the comment ID as a string
func (c *LocalPRComment) GetID() string {
	return strconv.FormatInt(c.ID, 10)
}

// GetPosition returns the comment position as a string
func (c *LocalPRComment) GetPosition() string {
	return strconv.FormatInt(c.Position, 10)
}

// GetInReplyTo returns the ID of the comment this is replying to, or 0 if it's a root comment
func (c *LocalPRComment) GetInReplyTo() int64 {
	if c.ReplyToID != nil {
		return *c.ReplyToID
	}
	return 0
}

// GetPath returns the file path of the comment
func (c *LocalPRComment) GetPath() string {
	return c.Filename
}

// GetCreatedAt returns zero time for local comments (no timestamp stored)
func (c *LocalPRComment) GetCreatedAt() time.Time {
	return time.Time{}
}

func (c *LocalPRComment) IsOutdated() bool {
	return false
}

func (c *LocalPRComment) GetCommitID() string {
	return ""
}

func (c *LocalPRComment) GetDiffHunk() string {
	return ""
}

// Local comments have not been submitted yet, so they belong to no review and
// have no URL on GitHub.
func (c *LocalPRComment) GetReviewID() int64 {
	return 0
}

func (c *LocalPRComment) GetHTMLURL() string {
	return ""
}

// convertToPRComments converts a slice of *github.PullRequestComment to []PRComment
func convertToPRComments(comments []*github.PullRequestComment) []PRComment {
	result := make([]PRComment, len(comments))
	for i, comment := range comments {
		result[i] = &GitHubPRComment{comment}
	}
	return result
}

// convertLocalCommentsToPRComments converts a slice of database.LocalComment to []PRComment
func convertLocalCommentsToPRComments(localComments []database.LocalComment) []PRComment {
	result := make([]PRComment, len(localComments))
	for i := range localComments {
		result[i] = &LocalPRComment{&localComments[i]}
	}
	return result
}

// convertIssueCommentToPRComment converts a *github.IssueComment to *github.PullRequestComment
func convertIssueCommentToPRComment(ic *github.IssueComment) *github.PullRequestComment {
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

// cacheMissState records what was and wasn't in the DB at the time of a GetPRDetails call.
type cacheMissState struct {
	owner        string
	repo         string
	number       int
	skipCache    bool
	missedFields []string
	// fetchErr is set when GetPRDetails bailed out on a GitHub API error rather
	// than on missing cache data. Without it, a failed request lands in the log
	// looking like an ordinary miss on whichever field it died at.
	fetchErr error
	// per-field: true = data was present in DB, false = cache miss
	metadataHit bool
	diffHit     bool
	commentsHit bool
	reviewsHit  bool
	commitsHit  bool
}

// humanizeAgo renders a duration as a short "how long ago" string.
func humanizeAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours()/24), int(d.Hours())%24)
	}
}

// cacheFieldToLogField maps a missed field name as GetPRDetails records it to
// the field name the workflow writes into WorkflowActionLog. "metadata" and
// "diff" happen to match; the rest are listed so the mapping stays explicit
// when either side gains a field.
var cacheFieldToLogField = map[string]string{
	"metadata": "metadata",
	"diff":     "diff",
	"comments": "comments",
	"reviews":  "reviews",
	"commits":  "commits",
}

// workflowActionSection reports what the workflows last did to this PR. A cache
// miss on its own only says the data was absent; the last action says whether a
// workflow ever wrote that field, when, from which SHA, and which workflow was
// responsible — which is what turns the report into something actionable.
func workflowActionSection(db *database.DB, state cacheMissState, now time.Time) string {
	var sb strings.Builder
	sb.WriteString("\nLast Workflow Action:\n")

	latest, err := db.GetLatestWorkflowAction(state.owner, state.repo, state.number)
	if err != nil {
		sb.WriteString(fmt.Sprintf("  (lookup failed: %v)\n", err))
		return sb.String()
	}
	if latest == nil {
		sb.WriteString("  (none recorded — no workflow has written this PR since the action log was introduced)\n")
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("  Workflow:        %s\n", orNone(latest.WorkflowName)))
	sb.WriteString(fmt.Sprintf("  Action:          %s\n", latest.Action))
	if latest.CreatedAt.IsZero() {
		sb.WriteString("  When:            (unknown)\n")
	} else {
		sb.WriteString(fmt.Sprintf("  When:            %s ago  (%s UTC)\n",
			humanizeAgo(now.Sub(latest.CreatedAt)), latest.CreatedAt.Format("2006-01-02 15:04:05")))
	}
	sb.WriteString(fmt.Sprintf("  Head SHA:        %s\n", orNone(latest.SHA)))
	sb.WriteString(fmt.Sprintf("  Fields written:  %s\n", latest.FieldsSummary()))
	if latest.SectionName != "" {
		sb.WriteString(fmt.Sprintf("  Section:         %s\n", latest.SectionName))
	}
	if latest.Detail != "" {
		sb.WriteString(fmt.Sprintf("  Note:            %s\n", latest.Detail))
	}

	// Per-field history for exactly the fields this request had to fetch. A
	// field the workflows have never written is the clearest possible signal
	// that the gap is on the workflow side, not the read side.
	if len(state.missedFields) > 0 {
		// Under skipCache the field list is what we refetched on purpose, not
		// what was missing, so the heading has to say so.
		if state.skipCache {
			sb.WriteString("\nLast Workflow Write Per Refetched Field:\n")
		} else {
			sb.WriteString("\nLast Workflow Write Per Missed Field:\n")
		}
		for _, missed := range state.missedFields {
			logField, mapped := cacheFieldToLogField[missed]
			if !mapped {
				sb.WriteString(fmt.Sprintf("  %-9s (not tracked in the workflow action log)\n", missed+":"))
				continue
			}
			action, err := db.GetLatestWorkflowActionWithField(state.owner, state.repo, state.number, logField)
			if err != nil {
				sb.WriteString(fmt.Sprintf("  %-9s (lookup failed: %v)\n", missed+":", err))
				continue
			}
			if action == nil {
				sb.WriteString(fmt.Sprintf("  %-9s never written by a workflow\n", missed+":"))
				continue
			}
			when := "unknown time"
			if !action.CreatedAt.IsZero() {
				when = humanizeAgo(now.Sub(action.CreatedAt)) + " ago"
			}
			sb.WriteString(fmt.Sprintf("  %-9s %s by %s (sha %s)\n",
				missed+":", when, orNone(action.WorkflowName), orNone(action.SHA)))
		}
	}

	return sb.String()
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func writeCacheMissLog(state cacheMissState) {
	if len(state.missedFields) == 0 {
		return
	}

	crsHome, err := config.GetCRSHome()
	if err != nil {
		slog.Warn("cache miss log: cannot determine CRS home", "error", err)
		return
	}

	logPath := filepath.Join(crsHome, "cache_miss.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Warn("cache miss log: cannot open log file", "path", logPath, "error", err)
		return
	}
	defer f.Close()

	db := config.C().DB

	// Gather local comment count
	localComments, _ := db.GetLocalCommentsForPR(state.owner, state.repo, state.number)
	localCommentCount := len(localComments)

	// Gather workflow info: when was this PR first added by the workflow?
	// Workflow items are keyed by "{owner}/{repo}-{number}" (see PRToOrgBridge.Identifier),
	// NOT the short "{repo}-{number}" form used for cache table lookups.
	identifier := fmt.Sprintf("%s/%s-%d", state.owner, state.repo, state.number)
	workflowAddedAt, sectionName, _ := db.GetItemWorkflowInfo(identifier)

	now := time.Now().UTC()

	var sb strings.Builder
	// A forced refresh (SyncPR) bypasses every cache on purpose, so its fields
	// are not misses. Keeping it under a separate header means grepping for
	// "Cache Miss Report" turns up only the entries worth investigating.
	if state.skipCache {
		sb.WriteString("=== Forced Refresh Report ===\n")
	} else {
		sb.WriteString("=== Cache Miss Report ===\n")
	}
	sb.WriteString(fmt.Sprintf("Time:     %s\n", now.Format("2006-01-02 15:04:05 UTC")))
	sb.WriteString(fmt.Sprintf("PR:       #%d  (owner: %s, repo: %s)\n", state.number, state.owner, state.repo))
	if state.skipCache {
		sb.WriteString(fmt.Sprintf("Fetched:  %s\n", strings.Join(state.missedFields, ", ")))
		sb.WriteString("Forced:   yes (skipCache=true — caches bypassed by request, not missing)\n")
	} else {
		sb.WriteString(fmt.Sprintf("Missed:   %s\n", strings.Join(state.missedFields, ", ")))
	}
	if state.fetchErr != nil {
		sb.WriteString(fmt.Sprintf("Error:    %v\n", state.fetchErr))
		sb.WriteString("          (request aborted here — fields below this point were never reached)\n")
	}

	sb.WriteString("\nCache State (before this request):\n")
	hitStr := func(hit bool) string {
		if hit {
			return "HIT"
		}
		return "MISS"
	}
	sb.WriteString(fmt.Sprintf("  metadata  (PRMetadataCache):  %s\n", hitStr(state.metadataHit)))
	sb.WriteString(fmt.Sprintf("  diff      (PullRequests):     %s\n", hitStr(state.diffHit)))
	sb.WriteString(fmt.Sprintf("  comments  (PRComments):       %s\n", hitStr(state.commentsHit)))
	sb.WriteString(fmt.Sprintf("  reviews   (PRReviews):        %s\n", hitStr(state.reviewsHit)))
	sb.WriteString(fmt.Sprintf("  commits   (PRCommits):        %s\n", hitStr(state.commitsHit)))

	sb.WriteString("\nLocal Data:\n")
	sb.WriteString(fmt.Sprintf("  Local comments:  %d\n", localCommentCount))

	sb.WriteString("\nWorkflow:\n")
	sb.WriteString(fmt.Sprintf("  Item identifier: %s\n", identifier))
	if sectionName != "" {
		sb.WriteString(fmt.Sprintf("  Section:         %s\n", sectionName))
	} else {
		sb.WriteString("  Section:         (not found in workflow items — PR may not have been fetched by a workflow yet)\n")
	}
	if !workflowAddedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("  Added:           %s ago  (%s UTC)\n",
			humanizeAgo(now.Sub(workflowAddedAt)), workflowAddedAt.UTC().Format("2006-01-02 15:04:05")))
	} else {
		sb.WriteString("  Added:           (unknown — item not found or created_at not set)\n")
	}

	sb.WriteString(workflowActionSection(db, state, now))

	sb.WriteString("---\n\n")

	if _, err := f.WriteString(sb.String()); err != nil {
		slog.Warn("cache miss log: write failed", "error", err)
	}
}

func GetPRDetails(owner string, repo string, number int, skipCache bool) (*PRDetails, error) {
	client := git_tools.GetGithubClient()
	ctx := context.Background()

	// Probe all caches upfront to record hit/miss state for the log.
	// This happens before the existing cache logic so we capture the true pre-fetch state.
	// The probes run even under skipCache: a forced refresh doesn't consult the
	// caches, but reporting every field as MISS just because we chose not to
	// read them tells us nothing about whether the warm path is working.
	missState := cacheMissState{owner: owner, repo: repo, number: number, skipCache: skipCache}
	if v, _ := config.C().DB.GetPRMetadataCache(owner, repo, number); v != "" {
		missState.metadataHit = true
	}
	if v, _, _ := config.C().DB.GetPullRequest(number, repo); v != "" {
		missState.diffHit = true
	}
	if v, _ := config.C().DB.GetPRComments(number, repo); v != "" {
		missState.commentsHit = true
	}
	if v, _ := config.C().DB.GetPRReviews(number, repo); v != "" {
		missState.reviewsHit = true
	}
	if v, _ := config.C().DB.GetPRCommits(number, repo); v != "" {
		missState.commitsHit = true
	}
	defer func() { writeCacheMissLog(missState) }()

	var metadata PRMetadata
	var headSHA string
	var baseSHA string
	var reviews []ReviewJSON
	// Tracked separately from `reviews != nil` because a PR with no reviews
	// loads as an empty slice — indistinguishable from "never loaded" if we go
	// by nil-ness, which used to cost a duplicate ListReviews call and a
	// phantom "reviews" entry in the cache miss log on every fresh fetch.
	reviewsLoaded := false
	needsFreshFetch := skipCache

	// 1. Try to load metadata from cache first (unless skipCache)
	if !skipCache {
		cachedMetadataJSON, err := config.C().DB.GetPRMetadataCache(owner, repo, number)
		if err == nil && cachedMetadataJSON != "" {
			if err := json.Unmarshal([]byte(cachedMetadataJSON), &metadata); err == nil {
				slog.Debug("Using cached PR metadata", "pr", number, "repo", repo)
				// We have cached metadata, but we still need SHA for diff lookup
				// Get it from the PullRequests table
				_, sha, _ := config.C().DB.GetPullRequest(number, repo)
				headSHA = sha
			} else {
				needsFreshFetch = true
			}
		} else {
			needsFreshFetch = true
		}
	}

	// 2. Fetch fresh PR details from GitHub if needed
	if needsFreshFetch {
		missState.missedFields = append(missState.missedFields, "metadata")
		pr, _, err := client.PullRequests.Get(ctx, owner, repo, number)
		if err != nil {
			// If we have cached data, return that instead of failing
			if metadata.Number != 0 {
				slog.Warn("GitHub API error, falling back to cached metadata", "error", err)
				missState.fetchErr = err
			} else {
				missState.fetchErr = err
				return nil, err
			}
		} else {
			// Extract SHAs for CI status lookup and context expansion
			if pr.Head != nil && pr.Head.SHA != nil {
				headSHA = *pr.Head.SHA
			}
			if pr.Base != nil && pr.Base.SHA != nil {
				baseSHA = *pr.Base.SHA
			}

			// Fetch Reviewers (Requested)
			reviewers, _ := GetRequestedReviewers(owner, repo, number, skipCache)
			reviewerLogins := []string{}
			teamLogins := []string{}
			if reviewers != nil {
				for _, r := range reviewers.Users {
					reviewerLogins = append(reviewerLogins, r.GetLogin())
				}
				for _, t := range reviewers.Teams {
					teamLogins = append(teamLogins, t.GetName())
				}
			}

			// Fetch reviews (with caching)
			var reviewsErr error
			reviews, reviewsErr = GetPRReviews(owner, repo, number, skipCache)
			reviewsLoaded = reviewsErr == nil

			approvedBy := []string{}
			changesRequestedBy := []string{}
			commentedBy := []string{}
			latestReviewState := make(map[string]string)
			for _, r := range reviews {
				latestReviewState[r.User] = r.State
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

			// Fetch CI Status
			var ciStatus string
			var ciFailures []string
			if headSHA != "" {
				status, err := GetLatestCIStatus(owner, repo, number, headSHA, skipCache)
				if err == nil && status != nil {
					total := 0
					success := 0
					overallState := "success"
					if status.Status != nil {
						if status.Status.GetState() != "success" && status.Status.GetState() != "" {
							overallState = status.Status.GetState()
						}
						total += status.Status.GetTotalCount()
						for _, s := range status.Status.Statuses {
							if s.GetState() == "success" {
								success++
							} else if s.GetState() == "failure" {
								ciFailures = append(ciFailures, fmt.Sprintf("%s: %s", s.GetContext(), s.GetDescription()))
							}
						}
					}
					if status.CheckRuns != nil {
						total += status.CheckRuns.GetTotal()
						for _, cr := range status.CheckRuns.CheckRuns {
							if cr.GetConclusion() == "success" {
								success++
							} else {
								if cr.GetConclusion() != "" && cr.GetConclusion() != "neutral" && cr.GetConclusion() != "skipped" {
									overallState = "failure"
									ciFailures = append(ciFailures, fmt.Sprintf("%s: %s", cr.GetName(), cr.GetConclusion()))
								}
							}
						}
					}
					if total == 0 && overallState == "success" {
						overallState = "pending"
					}
					ciStatus = fmt.Sprintf("%s (%d/%d checks passed)", overallState, success, total)
				}
			}

			labels := []string{}
			for _, l := range pr.Labels {
				labels = append(labels, l.GetName())
			}

			assignees := []string{}
			for _, u := range pr.Assignees {
				assignees = append(assignees, u.GetLogin())
			}

			metadata = PRMetadata{
				Number:             number,
				Title:              pr.GetTitle(),
				Author:             pr.User.GetLogin(),
				BaseRef:            pr.Base.GetRef(),
				HeadRef:            pr.Head.GetRef(),
				State:              pr.GetState(),
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
				ChangedFiles:       int(pr.GetChangedFiles()),
				Additions:          int(pr.GetAdditions()),
				Deletions:          int(pr.GetDeletions()),
			}

			// Fetch worktree path if it exists
			if worktreePath, err := config.C().DB.GetWorktree(number, repo, owner); err == nil {
				metadata.WorktreePath = worktreePath
			}

			if pr.Milestone != nil {
				metadata.Milestone = pr.Milestone.GetTitle()
			}

			// Cache the metadata
			if metadataJSON, err := json.Marshal(metadata); err != nil {
				slog.Error("Error marshaling PR metadata for cache", "pr", number, "repo", repo, "error", err)
			} else if err := config.C().DB.UpsertPRMetadataCache(owner, repo, number, string(metadataJSON)); err != nil {
				slog.Error("Error caching PR metadata", "pr", number, "repo", repo, "error", err)
			}
		}
	}

	// Always ensure RepoPath is set based on current configuration, even if metadata was cached
	if path, err := GetLocalRepoPath(repo); err == nil {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			metadata.RepoPath = path
		}
	}

	// Load release status from DB
	if releaseStatus, err := config.C().DB.GetReleaseStatus(owner, repo, number); err == nil && releaseStatus != "" {
		metadata.ReleaseStatus = releaseStatus
	}

	// Load the LLM review-ease rating from DB when the feature is enabled
	if config.C().ExperimentalLLMReviewEase {
		if ease, err := config.C().DB.GetLatestReviewEase(number, repo); err == nil && ease != "" {
			metadata.ReviewEase = ease
		}
	}

	// 3. Fetch Diff (with caching)
	var diff string
	if !skipCache {
		cachedDiff, _, err := config.C().DB.GetPullRequest(number, repo)
		if err == nil && cachedDiff != "" {
			diff = cachedDiff
		}
	}
	if diff == "" {
		missState.missedFields = append(missState.missedFields, "diff")
		d, _, err := client.PullRequests.GetRaw(ctx, owner, repo, number, github.RawOptions{Type: github.Diff})
		if err != nil {
			slog.Error("Error getting PR diff", "pr", number, "repo", repo, "error", err)
		} else {
			diff = d
			// Store in cache
			if err := config.C().DB.UpsertPullRequest(number, repo, headSHA, baseSHA, diff); err != nil {
				slog.Error("Error caching PR diff", "pr", number, "repo", repo, "error", err)
			}
		}
	}

	parsedDiff, _ := utils.Parse(diff)
	formattedDiff := diff
	if parsedDiff != nil {
		formattedDiff = formatDiff(parsedDiff, repo, number, headSHA)
	}

	// 4. Fetch Comments (GitHub + Local)
	var githubComments []*github.PullRequestComment
	if !skipCache {
		cachedCommentsJSON, err := config.C().DB.GetPRComments(number, repo)
		if err == nil && cachedCommentsJSON != "" {
			if err := json.Unmarshal([]byte(cachedCommentsJSON), &githubComments); err != nil {
				slog.Error("Error unmarshaling cached PR comments", "pr", number, "repo", repo, "error", err)
			}
		}
	}
	if githubComments == nil {
		missState.missedFields = append(missState.missedFields, "comments")
		var err error
		githubComments, err = git_tools.ListAllPRComments(ctx, client, owner, repo, number)
		if err != nil {
			slog.Error("Error fetching PR review comments", "pr", number, "repo", repo, "error", err)
		}

		issueComments, err := git_tools.ListAllIssueComments(ctx, client, owner, repo, number)
		if err != nil {
			slog.Error("Error fetching PR issue comments", "pr", number, "repo", repo, "error", err)
		}
		for _, ic := range issueComments {
			githubComments = append(githubComments, convertIssueCommentToPRComment(ic))
		}

		if githubComments != nil {
			// Sort comments by creation date to maintain order
			sort.Slice(githubComments, func(i, j int) bool {
				return githubComments[i].CreatedAt.Before(githubComments[j].CreatedAt.Time)
			})

			commentsJSON, err := json.Marshal(githubComments)
			if err != nil {
				slog.Error("Error marshaling PR comments for cache", "pr", number, "repo", repo, "error", err)
			} else if err := config.C().DB.UpsertPRComments(number, repo, string(commentsJSON)); err != nil {
				slog.Error("Error caching PR comments", "pr", number, "repo", repo, "error", err)
			}
		}
	}

	comments := convertToPRComments(githubComments)
	comments = filterComments(comments)

	localComments, err := config.C().DB.GetLocalCommentsForPR(owner, repo, number)
	if err != nil {
		slog.Error("Error fetching local comments", "pr", number, "repo", repo, "error", err)
	}
	comments = append(comments, convertLocalCommentsToPRComments(localComments)...)

	// Resolution state is GraphQL-only, so it is fetched separately from the
	// REST comments above and merged in during the split.
	reviewThreads := GetPRReviewThreads(owner, repo, number, skipCache)

	commentJSONs, outdatedCommentJSONs := splitComments(comments, reviewThreads)

	// 5. Load Reviews (with caching)
	if !reviewsLoaded {
		if !missState.reviewsHit {
			missState.missedFields = append(missState.missedFields, "reviews")
		}
		reviews, _ = GetPRReviews(owner, repo, number, skipCache)
	}

	// 6. Fetch Commits (with caching)
	var commits []CommitJSON
	if !skipCache {
		cachedCommitsJSON, err := config.C().DB.GetPRCommits(number, repo)
		if err == nil && cachedCommitsJSON != "" {
			if err := json.Unmarshal([]byte(cachedCommitsJSON), &commits); err != nil {
				slog.Error("Error unmarshaling cached PR commits", "pr", number, "repo", repo, "error", err)
			}
		}
	}
	if commits == nil {
		missState.missedFields = append(missState.missedFields, "commits")
		ghCommits, err := git_tools.ListAllPRCommits(ctx, client, owner, repo, number)
		if err != nil {
			slog.Error("Error fetching commits", "error", err)
		} else {
			for _, c := range ghCommits {
				msg := c.Commit.GetMessage()
				// if idx := strings.Index(msg, "\n"); idx != -1 {
				// 	msg = msg[:idx]
				// }
				commits = append(commits, CommitJSON{
					SHA:     c.GetSHA(),
					Message: msg,
					Author:  c.Commit.Author.GetName(),
					Date:    c.Commit.Author.GetDate().Format(time.RFC3339),
					URL:     c.GetHTMLURL(),
				})
			}

			// Cache the commits
			if commitsJSON, err := json.Marshal(commits); err != nil {
				slog.Error("Error marshaling PR commits for cache", "pr", number, "repo", repo, "error", err)
			} else if err := config.C().DB.UpsertPRCommits(number, repo, string(commitsJSON)); err != nil {
				slog.Error("Error caching PR commits", "pr", number, "repo", repo, "error", err)
			}
		}
	}

	return &PRDetails{
		Metadata:         metadata,
		Diff:             formattedDiff,
		Comments:         commentJSONs,
		OutdatedComments: outdatedCommentJSONs,
		Reviews:          reviews,
		Commits:          commits,
	}, nil
}

func GetFullPRResponse(owner string, repo string, number int, skipCache bool, details *PRDetails) (string, error) {

	// If we have details, use them. Otherwise, fetch everything.
	// NOTE: This fallback path might be less optimized than the original if we don't fully implement it,
	// but currently GetFullPRResponse is only called with details from fetchPRAndRunPlugins.
	// If it's called with nil, we should probably fetch details first.
	if details == nil {
		d, err := GetPRDetails(owner, repo, number, skipCache)
		if err != nil {
			return "", err
		}
		details = d
	}

	// Unpack details
	metadata := details.Metadata
	reviews := details.Reviews
	comments := details.Comments
	outdatedComments := details.OutdatedComments
	commits := details.Commits

	// Diff (used for inline comments)
	diff := details.Diff

	// Prepare data for rendering

	// Header
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("#%d: %s\n", number, metadata.Title))
	sb.WriteString(fmt.Sprintf("Author: \t@%s\n", metadata.Author))
	sb.WriteString(fmt.Sprintf("Title: \t%s\n", metadata.Title))
	sb.WriteString(fmt.Sprintf("Refs:  %s ... %s\n", metadata.BaseRef, metadata.HeadRef))
	sb.WriteString(fmt.Sprintf("URL:   %s\n", metadata.URL))
	sb.WriteString(fmt.Sprintf("State: \t%s\n", metadata.State))

	milestone := "No milestone"
	if metadata.Milestone != "" {
		milestone = metadata.Milestone
	}
	sb.WriteString(fmt.Sprintf("Milestone: \t%s\n", milestone))

	labels := "None yet"
	if len(metadata.Labels) > 0 {
		labels = strings.Join(metadata.Labels, ", ")
	}
	sb.WriteString(fmt.Sprintf("Labels: \t%s\n", labels))
	sb.WriteString("Projects: \tNone yet\n")
	sb.WriteString(fmt.Sprintf("Draft: \t%t\n", metadata.Draft))

	assignees := "No one -- Assign yourself"
	if len(metadata.Assignees) > 0 {
		assignees = strings.Join(metadata.Assignees, ", ")
	}
	sb.WriteString(fmt.Sprintf("Assignees: \t%s\n", assignees))
	sb.WriteString("Suggested-Reviewers: No suggestions\n")

	reviewersStr := ""
	if len(metadata.Reviewers) > 0 {
		for _, r := range metadata.Reviewers {
			if reviewersStr != "" {
				reviewersStr += ", "
			}
			reviewersStr += "@" + r
		}
	}
	if len(metadata.RequestedTeams) > 0 {
		for _, t := range metadata.RequestedTeams {
			if reviewersStr != "" {
				reviewersStr += ", "
			}
			reviewersStr += "team:" + t
		}
	}
	sb.WriteString(fmt.Sprintf("Reviewers: \t%s\n", reviewersStr))

	if len(metadata.ApprovedBy) > 0 {
		sb.WriteString(fmt.Sprintf("Approved-By: \t%s\n", strings.Join(metadata.ApprovedBy, ", ")))
	}
	if len(metadata.ChangesRequestedBy) > 0 {
		sb.WriteString(fmt.Sprintf("Changes-Requested-By: \t%s\n", strings.Join(metadata.ChangesRequestedBy, ", ")))
	}
	if len(metadata.CommentedBy) > 0 {
		sb.WriteString(fmt.Sprintf("Commented-By: \t%s\n", strings.Join(metadata.CommentedBy, ", ")))
	}

	// CI Status
	if metadata.CIStatus != "" {
		sb.WriteString(fmt.Sprintf("CI Status: \t%s\n", metadata.CIStatus))
		for _, failure := range metadata.CIFailures {
			sb.WriteString(fmt.Sprintf("  - %s\n", failure))
		}
	}
	sb.WriteString("\n")

	// Commits
	sb.WriteString(fmt.Sprintf("Commits (%d)\n", len(commits)))
	for _, c := range commits {
		sha := c.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		msg := c.Message
		if idx := strings.Index(msg, "\n"); idx != -1 {
			msg = msg[:idx]
		}
		sb.WriteString(fmt.Sprintf("%s %s\n", sha, msg))
	}
	sb.WriteString("\n")

	// Description
	sb.WriteString("Description\n\n")
	body := metadata.Body
	if body == "" {
		sb.WriteString("No description provided.\n")
	} else {
		sb.WriteString(escapeBodyString(body) + "\n")
	}
	sb.WriteString("\n")

	// Your Review Feedback
	sb.WriteString("Your Review Feedback\nLeave a comment here.\n\n")

	// Conversation
	sb.WriteString("Conversation\n")

	// Combine Issue Comments and Reviews for Conversation (using fetched data)
	type conversationItem struct {
		Time   time.Time
		Author string
		Body   string
		Type   string // "Comment" or "Review"
		State  string // For reviews
	}
	var convItems []conversationItem

	// Add comments (we need to filter for top-level conversation comments, usually those with empty path?)
	// Actually, GetFullPRResponse previously fetched IssueComments separately from PR Comments.
	// But ListComments (PR) + ListComments (Issue) covers everything.
	// In GetPRDetails, we did:
	// 		githubComments, _, _ = client.PullRequests.ListComments...
	// 		issueComments, _, _ = client.Issues.ListComments...
	// AND combined them.
	// So `details.Comments` contains ALL comments (both review comments and general issue comments).

	// Wait, `GetPRDetails` implementation I saw earlier:
	// It calls `client.PullRequests.ListComments` AND `client.Issues.ListComments`.
	// Then it appends them to `githubComments`.
	// Then it converts to `comments`.

	// So `details.Comments` has everything.
	// However, `GetFullPRResponse` previously treated "Conversation" as Issue Comments + Reviews.
	// And "Files changed" had inline comments.

	// We need to differentiate standard comments from review comments.
	// Standard comments (Issue Comments) usually have `Position` nil and `Path` nil?
	// `CommentJSON` has `Path` string.

	for _, c := range comments {
		// If it has a path, it's likely a code comment, so skip for "Conversation" section unless we want all?
		// Previous implementation: `issueComments, _, _ := client.Issues.ListComments`
		// These are specifically "general" comments.
		// `PullRequests.ListComments` returns comments on code.
		// `Issues.ListComments` returns comments on the PR itself (conversation).

		// In `GetPRDetails`, we merge them.
		// How to distinguish?
		// Issue comments usually have empty Path.
		if c.Path != "" {
			continue
		}

		convItems = append(convItems, conversationItem{
			Time:   c.CreatedAt,
			Author: c.Author,
			Body:   c.Body,
			Type:   "Comment",
		})
	}

	// Use outdated comments too if they are conversation comments?
	// Usually conversation comments don't become outdated in the same way (no line change).

	for _, r := range reviews {
		// Skip empty commented reviews as they are usually just pending or noise
		if r.State == "COMMENTED" && r.Body == "" {
			continue
		}

		convItems = append(convItems, conversationItem{
			Time:   r.SubmittedAt,
			Author: r.User,
			Body:   r.Body,
			Type:   "Review",
			State:  r.State,
		})
	}

	// Sort by time
	sort.Slice(convItems, func(i, j int) bool {
		return convItems[i].Time.Before(convItems[j].Time)
	})

	if len(convItems) == 0 {
		sb.WriteString("No conversation found.\n")
	} else {
		for i, item := range convItems {
			if i > 0 {
				sb.WriteString("--------------------------------------------------------------------------------\n")
			}
			dateStr := item.Time.Format(time.DateTime)

			header := fmt.Sprintf("From: %s at %s", item.Author, dateStr)
			if item.Type == "Review" {
				header += fmt.Sprintf(" [%s]", item.State)
			}
			sb.WriteString(header + "\n")

			if item.Body != "" {
				sb.WriteString(escapeBodyString(item.Body))
				sb.WriteString("\n\n")
			} else {
				sb.WriteString("(No body)\n\n")
			}
		}
	}
	sb.WriteString("\n")

	// Files Changed Header
	// We need file count. PRDetails.Metadata doesn't strictly have file counts?
	// PRMetadata struct has: Number, Title, etc. but not changed files count.
	// Wait, we used `pr.GetChangedFiles()` before.
	// We might need to add this to PRMetadata if we want to avoid fetching the PR object again.
	// But `PRMetadata` is what we cache.

	// Let's look at `PRMetadata` definition again.
	// It does NOT have ChangedFiles/Additions/Deletions.
	// So we might lose that info if we don't add it.

	// However, we have the Diff string. We can parse it to count files?
	// `utils.Parse(diff)` returns `*utils.Diff`. `ParsedDiff.Files`.
	parsedDiff, _ := utils.Parse(diff)
	fileCount := 0
	if parsedDiff != nil {
		fileCount = len(parsedDiff.Files)
	}
	// Additions/Deletions are harder to get exactly from just the diff string without parsing hunks
	// but strictly speaking we just display them.
	// If we accept losing the exact + - count for now, or calculate it from diff.

	sb.WriteString(fmt.Sprintf("Files changed (%d files)\n\n", fileCount))

	// Get diff with inline comments
	// We have the diff string and the comments.
	// We can reuse `processPRDiffWithComments` logic but we need to pass our comments.
	// Actually `processPRDiffWithComments` fetches comments if they are not passed?
	// No, `processPRDiffWithComments` does the fetching.

	// We effectively want to run `processPRDiffWithComments` but utilizing the data we already have.
	// But `processPRDiffWithComments` is designed to fetch.

	// Let's see: `processPRDiffWithComments(client, owner, repo, number, diff, parsedDiff, skipCache, latestSha)`
	// It checks DB or fetches.

	// Since we already HAVE the comments in `details.Comments`, we should use them.
	// But `processPRDiffWithComments` doesn't take a comments argument.

	// We can inline the logic of `processPRDiffWithComments` or refactor it.
	// Refactoring `processPRDiffWithComments` to accept comments would be best but it's used elsewhere?
	// It's used in `GetPRDiffWithInlineComments`.

	// I will duplicate the relevant logic here to ensure we use our `details.Comments`.
	// The logic is:
	// 1. Convert our `details.Comments` (which are `CommentJSON`) back to `PRComment` interface?
	//    `CommentJSON` is a struct, `PRComment` is an interface.
	//    We can make a wrapper or just use `CommentJSON` if we adapt the tree building.

	// Actually `CommentJSON` is a valid struct. Does it implement `PRComment`?
	// `PRComment` interface: GetLogin, GetBody, GetID...
	// `CommentJSON` fields: Author, Body, ID...
	// The method names don't match (GetLogin vs Author field).

	// We can create a quick adapter for `CommentJSON` to `PRComment`.

	prComments := make([]PRComment, 0, len(comments)+len(outdatedComments))
	for _, c := range comments {
		prComments = append(prComments, &JSONPRComment{c})
	}
	for _, c := range outdatedComments {
		prComments = append(prComments, &JSONPRComment{c})
	}

	// Note: We need the `JSONPRComment` adapter struct defined.

	// Build comment trees
	allCommentTrees := buildCommentTreesFromList(prComments)
	commentsByFileAndLine := make(map[string][][]PRComment)

	for _, tree := range allCommentTrees {
		if len(tree) == 0 {
			continue
		}
		root := tree[0]
		// Skip if no path (general conversation)
		if root.GetPath() == "" {
			continue
		}

		key := root.GetPath() + ":"
		if root.GetPosition() != "" {
			key += root.GetPosition()
		}
		commentsByFileAndLine[key] = append(commentsByFileAndLine[key], tree)
	}

	// Format Diff with comments
	// `formatDiff` just prints the diff. We need to inject comments.
	// The original `processPRDiffWithComments` calls `formatDiff(parsedDiff)`
	// Wait, checking `processPRDiffWithComments` implementation in previous turns...
	// It calculated `commentsByFileAndLine` but then just returned `formatDiff(parsedDiff)`.
	// IT DID NOT ACTUALLY INSERT COMMENTS INTO THE DIFF in lines 1294-1306 of original file!
	// START LINE 1294:
	// 	result := formatDiff(parsedDiff)
	// 	// Insert any remaining comments...
	// 	// for key, trees := range commentsByFileAndLine { ... } (COMMENTED OUT)
	// 	return result, len(comments)

	// Ah, so does it NOT show inline comments?
	// `renderPullRequest` function earlier (line 305) appends comments at the end?
	// But `GetFullPRResponse` calls `GetPRDiffWithInlineComments`.

	// Let's look at `GetFullPRResponse` from line 1128:
	// `diffLines, _ := GetPRDiffWithInlineComments(owner, repo, number, skipCache, pr)`
	// And `GetPRDiffWithInlineComments` calls `processPRDiffWithComments`.
	// And `processPRDiffWithComments` (lines 1193-1307) returns... `formatDiff(parsedDiff)` !

	// It seems the current server implementation MIGHT NOT be actually interleaving comments?
	// Or I missed something in `formatDiff`?
	// `formatDiff` (line 1351) iterates files and hunks and just prints lines.

	// Wait, if the current implementation doesn't interleave comments, then I explicitly shouldn't duplicate that broken/missing feature or I should replicate "just diff".
	// But the user asked to optimize `GetFullPRResponse`...

	// There is a `renderPullRequest` function at line 305 that takes diff and comments.
	// But `GetFullPRResponse` doesn't call it.

	// Okay, assuming I just return the formatted diff strings as the original did.
	// The original returns `diffLines` which comes from `GetPRDiffWithInlineComments`.
	// which returns result of `processPRDiffWithComments`
	// which returns result of `formatDiff`.

	// So yeah, sticking to returning `formatDiff(parsedDiff)` is correct behavior-preserving.
	// The comments are seemingly unused in the diff section currently ??
	// OR `formatDiff` does something with global state? No.

	// I will just format the diff.

	if parsedDiff != nil {
		headSHA, _, _ := config.C().DB.GetPullRequestSHAs(number, repo)
		sb.WriteString(formatDiff(parsedDiff, repo, number, headSHA))
	} else {
		sb.WriteString(diff) // Fallback if parse failed but we have raw string
	}

	return sb.String(), nil
}

// JSONPRComment adapter
type JSONPRComment struct {
	CommentJSON
}

func (c *JSONPRComment) GetLogin() string        { return c.Author }
func (c *JSONPRComment) GetBody() string         { return c.Body }
func (c *JSONPRComment) GetID() string           { return c.ID }
func (c *JSONPRComment) GetPosition() string     { return c.Position }
func (c *JSONPRComment) GetInReplyTo() int64     { return c.InReplyTo }
func (c *JSONPRComment) GetPath() string         { return c.Path }
func (c *JSONPRComment) GetCreatedAt() time.Time { return c.CreatedAt }
func (c *JSONPRComment) IsOutdated() bool        { return c.Outdated }
func (c *JSONPRComment) GetCommitID() string     { return "" } // Not in JSON currently
func (c *JSONPRComment) GetDiffHunk() string     { return c.DiffHunk }
func (c *JSONPRComment) GetReviewID() int64      { return c.ReviewID }
func (c *JSONPRComment) GetHTMLURL() string      { return c.HTMLURL }

func GetPRDiffWithInlineComments(owner string, repo string, number int, skipCache bool, pr *github.PullRequest) (string, int) {
	client := git_tools.GetGithubClient()

	// Check database first - skip API call if cached
	if !skipCache {
		cachedBody, cachedSha, err := config.C().DB.GetPullRequest(number, repo)
		if err != nil {
			slog.Error("Error checking database for PR", "pr", number, "repo", repo, "error", err)
			// Continue to fetch from API
		} else if cachedBody != "" {
			// Found in cache, parse and process it
			parsedDiff, err := utils.Parse(cachedBody)
			if err != nil {
				slog.Error("Error parsing cached diff", "error", err)
				// Continue to fetch from API
			} else {
				// Process cached diff with comments
				return processPRDiffWithComments(client, owner, repo, number, cachedBody, parsedDiff, skipCache, cachedSha)
			}
		}
	}

	// Not in cache or error occurred, fetch from API
	// Use the provided PR object if available to get the latest SHA
	latestSha := ""
	if pr != nil && pr.Head != nil && pr.Head.SHA != nil {
		latestSha = *pr.Head.SHA
	} else {
		// If no PR provided, fetch briefly
		p, _, err := client.PullRequests.Get(context.Background(), owner, repo, number)
		if err == nil && p.Head != nil && p.Head.SHA != nil {
			latestSha = *p.Head.SHA
		}
	}

	diff, _, err := client.PullRequests.GetRaw(context.Background(), owner, repo, number, github.RawOptions{Type: github.Diff})
	if err != nil {
		slog.Error("Error getting PR diff", "pr", number, "repo", repo, "error", err)
		return "", 0
	}
	parsedDiff, err := utils.Parse(diff)
	if err != nil {
		slog.Error("Error parsing PR diff", "pr", number, "repo", repo, "error", err)
		return "", 0
	}
	for _, diffFile := range parsedDiff.Files {
		slog.Debug("Parsed diff file", "file", diffFile.NewName, "hunks", len(diffFile.Hunks))
	}

	// Store the result in the database (with latest_sha for future feature)
	return processPRDiffWithComments(client, owner, repo, number, diff, parsedDiff, skipCache, latestSha)
}

func processPRDiffWithComments(client *github.Client, owner string, repo string, number int, diff string, parsedDiff *utils.Diff, skipCache bool, latestSha string) (string, int) {
	var githubComments []*github.PullRequestComment
	var comments []PRComment

	// Check database first - skip API call if cached
	if !skipCache {
		cachedCommentsJSON, err := config.C().DB.GetPRComments(number, repo)
		if err != nil {
			slog.Error("Error checking database for PR comments", "pr", number, "repo", repo, "error", err)
			// Continue to fetch from API
		} else if cachedCommentsJSON != "" {
			// Found in cache, unmarshal and use it
			if err := json.Unmarshal([]byte(cachedCommentsJSON), &githubComments); err != nil {
				slog.Error("Error unmarshaling cached comments", "error", err)
				// Continue to fetch from API
			} else {
				// Convert to PRComment interface
				comments = convertToPRComments(githubComments)
				comments = filterComments(comments)
				// Continue with processing cached comments
			}
		}
	}

	// Not in cache or error occurred, fetch from API
	if comments == nil {
		var apiErr error
		githubComments, apiErr = git_tools.ListAllPRComments(context.Background(), client, owner, repo, number)
		if apiErr != nil {
			slog.Error("Error getting Comments", "pr", number, "repo", repo, "error", apiErr)
			return diff, 0
		}

		// Store the result in the database
		commentsJSON, err := json.Marshal(githubComments)
		if err != nil {
			slog.Error("Error marshaling comments for storage", "pr", number, "repo", repo, "error", err)
		} else {
			if err := config.C().DB.UpsertPRComments(number, repo, string(commentsJSON)); err != nil {
				slog.Error("Error storing PR comments in database", "pr", number, "repo", repo, "error", err)
				// Continue even if storage fails
			}
		}

		// Convert to PRComment interface
		comments = convertToPRComments(githubComments)
		comments = filterComments(comments)
	}

	// Fetch LocalComments from database for this specific PR and add them to the comments list
	localComments, err := config.C().DB.GetLocalCommentsForPR(owner, repo, number)
	if err != nil {
		slog.Error("Error fetching local comments", "error", err)
		// Continue without local comments
	} else {
		localPRComments := convertLocalCommentsToPRComments(localComments)
		comments = append(comments, localPRComments...)
	}

	// Build comment trees first to group replies with their parents
	allCommentTrees := buildCommentTreesFromList(comments)

	// Build a map of comments by file path and line number
	// Key: "filepath:line" or "filepath:" for general comments
	// Value: slice of comment trees (each tree is a root comment with its replies)
	commentsByFileAndLine := make(map[string][][]PRComment)

	for _, tree := range allCommentTrees {
		for _, comment := range tree {
			slog.Debug("Processing PR comment", "file", comment.GetPath(), "in_reply_to", comment.GetInReplyTo())
		}
		if len(tree) == 0 {
			continue
		}
		rootComment := tree[0]

		// Use the root comment's position for the entire tree
		filePath := rootComment.GetPath()
		if filePath != "" {
			var key string
			position := rootComment.GetPosition()

			if position != "" {
				// Comment on a specific line
				key = fmt.Sprintf("%s:%s", filePath, position)
			} else {
				// General comment on the file (no specific line)
				key = filePath + ":"
			}

			slog.Debug("Adding comment tree", "key", key, "outdated", rootComment.IsOutdated())
			commentsByFileAndLine[key] = append(commentsByFileAndLine[key], tree)
		}
	}
	result := formatDiff(parsedDiff, repo, number, latestSha)
	// Insert any remaining comments (general file comments or comments we couldn't match)
	// for key, trees := range commentsByFileAndLine {
	//	parts := strings.Split(key, ":")
	//	if len(parts) >= 1 {
	//		filePath := parts[0]
	//		for _, tree := range trees {
	//			insertCommentTree(&result, tree, filePath)
	//		}
	//	}
	// }

	return result, len(comments)
}

func buildCommentTree(tree []PRComment, filePath string, forceOutdated bool) string {
	var result []string // leftover from refactor
	if len(tree) == 0 {
		return ""
	}

	rootComment := tree[0]
	commentIDInt, _ := strconv.ParseInt(rootComment.GetID(), 10, 64)
	header := "    ┌─ REVIEW COMMENT ─────────────────"
	if forceOutdated || rootComment.IsOutdated() {
		header = "    ┌─ REVIEW COMMENT [OUTDATED] ──────"
	} else if rootComment.GetPosition() == "" {
		header = "    ┌─ FILE COMMENT ───────────────────"
	}
	result = append(result, header)
	result = append(result, fmt.Sprintf("    │ File: %s", filePath))
	result = append(result, fmt.Sprintf("    │ %s : %d", rootComment.GetCreatedAt().Format(time.DateTime)+" "+treeAuthorsFromList(tree), commentIDInt))
	result = append(result, "    │")

	for idx, comment := range tree {
		cleanBody := escapeBodyString(comment.GetBody())
		commentLines := strings.Split(cleanBody, "\n")

		if idx == 0 {
			result = append(result, fmt.Sprintf("    │ [%s]:", comment.GetLogin()))
		} else {
			result = append(result, "    │")
			replyIDInt, _ := strconv.ParseInt(comment.GetID(), 10, 64)
			result = append(result, fmt.Sprintf("    │ Reply by [%s]:[%d]", comment.GetLogin(), replyIDInt))
		}

		for _, bodyLine := range commentLines {
			result = append(result, fmt.Sprintf("    │   %s", bodyLine))
		}
	}

	result = append(result, "    └──────────────────────────────────")
	result = append(result, "")

	return strings.Join(result, "\n")
}

// isTestFile reports whether a diff file looks like a test file, based on a
// simple case-insensitive substring match of "test" against either path.
func isTestFile(file *utils.DiffFile) bool {
	return strings.Contains(strings.ToLower(file.NewName), "test") ||
		strings.Contains(strings.ToLower(file.OrigName), "test")
}

// sortFilesTestsLast returns a copy of files with test files ordered after
// non-test files, preserving the original relative order within each group.
func sortFilesTestsLast(files []*utils.DiffFile) []*utils.DiffFile {
	sorted := make([]*utils.DiffFile, len(files))
	copy(sorted, files)
	sort.SliceStable(sorted, func(i, j int) bool {
		return !isTestFile(sorted[i]) && isTestFile(sorted[j])
	})
	return sorted
}

func formatDiff(diff *utils.Diff, repo string, prNumber int, sha string) string {
	var builder strings.Builder
	for _, file := range orderDiffFiles(diff.Files, repo, prNumber, sha) {
		builder.WriteString(file.DiffHeader + "\n")

		// The diff parser's lookahead misses ---/+++ lines for new/deleted files
		// (because "new file mode" / "deleted file mode" displaces the index line).
		// Emit them explicitly so the frontend can identify the filename.
		switch file.Mode {
		case utils.NEW:
			builder.WriteString("--- /dev/null\n")
			builder.WriteString("+++ b/" + file.NewName + "\n")
		case utils.DELETED:
			builder.WriteString("--- a/" + file.OrigName + "\n")
			builder.WriteString("+++ /dev/null\n")
		case utils.MODIFIED:
			// Detect renames: OrigName and NewName differ (rename with content changes).
			if file.OrigName != "" && file.NewName != "" && file.OrigName != file.NewName {
				builder.WriteString("rename from " + file.OrigName + "\n")
				builder.WriteString("rename to " + file.NewName + "\n")
				// DiffHeader for renames doesn't include ---/+++ lines; add them.
				if !strings.Contains(file.DiffHeader, "\n--- ") {
					builder.WriteString("--- a/" + file.OrigName + "\n")
					builder.WriteString("+++ b/" + file.NewName + "\n")
				}
			} else if file.OrigName == "" && file.NewName == "" {
				// Rename-only (no content changes): no ---/+++ parsed, detect from DiffHeader.
				firstLine := strings.SplitN(file.DiffHeader, "\n", 2)[0]
				oldName, newName := extractGitDiffNames(firstLine)
				if oldName != "" && newName != "" && oldName != newName {
					builder.WriteString("rename from " + oldName + "\n")
					builder.WriteString("rename to " + newName + "\n")
					builder.WriteString("--- a/" + oldName + "\n")
					builder.WriteString("+++ b/" + newName + "\n")
				}
			}
		}

		for _, hunk := range file.Hunks {
			builder.WriteString("\n")
			builder.WriteString(hunk.RangeHeader() + "\n")
			for _, line := range hunk.WholeRange.Lines {
				builder.WriteString(line.Render())
			}
		}
	}
	return builder.String()
}

// extractGitDiffNames parses the old and new filenames from a "diff --git a/X b/Y" line.
func extractGitDiffNames(line string) (oldName, newName string) {
	const prefix = "diff --git a/"
	if !strings.HasPrefix(line, prefix) {
		return "", ""
	}
	rest := strings.TrimPrefix(line, prefix)
	idx := strings.Index(rest, " b/")
	if idx < 0 {
		return "", ""
	}
	return rest[:idx], rest[idx+3:]
}

func buildCommentTreesFromList(comments []PRComment) [][]PRComment {
	commentMap := make(map[string]PRComment)
	for _, c := range comments {
		commentMap[c.GetID()] = c
	}

	output := [][]PRComment{}
	processed := make(map[string]bool)

	for _, comment := range comments {
		commentID := comment.GetID()
		if processed[commentID] {
			continue
		}

		// If this is a root comment (no reply-to)
		if comment.GetInReplyTo() == 0 {
			tree := []PRComment{comment}
			processed[commentID] = true

			// Find all replies to this comment
			for _, potentialReply := range comments {
				replyID := potentialReply.GetID()
				if !processed[replyID] {
					if potentialReply.GetInReplyTo() != 0 {
						// Convert reply-to ID to string for comparison
						replyToIDStr := strconv.FormatInt(potentialReply.GetInReplyTo(), 10)
						if replyToIDStr == commentID {
							tree = append(tree, potentialReply)
							processed[replyID] = true
						}
					}
				}
			}

			output = append(output, tree)
		}
	}

	// Handle orphaned comments (replies without parents in this list)
	for _, comment := range comments {
		commentID := comment.GetID()
		if !processed[commentID] {
			output = append(output, []PRComment{comment})
			processed[commentID] = true
		}
	}

	return output
}

func treeAuthorsFromList(tree []PRComment) string {
	authors := []string{}
	seen := make(map[string]bool)
	for _, comment := range tree {
		login := comment.GetLogin()
		if _, ok := seen[login]; !ok {
			authors = append(authors, login)
			seen[login] = true
		}
	}
	return strings.Join(authors, "|")
}

func escapeBody(body *string) string {
	// Body comes in a single string with newlines and can have things that break orgmode like *
	if body == nil {
		// pretty sure the library uses json:omitempty?
		return ""
	}

	lines := strings.Split(*body, "\n")
	if len(lines) == 0 {
		return ""
	}
	return cleanLines(&lines)
}

func escapeBodyString(body string) string {
	// Body comes in a single string with newlines and can have things that break orgmode like *
	if body == "" {
		return ""
	}

	lines := strings.Split(body, "\n")
	if len(lines) == 0 {
		return ""
	}
	return cleanLines(&lines)
}

func cleanLines(lines *[]string) string {
	flat_lines := []string{}
	for _, line := range *lines {
		if strings.Contains(line, "\n") {
			split_lines := strings.Split(line, "\n")
			flat_lines = append(flat_lines, split_lines...)
		} else {
			flat_lines = append(flat_lines, line)
		}
	}

	shorted_lines := cleanEmptyEndingLines(&flat_lines)
	output_lines := make([]string, len(shorted_lines))
	for i, line := range shorted_lines {
		if strings.HasPrefix(line, "*") {
			output_lines[i] = strings.Replace(line, "*", "-", 1)
		} else {
			output_lines[i] = line
		}
	}

	return strings.Join(output_lines, "\n")
}

func cleanEmptyEndingLines(lines *[]string) []string {
	// Removes the empty lines at the end of the details so org collapses prettier
	i := len(*lines) - 1
	for i >= 0 && strings.TrimSpace((*lines)[i]) == "" {
		i--
	}
	return (*lines)[:i+1]
}

// threadInfo is the per-comment view of GitHub's review-thread state, keyed by
// comment ID. Built from the GraphQL reviewThreads connection.
type threadInfo struct {
	ThreadID   string
	Resolved   bool
	ResolvedBy string
	Outdated   bool
}

// indexReviewThreads flattens review threads into a comment-ID keyed lookup.
// Passing nil threads yields an empty map, which leaves splitComments behaving
// exactly as it did before resolution state existed.
func indexReviewThreads(threads []git_tools.ReviewThread) map[string]threadInfo {
	byComment := make(map[string]threadInfo)
	for _, t := range threads {
		info := threadInfo{
			ThreadID:   t.ID,
			Resolved:   t.IsResolved,
			ResolvedBy: t.ResolvedBy,
			Outdated:   t.IsOutdated,
		}
		for _, id := range t.CommentIDs {
			byComment[strconv.FormatInt(id, 10)] = info
		}
	}
	return byComment
}

func splitComments(comments []PRComment, threads []git_tools.ReviewThread) ([]CommentJSON, []CommentJSON) {
	threadByComment := indexReviewThreads(threads)

	// Map to track if a comment (by ID) is outdated, including inherited status from parents
	isIDOutdated := make(map[string]bool)

	// First pass: identify explicitly outdated roots and populate initial map
	for _, c := range comments {
		if c.GetInReplyTo() == 0 {
			isIDOutdated[c.GetID()] = c.IsOutdated()
		}
	}

	// Second pass: propagate status to replies.
	// Since comments are sorted by CreatedAt, parents should come before replies.
	for _, c := range comments {
		if c.GetInReplyTo() != 0 {
			parentID := strconv.FormatInt(c.GetInReplyTo(), 10)
			if isIDOutdated[parentID] {
				isIDOutdated[c.GetID()] = true
			} else {
				isIDOutdated[c.GetID()] = c.IsOutdated()
			}
		}
	}

	// Build a map of comment ID -> position/path for parent lookup
	commentPositionByID := make(map[string]string)
	commentPathByID := make(map[string]string)
	for _, c := range comments {
		commentPositionByID[c.GetID()] = c.GetPosition()
		commentPathByID[c.GetID()] = c.GetPath()
	}

	active := []CommentJSON{}
	outdated := []CommentJSON{}
	for _, c := range comments {
		isOutdated := isIDOutdated[c.GetID()]
		position := c.GetPosition()
		path := c.GetPath()

		// Reply comments (especially local ones) may have position "0" or empty path.
		// Inherit the parent's position/path so the client can group them correctly.
		if c.GetInReplyTo() != 0 {
			parentID := strconv.FormatInt(c.GetInReplyTo(), 10)
			if position == "" || position == "0" {
				if parentPos, ok := commentPositionByID[parentID]; ok && parentPos != "" && parentPos != "0" {
					position = parentPos
				}
			}
			if path == "" {
				if parentPath, ok := commentPathByID[parentID]; ok {
					path = parentPath
				}
			}
		}

		// When GitHub told us about this comment's thread, its own outdated
		// judgement is authoritative: it accounts for the thread as a whole,
		// while the REST-derived flag above only looks at a single comment's
		// position mapping.
		info, hasThread := threadByComment[c.GetID()]
		if hasThread {
			isOutdated = info.Outdated
		}

		item := CommentJSON{
			ID:         c.GetID(),
			Author:     c.GetLogin(),
			Body:       c.GetBody(),
			Path:       path,
			Position:   position,
			InReplyTo:  c.GetInReplyTo(),
			CreatedAt:  c.GetCreatedAt(),
			Outdated:   isOutdated,
			DiffHunk:   c.GetDiffHunk(),
			ReviewID:   c.GetReviewID(),
			HTMLURL:    c.GetHTMLURL(),
			ThreadID:   info.ThreadID,
			Resolved:   info.Resolved,
			ResolvedBy: info.ResolvedBy,
		}
		if isOutdated {
			outdated = append(outdated, item)
		} else {
			active = append(active, item)
		}
	}
	return active, outdated
}

func filterComments(comments []PRComment) []PRComment {
	output := []PRComment{}
	for _, comment := range comments {
		if strings.Contains(comment.GetLogin(), "advanced") {
			// I don't care about the lint warning stuff
			continue
		}
		output = append(output, comment)
	}
	return output
}

func GetRequestedReviewers(owner, repo string, number int, skipCache bool) (*github.Reviewers, error) {
	client := git_tools.GetGithubClient()

	if !skipCache {
		cachedReviewersJSON, err := config.C().DB.GetRequestedReviewers(number, repo)
		if err != nil {
			slog.Error("Error checking database for requested reviewers", "pr", number, "repo", repo, "error", err)
		} else if cachedReviewersJSON != "" {
			var reviewers *github.Reviewers
			if err := json.Unmarshal([]byte(cachedReviewersJSON), &reviewers); err != nil {
				slog.Error("Error unmarshaling cached reviewers", "error", err)
			} else {
				return reviewers, nil
			}
		}
	}

	reviewers, err := git_tools.ListAllRequestedReviewers(context.Background(), client, owner, repo, number)
	if err != nil {
		return nil, err
	}

	reviewersJSON, err := json.Marshal(reviewers)
	if err != nil {
		slog.Error("Error marshaling reviewers for storage", "error", err)
	} else {
		if err := config.C().DB.UpsertRequestedReviewers(number, repo, string(reviewersJSON)); err != nil {
			slog.Error("Error storing requested reviewers in database", "error", err)
		}
	}

	return reviewers, nil
}

func GetPRReviews(owner, repo string, number int, skipCache bool) ([]ReviewJSON, error) {
	if !skipCache {
		cachedReviewsJSON, err := config.C().DB.GetPRReviews(number, repo)
		if err != nil {
			slog.Error("Error checking database for PR reviews", "pr", number, "repo", repo, "error", err)
		} else if cachedReviewsJSON != "" {
			var reviews []ReviewJSON
			if err := json.Unmarshal([]byte(cachedReviewsJSON), &reviews); err != nil {
				slog.Error("Error unmarshaling cached reviews", "error", err)
			} else {
				if reviews == nil {
					// Rows written before reviews were cached as "[]" hold "null".
					reviews = []ReviewJSON{}
				}
				return reviews, nil
			}
		}
	}

	client := git_tools.GetGithubClient()
	ghReviews, err := git_tools.ListAllPRReviews(context.Background(), client, owner, repo, number)
	if err != nil {
		return nil, err
	}

	// Non-nil so a PR with no reviews serializes as "[]" instead of "null", and
	// so callers can tell "loaded, none found" from "not loaded".
	reviews := []ReviewJSON{}
	for _, r := range ghReviews {
		var submittedAt time.Time
		if r.SubmittedAt != nil {
			submittedAt = r.SubmittedAt.Time
		}
		reviews = append(reviews, ReviewJSON{
			ID:          r.GetID(),
			User:        r.User.GetLogin(),
			Body:        r.GetBody(),
			State:       r.GetState(),
			SubmittedAt: submittedAt,
			HTMLURL:     r.GetHTMLURL(),
		})
	}

	reviewsJSON, err := json.Marshal(reviews)
	if err != nil {
		slog.Error("Error marshaling reviews for storage", "error", err)
	} else {
		if err := config.C().DB.UpsertPRReviews(number, repo, string(reviewsJSON)); err != nil {
			slog.Error("Error storing reviews in database", "error", err)
		}
	}

	return reviews, nil
}

// GetPRReviewThreads returns the PR's review-thread resolution state, reading
// the DB cache first. A GraphQL failure is logged and reported as an empty set
// rather than an error so the PR still renders without resolution badges.
func GetPRReviewThreads(owner, repo string, number int, skipCache bool) []git_tools.ReviewThread {
	if !skipCache {
		cached, err := config.C().DB.GetPRReviewThreads(number, repo)
		if err != nil {
			slog.Error("Error checking database for PR review threads", "pr", number, "repo", repo, "error", err)
		} else if cached != "" {
			var threads []git_tools.ReviewThread
			if err := json.Unmarshal([]byte(cached), &threads); err != nil {
				slog.Error("Error unmarshaling cached review threads", "pr", number, "repo", repo, "error", err)
			} else {
				return threads
			}
		}
	}

	threads, err := git_tools.GetReviewThreads(owner, repo, number)
	if err != nil {
		slog.Error("Error fetching review threads", "pr", number, "repo", repo, "error", err)
		return nil
	}

	if threadsJSON, err := json.Marshal(threads); err != nil {
		slog.Error("Error marshaling review threads for storage", "error", err)
	} else if err := config.C().DB.UpsertPRReviewThreads(number, repo, string(threadsJSON)); err != nil {
		slog.Error("Error storing review threads in database", "pr", number, "repo", repo, "error", err)
	}

	return threads
}

type CombinedPRStatus struct {
	Status    *github.CombinedStatus       `json:"status"`
	CheckRuns *github.ListCheckRunsResults `json:"check_runs"`
}

func GetLatestCIStatus(owner, repo string, prNumber int, sha string, skipCache bool) (*CombinedPRStatus, error) {
	client := git_tools.GetGithubClient()

	if !skipCache {
		cachedStatusJSON, err := config.C().DB.GetCIStatus(prNumber, repo, sha)
		if err != nil {
			slog.Error("Error checking database for CI status", "pr", prNumber, "repo", repo, "sha", sha, "error", err)
		} else if cachedStatusJSON != "" {
			var combined CombinedPRStatus
			if err := json.Unmarshal([]byte(cachedStatusJSON), &combined); err != nil {
				// Fallback for old cache format
				var status github.CombinedStatus
				if err := json.Unmarshal([]byte(cachedStatusJSON), &status); err == nil {
					combined.Status = &status
					return &combined, nil
				}
				slog.Error("Error unmarshaling cached CI status", "error", err)
			} else {
				return &combined, nil
			}
		}
	}

	status, err := git_tools.GetCombinedStatus(client, owner, repo, sha)
	if err != nil {
		slog.Error("Error fetching combined status", "error", err)
	}

	checkRuns, err := git_tools.GetCheckRuns(client, owner, repo, sha)
	if err != nil {
		slog.Error("Error fetching check runs", "error", err)
	}

	combined := &CombinedPRStatus{
		Status:    status,
		CheckRuns: checkRuns,
	}

	statusJSON, err := json.Marshal(combined)
	if err != nil {
		slog.Error("Error marshaling CI status for storage", "error", err)
	} else {
		if err := config.C().DB.UpsertCIStatus(prNumber, repo, sha, string(statusJSON)); err != nil {
			slog.Error("Error storing CI status in database", "error", err)
		}
	}

	return combined, nil
}
