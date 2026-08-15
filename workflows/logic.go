package workflows

import (
	"bytes"
	"context"
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"crs/utils"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v74/github"
)

type PRRequirement struct {
	Owner     string
	Repo      string
	State     string // "open", "closed", etc.
	PRNumbers []int  // Optional, if empty fetch all for state
	AuxData   AuxDataRequirement
}

// AuxDataRequirement specifies what auxiliary data a workflow needs
type AuxDataRequirement struct {
	Comments bool // PR review comments
	CIStatus bool // CI/workflow status
	Diff     bool // PR diff content
	Reviews  bool // PR reviews (approved_by, changes_requested_by, commented_by)
	Commits  bool // PR commits
	// ReviewThreads is the GraphQL review-thread resolution state. No workflow
	// filter needs it, but GetPRDetails does, so the cache warm asks for it.
	ReviewThreads bool
	// Teams resolves which teams are required to review the PR and where each
	// one stands, for the chips on the review list's rows. It is derived from
	// the PR's reviews, so asking for it implies Reviews.
	Teams bool
}

// PRAuxData holds pre-fetched auxiliary data for a PR
type PRAuxData struct {
	Comments          []*github.PullRequestComment
	FormattedComments []string
	CommentsCount     int
	CIStatus          *git_tools.CIStatusInfo
	Diff              string
	DiffLines         []string
	Reviews           []*github.PullRequestReview // PR reviews (for approved_by / changes_requested_by / commented_by)
	ReviewThreads     []git_tools.ReviewThread    // Review-thread resolution state, for the DB cache
	// TeamReviews is the required-team standing shown on the review list's
	// rows. Non-nil (possibly empty) once resolved; nil means "not resolved
	// this pass", which leaves any cached row alone.
	TeamReviews []git_tools.TeamReviewStatus
	HeadSHA     string // SHA of the PR head for DB storage
	BaseSHA     string // SHA of the PR base for DB storage
}

type Workflow interface {
	GetName() string
	GetPRRequirements() []PRRequirement
	Run(prs []*github.PullRequest, c chan FileChanges, file_change_wg *sync.WaitGroup) (RunResult, error)
	GetOrgSectionName() string
}

type FileChanges struct {
	ChangeType   string
	Identifier   string
	Status       string
	Title        string
	Details      []string
	Tags         []string
	SectionID    int64
	SectionName  string
	TTL          int64
	WorkflowName string
	// HeadSHA is the PR head SHA this change was built from. Recorded in
	// WorkflowActionLog so a later cache-miss report can tell whether the
	// workflow was working from the SHA the reviewer is now looking at. Empty
	// for Delete changes, which are built from DB rows rather than a PR.
	HeadSHA string
	// NotifyOnAdd is the effective desktop-notification decision for the
	// workflow that produced this change. When true and ChangeType is
	// "Addition", a desktop notification will be sent.
	NotifyOnAdd bool
}

type SerializedFileChange struct {
	FileChange *FileChanges
	Lines      []string
}

func (fc FileChanges) Report() {
	slog.Info(fmt.Sprintf("[%s] %-20s - %s (%s)", fc.ChangeType[:2], fc.SectionName, fc.Title, fc.Identifier))
}

// Deserialize is no longer strictly needed for org rendering here as ApplyChanges will handle DB directly,
// but we keep the concept if we need to pass lines around.
func (fc *FileChanges) GetLines(indentLevel int) []string {
	var lines []string
	if fc.ChangeType != "Delete" {
		// Mock title line for backward compatibility if needed during transition
		indentStars := strings.Repeat("*", indentLevel)
		titleLine := fmt.Sprintf("%s %s %s", indentStars, fc.Status, fc.Title)
		if len(fc.Tags) > 0 {
			titleLine += "\t\t:" + strings.Join(fc.Tags, ":") + ":"
		}
		lines = append(lines, titleLine)
		lines = append(lines, fc.Details...)
	}
	return lines
}

type PRToOrgBridge struct {
	// Implement the OrgTODO Interface for PRs
	PR          *github.PullRequest
	IncludeDiff bool
}

func (prb PRToOrgBridge) ID() string {
	return fmt.Sprintf("%d", *prb.PR.Number)
}

func (prb PRToOrgBridge) Repo() string {
	return prb.PR.Base.Repo.GetFullName()

}

func (prb PRToOrgBridge) StartLine() int {
	// This implementation of the interface is for when we pull things from the API and want to compare
	// Therefore the StartLine should't be checked
	panic("Called StartLine for PRToOrgBridge which shouldn't be done.")
	// return -1
}

func (prb PRToOrgBridge) LinesCount() int {
	// This implementation of the interface is for when we pull things from the API and want to compare
	// Therefore the LinesCount should't be checked
	panic("Called LinesCount for PRToOrgBridge which shouldn't be done.")
	// return -1
}

func (prb PRToOrgBridge) Title() string {
	return *prb.PR.Title
}

func (prb PRToOrgBridge) Identifier() string {
	return fmt.Sprintf("%s-%s", prb.Repo(), prb.ID())
}

func (prb PRToOrgBridge) GetTags() []string {
	tags := []string{*prb.PR.Head.Repo.Name}
	if *prb.PR.Draft {
		tags = append(tags, "draft")
	} else if prb.PR.MergedAt != nil {
		// We don't have release_check_command here easily without passing it in.
		// For now, let's assume we want to mark as merged.
		// If we need the release check command, we might need to change struct or pass it.
		// The original code used release_check_command from serializer/section logic.
		// Let's defer complex release check for now or handle it in logic.
		tags = append(tags, "merged")
	}
	return tags
}

func (prb PRToOrgBridge) ItemTitle(indent_level int, release_check_command string) string {
	// Deprecated or Legacy usage support if needed, but we aim to remove
	return fmt.Sprintf("%s %s %s", strings.Repeat("*", indent_level), prb.GetStatus(), prb.Title())
}

func (prb PRToOrgBridge) Summary() string {
	return prb.Title()
}

func (prb PRToOrgBridge) CheckDone() bool {
	return *prb.PR.State == "closed"
}

func (prb PRToOrgBridge) GetStatus() string {
	if prb.CheckDone() {
		return "DONE"
	}
	if *prb.PR.Draft {
		return "WAITING"
	}
	return "TODO"
}

func (prb PRToOrgBridge) Details() []string {
	details := []string{}
	details = append(details, fmt.Sprintf("%d", prb.PR.GetNumber()))
	details = append(details, "Repo: "+prb.PR.Base.Repo.GetFullName())
	details = append(details, fmt.Sprintf("%s\n", prb.PR.GetHTMLURL()))
	details = append(details, fmt.Sprintf("Title: %s\n", prb.PR.GetTitle()))

	user := prb.PR.GetUser()
	author_string := fmt.Sprintf("Author: %s", user.GetLogin())
	if user.GetName() != "" {
		author_string = author_string + fmt.Sprintf(" (%s)", user.GetName())
	}

	details = append(details, author_string+"\n")

	details = append(details, fmt.Sprintf("Branch: %s\n", *prb.PR.Head.Label))

	reviewers := strings.Join(utils.Map(prb.PR.RequestedReviewers, getReviewerName), ", ")
	if reviewers != "" {
		details = append(details, fmt.Sprintf("Requested Reviewers: %s\n", reviewers))
	} else {
		details = append(details, "Requested Reviewers:\n")
	}
	teams := strings.Join(utils.Map(prb.PR.RequestedTeams, getTeamName), ", ")
	if teams != "" {
		details = append(details, fmt.Sprintf("Requested Teams: %s\n", teams))
	} else {
		details = append(details, "Requested Teams:\n")
	}

	// Get pre-fetched aux data if available
	var auxData *PRAuxData
	if store := GetCurrentAuxDataStore(); store != nil {
		key := PRKey{
			Owner:  *prb.PR.Base.Repo.Owner.Login,
			Repo:   *prb.PR.Base.Repo.Name,
			Number: *prb.PR.Number,
		}
		auxData, _ = store.Get(key)
	}

	// For open non-draft PRs, surface who has already reviewed so the dashboard
	// reflects current review state alongside the pending-reviewer list.
	isOpen := prb.PR.State != nil && *prb.PR.State == "open"
	isDraft := prb.PR.Draft != nil && *prb.PR.Draft
	if isOpen && !isDraft {
		approvedBy, changesRequestedBy, commentedBy := summarizeReviewers(auxData)
		details = append(details, fmt.Sprintf("Approved By: %s\n", strings.Join(approvedBy, ", ")))
		details = append(details, fmt.Sprintf("Changes Requested By: %s\n", strings.Join(changesRequestedBy, ", ")))
		details = append(details, fmt.Sprintf("Commented By: %s\n", strings.Join(commentedBy, ", ")))
	}

	// TODO: Consider putting these in subsection?
	if prb.PR.MergedAt != nil {
		details = append(details, fmt.Sprintf("Merged at: %s\n", *prb.PR.MergedAt))
		if prb.PR.MergeCommitSHA != nil {
			details = append(details, fmt.Sprintf("Merge Commit SHA: %s\n", *prb.PR.MergeCommitSHA))
		} else {
			details = append(details, "Merged with Empty Merge Commit SHA?")
		}
	} else {
		// CI Status - use pre-fetched or fallback to fetch
		var ciInfo git_tools.CIStatusInfo
		if auxData != nil && auxData.CIStatus != nil {
			ciInfo = *auxData.CIStatus
		} else {
			ciInfo = git_tools.GetCIStatus(*prb.PR.Base.Repo.Owner.Login, *prb.PR.Head.Repo.Name, *prb.PR.Head.Label)
		}
		if len(ciInfo.Statuses) > 0 {
			details = append(details, fmt.Sprintf("*** %s CI Status\n", ciInfo.OverallStatus))
			details = append(details, ciInfo.Statuses...)
		}
	}

	// Diff - use pre-fetched or fallback to fetch
	if prb.IncludeDiff {
		var diff []string
		headSHA := ""
		if prb.PR.Head != nil && prb.PR.Head.SHA != nil {
			headSHA = *prb.PR.Head.SHA
		}

		if auxData != nil && len(auxData.DiffLines) > 0 {
			diff = auxData.DiffLines
			// Store pre-fetched diff in database
			if auxData.Diff != "" {
				sha := auxData.HeadSHA
				if sha == "" {
					sha = headSHA
				}
				baseSHA := ""
				if auxData.BaseSHA != "" {
					baseSHA = auxData.BaseSHA
				} else if prb.PR.Base != nil && prb.PR.Base.SHA != nil {
					baseSHA = *prb.PR.Base.SHA
				}
				if err := config.C().DB.UpsertPullRequest(prb.PR.GetNumber(), prb.PR.Base.Repo.GetName(), sha, baseSHA, auxData.Diff); err != nil {
					slog.Error("Error storing pre-fetched PR diff in database", "pr", prb.PR.GetNumber(), "error", err)
				}
			}
		} else {
			diff = getPRDiff(*prb.PR.Base.Repo.Owner.Login, *prb.PR.Base.Repo.Name, prb.PR.GetNumber(), headSHA)
		}
		if len(diff) > 0 {
			details = append(details, diff...)
		}
	}

	escaped_body := removePRBodySections(prb.PR.Body)
	escaped_body = escapeBody(&escaped_body)
	details = append(details, fmt.Sprintf("*** BODY\n %s\n", escaped_body)) // TODO: Do we need this end newline?

	// Comments - use pre-fetched or fallback to fetch
	var comments_count int
	var comments []string
	if auxData != nil && auxData.Comments != nil {
		// Format pre-fetched comments
		comments_count, comments = formatComments(auxData.Comments)
	} else {
		comments_count, comments = getComments(*prb.PR.Base.Repo.Owner.Login, *prb.PR.Head.Repo.Name, *prb.PR.Number)
	}
	if len(comments) != 0 {
		details = append(details, fmt.Sprintf("*** Comments [%v]\n", comments_count))
		details = append(details, comments...)
	}
	return details
}

func getReviewerName(reviewer *github.User) string {
	return *reviewer.Login
}

// summarizeReviewers reduces a PR's review list down to the latest state per
// reviewer and partitions them into approved / changes-requested / commented
// buckets. Returns empty slices when auxData or its Reviews field is nil.
func summarizeReviewers(auxData *PRAuxData) (approvedBy, changesRequestedBy, commentedBy []string) {
	approvedBy = []string{}
	changesRequestedBy = []string{}
	commentedBy = []string{}
	if auxData == nil || auxData.Reviews == nil {
		return
	}
	latest := make(map[string]string)
	for _, r := range auxData.Reviews {
		if r == nil || r.User == nil {
			continue
		}
		state := r.GetState()
		// Ignore non-terminal states like PENDING / DISMISSED so they don't
		// clobber a reviewer's actual APPROVED / CHANGES_REQUESTED verdict.
		if state != "APPROVED" && state != "CHANGES_REQUESTED" && state != "COMMENTED" {
			continue
		}
		latest[r.User.GetLogin()] = state
	}
	for user, state := range latest {
		switch state {
		case "APPROVED":
			approvedBy = append(approvedBy, user)
		case "CHANGES_REQUESTED":
			changesRequestedBy = append(changesRequestedBy, user)
		case "COMMENTED":
			commentedBy = append(commentedBy, user)
		}
	}
	sort.Strings(approvedBy)
	sort.Strings(changesRequestedBy)
	sort.Strings(commentedBy)
	return
}

func getTeamName(reviewer *github.Team) string {
	return *reviewer.Name
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

func cleanEmptyEndingLines(lines *[]string) []string {
	// Removes the empty lines at the end of the details so org collapses prettier
	i := len(*lines) - 1
	for i >= 0 && strings.TrimSpace((*lines)[i]) == "" {
		i--
	}
	return (*lines)[:i+1]
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

func removePRBodySections(body *string) string {
	// Define the regular expression pattern to match everything between <!-- and -->
	//	re := regexp.MustCompile(`<!--.*?-->`)
	// TODO more empty line cleaning
	re := regexp.MustCompile(`(?s)<!--.*?-->`)

	if body == nil {
		return ""
	}

	// Replace all matches with an empty string
	cleaned := re.ReplaceAllString(*body, "")
	return cleaned
}

func getComments(owner string, repo string, number int) (int, []string) {
	comments, err := git_tools.GetPRComments(git_tools.GetGithubClient(), owner, repo, number)
	if err != nil {
		slog.Error("Error getting Comments", "pr", number, "repo", repo, "error", err)
		return 0, []string{}
	}

	comments = filterComments(comments)

	// Store the result in the database
	commentsJSON, marshalErr := json.Marshal(comments)
	if marshalErr != nil {
		slog.Error("Error marshaling comments for storage", "pr", number, "repo", repo, "error", marshalErr)
	} else {
		if err := config.C().DB.UpsertPRComments(number, repo, string(commentsJSON)); err != nil {
			slog.Error("Error storing PR comments in database", "pr", number, "repo", repo, "error", err)
			// Continue even if storage fails
		}
	}

	return formatComments(comments)
}

// formatComments formats pre-fetched PR comments into display strings
func formatComments(comments []*github.PullRequestComment) (int, []string) {
	comments = filterComments(comments)
	trees := buildCommentTrees(comments)

	str_comments := []string{}
	for _, tree := range trees {
		for i, comment := range tree {
			if i == 0 {
				str_comments = append(str_comments, "**** "+comment.CreatedAt.Format(time.DateTime)+" "+treeAuthors(tree))
				str_comments = append(str_comments, *comment.DiffHunk)
			}
			clean_body := escapeBody(comment.Body)
			str_comments = append(str_comments, fmt.Sprintf("***** (%d) %s %s", i, comment.CreatedAt.Format(time.DateTime), *comment.User.Login))
			str_comments = append(str_comments, clean_body)
		}
	}

	return len(comments), str_comments
}

func ProcessPRsDB(workflowName string, prs []*github.PullRequest, changes_channel chan FileChanges, db *database.DB, section *database.Section, change_wg *sync.WaitGroup, includeDiff bool, notifyOnAdd bool) RunResult {
	result := RunResult{}

	ttl := time.Now().Add(2 * time.Hour).Unix()

	// Build FileChanges for each PR in parallel; each goroutine writes to its
	// own index so no mutex is needed.
	prChanges := make([]FileChanges, len(prs))
	var buildWg sync.WaitGroup
	for i, pr := range prs {
		buildWg.Add(1)
		go func(i int, pr *github.PullRequest) {
			defer buildWg.Done()
			fc := SyncTODOToSectionDB(db, workflowName, pr, section, includeDiff)
			fc.TTL = ttl
			fc.NotifyOnAdd = notifyOnAdd
			prChanges[i] = fc
		}(i, pr)
	}
	buildWg.Wait()

	pr_identifiers := make([]string, 0, len(prs))
	changes := make([]FileChanges, 0, len(prs))
	for _, fc := range prChanges {
		pr_identifiers = append(pr_identifiers, fc.Identifier)
		changes = append(changes, fc)
	}

	{
		items, err := db.GetItemsBySection(section.ID)
		if err != nil {
			slog.Error("Error getting items from section", "error", err)
		} else {
			for _, item := range items {
				if slices.Contains(pr_identifiers, item.Identifier) {
					continue
				}
				// Only release ownership for items this workflow actually
				// claims. Items maintained by other workflows are left alone
				// here; cycle-end pruning removes anything with no owners.
				if !slices.Contains(item.GetWorkflows(), workflowName) {
					continue
				}
				var details []string
				json.Unmarshal([]byte(item.DetailsJSON), &details)
				var tags []string
				json.Unmarshal([]byte(item.Tags), &tags)

				fileChange := FileChanges{
					ChangeType:   "Delete",
					Identifier:   item.Identifier,
					Status:       item.Status,
					Title:        item.Title,
					Details:      details,
					Tags:         tags,
					SectionID:    section.ID,
					SectionName:  section.SectionName,
					TTL:          0,
					WorkflowName: workflowName,
				}
				changes = append(changes, fileChange)
			}
		}
	}

	// Cleanup expired items
	expiredItems, err := db.GetExpiredItems(section.ID)
	if err == nil {
		for _, item := range expiredItems {
			isBeingProcessed := false
			for _, change := range changes {
				if change.Identifier == item.Identifier {
					isBeingProcessed = true
					break
				}
			}

			if isBeingProcessed {
				continue
			}
			if !slices.Contains(item.GetWorkflows(), workflowName) {
				continue
			}
			var details []string
			json.Unmarshal([]byte(item.DetailsJSON), &details)
			var tags []string
			json.Unmarshal([]byte(item.Tags), &tags)

			fileChange := FileChanges{
				ChangeType:   "Delete",
				Identifier:   item.Identifier,
				Status:       item.Status,
				Title:        item.Title,
				Details:      details,
				Tags:         tags,
				SectionID:    section.ID,
				SectionName:  section.SectionName,
				TTL:          0,
				WorkflowName: workflowName,
			}
			changes = append(changes, fileChange)
		}
	} else {
		slog.Error("Error getting expired items", "error", err)
	}

	for _, output := range changes {
		result.Process(&output, changes_channel, change_wg)
	}

	return result
}

func SyncTODOToSectionDB(db *database.DB, workflowName string, pr *github.PullRequest, section *database.Section, includeDiff bool) FileChanges {
	pr_as_org := PRToOrgBridge{PR: pr, IncludeDiff: includeDiff}

	identifier := pr_as_org.Identifier()
	dbItem, err := db.GetItem(section.ID, identifier)

	found := err == nil && dbItem != nil
	changeType := "Addition"
	if found {
		newStatus := pr_as_org.GetStatus()
		newTags := pr_as_org.GetTags()
		isOpen := pr.State != nil && *pr.State == "open"
		isDraft := pr.Draft != nil && *pr.Draft
		// Always update if status or tags changed (e.g. draft → non-draft)
		if dbItem.Status != newStatus || dbItem.Tags != strings.Join(newTags, ",") {
			changeType = "Update"
		} else if workflowName != "" && !slices.Contains(dbItem.GetWorkflows(), workflowName) {
			// Item exists but this workflow doesn't yet claim it; we need an
			// upsert so the workflow gets added to the ownership list.
			changeType = "Update"
		} else if isOpen && !isDraft {
			// Refresh reviewer status (approved / changes-requested / commented)
			// on every cycle for open non-draft PRs.
			changeType = "Update"
		} else {
			// After a week we stop updating old merged PRs
			mergedAt := pr_as_org.PR.GetMergedAt()
			if !mergedAt.IsZero() && mergedAt.After(time.Now().Add(-7*24*time.Hour)) {
				changeType = "Update"
			} else {
				changeType = "No Change"
			}
		}
	}

	return FileChanges{
		ChangeType:   changeType,
		Identifier:   identifier,
		Status:       pr_as_org.GetStatus(),
		Title:        pr_as_org.Title(),
		Details:      pr_as_org.Details(),
		Tags:         pr_as_org.GetTags(),
		SectionID:    section.ID,
		SectionName:  section.SectionName,
		TTL:          0, // Set by caller
		WorkflowName: workflowName,
		HeadSHA:      pr.GetHead().GetSHA(),
	}
}

// Assume git_tools.GetGithubClient() and processWorkflowRuns are defined elsewhere
// and work as intended.

// getPRDiff fetches and stores the PR diff. If headSHA is provided, it skips the API call to get the PR.
func getPRDiff(owner string, repo string, number int, headSHA string) []string {
	client := git_tools.GetGithubClient()

	diff, _, err := client.PullRequests.GetRaw(context.Background(), owner, repo, number, github.RawOptions{Type: github.Diff})
	if err != nil {
		slog.Error("Error getting PR diff", "pr", number, "repo", repo, "error", err)
		return []string{}
	}

	// Store the result in the database (base SHA unknown at this point, will be filled by workflow)
	if err := config.C().DB.UpsertPullRequest(number, repo, headSHA, "", diff); err != nil {
		slog.Error("Error storing PR diff in database", "pr", number, "repo", repo, "error", err)
		// Continue even if storage fails
	}

	return []string{"*** Diff\n", diff}
}

// GetReleaseStatus runs the release check command with args: <owner> <repo> <commit-sha>
// and returns the output string (e.g. "released", "release-client", "merged").
func GetReleaseStatus(command, owner, repo, sha string) (string, error) {
	cmd := exec.Command(command, owner, repo, sha)

	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb

	err := cmd.Run()

	if err != nil {
		return "", fmt.Errorf("release check command failed: %w (stderr: %s)", err, errb.String())
	}

	stdout := outb.String()
	return strings.Replace(stdout, "\n", "", -1), nil
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

func buildCommentTrees(comments []*github.PullRequestComment) [][]*github.PullRequestComment {
	output := [][]*github.PullRequestComment{}
	for _, comment := range comments {

		replyTo := int64(-1)
		if comment.InReplyTo != nil {
			replyTo = comment.GetInReplyTo()
		}

		if len(output) == 0 || replyTo == -1 {
			output = append(output, []*github.PullRequestComment{comment})
			continue
		}

		for j, commentTree := range output {
			if commentTree[len(commentTree)-1].GetID() == replyTo {
				output[j] = append(commentTree, comment)
				continue
			}
		}
	}
	return output
}

// List all of the authors in a tree for the tree title line.
func treeAuthors(tree []*github.PullRequestComment) string {
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
