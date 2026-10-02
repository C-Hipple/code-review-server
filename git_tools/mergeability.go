package git_tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/go-github/v74/github"
)

// Whether a pull request can merge without conflicts is something GitHub works
// out itself, by test-merging the head into the base. Over REST only the
// single-PR endpoint reports it (`mergeable`); the list endpoint most workflows
// draw their PRs from leaves it out, so REST would cost a call per PR on the
// review list, every cycle — the answer changes whenever the base branch moves,
// with nothing on the PR itself changing. GraphQL's `mergeable` is the same
// answer, and one request asks about a whole batch of PRs.
//
// GitHub computes the answer lazily. Asking about a PR whose head or base has
// moved since it last checked starts the computation and comes back unknown;
// the answer is there a few seconds later.

// A PR's mergeability, as GraphQL's MergeableState reports it.
const (
	// MergeabilityMergeable means the head merges into the base cleanly.
	MergeabilityMergeable = "mergeable"
	// MergeabilityConflicting means the head conflicts with the base: GitHub
	// won't merge the PR until the conflicts are resolved.
	MergeabilityConflicting = "conflicting"
	// MergeabilityUnknown means GitHub hasn't worked it out yet.
	MergeabilityUnknown = "unknown"
)

// Mergeability is GitHub's answer for one PR, together with the head commit it
// was given for: the answer is about that commit, so a push makes it stale.
type Mergeability struct {
	State   string
	HeadSHA string
}

// RESTMergeability reads the answer a PR fetched over REST already carries.
// Only the single-PR endpoint reports one, and it answers null while GitHub is
// still computing; a PR from the list endpoint never carries one. All of those
// read as unknown.
func RESTMergeability(pr *github.PullRequest) string {
	if pr == nil || pr.Mergeable == nil {
		return MergeabilityUnknown
	}
	if *pr.Mergeable {
		return MergeabilityMergeable
	}
	return MergeabilityConflicting
}

// mergeabilityBatchSize caps how many PRs one request asks about. Each is a
// plain lookup with no connection to page through, so a request costs a single
// point of the GraphQL budget however many it holds; the cap keeps a long
// review list's request well inside GitHub's per-request time limit.
const mergeabilityBatchSize = 50

// GetMergeability asks GitHub whether each PR merges cleanly, in batches of
// mergeabilityBatchSize.
//
// The result has an entry for every PR GitHub answered. One it could not
// resolve — deleted, transferred, or in a repository the token can no longer
// read — is simply absent: GitHub fails that lookup on its own and answers the
// rest, so one dead row on a review list can't keep the others from being
// checked. The error is for a request that answered nothing, such as a
// rate-limited one; the result then still holds the batches answered before it.
func GetMergeability(refs []PRRef) (map[PRRef]Mergeability, error) {
	refs = DedupeRefs(refs)
	answers := make(map[PRRef]Mergeability, len(refs))
	for start := 0; start < len(refs); start += mergeabilityBatchSize {
		end := min(start+mergeabilityBatchSize, len(refs))
		if err := getMergeabilityBatch(refs[start:end], answers); err != nil {
			return answers, err
		}
	}
	return answers, nil
}

type mergeabilityResponse struct {
	// Data is keyed by each PR's alias (mergeabilityAlias). A lookup that
	// failed is null, at the repository or the pull request, with the reason
	// in Errors.
	Data map[string]*struct {
		PullRequest *struct {
			HeadRefOid string `json:"headRefOid"`
			Mergeable  string `json:"mergeable"`
		} `json:"pullRequest"`
	} `json:"data"`
	Errors []graphQLError `json:"errors"`
}

func getMergeabilityBatch(refs []PRRef, answers map[PRRef]Mergeability) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query, variables := mergeabilityQuery(refs)
	body, err := postGraphQL(ctx, query, variables)
	if err != nil {
		return err
	}
	var parsed mergeabilityResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return err
	}
	if parsed.Data == nil {
		if err := errorFromGraphQL(parsed.Errors); err != nil {
			return err
		}
		return fmt.Errorf("github graphql returned no data")
	}
	if len(parsed.Errors) > 0 {
		slog.Warn("GitHub could not report mergeability for some PRs",
			"asked", len(refs), "error", errorFromGraphQL(parsed.Errors))
	}

	for i, ref := range refs {
		repo := parsed.Data[mergeabilityAlias(i)]
		if repo == nil || repo.PullRequest == nil {
			continue
		}
		answers[ref] = Mergeability{
			State:   mergeabilityState(repo.PullRequest.Mergeable),
			HeadSHA: repo.PullRequest.HeadRefOid,
		}
	}
	return nil
}

// mergeabilityQuery builds one batch's query: a repository lookup per PR, each
// under its own alias, with every argument passed as a variable.
func mergeabilityQuery(refs []PRRef) (string, map[string]any) {
	params := make([]string, 0, len(refs))
	var fields strings.Builder
	variables := make(map[string]any, 3*len(refs))
	for i, ref := range refs {
		params = append(params, fmt.Sprintf("$owner%d:String!, $name%d:String!, $number%d:Int!", i, i, i))
		fmt.Fprintf(&fields,
			"  %s: repository(owner:$owner%d, name:$name%d) { pullRequest(number:$number%d) { headRefOid mergeable } }\n",
			mergeabilityAlias(i), i, i, i)
		variables[fmt.Sprintf("owner%d", i)] = ref.Owner
		variables[fmt.Sprintf("name%d", i)] = ref.Repo
		variables[fmt.Sprintf("number%d", i)] = ref.Number
	}
	return fmt.Sprintf("query(%s) {\n%s}", strings.Join(params, ", "), fields.String()), variables
}

func mergeabilityAlias(i int) string {
	return fmt.Sprintf("pr%d", i)
}

// mergeabilityState maps GraphQL's MergeableState onto the constants above.
// Anything GitHub adds later reads as unknown rather than being taken for an
// answer.
func mergeabilityState(state string) string {
	switch state {
	case "MERGEABLE":
		return MergeabilityMergeable
	case "CONFLICTING":
		return MergeabilityConflicting
	}
	return MergeabilityUnknown
}
