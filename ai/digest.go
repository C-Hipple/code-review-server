package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"sort"
	"strconv"
)

// InputsDigest hashes the parts of a PR's discussion a feature reads beyond
// the code: every comment's ID and body, every review's state, and each review
// thread's resolved and outdated flags. The head SHA covers the code; together
// the two key a stored result, and a change to either makes it stale.
//
// It takes the raw cache entries (PRComments, PRReviews and PRReviewThreads)
// so the digest a result was computed from can be recomputed on any later read
// without fetching anything. Order within an entry doesn't matter. An absent
// entry and an empty one hash differently, so a cache filling in for the first
// time — review threads arriving after a GraphQL failure, say — counts as a
// change. Local (unsubmitted) comments and reactions are not inputs and are
// not hashed.
func InputsDigest(commentsJSON, reviewsJSON, threadsJSON string) string {
	h := sha256.New()
	digestSection(h, "comments", commentsJSON, func() ([]string, error) {
		var comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		err := json.Unmarshal([]byte(commentsJSON), &comments)
		entries := make([]string, 0, len(comments))
		for _, c := range comments {
			entries = append(entries, strconv.FormatInt(c.ID, 10)+"\x00"+c.Body)
		}
		return entries, err
	})
	digestSection(h, "reviews", reviewsJSON, func() ([]string, error) {
		var reviews []struct {
			ID    int64  `json:"id"`
			State string `json:"state"`
		}
		err := json.Unmarshal([]byte(reviewsJSON), &reviews)
		entries := make([]string, 0, len(reviews))
		for _, r := range reviews {
			entries = append(entries, strconv.FormatInt(r.ID, 10)+"\x00"+r.State)
		}
		return entries, err
	})
	digestSection(h, "threads", threadsJSON, func() ([]string, error) {
		var threads []struct {
			ID         string `json:"id"`
			IsResolved bool   `json:"is_resolved"`
			IsOutdated bool   `json:"is_outdated"`
		}
		err := json.Unmarshal([]byte(threadsJSON), &threads)
		entries := make([]string, 0, len(threads))
		for _, t := range threads {
			entries = append(entries, fmt.Sprintf("%s\x00%t\x00%t", t.ID, t.IsResolved, t.IsOutdated))
		}
		return entries, err
	})
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// digestSection writes one cache entry into h: absent, unreadable (hashed
// whole, so a change still changes the digest), or its sorted entries, each
// length-prefixed so no two different lists write the same bytes.
func digestSection(h hash.Hash, name, raw string, decode func() ([]string, error)) {
	if raw == "" {
		fmt.Fprintf(h, "%s absent\n", name)
		return
	}
	entries, err := decode()
	if err != nil {
		sum := sha256.Sum256([]byte(raw))
		fmt.Fprintf(h, "%s unreadable %x\n", name, sum)
		return
	}
	sort.Strings(entries)
	fmt.Fprintf(h, "%s %d\n", name, len(entries))
	for _, e := range entries {
		fmt.Fprintf(h, "%d:%s\n", len(e), e)
	}
}
