package git_tools

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// GitHub's REST API reports reactions on a comment only as per-emoji totals
// (`reactions: {"+1": 3}`), and answering "did anyone actually acknowledge my
// comment?" needs the logins behind those totals. Who reacted is a separate
// REST endpoint per comment, which is one API call per comment on the PR; the
// GraphQL `reactionGroups` field carries it inline, so this file gets every
// reaction on every comment and review of a PR in one call.

// Reaction is one emoji's worth of reactions on a single comment or review:
// which emoji it is, and who left it.
type Reaction struct {
	// Content is the REST-style name of the emoji ("+1", "heart", "rocket"),
	// not GraphQL's THUMBS_UP spelling, so it matches what the rest of GitHub's
	// API — and anything a client already knows about reactions — calls it.
	Content string `json:"content"`
	// Emoji is the character itself. It lives on the wire so the web client and
	// the Emacs client don't each carry their own copy of the mapping.
	Emoji string `json:"emoji"`
	// Users are the logins that left this reaction, capped at
	// reactionUsersPerGroup. Count is GitHub's own total, so Count >
	// len(Users) means the list was truncated.
	Users []string `json:"users"`
	Count int      `json:"count"`
	// ViewerReacted is true when the account behind CRS_GITHUB_TOKEN is one of
	// the reactors, which is what lets a client render its own reaction
	// differently from everyone else's.
	ViewerReacted bool `json:"viewer_reacted"`
}

// PRReactions is every reaction on a PR's comments and reviews, keyed by the
// REST database ID of the thing that was reacted to (as a string, matching
// CommentJSON.ID on the wire).
//
// Comments covers both review comments and conversation comments, which is the
// same single ID space the comment caches already merge them into.
type PRReactions struct {
	Comments map[string][]Reaction `json:"comments"`
	Reviews  map[string][]Reaction `json:"reviews"`
}

// Empty reports whether nothing on the PR has been reacted to. A non-nil but
// empty result is a successful fetch, not a missing one.
func (r *PRReactions) Empty() bool {
	return r == nil || (len(r.Comments) == 0 && len(r.Reviews) == 0)
}

// ForComment returns the reactions on one comment, by its REST ID.
func (r *PRReactions) ForComment(id string) []Reaction {
	if r == nil {
		return nil
	}
	return r.Comments[id]
}

// ForReview returns the reactions on one review body, by its REST ID.
func (r *PRReactions) ForReview(id int64) []Reaction {
	if r == nil {
		return nil
	}
	return r.Reviews[strconv.FormatInt(id, 10)]
}

// reactionContentNames maps GraphQL's ReactionContent enum onto the names REST
// uses. Anything GitHub adds later falls through with its enum value
// lowercased and no emoji rather than being dropped.
var reactionContentNames = map[string]struct {
	name  string
	emoji string
}{
	"THUMBS_UP":   {"+1", "👍"},
	"THUMBS_DOWN": {"-1", "👎"},
	"LAUGH":       {"laugh", "😄"},
	"HOORAY":      {"hooray", "🎉"},
	"CONFUSED":    {"confused", "😕"},
	"HEART":       {"heart", "❤️"},
	"ROCKET":      {"rocket", "🚀"},
	"EYES":        {"eyes", "👀"},
}

// How much of each connection one request asks for. Every review comment on a
// PR belongs to a review, so paging `reviews` and their nested `comments`
// covers the inline comments, and `comments` covers the conversation ones. A
// review carrying more than reactionCommentsPerPage comments loses reaction
// info for the overflow, the same trade GetReviewThreads makes.
const (
	reactionReviewsPerPage  = 50
	reactionCommentsPerPage = 50
	// A reaction with more reactors than this reports the overflow through
	// Count. Nobody needs the 21st name in a tooltip.
	reactionUsersPerGroup = 20
	// Pagination bound, matching GetReviewThreads: enough for any real PR,
	// and a guard against a malformed cursor response spinning forever.
	reactionMaxPages = 20
)

// reactionGroupsFragment is repeated at each of the three places a reactable
// appears in the query.
var reactionGroupsFragment = fmt.Sprintf(
	"reactionGroups { content viewerHasReacted users(first:%d) { totalCount nodes { login } } }",
	reactionUsersPerGroup)

var reactionsQuery = fmt.Sprintf(`query($owner:String!, $repo:String!, $number:Int!, $reviewsAfter:String, $commentsAfter:String) {
  repository(owner:$owner, name:$repo) {
    pullRequest(number:$number) {
      reviews(first:%d, after:$reviewsAfter) {
        pageInfo { hasNextPage endCursor }
        nodes {
          databaseId
          %s
          comments(first:%d) {
            nodes { databaseId %s }
          }
        }
      }
      comments(first:%d, after:$commentsAfter) {
        pageInfo { hasNextPage endCursor }
        nodes { databaseId %s }
      }
    }
  }
}`, reactionReviewsPerPage, reactionGroupsFragment,
	reactionCommentsPerPage, reactionGroupsFragment,
	reactionCommentsPerPage, reactionGroupsFragment)

type reactionGroupNode struct {
	Content          string `json:"content"`
	ViewerHasReacted bool   `json:"viewerHasReacted"`
	Users            struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"users"`
}

type reactableNode struct {
	DatabaseID     *int64              `json:"databaseId"`
	ReactionGroups []reactionGroupNode `json:"reactionGroups"`
}

type reactionsPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type reactionsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				Reviews struct {
					PageInfo reactionsPageInfo `json:"pageInfo"`
					Nodes    []struct {
						reactableNode
						Comments struct {
							Nodes []reactableNode `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviews"`
				Comments struct {
					PageInfo reactionsPageInfo `json:"pageInfo"`
					Nodes    []reactableNode   `json:"nodes"`
				} `json:"comments"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// GetReactions returns every reaction on a PR's comments and reviews, with the
// logins behind each one. Errors are returned rather than swallowed; callers
// treat a failure as "no reaction info available" and still render the PR.
func GetReactions(owner, repo string, number int) (*PRReactions, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out := &PRReactions{
		Comments: map[string][]Reaction{},
		Reviews:  map[string][]Reaction{},
	}

	// The two connections are paged together in one query. A connection that
	// has run out keeps being asked for the page after its last cursor, which
	// comes back empty — cheaper in round trips than splitting them into
	// separate queries, and the maps dedupe by ID either way.
	var reviewsAfter, commentsAfter *string
	for page := 0; page < reactionMaxPages; page++ {
		var parsed reactionsResponse
		err := runGraphQL(ctx, reactionsQuery, map[string]any{
			"owner":         owner,
			"repo":          repo,
			"number":        number,
			"reviewsAfter":  reviewsAfter,
			"commentsAfter": commentsAfter,
		}, &parsed)
		if err != nil {
			return nil, err
		}

		pr := parsed.Data.Repository.PullRequest
		for _, review := range pr.Reviews.Nodes {
			addReactions(out.Reviews, review.reactableNode)
			for _, c := range review.Comments.Nodes {
				addReactions(out.Comments, c)
			}
		}
		for _, c := range pr.Comments.Nodes {
			addReactions(out.Comments, c)
		}

		if !pr.Reviews.PageInfo.HasNextPage && !pr.Comments.PageInfo.HasNextPage {
			return out, nil
		}
		reviewsAfter = nextCursor(reviewsAfter, pr.Reviews.PageInfo)
		commentsAfter = nextCursor(commentsAfter, pr.Comments.PageInfo)
	}

	slog.Warn("Stopped paging reactions at the page cap", "owner", owner, "repo", repo, "pr", number)
	return out, nil
}

// nextCursor advances a connection's cursor, holding the previous one when
// there is nothing more to fetch: re-asking for the page after the last cursor
// returns an empty list, while resetting to nil would re-read page one.
func nextCursor(current *string, info reactionsPageInfo) *string {
	if info.EndCursor == "" {
		return current
	}
	cursor := info.EndCursor
	return &cursor
}

// addReactions records a reactable's groups under its database ID. Nodes with
// no database ID (or no reactions at all) contribute nothing, so a comment
// nobody reacted to never appears in the map.
func addReactions(into map[string][]Reaction, node reactableNode) {
	if node.DatabaseID == nil || len(node.ReactionGroups) == 0 {
		return
	}
	reactions := make([]Reaction, 0, len(node.ReactionGroups))
	for _, g := range node.ReactionGroups {
		// GitHub returns a group per emoji whether or not anyone used it.
		if g.Users.TotalCount == 0 {
			continue
		}
		known, ok := reactionContentNames[g.Content]
		if !ok {
			known.name, known.emoji = strings.ToLower(g.Content), ""
		}
		users := make([]string, 0, len(g.Users.Nodes))
		for _, u := range g.Users.Nodes {
			if u.Login != "" {
				users = append(users, u.Login)
			}
		}
		reactions = append(reactions, Reaction{
			Content:       known.name,
			Emoji:         known.emoji,
			Users:         users,
			Count:         g.Users.TotalCount,
			ViewerReacted: g.ViewerHasReacted,
		})
	}
	if len(reactions) == 0 {
		return
	}
	into[strconv.FormatInt(*node.DatabaseID, 10)] = reactions
}
