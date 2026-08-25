package git_tools

import (
	"context"
	"log/slog"
	"time"
)

// GitHub's REST API does not report whether a review-comment thread has been
// resolved — that lives only in GraphQL's `reviewThreads` connection. This file
// fetches it so clients can show "resolved" alongside "outdated".

// ReviewThread is the resolution state of one review-comment thread, keyed back
// to REST by the database IDs of the comments it contains.
type ReviewThread struct {
	// ID is the GraphQL node ID of the thread (usable with resolveReviewThread).
	ID string `json:"id"`
	// IsResolved is true once someone marked the conversation resolved.
	IsResolved bool `json:"is_resolved"`
	// IsOutdated is GitHub's own judgement that the thread's lines no longer
	// exist in the current diff. It is tracked separately from the position-based
	// outdated flag the REST path computes, and is the more reliable of the two.
	IsOutdated bool `json:"is_outdated"`
	// ResolvedBy is the login of whoever resolved it, empty when unresolved.
	ResolvedBy string `json:"resolved_by"`
	Path       string `json:"path"`
	// CommentIDs are the REST/database IDs of every comment in the thread, in
	// posting order. These match CommentJSON.ID on the wire.
	CommentIDs []int64 `json:"comment_ids"`
}

// reviewThreadsQuery pages through a PR's review threads. 100 threads and 100
// comments per thread is GitHub's per-page maximum; threads paginate properly
// and a thread with more than 100 comments only loses resolution info for the
// overflow, which is not worth a second round of pagination.
const reviewThreadsQuery = `query($owner:String!, $repo:String!, $number:Int!, $after:String) {
  repository(owner:$owner, name:$repo) {
    pullRequest(number:$number) {
      reviewThreads(first:100, after:$after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          isOutdated
          path
          resolvedBy { login }
          comments(first:100) { nodes { databaseId } }
        }
      }
    }
  }
}`

type reviewThreadsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						ID         string `json:"id"`
						IsResolved bool   `json:"isResolved"`
						IsOutdated bool   `json:"isOutdated"`
						Path       string `json:"path"`
						ResolvedBy *struct {
							Login string `json:"login"`
						} `json:"resolvedBy"`
						Comments struct {
							Nodes []struct {
								DatabaseID *int64 `json:"databaseId"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// GetReviewThreads returns the resolution state of every review thread on a PR.
// Errors are returned rather than swallowed; callers treat a failure as "no
// resolution info available" and still render the PR.
func GetReviewThreads(owner, repo string, number int) ([]ReviewThread, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	threads := []ReviewThread{}
	var after *string
	// Bound the paging so a malformed cursor response can't spin forever.
	for page := 0; page < 20; page++ {
		var parsed reviewThreadsResponse
		err := runGraphQL(ctx, reviewThreadsQuery, map[string]any{
			"owner":  owner,
			"repo":   repo,
			"number": number,
			"after":  after,
		}, &parsed)
		if err != nil {
			return nil, err
		}

		conn := parsed.Data.Repository.PullRequest.ReviewThreads
		for _, n := range conn.Nodes {
			t := ReviewThread{
				ID:         n.ID,
				IsResolved: n.IsResolved,
				IsOutdated: n.IsOutdated,
				Path:       n.Path,
			}
			if n.ResolvedBy != nil {
				t.ResolvedBy = n.ResolvedBy.Login
			}
			for _, c := range n.Comments.Nodes {
				if c.DatabaseID != nil {
					t.CommentIDs = append(t.CommentIDs, *c.DatabaseID)
				}
			}
			threads = append(threads, t)
		}

		if !conn.PageInfo.HasNextPage || conn.PageInfo.EndCursor == "" {
			return threads, nil
		}
		cursor := conn.PageInfo.EndCursor
		after = &cursor
	}

	slog.Warn("Stopped paging review threads at the page cap", "owner", owner, "repo", repo, "pr", number)
	return threads, nil
}
