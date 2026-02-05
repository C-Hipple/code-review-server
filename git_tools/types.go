package git_tools

import (
	"time"
	"github.com/google/go-github/v48/github"
)

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
	Reviewers          []string `json:"reviewers"`           // Requested individual reviewers
	RequestedTeams     []string `json:"requested_teams"`     // Requested team reviewers
	ApprovedBy         []string `json:"approved_by"`         // Logins of users who approved
	ChangesRequestedBy []string `json:"changes_requested_by"` // Logins of users who requested changes
	CommentedBy        []string `json:"commented_by"`         // Logins of users who commented (non-approval/non-request)
	Draft              bool     `json:"draft"`
	CIStatus           string   `json:"ci_status"`
	CIFailures         []string `json:"ci_failures"`
	Body               string   `json:"body"`
	URL                string   `json:"url"`
	RepoPath           string   `json:"repo_path"`
	WorktreePath       string   `json:"worktree_path"`
}

type PRDetails struct {
	Metadata PRMetadata    `json:"metadata"`
	Diff     string        `json:"diff"`
	Comments         []CommentJSON `json:"comments"`
	OutdatedComments []CommentJSON `json:"outdated_comments"`
	Reviews          []ReviewJSON  `json:"reviews"`
	Commits          []CommitJSON  `json:"commits"`
}

type CombinedPRStatus struct {
	Status    *github.CombinedStatus       `json:"status"`
	CheckRuns *github.ListCheckRunsResults `json:"check_runs"`
}
