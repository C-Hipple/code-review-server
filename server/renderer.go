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

func renderPullRequest(diff string, comments []*github.PullRequestComment) string {
	var output strings.Builder
	output.WriteString(diff)
	for _, comment := range comments {
		output.WriteString(formatComment(comment))
	}
	return output.String()
}

func formatComment(comment *github.PullRequestComment) string {
	var formatted strings.Builder
	formatted.WriteString("Reviewed By: " + comment.User.GetLogin() + "\n")
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
}

func GetPRDiffWithInlineComments(owner string, repo string, number int) (string, int) {
	client := git_tools.GetGithubClient()

	// Check database first - skip API call if cached
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
			return processPRDiffWithComments(client, owner, repo, number, cachedBody, parsedDiff)
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
				slog.Info("Parsed Hunnk: " + hunk.HunkHeader)
				slog.Info("Parsed Hunnk: " + strconv.Itoa(hunk.NewRange.Start))
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

	return processPRDiffWithComments(client, owner, repo, number, diff, parsedDiff)
}

func processPRDiffWithComments(client *github.Client, owner string, repo string, number int, diff string, parsedDiff *utils.Diff) (string, int) {
	var comments []*github.PullRequestComment

	// Check database first - skip API call if cached
	cachedCommentsJSON, err := config.C.DB.GetPRComments(number, repo)
	if err != nil {
		slog.Error("Error checking database for PR comments", "pr", number, "repo", repo, "error", err)
		// Continue to fetch from API
	} else if cachedCommentsJSON != "" {
		// Found in cache, unmarshal and use it
		if err := json.Unmarshal([]byte(cachedCommentsJSON), &comments); err != nil {
			slog.Error("Error unmarshaling cached comments", "error", err)
			// Continue to fetch from API
		} else {
			// Use cached comments
			comments = filterComments(comments)
			if len(comments) == 0 {
				return diff, 0
			}
			// Continue with processing cached comments
		}
	}

	// Not in cache or error occurred, fetch from API
	if comments == nil {
		opts := github.PullRequestListCommentsOptions{}
		var apiErr error
		comments, _, apiErr = client.PullRequests.ListComments(context.Background(), owner, repo, number, &opts)
		if apiErr != nil {
			slog.Error("Error getting Comments", "pr", number, "repo", repo, "error", apiErr)
			return diff, 0
		}

		// Store the result in the database
		commentsJSON, err := json.Marshal(comments)
		if err != nil {
			slog.Error("Error marshaling comments for storage", "pr", number, "repo", repo, "error", err)
		} else {
			if err := config.C.DB.UpsertPRComments(number, repo, string(commentsJSON)); err != nil {
				slog.Error("Error storing PR comments in database", "pr", number, "repo", repo, "error", err)
				// Continue even if storage fails
			}
		}

		comments = filterComments(comments)
		if len(comments) == 0 {
			return diff, 0
		}
	}

	// Build comment trees first to group replies with their parents
	allCommentTrees := buildCommentTreesFromList(comments)

	// Build a map of comments by file path and line number
	// Key: "filepath:line" or "filepath:" for general comments
	// Value: slice of comment trees (each tree is a root comment with its replies)
	commentsByFileAndLine := make(map[string][][]*github.PullRequestComment)

	for _, tree := range allCommentTrees {
		for _, comment := range tree {
			slog.Info("file: " + *comment.Path)
			slog.Info("body : " + *comment.Body)
			// slog.Info("subject type: " + comment.Get)
			if comment.InReplyTo != nil {
				slog.Info("Reply To: " + strconv.FormatInt(*comment.InReplyTo, 10))
			}
			if comment.StartLine != nil {
				slog.Info("Reply To: " + strconv.Itoa(*comment.StartLine))
			}
			if comment.Line != nil {
				slog.Info("Line : " + strconv.Itoa(*comment.Line))
			}

		}
		if len(tree) == 0 {
			continue
		}
		rootComment := tree[0]

		// Use the root comment's position for the entire tree
		if rootComment.Path != nil {
			filePath := *rootComment.Path
			var key string

			if rootComment.Line != nil {
				// Comment on a specific line
				key = fmt.Sprintf("%s:%d", filePath, *rootComment.Position)
			} else {
				// General comment on the file (no specific line)
				key = filePath + ":"
			}

			slog.Info("Adding tree at key: " + key + *tree[0].Body)
			commentsByFileAndLine[key] = append(commentsByFileAndLine[key], tree)

		}
	}
	var builder strings.Builder
	for _, file := range parsedDiff.Files {
		builder.WriteString(file.DiffHeader)
		for _, hunk := range file.Hunks {
			builder.WriteString("\n")
			builder.WriteString(hunk.HunkHeader)
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

func buildCommentTree(tree []*github.PullRequestComment, filePath string) string {
	var result []string // leftover from refactor
	if len(tree) == 0 {
		return ""
	}

	result = append(result, "    ┌─ REVIEW COMMENT ─────────────────")
	result = append(result, fmt.Sprintf("    │ File: %s", filePath))
	result = append(result, fmt.Sprintf("    │ %s : %d", tree[0].CreatedAt.Format(time.DateTime)+" "+treeAuthorsFromList(tree), tree[0].GetID()))
	result = append(result, "    │")

	for idx, comment := range tree {
		cleanBody := escapeBody(comment.Body)
		commentLines := strings.Split(cleanBody, "\n")

		if idx == 0 {
			result = append(result, fmt.Sprintf("    │ [%s]:", *comment.User.Login))
		} else {
			result = append(result, "    │")
			result = append(result, fmt.Sprintf("    │ Reply by [%s]:[%d]", *comment.User.Login, comment.GetID()))
		}

		for _, bodyLine := range commentLines {
			result = append(result, fmt.Sprintf("    │   %s", bodyLine))
		}
	}

	result = append(result, "    └──────────────────────────────────")
	result = append(result, "")

	return strings.Join(result, "\n")
}

func buildCommentTreesFromList(comments []*github.PullRequestComment) [][]*github.PullRequestComment {
	commentMap := make(map[int64]*github.PullRequestComment)
	for _, c := range comments {
		commentMap[c.GetID()] = c
	}

	output := [][]*github.PullRequestComment{}
	processed := make(map[int64]bool)

	for _, comment := range comments {
		if processed[comment.GetID()] {
			continue
		}

		// If this is a root comment (no reply-to)
		if comment.InReplyTo == nil || comment.GetInReplyTo() == 0 {
			tree := []*github.PullRequestComment{comment}
			processed[comment.GetID()] = true

			// Find all replies to this comment
			for _, potentialReply := range comments {
				if !processed[potentialReply.GetID()] && potentialReply.InReplyTo != nil {
					if potentialReply.GetInReplyTo() == comment.GetID() {
						tree = append(tree, potentialReply)
						processed[potentialReply.GetID()] = true
					}
				}
			}

			output = append(output, tree)
		}
	}

	// Handle orphaned comments (replies without parents in this list)
	for _, comment := range comments {
		if !processed[comment.GetID()] {
			output = append(output, []*github.PullRequestComment{comment})
			processed[comment.GetID()] = true
		}
	}

	return output
}

func treeAuthorsFromList(tree []*github.PullRequestComment) string {
	authors := []string{}
	seen := make(map[string]bool)
	for _, comment := range tree {
		login := comment.User.GetLogin()
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

func filterComments(comments []*github.PullRequestComment) []*github.PullRequestComment {
	output := []*github.PullRequestComment{}
	for _, comment := range comments {
		if strings.Contains(*comment.User.Login, "advanced") {
			// I don't care about the lint warning stuff
			continue
		}
		output = append(output, comment)
	}
	return output
}
