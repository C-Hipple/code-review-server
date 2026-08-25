package workflows

import (
	"crs/config"
	"crs/git_tools"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/google/go-github/v74/github"
)

// resolvePRTeamReviews works out which teams a PR needs a review from and where
// each of them stands, for the chips the review list shows on every row.
//
// The set of teams is a high-water mark, because GitHub drops a team from a
// PR's requested_teams the moment one of its members reviews. Three sources
// feed it, cheapest first:
//
//   - the teams still listed on the PR object, which cost nothing;
//   - the teams a previous cycle already recorded for this PR, which is why a
//     team that approved yesterday is still shown (in green) today;
//   - the PR's full review-request history from the timeline, the only source
//     that can recover a team whose review was already satisfied before we
//     first saw the PR. That one is a GraphQL call, so it is made once per PR
//     — on first sight, when there is nothing cached to build on.
//
// Returns a non-nil slice whenever it resolved anything, including the empty
// slice for a PR with no required reviewers: persisting that empty answer is
// what stops the next cycle asking GitHub for the history all over again.
func resolvePRTeamReviews(apiCalls *apiCallCounter, key PRKey,
	pr *github.PullRequest, reviews []*github.PullRequestReview) []git_tools.TeamReviewStatus {

	if pr == nil {
		return nil
	}

	cached, hasCachedRow := loadCachedTeamReviews(key)

	teams := []git_tools.TeamRef{}
	seen := map[string]bool{}
	addTeam := func(team git_tools.TeamRef) {
		if team.Slug == "" {
			return
		}
		if team.Org == "" {
			team.Org = key.Owner
		}
		if team.Name == "" {
			team.Name = team.Slug
		}
		if seen[team.Key()] {
			return
		}
		seen[team.Key()] = true
		teams = append(teams, team)
	}

	// Teams still awaiting review, straight off the PR object.
	pendingKeys := map[string]bool{}
	for _, team := range pr.RequestedTeams {
		if team == nil {
			continue
		}
		ref := git_tools.TeamRef{
			Name: team.GetName(),
			Slug: team.GetSlug(),
			Org:  team.GetOrganization().GetLogin(),
		}
		if ref.Org == "" {
			ref.Org = key.Owner
		}
		if ref.Slug == "" {
			continue
		}
		pendingKeys[ref.Key()] = true
		addTeam(ref)
	}

	// Teams (and a personal request) carried over from the last cycle.
	personal := false
	for _, entry := range cached {
		if entry.Personal {
			personal = true
			continue
		}
		addTeam(git_tools.TeamRef{Name: entry.Name, Slug: entry.Slug, Org: entry.Org})
	}

	myLogin := currentGitHubLogin()
	personalPending := myLogin != "" && requestedFrom(pr, myLogin)
	personal = personal || personalPending

	// First sight of this PR: ask for the full request history, which is the
	// only way to learn about requests GitHub has already cleared.
	if !hasCachedRow {
		apiCalls.TeamReviews.Add(1)
		history, err := git_tools.GetReviewRequestHistory(key.Owner, key.Repo, key.Number)
		if err != nil {
			// Nothing is written for this PR, so the next cycle asks again.
			// Storing the teams still listed on the PR instead would look like
			// a complete answer and stop the retry, quietly leaving out every
			// team whose review GitHub had already cleared.
			slog.Warn("Failed to fetch review request history; leaving this PR's required teams unresolved for now",
				"pr", key.Number, "repo", key.Repo, "error", err)
			return nil
		}
		for _, team := range history.Teams {
			addTeam(team)
		}
		if myLogin != "" && containsFold(history.Users, myLogin) {
			personal = true
		}
	}

	if len(teams) == 0 && !personal {
		return []git_tools.TeamReviewStatus{}
	}

	return git_tools.ResolveTeamReviews(git_tools.TeamReviewInput{
		Teams:           teams,
		PendingKeys:     pendingKeys,
		Members:         git_tools.GetTeamMembersForTeams(teams),
		MyTeamKeys:      myTeamKeys(),
		MyLogin:         myLogin,
		Personal:        personal,
		PersonalPending: personalPending,
		Decisions:       git_tools.LatestReviewDecisions(reviews),
	})
}

// loadCachedTeamReviews reads the statuses a previous cycle stored for a PR.
// The second return value distinguishes a cached empty answer from no row at
// all — see resolvePRTeamReviews for why that difference matters.
func loadCachedTeamReviews(key PRKey) ([]git_tools.TeamReviewStatus, bool) {
	db := config.C().DB
	if db == nil {
		return nil, false
	}
	raw, err := db.GetPRTeamReviews(key.Number, key.Repo)
	if err != nil {
		slog.Warn("Failed to read cached team reviews", "pr", key.Number, "repo", key.Repo, "error", err)
		return nil, false
	}
	if raw == "" {
		return nil, false
	}
	var cached []git_tools.TeamReviewStatus
	if err := json.Unmarshal([]byte(raw), &cached); err != nil {
		slog.Warn("Failed to decode cached team reviews", "pr", key.Number, "repo", key.Repo, "error", err)
		return nil, false
	}
	return cached, true
}

// currentGitHubLogin is who the review list is being built for: the configured
// username, or the identity behind the API token when the config leaves it
// unset. Empty when neither is available, which leaves every chip unhighlighted
// rather than highlighting the wrong ones.
func currentGitHubLogin() string {
	if login := config.C().GithubUsername; login != "" {
		return login
	}
	return git_tools.GetAuthenticatedLogin()
}

// myTeamKeys is the authenticated user's own teams as "org/slug", the same key
// TeamRef.Key() produces. It answers "is this my team" even for teams whose
// member list the token cannot read.
func myTeamKeys() map[string]bool {
	keys := map[string]bool{}
	for _, ref := range git_tools.GetMyTeamRefs() {
		keys[ref] = true
	}
	return keys
}

// requestedFrom reports whether login has an open personal review request on
// the PR (as opposed to being asked through one of their teams).
func requestedFrom(pr *github.PullRequest, login string) bool {
	for _, reviewer := range pr.RequestedReviewers {
		if reviewer != nil && strings.EqualFold(reviewer.GetLogin(), login) {
			return true
		}
	}
	return false
}

func containsFold(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if strings.EqualFold(candidate, needle) {
			return true
		}
	}
	return false
}
