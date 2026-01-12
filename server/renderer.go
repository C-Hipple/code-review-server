package server

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"crs/org"
	"crs/utils"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v48/github"
)

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

func (r *OrgRenderer) RenderAllSectionsToString() (string, error) {
	sections, err := r.db.GetAllSections()
	if err != nil {
		return "", err
	}

	// Sort sections by ID to maintain order
	sort.Slice(sections, func(i, j int) bool {
		return sections[i].ID < sections[j].ID
	})

	// Build the org file content
	var content strings.Builder

	for _, section := range sections {
		// Get items for this section
		items, err := r.db.GetItemsBySection(section.ID)
		if err != nil {
			return "", err
		}

		// Build section header
		sectionHeader := r.buildSectionHeader(section, items)
		content.WriteString(sectionHeader)
		content.WriteString("\n")

		// Build items
		for _, item := range items {
			itemLines := r.buildItemLines(item, section.IndentLevel)
			for _, line := range itemLines {
				content.WriteString(line)
				if !strings.HasSuffix(line, "\n") {
					content.WriteString("\n")
				}
			}
		}
		// Add blank line between sections
		content.WriteString("\n")
	}

	return content.String(), nil
}

// ReviewItem represents a single PR review item with structured metadata
type ReviewItem struct {
	Section string `json:"section"`
	Status  string `json:"status"`
	Title   string `json:"title"`
	Owner   string `json:"owner"`
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	Author  string `json:"author"`
	URL     string `json:"url"`
}

// GetAllReviewItems returns structured review items from all sections
func (r *OrgRenderer) GetAllReviewItems() ([]ReviewItem, error) {
	sections, err := r.db.GetAllSections()
	if err != nil {
		return nil, err
	}

	var reviewItems []ReviewItem

	for _, section := range sections {
		items, err := r.db.GetItemsBySection(section.ID)
		if err != nil {
			return nil, err
		}

		for _, item := range items {
			reviewItem := r.parseItemToReviewItem(item, section.SectionName)
			reviewItems = append(reviewItems, reviewItem)
		}
	}

	return reviewItems, nil
}

// parseItemToReviewItem extracts structured metadata from an item's details
func (r *OrgRenderer) parseItemToReviewItem(item *database.Item, sectionName string) ReviewItem {
	details, err := item.GetDetails()
	if err != nil {
		details = []string{}
	}

	reviewItem := ReviewItem{
		Section: sectionName,
		Status:  item.Status,
		Title:   item.Title,
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

	indentStars := strings.Repeat("*", section.IndentLevel-1)
	ratio := fmt.Sprintf("[%d/%d]", len(items), doneCount)

	return fmt.Sprintf("%s %s %s %s", indentStars, status, section.SectionName, ratio)
}

func (r *OrgRenderer) buildItemLines(item *database.Item, indentLevel int) []string {
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

	// Build the title line
	indentStars := strings.Repeat("*", indentLevel)
	titleLine := fmt.Sprintf("%s %s %s", indentStars, item.Status, item.Title)

	// Add tags
	if len(tags) > 0 {
		tagStr := ":" + strings.Join(tags, ":") + ":"
		titleLine += "\t\t" + tagStr
	}

	// Add archived tag if needed
	if item.Archived {
		if !strings.Contains(titleLine, ":") {
			titleLine += "\t\t:ARCHIVE:"
		} else {
			titleLine += ":ARCHIVE:"
		}
	}

	lines := []string{titleLine + "\n"}
	lines = append(lines, details...)

	return lines
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
}

type ReviewJSON struct {
	ID          int64     `json:"id"`
	User        string    `json:"user"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
	HTMLURL     string    `json:"html_url"`
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
	Reviewers          []string `json:"reviewers"`           // Requested individual reviewers
	RequestedTeams     []string `json:"requested_teams"`     // Requested team reviewers
	ApprovedBy         []string `json:"approved_by"`         // Logins of users who approved
	ChangesRequestedBy []string `json:"changes_requested_by"` // Logins of users who requested changes
	CommentedBy        []string `json:"commented_by"`         // Logins of users who commented (non-approval/non-request)
	Draft              bool     `json:"draft"`
	CIStatus           string   `json:"ci_status"`
	CIFailures         []string `json:"ci_failures"`
	Description        string   `json:"description"`
	URL                string   `json:"url"`
}

type PRDetails struct {
	Metadata PRMetadata    `json:"metadata"`
	Diff     string        `json:"diff"`
	Comments []CommentJSON `json:"comments"`
	Reviews  []ReviewJSON  `json:"reviews"`
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
		return *c.CreatedAt
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

func GetPRDetails(owner string, repo string, number int, skipCache bool) (*PRDetails, error) {
	client := git_tools.GetGithubClient()
	ctx := context.Background()

	var metadata PRMetadata
	var headSHA string
	needsFreshFetch := skipCache

	// 1. Try to load metadata from cache first (unless skipCache)
	if !skipCache {
		cachedMetadataJSON, err := config.C.DB.GetPRMetadataCache(owner, repo, number)
		if err == nil && cachedMetadataJSON != "" {
			if err := json.Unmarshal([]byte(cachedMetadataJSON), &metadata); err == nil {
				slog.Debug("Using cached PR metadata", "pr", number, "repo", repo)
				// We have cached metadata, but we still need SHA for diff lookup
				// Get it from the PullRequests table
				_, sha, _ := config.C.DB.GetPullRequest(number, repo)
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
		pr, _, err := client.PullRequests.Get(ctx, owner, repo, number)
		if err != nil {
			// If we have cached data, return that instead of failing
			if metadata.Number != 0 {
				slog.Warn("GitHub API error, falling back to cached metadata", "error", err)
			} else {
				return nil, err
			}
		} else {
			// Extract SHA for CI status lookup
			if pr.Head != nil && pr.Head.SHA != nil {
				headSHA = *pr.Head.SHA
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

			// Fetch actual reviews to see who has approved/commented/etc.
			ghReviews, _, _ := client.PullRequests.ListReviews(ctx, owner, repo, number, nil)
			approvedBy := []string{}
			changesRequestedBy := []string{}
			commentedBy := []string{}

			// Map to keep track of the latest review state for each user
			latestReviewState := make(map[string]string)
			for _, review := range ghReviews {
				if review.User != nil && review.State != nil {
					latestReviewState[review.User.GetLogin()] = review.GetState()
				}
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

			// Capture Reviews for display
			var formattedReviews []ReviewJSON
			for _, r := range ghReviews {
				var submittedAt time.Time
				if r.SubmittedAt != nil {
					submittedAt = *r.SubmittedAt
				}
				formattedReviews = append(formattedReviews, ReviewJSON{
					ID:          r.GetID(),
					User:        r.User.GetLogin(),
					Body:        r.GetBody(),
					State:       r.GetState(),
					SubmittedAt: submittedAt,
					HTMLURL:     r.GetHTMLURL(),
				})
			}
			// Cache Reviews
			if reviewsJSON, err := json.Marshal(formattedReviews); err == nil {
				config.C.DB.UpsertPRReviews(number, repo, string(reviewsJSON))
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
				Description:        pr.GetBody(),
				URL:                pr.GetHTMLURL(),
			}
			if pr.Milestone != nil {
				metadata.Milestone = pr.Milestone.GetTitle()
			}

			// Cache the metadata
			metadataJSON, err := json.Marshal(metadata)
			if err == nil {
				config.C.DB.UpsertPRMetadataCache(owner, repo, number, string(metadataJSON))
			}
		}
	}

	// 3. Fetch Diff (with caching)
	var diff string
	if !skipCache {
		cachedDiff, _, err := config.C.DB.GetPullRequest(number, repo)
		if err == nil && cachedDiff != "" {
			diff = cachedDiff
		}
	}
	if diff == "" {
		d, _, err := client.PullRequests.GetRaw(ctx, owner, repo, number, github.RawOptions{Type: github.Diff})
		if err != nil {
			slog.Error("Error getting PR diff", "pr", number, "repo", repo, "error", err)
		} else {
			diff = d
			// Store in cache
			config.C.DB.UpsertPullRequest(number, repo, headSHA, diff)
		}
	}

	parsedDiff, _ := utils.Parse(diff)
	formattedDiff := diff
	if parsedDiff != nil {
		formattedDiff = formatDiff(parsedDiff)
	}

	// 4. Fetch Comments (GitHub + Local)
	var githubComments []*github.PullRequestComment
	if !skipCache {
		cachedCommentsJSON, err := config.C.DB.GetPRComments(number, repo)
		if err == nil && cachedCommentsJSON != "" {
			json.Unmarshal([]byte(cachedCommentsJSON), &githubComments)
		}
	}
	if githubComments == nil {
		opts := github.PullRequestListCommentsOptions{}
		githubComments, _, _ = client.PullRequests.ListComments(ctx, owner, repo, number, &opts)

		issueComments, _, _ := client.Issues.ListComments(ctx, owner, repo, number, nil)
		for _, ic := range issueComments {
			githubComments = append(githubComments, convertIssueCommentToPRComment(ic))
		}

		if githubComments != nil {
			// Sort comments by creation date to maintain order
			sort.Slice(githubComments, func(i, j int) bool {
				return githubComments[i].CreatedAt.Before(*githubComments[j].CreatedAt)
			})

			commentsJSON, _ := json.Marshal(githubComments)
			config.C.DB.UpsertPRComments(number, repo, string(commentsJSON))
		}
	}

	comments := convertToPRComments(githubComments)
	comments = filterComments(comments)

	localComments, _ := config.C.DB.GetLocalCommentsForPR(owner, repo, number)
	comments = append(comments, convertLocalCommentsToPRComments(localComments)...)

	commentJSONs := []CommentJSON{}
	for _, c := range comments {
		commentJSONs = append(commentJSONs, CommentJSON{
			ID:        c.GetID(),
			Author:    c.GetLogin(),
			Body:      c.GetBody(),
			Path:      c.GetPath(),
			Position:  c.GetPosition(),
			InReplyTo: c.GetInReplyTo(),
			CreatedAt: c.GetCreatedAt(),
			Outdated:  c.IsOutdated(),
		})
	}

	// 5. Load Reviews from DB
	var reviews []ReviewJSON
	cachedReviewsJSON, err := config.C.DB.GetPRReviews(number, repo)
	if err == nil && cachedReviewsJSON != "" {
		json.Unmarshal([]byte(cachedReviewsJSON), &reviews)
	}

	// If not in DB, fetch fresh
	if reviews == nil {
		ghReviews, _, _ := client.PullRequests.ListReviews(ctx, owner, repo, number, nil)
		var formattedReviews []ReviewJSON
		for _, r := range ghReviews {
			var submittedAt time.Time
			if r.SubmittedAt != nil {
				submittedAt = *r.SubmittedAt
			}
			formattedReviews = append(formattedReviews, ReviewJSON{
				ID:          r.GetID(),
				User:        r.User.GetLogin(),
				Body:        r.GetBody(),
				State:       r.GetState(),
				SubmittedAt: submittedAt,
				HTMLURL:     r.GetHTMLURL(),
			})
		}
		reviews = formattedReviews
		if reviewsJSON, err := json.Marshal(formattedReviews); err == nil {
			config.C.DB.UpsertPRReviews(number, repo, string(reviewsJSON))
		}
	}

	return &PRDetails{
		Metadata: metadata,
		Diff:     formattedDiff,
		Comments: commentJSONs,
		Reviews:  reviews,
	}, nil
}

func GetFullPRResponse(owner string, repo string, number int, skipCache bool) (string, error) {
	client := git_tools.GetGithubClient()
	ctx := context.Background()

	// Fetch PR details
	pr, _, err := client.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		slog.Error("Error fetching PR details", "error", err)
		return "", err
	}

	// Get requested reviewers
	reviewers, err := GetRequestedReviewers(owner, repo, number, skipCache)
	if err != nil {
		slog.Error("Error fetching requested reviewers", "error", err)
	}

	reviewersStr := ""
	if reviewers != nil {
		for _, reviewer := range reviewers.Users {
			if reviewersStr != "" {
				reviewersStr += ", "
			}
			reviewersStr += "@" + reviewer.GetLogin()
		}
		for _, team := range reviewers.Teams {
			if reviewersStr != "" {
				reviewersStr += ", "
			}
			reviewersStr += "team:" + team.GetName()
		}
	}

	// Fetch actual reviews to see status
	reviews, _, _ := client.PullRequests.ListReviews(ctx, owner, repo, number, nil)
	approvedBy := []string{}
	changesRequestedBy := []string{}
	commentedBy := []string{}

	latestReviewState := make(map[string]string)
	for _, review := range reviews {
		if review.User != nil && review.State != nil {
			latestReviewState[review.User.GetLogin()] = review.GetState()
		}
	}

	for user, state := range latestReviewState {
		switch state {
		case "APPROVED":
			approvedBy = append(approvedBy, "@"+user)
		case "CHANGES_REQUESTED":
			changesRequestedBy = append(changesRequestedBy, "@"+user)
		case "COMMENTED":
			commentedBy = append(commentedBy, "@"+user)
		}
	}

	// ... rest of the function ...
	// Fetch Commits
	commits, _, err := client.PullRequests.ListCommits(ctx, owner, repo, number, nil)
	if err != nil {
		slog.Error("Error fetching commits", "error", err)
	}

	// Fetch Conversation (Issue Comments)
	issueComments, _, err := client.Issues.ListComments(ctx, owner, repo, number, nil)
	if err != nil {
		slog.Error("Error fetching conversation", "error", err)
	}

	// Build the response
	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("#%d: %s\n", number, pr.GetTitle()))
	sb.WriteString(fmt.Sprintf("Author: \t@%s\n", pr.User.GetLogin()))
	sb.WriteString(fmt.Sprintf("Title: \t%s\n", pr.GetTitle()))

	headRef := ""
	if pr.Head != nil {
		headRef = pr.Head.GetRef()
	}
	baseRef := ""
	if pr.Base != nil {
		baseRef = pr.Base.GetRef()
	}
	sb.WriteString(fmt.Sprintf("Refs:  %s ... %s\n", baseRef, headRef))
	sb.WriteString(fmt.Sprintf("URL:   %s\n", pr.GetHTMLURL()))
	sb.WriteString(fmt.Sprintf("State: \t%s\n", pr.GetState()))

	milestone := "No milestone"
	if pr.Milestone != nil {
		milestone = pr.Milestone.GetTitle()
	}
	sb.WriteString(fmt.Sprintf("Milestone: \t%s\n", milestone))

	labels := "None yet"
	if len(pr.Labels) > 0 {
		var labelNames []string
		for _, l := range pr.Labels {
			labelNames = append(labelNames, l.GetName())
		}
		labels = strings.Join(labelNames, ", ")
	}
	sb.WriteString(fmt.Sprintf("Labels: \t%s\n", labels))
	sb.WriteString("Projects: \tNone yet\n")
	sb.WriteString(fmt.Sprintf("Draft: \t%t\n", pr.GetDraft()))

	assignees := "No one -- Assign yourself"
	if len(pr.Assignees) > 0 {
		var names []string
		for _, u := range pr.Assignees {
			names = append(names, u.GetLogin())
		}
		assignees = strings.Join(names, ", ")
	}
	sb.WriteString(fmt.Sprintf("Assignees: \t%s\n", assignees))
	sb.WriteString("Suggested-Reviewers: No suggestions\n")
	sb.WriteString(fmt.Sprintf("Reviewers: \t%s\n", reviewersStr))

	if len(approvedBy) > 0 {
		sb.WriteString(fmt.Sprintf("Approved-By: \t%s\n", strings.Join(approvedBy, ", ")))
	}
	if len(changesRequestedBy) > 0 {
		sb.WriteString(fmt.Sprintf("Changes-Requested-By: \t%s\n", strings.Join(changesRequestedBy, ", ")))
	}
	if len(commentedBy) > 0 {
		sb.WriteString(fmt.Sprintf("Commented-By: \t%s\n", strings.Join(commentedBy, ", ")))
	}

	// CI Status
	if pr.Head != nil && pr.Head.SHA != nil {
		status, err := GetLatestCIStatus(owner, repo, number, *pr.Head.SHA, skipCache)
		if err != nil {
			slog.Error("Error fetching CI status", "error", err)
			sb.WriteString("CI Status: \tError fetching status\n")
		} else if status != nil {
			total := 0
			success := 0
			var failures []string
			overallState := "success"

			// Process classic statuses
			if status.Status != nil {
				if status.Status.GetState() != "success" && status.Status.GetState() != "" {
					overallState = status.Status.GetState()
				}
				total += status.Status.GetTotalCount()
				for _, s := range status.Status.Statuses {
					if s.GetState() == "success" {
						success++
					} else if s.GetState() == "failure" {
						failures = append(failures, fmt.Sprintf("%s: %s", s.GetContext(), s.GetDescription()))
					}
				}
			}

			// Process check runs
			if status.CheckRuns != nil {
				total += status.CheckRuns.GetTotal()
				for _, cr := range status.CheckRuns.CheckRuns {
					if cr.GetConclusion() == "success" {
						success++
					} else {
						if cr.GetConclusion() != "" && cr.GetConclusion() != "neutral" && cr.GetConclusion() != "skipped" {
							overallState = "failure"
							failures = append(failures, fmt.Sprintf("%s: %s", cr.GetName(), cr.GetConclusion()))
						}
					}
				}
			}

			if total == 0 && overallState == "success" {
				overallState = "pending"
			}

			summary := fmt.Sprintf("%s (%d/%d checks passed)", overallState, success, total)
			sb.WriteString(fmt.Sprintf("CI Status: \t%s\n", summary))
			for _, failure := range failures {
				sb.WriteString(fmt.Sprintf("  - %s\n", failure))
			}
		}
	}
	sb.WriteString("\n")

	// Commits
	sb.WriteString(fmt.Sprintf("Commits (%d)\n", len(commits)))
	for _, c := range commits {
		sha := c.GetSHA()
		if len(sha) > 7 {
			sha = sha[:7]
		}
		msg := c.Commit.GetMessage()
		if idx := strings.Index(msg, "\n"); idx != -1 {
			msg = msg[:idx]
		}
		sb.WriteString(fmt.Sprintf("%s %s\n", sha, msg))
	}
	sb.WriteString("\n")

	// Description
	sb.WriteString("Description\n\n")
	body := pr.GetBody()
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

	// Combine Issue Comments and Reviews for Conversation
	type conversationItem struct {
		Time   time.Time
		Author string
		Body   string
		Type   string // "Comment" or "Review"
		State  string // For reviews
	}
	var convItems []conversationItem

	for _, c := range issueComments {
		t := time.Time{}
		if c.CreatedAt != nil {
			t = *c.CreatedAt
		}
		convItems = append(convItems, conversationItem{
			Time:   t,
			Author: c.User.GetLogin(),
			Body:   c.GetBody(),
			Type:   "Comment",
		})
	}

	for _, r := range reviews {
		// Skip empty commented reviews as they are usually just pending or noise
		if r.GetState() == "COMMENTED" && r.GetBody() == "" {
			continue
		}

		t := time.Time{}
		if r.SubmittedAt != nil {
			t = *r.SubmittedAt
		}

		convItems = append(convItems, conversationItem{
			Time:   t,
			Author: r.User.GetLogin(),
			Body:   r.GetBody(),
			Type:   "Review",
			State:  r.GetState(),
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
	fileCount := pr.GetChangedFiles()
	additions := pr.GetAdditions()
	deletions := pr.GetDeletions()
	sb.WriteString(fmt.Sprintf("Files changed (%d files; %d additions, %d deletions)\n\n", fileCount, additions, deletions))

	// Get diff with inline comments
	diffLines, _ := GetPRDiffWithInlineComments(owner, repo, number, skipCache, pr)
	sb.WriteString(diffLines)

	return sb.String(), nil
}


func GetPRDiffWithInlineComments(owner string, repo string, number int, skipCache bool, pr *github.PullRequest) (string, int) {
	client := git_tools.GetGithubClient()

	// Check database first - skip API call if cached
	if !skipCache {
		cachedBody, cachedSha, err := config.C.DB.GetPullRequest(number, repo)
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
	parsedDiff, err := utils.Parse(diff)
	if err != nil {
		slog.Error(err.Error())
	} else {
		for _, diffFile := range parsedDiff.Files {
			slog.Info("parsed file:" + diffFile.NewName)
			for _, hunk := range diffFile.Hunks {
				slog.Info("Parsed Hunnk: " + hunk.RangeHeader())
			}
		}
	}

	if err != nil {
		slog.Error("Error getting PR diff", "pr", number, "repo", repo, "error", err)
		return "", 0
	}

	// Store the result in the database (with latest_sha for future feature)
	return processPRDiffWithComments(client, owner, repo, number, diff, parsedDiff, skipCache, latestSha)
}


func processPRDiffWithComments(client *github.Client, owner string, repo string, number int, diff string, parsedDiff *utils.Diff, skipCache bool, latestSha string) (string, int) {
	var githubComments []*github.PullRequestComment
	var comments []PRComment

	// Check database first - skip API call if cached
	if !skipCache {
		cachedCommentsJSON, err := config.C.DB.GetPRComments(number, repo)
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
		opts := github.PullRequestListCommentsOptions{}
		var apiErr error
		githubComments, _, apiErr = client.PullRequests.ListComments(context.Background(), owner, repo, number, &opts)
		if apiErr != nil {
			slog.Error("Error getting Comments", "pr", number, "repo", repo, "error", apiErr)
			return diff, 0
		}

		// Store the result in the database
		commentsJSON, err := json.Marshal(githubComments)
		if err != nil {
			slog.Error("Error marshaling comments for storage", "pr", number, "repo", repo, "error", err)
		} else {
			if err := config.C.DB.UpsertPRComments(number, repo, string(commentsJSON)); err != nil {
				slog.Error("Error storing PR comments in database", "pr", number, "repo", repo, "error", err)
				// Continue even if storage fails
			}
		}

		// Convert to PRComment interface
		comments = convertToPRComments(githubComments)
		comments = filterComments(comments)
	}

	// Fetch LocalComments from database for this specific PR and add them to the comments list
	localComments, err := config.C.DB.GetLocalCommentsForPR(owner, repo, number)
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
			filePath := comment.GetPath()
			body := comment.GetBody()
			slog.Info("file: " + filePath)
			slog.Info("body : " + body)
			if comment.GetInReplyTo() != 0 {
				slog.Info("Reply To: " + strconv.FormatInt(comment.GetInReplyTo(), 10))
			}
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

			slog.Info("Adding tree at key: " + key + " (Outdated: " + strconv.FormatBool(rootComment.IsOutdated()) + ")")
			commentsByFileAndLine[key] = append(commentsByFileAndLine[key], tree)
		}
	}
	result := formatDiff(parsedDiff)
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

func formatDiff(diff *utils.Diff) string {
	var builder strings.Builder
	for _, file := range diff.Files {
		// status := "modified"
		// filename := file.NewName
		// if file.Mode == utils.DELETED {
		// 	status = "deleted"
		// 	filename = file.OrigName
		// } else if file.Mode == utils.NEW {
		// 	status = "new file"
		// }
		// builder.WriteString(fmt.Sprintf("%-12s %s\n", status, filename))

		builder.WriteString(file.DiffHeader + "\n")

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
		cachedReviewersJSON, err := config.C.DB.GetRequestedReviewers(number, repo)
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

	reviewers, _, err := client.PullRequests.ListReviewers(context.Background(), owner, repo, number, nil)
	if err != nil {
		return nil, err
	}

	reviewersJSON, err := json.Marshal(reviewers)
	if err != nil {
		slog.Error("Error marshaling reviewers for storage", "error", err)
	} else {
		if err := config.C.DB.UpsertRequestedReviewers(number, repo, string(reviewersJSON)); err != nil {
			slog.Error("Error storing requested reviewers in database", "error", err)
		}
	}

	return reviewers, nil
}

type CombinedPRStatus struct {
	Status    *github.CombinedStatus       `json:"status"`
	CheckRuns *github.ListCheckRunsResults `json:"check_runs"`
}

func GetLatestCIStatus(owner, repo string, prNumber int, sha string, skipCache bool) (*CombinedPRStatus, error) {
	client := git_tools.GetGithubClient()

	if !skipCache {
		cachedStatusJSON, err := config.C.DB.GetCIStatus(prNumber, repo, sha)
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
		if err := config.C.DB.UpsertCIStatus(prNumber, repo, sha, string(statusJSON)); err != nil {
			slog.Error("Error storing CI status in database", "error", err)
		}
	}

	return combined, nil
}
