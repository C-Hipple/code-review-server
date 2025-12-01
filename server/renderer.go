package server

import (
	"codereviewserver/database"
	"codereviewserver/git_tools"
	"codereviewserver/org"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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
	formatted.WriteString("Comment By: " + comment.User.GetLogin() + "\n")
	formatted.WriteString(comment.GetBody())
	formatted.WriteString("\n------------------\n")
	return formatted.String()
}

func GetPRDiffWithInlineComments(owner string, repo string, number int) ([]string, int) {
	client := git_tools.GetGithubClient()

	// Get the diff
	diff, _, err := client.PullRequests.GetRaw(context.Background(), owner, repo, number, github.RawOptions{Type: github.Diff})
	if err != nil {
		slog.Error("Error getting PR diff", "pr", number, "repo", repo, "error", err)
		return []string{}, 0
	}

	// Get comments
	opts := github.PullRequestListCommentsOptions{}
	comments, _, err := client.PullRequests.ListComments(context.Background(), owner, repo, number, &opts)
	if err != nil {
		slog.Error("Error getting Comments", "pr", number, "repo", repo, "error", err)
		return []string{"*** Diff\n", diff}, 0
	}

	comments = filterComments(comments)
	if len(comments) == 0 {
		return []string{"*** Diff\n", diff}, 0
	}

	// Build a map of comments by file path and line position
	commentsByFileAndHunk := make(map[string][]*github.PullRequestComment)
	for _, comment := range comments {
		if comment.DiffHunk != nil {
			key := *comment.Path + "::" + *comment.DiffHunk
			commentsByFileAndHunk[key] = append(commentsByFileAndHunk[key], comment)
		}
	}


	// Parse the diff and insert comments inline
	diffLines := strings.Split(diff, "\n")
	result := []string{"*** Diff with Inline Comments\n"}

	i := 0
	for i < len(diffLines) {
		line := diffLines[i]
		result = append(result, line)

		// Check if this is a diff hunk header (e.g., "@@ -1,5 +1,6 @@")
		if strings.HasPrefix(line, "@@") {
			// Collect the hunk context (next few lines that form the diff hunk)
			hunkLines := []string{line}

			// Look ahead to collect hunk context (typically 3-5 lines)
			for j := i + 1; j < len(diffLines) && j < i+10; j++ {
				nextLine := diffLines[j]
				// Stop if we hit another hunk header or diff file header
				if strings.HasPrefix(nextLine, "@@") || strings.HasPrefix(nextLine, "diff --git") {
					break
				}
				hunkLines = append(hunkLines, nextLine)
			}

			hunkText := strings.Join(hunkLines, "\n")

			// Check for comments matching this hunk in any file
			for path, commentsForHunk := range commentsByFileAndHunk {
				parts := strings.Split(path, "::")
				if len(parts) == 2 {
					filePath := parts[0]
					commentDiffHunk := parts[1]

					// Check if the diff hunk matches or is similar
					if strings.Contains(hunkText, strings.Split(commentDiffHunk, "\n")[0]) {
						// Insert comments after the hunk
						trees := buildCommentTreesFromList(commentsForHunk)
						for _, tree := range trees {
							result = append(result, "")
							result = append(result, "    ┌─ REVIEW COMMENT ─────────────────")
							result = append(result, fmt.Sprintf("    │ File: %s", filePath))
							result = append(result, fmt.Sprintf("    │ %s", tree[0].CreatedAt.Format(time.DateTime)+" "+treeAuthorsFromList(tree)))
							result = append(result, "    │")

							for idx, comment := range tree {
								cleanBody := escapeBody(comment.Body)
								commentLines := strings.Split(cleanBody, "\n")

								if idx == 0 {
									result = append(result, fmt.Sprintf("    │ [%s]:", *comment.User.Login))
								} else {
									result = append(result, "    │")
									result = append(result, fmt.Sprintf("    │ Reply by [%s]:", *comment.User.Login))
								}

								for _, bodyLine := range commentLines {
									result = append(result, fmt.Sprintf("    │   %s", bodyLine))
								}
							}

							result = append(result, "    └──────────────────────────────────")
							result = append(result, "")
						}
					}
				}
			}
		}

		i++
	}

	return result, len(comments)
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
