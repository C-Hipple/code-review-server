package git_tools

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v74/github"
)

// Which teams a PR needs a review from is a moving target on GitHub: the moment
// a member of a requested team submits a review, GitHub drops that team from
// the PR's requested_teams. REST alone therefore only ever answers "which teams
// are still waiting", which would make an approving team disappear from the
// review list rather than turn green. The timeline's REVIEW_REQUESTED_EVENTs
// keep the full history, so this file pairs that history with the PR's reviews
// to say where each required team actually stands.

const (
	// teamMembersTTL caches a team's member list. Membership changes on the
	// scale of weeks, and every PR in a cycle asks about the same handful of
	// teams, so this keeps the per-cycle cost at roughly one call per team.
	teamMembersTTL = 30 * time.Minute

	// teamMembersErrTTL caches a failed member lookup briefly. A token without
	// read:org can't list members at all, and retrying that per PR per cycle
	// would burn the budget for an answer that isn't coming.
	teamMembersErrTTL = 5 * time.Minute
)

// Review status of one required reviewer on a PR.
const (
	// TeamReviewPending — nobody from the team has reviewed yet.
	TeamReviewPending = "pending"
	// TeamReviewApproved — a member approved and nobody asked for changes.
	TeamReviewApproved = "approved"
	// TeamReviewChangesRequested — a member requested changes.
	TeamReviewChangesRequested = "changes_requested"
	// TeamReviewReviewed — GitHub has cleared the request (so somebody
	// reviewed) but we can't attribute it to a member: usually a token that
	// can't list the team's members, sometimes a member who has since left.
	TeamReviewReviewed = "reviewed"
)

// TeamRef identifies one GitHub team.
type TeamRef struct {
	// Name is the display name, e.g. "Platform Engineering".
	Name string `json:"name"`
	// Slug is the API name, e.g. "platform-engineering".
	Slug string `json:"slug"`
	// Org is the login of the organization that owns the team.
	Org string `json:"org"`
}

// Key is the "org/slug" form GitHub uses for team references, and the key both
// the member cache and GetMyTeamRefs are indexed by.
func (t TeamRef) Key() string {
	return t.Org + "/" + t.Slug
}

// TeamReviewStatus is one required reviewer as the review list displays it: a
// team, or the authenticated user personally, together with where its review
// stands and how it relates to whoever is looking at the list.
type TeamReviewStatus struct {
	// Name is the team's display name, or the user's login for a personal
	// request (Personal is what tells the two apart).
	Name string `json:"name"`
	Slug string `json:"slug"`
	Org  string `json:"org"`
	// Status is one of the TeamReview* constants above.
	Status string `json:"status"`
	// Mine is true when the authenticated user is on this team (always true
	// for a personal request), which is what makes a row's chip stand out.
	Mine bool `json:"mine"`
	// Personal marks the pseudo-entry for a review GitHub asked the
	// authenticated user for directly rather than through a team.
	Personal bool `json:"personal"`
}

// ReviewRequestHistory is every reviewer a PR has ever been assigned, including
// the ones GitHub has since cleared because they reviewed.
type ReviewRequestHistory struct {
	Teams []TeamRef
	Users []string
}

// reviewRequestHistoryQuery pages the PR timeline for review-request events.
// 100 events per page is GitHub's maximum; PRs rarely have more than a handful.
const reviewRequestHistoryQuery = `query($owner:String!, $repo:String!, $number:Int!, $after:String) {
  repository(owner:$owner, name:$repo) {
    pullRequest(number:$number) {
      timelineItems(first:100, after:$after, itemTypes:[REVIEW_REQUESTED_EVENT]) {
        pageInfo { hasNextPage endCursor }
        nodes {
          ... on ReviewRequestedEvent {
            requestedReviewer {
              __typename
              ... on User { login }
              ... on Team { name slug organization { login } }
            }
          }
        }
      }
    }
  }
}`

type reviewRequestHistoryResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				TimelineItems struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						RequestedReviewer struct {
							TypeName     string `json:"__typename"`
							Login        string `json:"login"`
							Name         string `json:"name"`
							Slug         string `json:"slug"`
							Organization struct {
								Login string `json:"login"`
							} `json:"organization"`
						} `json:"requestedReviewer"`
					} `json:"nodes"`
				} `json:"timelineItems"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// GetReviewRequestHistory returns every team and user ever asked to review the
// PR, deduplicated and in the order they were first requested. Errors are
// returned rather than swallowed; callers fall back to the teams still listed
// on the PR object, which is a subset of this.
func GetReviewRequestHistory(owner, repo string, number int) (ReviewRequestHistory, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	history := ReviewRequestHistory{}
	seenTeams := map[string]bool{}
	seenUsers := map[string]bool{}

	var after *string
	// Bound the paging so a malformed cursor response can't spin forever.
	for page := 0; page < 10; page++ {
		var parsed reviewRequestHistoryResponse
		err := runGraphQL(ctx, reviewRequestHistoryQuery, map[string]any{
			"owner":  owner,
			"repo":   repo,
			"number": number,
			"after":  after,
		}, &parsed)
		if err != nil {
			return ReviewRequestHistory{}, err
		}

		conn := parsed.Data.Repository.PullRequest.TimelineItems
		for _, node := range conn.Nodes {
			reviewer := node.RequestedReviewer
			switch reviewer.TypeName {
			case "User":
				login := strings.ToLower(reviewer.Login)
				if login == "" || seenUsers[login] {
					continue
				}
				seenUsers[login] = true
				history.Users = append(history.Users, reviewer.Login)
			case "Team":
				if reviewer.Slug == "" {
					continue
				}
				team := TeamRef{
					Name: reviewer.Name,
					Slug: reviewer.Slug,
					Org:  reviewer.Organization.Login,
				}
				if team.Org == "" {
					// The org is only missing on malformed responses; fall back
					// to the repo's owner, which owns the team in practice.
					team.Org = owner
				}
				if team.Name == "" {
					team.Name = team.Slug
				}
				if seenTeams[team.Key()] {
					continue
				}
				seenTeams[team.Key()] = true
				history.Teams = append(history.Teams, team)
			}
		}

		if !conn.PageInfo.HasNextPage || conn.PageInfo.EndCursor == "" {
			return history, nil
		}
		cursor := conn.PageInfo.EndCursor
		after = &cursor
	}

	slog.Warn("Stopped paging review request history at the page cap", "owner", owner, "repo", repo, "pr", number)
	return history, nil
}

// GetTeamMembers returns the logins of everyone on org/slug, cached (see
// teamMembersTTL). The second return value reports whether the answer is known:
// a token without read:org gets (nil, false) rather than an empty team, which
// is the difference between "nobody is on this team" and "we can't tell".
func GetTeamMembers(org, slug string) ([]string, bool) {
	if org == "" || slug == "" || !HasToken() {
		return nil, false
	}

	cacheKey := "team_members:" + org + "/" + slug
	if val, found := GlobalCache.Get(cacheKey); found {
		members, ok := val.([]string)
		return members, ok && members != nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	members, err := ListAllPages("teams/members", func(opts *github.ListOptions) ([]*github.User, *github.Response, error) {
		return GetGithubClient().Teams.ListTeamMembersBySlug(ctx, org, slug,
			&github.TeamListTeamMembersOptions{ListOptions: *opts})
	})
	if err != nil {
		slog.Warn("Failed to list team members; that team's review status will show as reviewed rather than approved",
			"org", org, "team", slug, "error", err)
		// A nil value caches the failure: Get finds it and reports "unknown".
		GlobalCache.Set(cacheKey, []string(nil), teamMembersErrTTL)
		return nil, false
	}

	logins := []string{}
	for _, member := range members {
		if login := member.GetLogin(); login != "" {
			logins = append(logins, login)
		}
	}
	GlobalCache.Set(cacheKey, logins, teamMembersTTL)
	return logins, true
}

// GetTeamMembersForTeams resolves the member lists of several teams at once,
// keyed by TeamRef.Key(). Teams whose membership could not be read are absent
// from the map rather than present-and-empty, so ResolveTeamReviews can tell
// "no members" from "not readable".
func GetTeamMembersForTeams(teams []TeamRef) map[string][]string {
	members := map[string][]string{}
	for _, team := range teams {
		if _, done := members[team.Key()]; done {
			continue
		}
		if logins, known := GetTeamMembers(team.Org, team.Slug); known {
			members[team.Key()] = logins
		}
	}
	return members
}

// LatestReviewDecisions maps each reviewer's login to their standing decision:
// "APPROVED" or "CHANGES_REQUESTED". Reviews that carry no decision (a plain
// comment) leave an earlier decision in place — that is how GitHub itself
// treats them — and a dismissal drops it. Logins are lowercased so lookups can
// ignore the casing GitHub is inconsistent about.
func LatestReviewDecisions(reviews []*github.PullRequestReview) map[string]string {
	decisions := map[string]string{}
	for _, review := range reviews {
		if review == nil {
			continue
		}
		login := strings.ToLower(review.User.GetLogin())
		if login == "" {
			continue
		}
		switch strings.ToUpper(review.GetState()) {
		case "APPROVED":
			decisions[login] = "APPROVED"
		case "CHANGES_REQUESTED":
			decisions[login] = "CHANGES_REQUESTED"
		case "DISMISSED":
			delete(decisions, login)
		}
	}
	return decisions
}

// TeamReviewInput is everything ResolveTeamReviews needs to place each required
// reviewer. It is deliberately all data — the fetching lives in the workflow
// layer — so the placement rules can be tested without touching GitHub.
type TeamReviewInput struct {
	// Teams is every team ever asked to review, the union of the PR's current
	// requested_teams and the teams a previous cycle or the timeline recorded.
	Teams []TeamRef
	// PendingKeys holds the TeamRef.Key() of the teams GitHub still lists as
	// awaiting review. A team outside this set has had its request cleared.
	PendingKeys map[string]bool
	// Members maps TeamRef.Key() to member logins. A missing key means the
	// membership could not be read, not that the team is empty.
	Members map[string][]string
	// MyTeamKeys holds the "org/slug" refs of the authenticated user's own
	// teams (GetMyTeamRefs), so a team still counts as mine when its member
	// list is unreadable.
	MyTeamKeys map[string]bool
	// MyLogin is the authenticated user. Empty means "identity unknown", which
	// leaves every entry unhighlighted rather than guessing.
	MyLogin string
	// Personal is true when the user was asked to review directly rather than
	// through a team, and PersonalPending true while that request is open.
	Personal        bool
	PersonalPending bool
	// Decisions is LatestReviewDecisions over the PR's reviews.
	Decisions map[string]string
}

// ResolveTeamReviews places every required reviewer of a PR. The personal
// request, when there is one, leads; teams the user belongs to come next, and
// the rest follow alphabetically, so the entries a reviewer is accountable for
// are the ones they read first.
func ResolveTeamReviews(in TeamReviewInput) []TeamReviewStatus {
	statuses := []TeamReviewStatus{}

	if in.Personal && in.MyLogin != "" {
		statuses = append(statuses, TeamReviewStatus{
			Name:     in.MyLogin,
			Status:   decisionStatus(in.Decisions[strings.ToLower(in.MyLogin)], in.PersonalPending),
			Mine:     true,
			Personal: true,
		})
	}

	teams := make([]TeamReviewStatus, 0, len(in.Teams))
	seen := map[string]bool{}
	for _, team := range in.Teams {
		if team.Slug == "" || seen[team.Key()] {
			continue
		}
		seen[team.Key()] = true

		name := team.Name
		if name == "" {
			name = team.Slug
		}
		members, known := in.Members[team.Key()]
		teams = append(teams, TeamReviewStatus{
			Name:   name,
			Slug:   team.Slug,
			Org:    team.Org,
			Status: teamStatus(members, known, in.Decisions, in.PendingKeys[team.Key()]),
			Mine:   in.MyLogin != "" && (in.MyTeamKeys[team.Key()] || containsFold(members, in.MyLogin)),
		})
	}

	sort.SliceStable(teams, func(i, j int) bool {
		if teams[i].Mine != teams[j].Mine {
			return teams[i].Mine
		}
		return strings.ToLower(teams[i].Name) < strings.ToLower(teams[j].Name)
	})

	return append(statuses, teams...)
}

// teamStatus resolves one team's standing. A member who asked for changes wins
// over one who approved: the PR still needs work either way.
func teamStatus(members []string, membersKnown bool, decisions map[string]string, pending bool) string {
	if membersKnown {
		approved := false
		for _, member := range members {
			switch decisions[strings.ToLower(member)] {
			case "CHANGES_REQUESTED":
				return TeamReviewChangesRequested
			case "APPROVED":
				approved = true
			}
		}
		if approved {
			return TeamReviewApproved
		}
	}
	if pending {
		return TeamReviewPending
	}
	// GitHub cleared the request without a review we can attribute to the team:
	// either its membership is unreadable, or the reviewer has since left it.
	return TeamReviewReviewed
}

// decisionStatus maps the authenticated user's own standing decision, falling
// back to whether their review request is still open.
func decisionStatus(decision string, pending bool) string {
	switch decision {
	case "APPROVED":
		return TeamReviewApproved
	case "CHANGES_REQUESTED":
		return TeamReviewChangesRequested
	}
	if pending {
		return TeamReviewPending
	}
	return TeamReviewReviewed
}

func containsFold(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if strings.EqualFold(candidate, needle) {
			return true
		}
	}
	return false
}
