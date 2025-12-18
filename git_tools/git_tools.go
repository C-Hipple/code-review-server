package git_tools

import (
	"crs/config"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/google/go-github/v48/github"
	"golang.org/x/oauth2"
)

type PullRequest interface {
}

type PRFilter func([]*github.PullRequest) []*github.PullRequest

func GetPRs(client *github.Client, state string, owner string, repo string) []*github.PullRequest {
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
			fmt.Println("Error!", err)
			//os.Exit(1)
			break
		}
		prs = append(prs, new_prs...)
		if len(new_prs) != per_page || i >= max_additional_calls {
			break
		}
		options.Page += 1
		i = i + 1
	}
	return prs
}

func GetManyRepoPRs(client *github.Client, state string, owner string, repos []string) []*github.PullRequest {
	var prs []*github.PullRequest
	for _, repo := range repos {
		repo_prs := GetPRs(
			client,
			state,
			owner,
			repo,
		)
		prs = append(prs, repo_prs...)
	}
	return prs
}

func GetSpecificPRs(client *github.Client, owner string, repo string, pr_numbers []int) []*github.PullRequest {
	var prs []*github.PullRequest
	for _, number := range pr_numbers {
		pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, number)
		if err != nil {
			fmt.Printf("Error Getting PR: %s/%s/%v: %v\n", owner, repo, number, err)
		}
		prs = append(prs, pr)
	}
	return prs
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
				if slices.Contains(teams, *team.Name) {
					filtered = append(filtered, pr)
					break
				}
			}
		}
		return filtered
	}
}

func FilterMyTeamRequested(prs []*github.PullRequest) []*github.PullRequest {
	teams := []string{"growth-pod-review", "purchase-pod-review", "growth-and-purchase-pod", "coreteam-review", "chat-pod-review-backend", "creator-team", "affiliate-program-experts", "email-experts", "identity-experts"}
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
	token := os.Getenv("GTDBOT_GITHUB_TOKEN")
	if token == "" {
		fmt.Println("Error! No Github Token!")
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
		fmt.Println(err)
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
