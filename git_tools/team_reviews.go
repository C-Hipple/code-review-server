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
// review list rather than turn green. The PR's review_requested events keep the
// full history — REST lists them among the issue events, GraphQL in the
// timeline — so this file pairs that history with the PR's reviews to say
// where each required team actually stands.

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
// PR, deduplicated and in the order they were first requested, from whichever
// API RouteFor picks. Errors are returned rather than swallowed; callers fall
// back to the teams still listed on the PR object, which is a subset of this.
func GetReviewRequestHistory(owner, repo string, number int) (ReviewRequestHistory, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if RouteFor(LookupReviewRequestHistory) == GraphQL {
		return reviewRequestHistoryGraphQL(ctx, owner, repo, number)
	}
	client, err := newRESTClient()
	if err != nil {
		return ReviewRequestHistory{}, err
	}
	return reviewRequestHistoryREST(ctx, client, owner, repo, number)
}

// reviewRequestHistoryREST reads the history from the PR's issue events, which
// record each review request as a review_requested event naming the user or
// team asked. Events list oldest first, so each reviewer is met in the order
// they were first requested.
func reviewRequestHistoryREST(ctx context.Context, client *github.Client, owner, repo string, number int) (ReviewRequestHistory, error) {
	events, err := ListAllPages("issues/events", func(opts *github.ListOptions) ([]*github.IssueEvent, *github.Response, error) {
		return client.Issues.ListIssueEvents(ctx, owner, repo, number, opts)
	})
	if err != nil {
		return ReviewRequestHistory{}, err
	}

	history := newReviewRequestCollector(owner)
	for _, event := range events {
		if event.GetEvent() != "review_requested" {
			continue
		}
		if user := event.RequestedReviewer; user != nil {
			history.addUser(user.GetLogin())
		}
		if team := event.RequestedTeam; team != nil {
			history.addTeam(TeamRef{Name: team.GetName(), Slug: team.GetSlug(), Org: teamOrg(team)})
		}
	}
	return history.history, nil
}

// teamOrg is the login of the organization that owns team. The team an issue
// event names leaves its organization out, but its html_url —
// https://github.com/orgs/<org>/teams/<slug> — carries it.
func teamOrg(team *github.Team) string {
	if org := team.GetOrganization().GetLogin(); org != "" {
		return org
	}
	_, path, ok := strings.Cut(team.GetHTMLURL(), "/orgs/")
	if !ok {
		return ""
	}
	org, _, _ := strings.Cut(path, "/")
	return org
}

// reviewRequestHistoryGraphQL pages the PR timeline's review-request events.
func reviewRequestHistoryGraphQL(ctx context.Context, owner, repo string, number int) (ReviewRequestHistory, error) {
	history := newReviewRequestCollector(owner)

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
				history.addUser(reviewer.Login)
			case "Team":
				history.addTeam(TeamRef{
					Name: reviewer.Name,
					Slug: reviewer.Slug,
					Org:  reviewer.Organization.Login,
				})
			}
		}

		if !conn.PageInfo.HasNextPage || conn.PageInfo.EndCursor == "" {
			return history.history, nil
		}
		cursor := conn.PageInfo.EndCursor
		after = &cursor
	}

	slog.Warn("Stopped paging review request history at the page cap", "owner", owner, "repo", repo, "pr", number)
	return history.history, nil
}

// reviewRequestCollector builds a ReviewRequestHistory from review requests
// taken in the order they were made, keeping each reviewer once. Both APIs'
// implementations feed it, so they agree on what counts as the same reviewer.
type reviewRequestCollector struct {
	owner     string
	history   ReviewRequestHistory
	seenTeams map[string]bool
	seenUsers map[string]bool
}

func newReviewRequestCollector(owner string) *reviewRequestCollector {
	return &reviewRequestCollector{owner: owner, seenTeams: map[string]bool{}, seenUsers: map[string]bool{}}
}

func (c *reviewRequestCollector) addUser(login string) {
	key := strings.ToLower(login)
	if key == "" || c.seenUsers[key] {
		return
	}
	c.seenUsers[key] = true
	c.history.Users = append(c.history.Users, login)
}

func (c *reviewRequestCollector) addTeam(team TeamRef) {
	if team.Slug == "" {
		return
	}
	if team.Org == "" {
		// A reply that doesn't name the team's org is malformed, or a REST
		// one without the usual html_url; the repo's owner owns the team in
		// practice.
		team.Org = c.owner
	}
	if team.Name == "" {
		team.Name = team.Slug
	}
	if c.seenTeams[team.Key()] {
		return
	}
	c.seenTeams[team.Key()] = true
	c.history.Teams = append(c.history.Teams, team)
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
