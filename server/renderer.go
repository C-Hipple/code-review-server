package server

import (
	"codereviewserver/config"
	"codereviewserver/database"
	"codereviewserver/git_tools"
	"codereviewserver/org"
	"codereviewserver/utils"
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
	return strconv.FormatInt(c.Postion, 10)
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

func GetFullPRResponse(owner string, repo string, number int, skipCache bool) (string, error) {
	client := git_tools.GetGithubClient()

	// Fetch PR details
	pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, number)
	if err != nil {
		slog.Error("Error fetching PR details", "error", err)
		return "", err
	}

	// Get requested reviewers
	reviewers, err := GetRequestedReviewers(owner, repo, number, skipCache)
	if err != nil {
		slog.Error("Error fetching requested reviewers", "error", err)
		// Continue without reviewers rather than failing
		reviewers = []*github.User{}
	}

	reviewersStr := ""
	for _, reviewer := range reviewers {
		if reviewersStr != "" {
			reviewersStr += ", "
		}
		reviewersStr += reviewer.GetLogin()
	}

	// Build header
	var header string
	if pr != nil {
		authorLogin := ""
		if pr.User != nil {
			authorLogin = pr.User.GetLogin()
		}
		header = fmt.Sprintf("Title: %s\nProject: %s\nAuthor: %s\nState: %s\nReviewers: %s\n\n",
			pr.GetTitle(),
			repo,
			authorLogin,
			pr.GetState(),
			reviewersStr)
	}

	// Get diff with inline comments
	diffLines, _ := GetPRDiffWithInlineComments(owner, repo, number, skipCache)

	return header + diffLines, nil
}


func GetPRDiffWithInlineComments(owner string, repo string, number int, skipCache bool) (string, int) {
	client := git_tools.GetGithubClient()

	// Check database first - skip API call if cached
	if !skipCache {
		cachedBody, err := config.C.DB.GetPullRequest(number, repo)
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
				return processPRDiffWithComments(client, owner, repo, number, cachedBody, parsedDiff, skipCache)
			}
		}
	}

	// Not in cache or error occurred, fetch from API
	// Get the PR object to get the latest SHA for storage (future feature)
	pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, number)
	latestSha := ""
	if err == nil && pr.Head != nil && pr.Head.SHA != nil {
		latestSha = *pr.Head.SHA
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
	if err := config.C.DB.UpsertPullRequest(number, repo, latestSha, diff); err != nil {
		slog.Error("Error storing PR in database", "pr", number, "repo", repo, "error", err)
		// Continue even if storage fails
	}

	return processPRDiffWithComments(client, owner, repo, number, diff, parsedDiff, skipCache)
}


func processPRDiffWithComments(client *github.Client, owner string, repo string, number int, diff string, parsedDiff *utils.Diff, skipCache bool) (string, int) {
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

	if len(comments) == 0 {
		return diff, 0
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

			slog.Info("Adding tree at key: " + key + rootComment.GetBody())
			commentsByFileAndLine[key] = append(commentsByFileAndLine[key], tree)
		}
	}
	var builder strings.Builder
	for _, file := range parsedDiff.Files {
		builder.WriteString(file.DiffHeader)
		for _, hunk := range file.Hunks {
			builder.WriteString("\n")
			builder.WriteString(hunk.RangeHeader()) // TODO: hunk.HunkHeader shows the context
			for _, line := range hunk.WholeRange.Lines {
				builder.WriteString(line.Render())
				key := fmt.Sprintf("%s:%d", file.NewName, line.Position)
				res, ok := commentsByFileAndLine[key]
				if ok {
					for _, tree := range res {
						tree_str := buildCommentTree(tree, file.NewName)
						builder.WriteString(tree_str)
					}
				}
			}
		}
	}

	// TODO redo insertCommentTree to also work with the builder
	result := builder.String()
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

func buildCommentTree(tree []PRComment, filePath string) string {
	var result []string // leftover from refactor
	if len(tree) == 0 {
		return ""
	}

	rootComment := tree[0]
	commentIDInt, _ := strconv.ParseInt(rootComment.GetID(), 10, 64)
	result = append(result, "    ┌─ REVIEW COMMENT ─────────────────")
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

func GetRequestedReviewers(owner, repo string, number int, skipCache bool) ([]*github.User, error) {
	client := git_tools.GetGithubClient()

	if !skipCache {
		cachedReviewersJSON, err := config.C.DB.GetRequestedReviewers(number, repo)
		if err != nil {
			slog.Error("Error checking database for requested reviewers", "pr", number, "repo", repo, "error", err)
		} else if cachedReviewersJSON != "" {
			var reviewers []*github.User
			if err := json.Unmarshal([]byte(cachedReviewersJSON), &reviewers); err != nil {
				slog.Error("Error unmarshaling cached reviewers", "error", err)
			} else {
				return reviewers, nil
			}
		}
	}

	reviewers, _, err := client.PullRequests.ListReviewers(context.Background(), owner, repo, number, nil)
	// TODO: Show status of already done reviews.
	// reviews, _, err := client.PullRequests.ListReviews(context.Background(), owner, repo, number, nil)
	if err != nil {
		return nil, err
	}

	reviewersJSON, err := json.Marshal(reviewers.Users)
	if err != nil {
		slog.Error("Error marshaling reviewers for storage", "error", err)
	} else {
		if err := config.C.DB.UpsertRequestedReviewers(number, repo, string(reviewersJSON)); err != nil {
			slog.Error("Error storing requested reviewers in database", "error", err)
		}
	}

	return reviewers.Users, nil
}
