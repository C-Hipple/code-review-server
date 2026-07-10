package workflows

import (
	"crs/config"
	"strings"

	"github.com/google/go-github/v74/github"
)

// filterMyReviewRequested matches PRs where the configured user owes a review.
// A user owes a review when either:
//  1. They are in pr.RequestedReviewers (a normal pending request), OR
//  2. Their latest review on the PR has state DISMISSED. CODEOWNERS stale-review
//     auto-dismissal removes the user's prior approval but does NOT add them
//     back to RequestedReviewers, so case 1 alone misses these PRs.
func filterMyReviewRequested(prs []*github.PullRequest) []*github.PullRequest {
	myLogin := config.C().GithubUsername
	store := GetCurrentAuxDataStore()
	filtered := []*github.PullRequest{}
	for _, pr := range prs {
		if pr == nil {
			continue
		}
		if userInRequestedReviewers(pr, myLogin) {
			filtered = append(filtered, pr)
			continue
		}
		if store == nil {
			continue
		}
		key, ok := prKey(pr)
		if !ok {
			continue
		}
		aux, ok := store.Get(key)
		if !ok || aux == nil {
			continue
		}
		if latestUserReviewIsDismissed(aux.Reviews, myLogin) {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func userInRequestedReviewers(pr *github.PullRequest, login string) bool {
	for _, reviewer := range pr.RequestedReviewers {
		if reviewer == nil || reviewer.Login == nil {
			continue
		}
		if strings.EqualFold(*reviewer.Login, login) {
			return true
		}
	}
	return false
}

func prKey(pr *github.PullRequest) (PRKey, bool) {
	if pr.Base == nil || pr.Base.Repo == nil ||
		pr.Base.Repo.Owner == nil || pr.Base.Repo.Owner.Login == nil ||
		pr.Base.Repo.Name == nil || pr.Number == nil {
		return PRKey{}, false
	}
	return PRKey{
		Owner:  *pr.Base.Repo.Owner.Login,
		Repo:   *pr.Base.Repo.Name,
		Number: *pr.Number,
	}, true
}

// latestUserReviewIsDismissed reports whether the most recent review submitted
// by login has state DISMISSED. Returns false if the user has not reviewed the
// PR or if their latest review is APPROVED / CHANGES_REQUESTED / COMMENTED
// (i.e. they have already weighed in since any prior dismissal).
func latestUserReviewIsDismissed(reviews []*github.PullRequestReview, login string) bool {
	var latest *github.PullRequestReview
	for _, r := range reviews {
		if r == nil || r.User == nil || r.User.Login == nil || r.SubmittedAt == nil {
			continue
		}
		if !strings.EqualFold(*r.User.Login, login) {
			continue
		}
		if latest == nil || r.SubmittedAt.After(latest.SubmittedAt.Time) {
			latest = r
		}
	}
	return latest != nil && latest.GetState() == "DISMISSED"
}
