package git_tools

import (
	"crs/config"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/google/go-github/v48/github"
	"golang.org/x/oauth2"
)

type PullRequest interface {
}

type PRFilter func([]*github.PullRequest) []*github.PullRequest

func GetPRs(client *github.Client, state string, owner string, repo string) ([]*github.PullRequest, error) {
	per_page := 100
	options := github.PullRequestListOptions{State: state, ListOptions: github.ListOptions{PerPage: per_page, Page: 1}}
	var prs []*github.PullRequest

	// TODO: Consider if I really want deep lookups.
	// Setting to 0 limits to 1 API call.
	max_additional_calls := 4
	i := 0

	for {
		new_prs, _, err := client.PullRequests.List(context.Background(), owner, repo, &options)
		if err != nil {
			slog.Error("Error listing PRs", "error", err)
			return nil, err
		}
		prs = append(prs, new_prs...)
		if len(new_prs) != per_page || i >= max_additional_calls {
			break
		}
		options.Page += 1
		i = i + 1
	}
	return prs, nil
}

func ParseRepoName(repoEntry string) (owner string, repo string, err error) {
	parts := strings.Split(repoEntry, "/")
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return "", "", fmt.Errorf("invalid repo entry: %s (expected 'owner/repo')", repoEntry)
}

func GetManyRepoPRs(client *github.Client, state string, repos []string) ([]*github.PullRequest, error) {
	var prs []*github.PullRequest
	for _, repoEntry := range repos {
		owner, repo, err := ParseRepoName(repoEntry)
		if err != nil {
			slog.Error("Skipping invalid repo entry", "entry", repoEntry, "error", err)
			continue
		}

		repo_prs, err := GetPRs(
			client,
			state,
			owner,
			repo,
		)
		if err != nil {
			return nil, err
		}
		prs = append(prs, repo_prs...)
	}
	return prs, nil
}

func GetSpecificPRs(client *github.Client, owner string, repo string, pr_numbers []int) ([]*github.PullRequest, error) {
	var prs []*github.PullRequest
	for _, number := range pr_numbers {
		pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, number)
		if err != nil {
			slog.Error("Error Getting PR", "owner", owner, "repo", repo, "number", number, "error", err)
			return nil, err
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

func GetPRDiff(client *github.Client, owner string, repo string, pr_number int) string {
	// Calls out to the
	diff, _, err := client.PullRequests.GetRaw(context.Background(), owner, repo, pr_number, github.RawOptions{
		Type: 1, // Diff format, not Patch format
	})
	if err != nil {
		return fmt.Sprintf("%s", err)
	}
	return diff

}

func GetPRComments(client *github.Client, owner string, repo string, number int) ([]*github.PullRequestComment, error) {
	opts := github.PullRequestListCommentsOptions{}
	comments, _, err := client.PullRequests.ListComments(context.Background(), owner, repo, number, &opts)
	if err != nil {
		// TODO: wump
		// unwrap in production
		return nil, err
	}
	return comments, nil
}

func ApplyPRFilters(prs []*github.PullRequest, filters []PRFilter) []*github.PullRequest {
	for _, filter := range filters {
		prs = filter(prs)
	}
	return prs
}

func FilterPRsByAuthor(prs []*github.PullRequest, author string) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		if *pr.User.Login == author {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func FilterPRsExcludeAuthor(prs []*github.PullRequest, author string) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		if *pr.User.Login != author {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func FilterPRsByState(prs []*github.PullRequest, state string) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		if *pr.State == state {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func FilterPRsByLabel(prs []*github.PullRequest, label string) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		for _, pr_label := range pr.Labels {
			if *pr_label.Name == label {
				filtered = append(filtered, pr)
				break
			}
		}
	}
	return filtered
}

func MyPRs(prs []*github.PullRequest) []*github.PullRequest {
	return FilterPRsByAuthor(prs, config.C.GithubUsername)
}

func FilterNotMyPRs(prs []*github.PullRequest) []*github.PullRequest {
	return FilterPRsExcludeAuthor(prs, config.C.GithubUsername)
}

func FilterIsDraft(prs []*github.PullRequest) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		if *pr.Draft {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func FilterNotDraft(prs []*github.PullRequest) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		if !*pr.Draft {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func MakeTeamFilters(teams []string) func([]*github.PullRequest) []*github.PullRequest {
	return func(prs []*github.PullRequest) []*github.PullRequest {
		filtered := []*github.PullRequest{}
		for _, pr := range prs {
			for _, team := range pr.RequestedTeams {
				if slices.Contains(teams, *team.Slug) {
					filtered = append(filtered, pr)
					break
				}
			}
		}
		return filtered
	}
}

func FilterMyReviewRequested(prs []*github.PullRequest) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		for _, reviewer := range pr.RequestedReviewers {
			if *reviewer.Login == config.C.GithubUsername {
				filtered = append(filtered, pr)
				break
			}
		}
	}
	return filtered
}

func GetGithubClient() *github.Client {
	ctx := context.Background()
	token := os.Getenv("CRS_GITHUB_TOKEN")
	if token == "" {
		slog.Error("Error! No Github Token!")
		os.Exit(1)
	}

	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	return github.NewClient(tc)
}

func GetLeankitCardTitle(pr PullRequest) string {
	return "abc"
}

func CheckBodyURLNotYetSet(body string) bool {
	return strings.Contains(body, "[Card Title]")

}

func ReplaceURLInBody(body string, title string, url string) string {
	lines := strings.Split(body, "\n")
	output_lines := []string{}
	for _, line := range lines {
		if strings.Contains(line, "[Card Title]") {
			output_lines = append(output_lines, fmt.Sprintf("[%s](%s)", title, url))
		} else {
			output_lines = append(output_lines, line)
		}
	}
	return strings.Join(output_lines, "\n")
}

func UpdatePRBody(pr *github.PullRequest, new_body string) bool {
	client := GetGithubClient()
	ctx := context.Background()

	pr.Body = &new_body

	pr, _, err := client.PullRequests.Edit(ctx, *pr.Base.Repo.Owner.Login, *pr.Base.Repo.Name, *pr.Number, pr)
	if err != nil {
		slog.Error("Error editing PR", "error", err)
		return false
	}
	return true
}

func FilterPRsByAssignedTeam(prs []*github.PullRequest, target_team string) []*github.PullRequest {
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		for _, team := range pr.RequestedTeams {
			if *team.Name == target_team {
				filtered = append(filtered, pr)
				continue
			}
		}
	}
	return filtered
}


func SubmitReview(client *github.Client, owner string, repo string, number int, review *github.PullRequestReviewRequest) error {
	ctx := context.Background()
	_, _, err := client.PullRequests.CreateReview(ctx, owner, repo, number, review)
	return err
}

func SubmitReply(client *github.Client, owner string, repo string, number int, body string, replyToID int64) error {
	ctx := context.Background()
	comment := &github.PullRequestComment{
		Body:      &body,
		InReplyTo: &replyToID,
	}
	_, _, err := client.PullRequests.CreateComment(ctx, owner, repo, number, comment)
	return err
}

func GetCombinedStatus(client *github.Client, owner, repo, ref string) (*github.CombinedStatus, error) {
	ctx := context.Background()
	status, _, err := client.Repositories.GetCombinedStatus(ctx, owner, repo, ref, nil)
	return status, err
}

func GetCheckRuns(client *github.Client, owner, repo, ref string) (*github.ListCheckRunsResults, error) {
	ctx := context.Background()
	checkRuns, _, err := client.Checks.ListCheckRunsForRef(ctx, owner, repo, ref, nil)
	return checkRuns, err
}

func CreateWorktree(repoDir, branch, worktreePath string) error {
	// Ensure the worktree directory doesn't exist (git worktree add will fail if it does, but maybe we want to be clean)
	// Actually let git handle it.

	// git worktree add <path> <branch>
	cmd := exec.Command("git", "worktree", "add", worktreePath, branch)
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		if strings.Contains(outputStr, "already exists") {
			slog.Info("Worktree already exists, skipping creation", "repo", repoDir, "branch", branch, "path", worktreePath)
			return nil
		}
		slog.Error("Failed to create worktree", "repo", repoDir, "branch", branch, "path", worktreePath, "output", outputStr, "error", err)
		return fmt.Errorf("git worktree add failed: %s: %w", outputStr, err)
	}
	slog.Info("Created worktree", "repo", repoDir, "branch", branch, "path", worktreePath)
	return nil
}

func RemoveWorktree(repoDir, worktreePath string) error {
	// git worktree remove <path> --force
	cmd := exec.Command("git", "worktree", "remove", worktreePath, "--force")
	cmd.Dir = repoDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If the worktree is already gone from disk, git might complain.
		// We might want to check if the path exists first, but git worktree prune might be needed.
		slog.Warn("Failed to remove worktree (might already be gone)", "repo", repoDir, "path", worktreePath, "output", string(output), "error", err)
		// We try to continue to prune anyway
	}

	// git worktree prune
	cmdPrune := exec.Command("git", "worktree", "prune")
	cmdPrune.Dir = repoDir
	if out, err := cmdPrune.CombinedOutput(); err != nil {
		slog.Warn("Failed to prune worktrees", "repo", repoDir, "output", string(out), "error", err)
	}

	// Ensure the directory is actually gone (if git failed to remove it for some reason but we want it gone)
	if _, err := os.Stat(worktreePath); err == nil {
		slog.Info("Worktree directory still exists, removing manually", "path", worktreePath)
		if err := os.RemoveAll(worktreePath); err != nil {
			return fmt.Errorf("failed to remove worktree directory: %w", err)
		}
	}

	return nil
}
