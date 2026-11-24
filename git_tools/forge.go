package git_tools

import (
	"context"
	"crs/config"
	"crs/forge"
	"fmt"

	"github.com/google/go-github/v74/github"
)

// This package is the GitHub driver behind forge.Forge: gitHubForge answers
// each forge question with the functions below it in this package, which talk
// to github.com with CRS_GITHUB_TOKEN. Everything outside this package reaches
// GitHub through a forge.Forge rather than through GetGithubClient.
//
// The driver holds no state of its own. Each call builds its client the way
// GetGithubClient always has — it exits without a token — and shares this
// package's process-wide rate limiter and HTTP cache, so every forge of kind
// github is the same GitHub, told apart only by its ID.

func init() {
	forge.Register(config.ForgeKindGitHub, func(id string, _ config.ForgeSettings) (forge.Forge, error) {
		return gitHubForge{id: id}, nil
	})
}

type gitHubForge struct {
	id string
}

var _ forge.Forge = gitHubForge{}

func (f gitHubForge) ID() string   { return f.id }
func (f gitHubForge) Kind() string { return config.ForgeKindGitHub }

// Identity

func (gitHubForge) Configured() bool           { return HasToken() }
func (gitHubForge) AuthenticatedLogin() string { return GetAuthenticatedLogin() }
func (gitHubForge) MyTeamRefs() []string       { return GetMyTeamRefs() }

// PRSource

func (gitHubForge) ListPRs(owner, repo, state string) ([]*github.PullRequest, error) {
	return GetPRs(GetGithubClient(), state, owner, repo)
}

func (gitHubForge) GetPR(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error) {
	pr, _, err := GetGithubClient().PullRequests.Get(ctx, owner, repo, number)
	return pr, err
}

func (gitHubForge) GetPRs(owner, repo string, numbers []int) ([]*github.PullRequest, error) {
	return GetSpecificPRs(GetGithubClient(), owner, repo, numbers)
}

func (gitHubForge) GetPRsForRefs(refs []PRRef) ([]*github.PullRequest, error) {
	return GetPRsForRefs(GetGithubClient(), refs)
}

func (gitHubForge) ReviewRequestedRefs(login string) ([]PRRef, error) {
	return ReviewRequestedRefs(login)
}

func (gitHubForge) MyInteractionRefs(login string) ([]PRRef, error) {
	return MyInteractionRefs(login)
}

func (gitHubForge) OpenInteractions(login string) InteractionSet {
	return SearchMyOpenInteractions(login)
}

// PRContent

func (gitHubForge) Diff(ctx context.Context, owner, repo string, number int) (string, error) {
	diff, _, err := GetGithubClient().PullRequests.GetRaw(ctx, owner, repo, number, github.RawOptions{Type: github.Diff})
	return diff, err
}

func (gitHubForge) ReviewComments(ctx context.Context, owner, repo string, number int) ([]*github.PullRequestComment, error) {
	return ListAllPRComments(ctx, GetGithubClient(), owner, repo, number)
}

func (gitHubForge) IssueComments(ctx context.Context, owner, repo string, number int) ([]*github.IssueComment, error) {
	return ListAllIssueComments(ctx, GetGithubClient(), owner, repo, number)
}

func (gitHubForge) Reviews(ctx context.Context, owner, repo string, number int) ([]*github.PullRequestReview, error) {
	return ListAllPRReviews(ctx, GetGithubClient(), owner, repo, number)
}

func (gitHubForge) Commits(ctx context.Context, owner, repo string, number int) ([]*github.RepositoryCommit, error) {
	return ListAllPRCommits(ctx, GetGithubClient(), owner, repo, number)
}

func (gitHubForge) RequestedReviewers(ctx context.Context, owner, repo string, number int) (*github.Reviewers, error) {
	return ListAllRequestedReviewers(ctx, GetGithubClient(), owner, repo, number)
}

func (gitHubForge) ReviewThreads(owner, repo string, number int) ([]ReviewThread, error) {
	return GetReviewThreads(owner, repo, number)
}

func (gitHubForge) Reactions(owner, repo string, number int) (*PRReactions, error) {
	return GetReactions(owner, repo, number)
}

func (gitHubForge) ReviewRequestHistory(owner, repo string, number int) (ReviewRequestHistory, error) {
	return GetReviewRequestHistory(owner, repo, number)
}

func (gitHubForge) TeamMembers(teams []TeamRef) map[string][]string {
	return GetTeamMembersForTeams(teams)
}

func (gitHubForge) Mergeability(refs []PRRef) (map[PRRef]Mergeability, error) {
	return GetMergeability(refs)
}

// CI

func (gitHubForge) CIStatus(owner, repo, branch string) CIStatusInfo {
	return GetCIStatus(owner, repo, branch)
}

func (gitHubForge) CombinedStatus(owner, repo, ref string) (*github.CombinedStatus, error) {
	return GetCombinedStatus(GetGithubClient(), owner, repo, ref)
}

func (gitHubForge) CheckRuns(owner, repo, ref string) (*github.ListCheckRunsResults, error) {
	return GetCheckRuns(GetGithubClient(), owner, repo, ref)
}

// Repository

func (gitHubForge) FileContent(owner, repo, path, ref string) (string, error) {
	return GetFileContent(GetGithubClient(), owner, repo, path, ref)
}

func (gitHubForge) CompareDiff(ctx context.Context, owner, repo, base, head string) (string, error) {
	return CompareDiff(ctx, GetGithubClient(), owner, repo, base, head)
}

func (gitHubForge) CompareStatus(ctx context.Context, owner, repo, base, head string) (string, error) {
	return CompareStatus(ctx, GetGithubClient(), owner, repo, base, head)
}

func (gitHubForge) PRHeadRef(number int) string {
	return gitHubPRHeadRef(number)
}

// gitHubPRHeadRef is where GitHub keeps PR number's head, for every PR, open
// or merged, from a fork or not.
func gitHubPRHeadRef(number int) string {
	return fmt.Sprintf("refs/pull/%d/head", number)
}

// Reviewing

func (gitHubForge) SubmitReview(owner, repo string, number int, review *github.PullRequestReviewRequest) error {
	return SubmitReview(GetGithubClient(), owner, repo, number, review)
}

func (gitHubForge) SubmitReply(owner, repo string, number int, body string, replyToID int64) error {
	return SubmitReply(GetGithubClient(), owner, repo, number, body, replyToID)
}

func (gitHubForge) MergePR(owner, repo string, number int, method string) (*github.PullRequestMergeResult, error) {
	return MergePR(GetGithubClient(), owner, repo, number, method)
}
