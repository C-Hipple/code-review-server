package filtereddiff

import (
	"encoding/json"
	"fmt"
	"time"
)

// The shapes below are the parts of the DB cache rows a filter reads: the
// PRReviews row (server.ReviewJSON), the PRComments row (GitHub's review
// comments, with conversation comments converted to the same shape) and the
// PRCommits row (server.CommitJSON plus the workflow layer's committed_at).

type cachedReview struct {
	ID          int64     `json:"id"`
	User        string    `json:"user"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
	CommitID    string    `json:"commit_id"`
}

type cachedComment struct {
	ID   int64 `json:"id"`
	User *struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt        *time.Time `json:"created_at"`
	CommitID         string     `json:"commit_id"`
	OriginalCommitID string     `json:"original_commit_id"`
}

type cachedCommit struct {
	SHA         string `json:"sha"`
	Date        string `json:"date"`
	CommittedAt string `json:"committed_at"`
}

// InputsFromCaches builds a filter's inputs from the raw cache rows. An empty
// row reads as nothing cached; a row that doesn't decode is an error, since a
// filter judging from part of the picture would pick the wrong baseline.
func InputsFromCaches(me, headSHA, reviewsJSON, commentsJSON, commitsJSON string) (Inputs, error) {
	in := Inputs{Me: me, HeadSHA: headSHA}

	if reviewsJSON != "" {
		var reviews []cachedReview
		if err := json.Unmarshal([]byte(reviewsJSON), &reviews); err != nil {
			return Inputs{}, fmt.Errorf("decoding cached reviews: %w", err)
		}
		for _, r := range reviews {
			in.Reviews = append(in.Reviews, Review(r))
		}
	}

	if commentsJSON != "" {
		var comments []cachedComment
		if err := json.Unmarshal([]byte(commentsJSON), &comments); err != nil {
			return Inputs{}, fmt.Errorf("decoding cached comments: %w", err)
		}
		for _, c := range comments {
			comment := Comment{ID: c.ID}
			if c.User != nil {
				comment.Author = c.User.Login
			}
			if c.CreatedAt != nil {
				comment.CreatedAt = *c.CreatedAt
			}
			// The original commit is the one the comment was left on; GitHub
			// moves commit_id along as the PR advances under a comment whose
			// line survives, which would make it look newer than it is.
			comment.CommitID = c.OriginalCommitID
			if comment.CommitID == "" {
				comment.CommitID = c.CommitID
			}
			in.Comments = append(in.Comments, comment)
		}
	}

	if commitsJSON != "" {
		var commits []cachedCommit
		if err := json.Unmarshal([]byte(commitsJSON), &commits); err != nil {
			return Inputs{}, fmt.Errorf("decoding cached commits: %w", err)
		}
		for _, c := range commits {
			commit := Commit{SHA: c.SHA}
			for _, raw := range []string{c.CommittedAt, c.Date} {
				if t, err := time.Parse(time.RFC3339, raw); err == nil {
					commit.At = t
					break
				}
			}
			in.Commits = append(in.Commits, commit)
		}
	}

	return in, nil
}
