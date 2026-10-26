package server

import (
	"context"
	"crs/config"
	"crs/database"
	"crs/filtereddiff"
	"crs/git_tools"
	"crs/utils"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Filtered diffs narrow a PR's diff to the commits after a baseline the
// filter picks (filtereddiff): the changes since the reviewer's last review,
// or since they last took part. Picking the baseline reads the DB caches
// only; the diff from it to the head is one GitHub compare, stored in
// PRFilteredDiffs keyed by the commit pair. The post-update hooks warm every
// filter's diff in the background whenever a PR is fetched, updated or opened
// (warmFilteredDiffs), so GetFilteredDiff is a cache read — it computes a
// diff itself only when a client asks before the warm has stored it.

const (
	// filteredDiffTimeout bounds the GitHub calls behind one diff.
	filteredDiffTimeout = 60 * time.Second
	// filteredDiffErrorRetry is how long a failed diff is served as failed
	// before the next request tries again, so a PR that GitHub won't compare
	// doesn't cost a request on every open.
	filteredDiffErrorRetry = 2 * time.Minute
	// maxConcurrentFilteredDiffs caps the compares in flight: a workflow cycle
	// that picks up many updated PRs warms them all at once.
	maxConcurrentFilteredDiffs = 4
)

// rangeDiffSource is the GitHub seam the filtered diffs fetch through; tests
// swap it.
type rangeDiffSource interface {
	// Diff returns the raw unified diff from base to head, or
	// git_tools.ErrNoCommonHistory when GitHub can't relate them.
	Diff(ctx context.Context, owner, repo, base, head string) (string, error)
	// Status reports how head relates to base, as git_tools.CompareStatus.
	Status(ctx context.Context, owner, repo, base, head string) (string, error)
}

var filteredDiffSource rangeDiffSource = githubCompare{}

type githubCompare struct{}

var errNoToken = errors.New("no GitHub token is configured")

func (githubCompare) Diff(ctx context.Context, owner, repo, base, head string) (string, error) {
	if !git_tools.HasToken() {
		return "", errNoToken
	}
	return git_tools.CompareDiff(ctx, git_tools.GetGithubClient(), owner, repo, base, head)
}

func (githubCompare) Status(ctx context.Context, owner, repo, base, head string) (string, error) {
	if !git_tools.HasToken() {
		return "", errNoToken
	}
	return git_tools.CompareStatus(ctx, git_tools.GetGithubClient(), owner, repo, base, head)
}

// GetFilteredDiff returns the PR's diff narrowed by the filter registered
// under filterID. Everything but an unknown filter is an answer rather than an
// error — no baseline, a rewritten history, a failed fetch — carried in the
// result's Status and Message. repo is the short repo name.
func GetFilteredDiff(owner, repo string, number int, filterID string) (filtereddiff.Result, error) {
	f, ok := filtereddiff.Lookup(filterID)
	if !ok {
		return filtereddiff.Result{}, fmt.Errorf("unknown diff filter %q", filterID)
	}
	db := config.C().DB
	in, err := filteredDiffInputs(db, repo, number)
	return filteredDiff(db, owner, repo, number, f, in, err), nil
}

// filteredDiff answers for one filter from inputs already read (or the error
// reading them), so a caller resolving every filter reads the caches once.
func filteredDiff(db *database.DB, owner, repo string, number int, f filtereddiff.Filter, in filtereddiff.Inputs, inputsErr error) filtereddiff.Result {
	result := filtereddiff.Result{Filter: f.ID(), Commits: []string{}}
	if inputsErr != nil {
		result.Status = filtereddiff.StatusError
		result.Message = inputsErr.Error()
		return result
	}
	result.HeadSHA = in.HeadSHA

	baseline, err := f.Baseline(in)
	var none *filtereddiff.NoBaselineError
	if errors.As(err, &none) {
		result.Status = filtereddiff.StatusNoBaseline
		result.Message = none.Reason
		return result
	}
	if err != nil {
		result.Status = filtereddiff.StatusError
		result.Message = err.Error()
		return result
	}
	result.Baseline = &baseline
	result.BaseSHA = baseline.SHA
	if after := in.CommitsAfter(baseline.SHA); after != nil {
		result.Commits = after
	}

	// Nothing pushed since: nothing to fetch.
	if baseline.SHA == in.HeadSHA {
		result.Status = filtereddiff.StatusReady
		return result
	}

	stored := rangeDiff(db, owner, repo, number, in, baseline.SHA)
	result.Status = stored.Status
	result.Message = stored.Message
	result.ComputedAt = stored.UpdatedAt
	if stored.Status == filtereddiff.StatusReady {
		result.Diff = renderFilteredDiff(stored.Diff, owner, repo, number, in.HeadSHA)
	}
	return result
}

// filteredDiffInputs reads what the filters judge from: the PR's head and its
// cached reviews, comments and commits, plus who the reviewer is.
func filteredDiffInputs(db *database.DB, repo string, number int) (filtereddiff.Inputs, error) {
	if db == nil {
		return filtereddiff.Inputs{}, errors.New("the server has no database")
	}
	_, head, err := db.GetPullRequest(number, repo)
	if err != nil {
		return filtereddiff.Inputs{}, fmt.Errorf("reading the PR's head commit: %w", err)
	}
	if head == "" {
		return filtereddiff.Inputs{}, errors.New("the PR's head commit isn't cached yet; open or sync the PR first")
	}
	reviews, err := db.GetPRReviews(number, repo)
	if err != nil {
		return filtereddiff.Inputs{}, fmt.Errorf("reading the PR's reviews: %w", err)
	}
	comments, err := db.GetPRComments(number, repo)
	if err != nil {
		return filtereddiff.Inputs{}, fmt.Errorf("reading the PR's comments: %w", err)
	}
	commits, err := db.GetPRCommits(number, repo)
	if err != nil {
		return filtereddiff.Inputs{}, fmt.Errorf("reading the PR's commits: %w", err)
	}
	return filtereddiff.InputsFromCaches(config.C().GithubUsername, head, reviews, comments, commits)
}

// rangeDiff returns the PR's diff from base to its head: the stored one when
// there is one, else a fresh one, which it stores. A stored failure stands for
// filteredDiffErrorRetry before it is tried again.
func rangeDiff(db *database.DB, owner, repo string, number int, in filtereddiff.Inputs, base string) database.FilteredDiff {
	stored, ok, err := db.GetFilteredDiff(owner, repo, number, base, in.HeadSHA)
	if err != nil {
		slog.Warn("Error reading a stored filtered diff", "repo", repo, "pr", number, "error", err)
	}
	if ok && (stored.Status != filtereddiff.StatusError || time.Since(stored.UpdatedAt) < filteredDiffErrorRetry) {
		return stored
	}
	key := fmt.Sprintf("%s/%s#%d@%s..%s", owner, repo, number, base, in.HeadSHA)
	return rangeDiffFlights.do(key, func() database.FilteredDiff {
		return computeRangeDiff(db, owner, repo, number, in, base)
	})
}

// rangeDiffSlots holds one token per compare allowed in flight.
var rangeDiffSlots = make(chan struct{}, maxConcurrentFilteredDiffs)

// computeRangeDiff fetches the PR's diff from base to its head and stores it.
//
// GitHub's compare diffs head against its merge base with base, which is the
// diff wanted only while base is one of head's ancestors. The cached commit
// list answers that for free; a base missing from it is asked about, since
// the list stops at 250 commits, and one GitHub confirms is no longer in the
// history — a force-push rewrote it — is stored as rebased, with no diff.
func computeRangeDiff(db *database.DB, owner, repo string, number int, in filtereddiff.Inputs, base string) database.FilteredDiff {
	rangeDiffSlots <- struct{}{}
	defer func() { <-rangeDiffSlots }()

	ctx, cancel := context.WithTimeout(context.Background(), filteredDiffTimeout)
	defer cancel()

	row := database.FilteredDiff{BaseSHA: base, HeadSHA: in.HeadSHA}
	rebased := func() {
		row.Status = filtereddiff.StatusRebased
		row.Message = fmt.Sprintf("Commit %s is no longer part of this PR's history (it was force-pushed or rebased away), so there's no diff since it.", shortSHA(base))
	}

	if !in.ContainsCommit(base) {
		status, err := filteredDiffSource.Status(ctx, owner, repo, base, in.HeadSHA)
		switch {
		case err != nil:
			row.Status = filtereddiff.StatusError
			row.Message = fmt.Sprintf("Couldn't ask GitHub how %s relates to the head: %v", shortSHA(base), err)
		case status != git_tools.CompareAhead && status != git_tools.CompareIdentical:
			rebased()
		}
	}

	if row.Status == "" {
		diff, err := filteredDiffSource.Diff(ctx, owner, repo, base, in.HeadSHA)
		switch {
		case errors.Is(err, git_tools.ErrNoCommonHistory):
			rebased()
		case err != nil:
			row.Status = filtereddiff.StatusError
			row.Message = fmt.Sprintf("Couldn't fetch the diff from %s: %v", shortSHA(base), err)
		default:
			row.Status = filtereddiff.StatusReady
			row.Diff = diff
		}
	}

	if row.Status == filtereddiff.StatusError {
		slog.Warn("Filtered diff failed", "repo", repo, "pr", number, "base", base, "head", in.HeadSHA, "error", row.Message)
	} else {
		slog.Debug("Filtered diff computed", "repo", repo, "pr", number, "base", base, "head", in.HeadSHA, "status", row.Status, "bytes", len(row.Diff))
	}

	row.UpdatedAt = time.Now().UTC()
	if err := db.UpsertFilteredDiff(owner, repo, number, row); err != nil {
		slog.Error("Error storing a filtered diff", "repo", repo, "pr", number, "error", err)
	}
	return row
}

// renderFilteredDiff renders a stored diff the way GetPRDetails renders the
// PR's full one, so clients parse both alike.
func renderFilteredDiff(raw, owner, repo string, number int, head string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	parsed, err := utils.Parse(raw)
	if err != nil || parsed == nil {
		return raw
	}
	return formatDiff(parsed, owner, repo, number, head)
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// warmFilteredDiffs computes each registered filter's diff for the PR, so a
// client switching to one finds it stored. The caches are read once for all
// of them, and only a commit pair not stored yet reaches GitHub. It blocks,
// so callers run it in a goroutine.
func warmFilteredDiffs(owner, repo string, number int) {
	db := config.C().DB
	if db == nil {
		return
	}
	in, err := filteredDiffInputs(db, repo, number)
	if err != nil {
		// Nothing to warm from yet: the PR's caches aren't there.
		slog.Debug("Not warming filtered diffs", "repo", repo, "pr", number, "reason", err)
		return
	}
	for _, f := range filtereddiff.Filters() {
		filteredDiff(db, owner, repo, number, f, in, nil)
	}
}

// rangeDiffFlights shares one computation between callers asking for the same
// diff at once — the warm a client's open dispatches and the client's own
// request, typically.
var rangeDiffFlights flightGroup

type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flight
}

type flight struct {
	done chan struct{}
	row  database.FilteredDiff
}

func (g *flightGroup) do(key string, fn func() database.FilteredDiff) database.FilteredDiff {
	g.mu.Lock()
	if f, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-f.done
		return f.row
	}
	if g.calls == nil {
		g.calls = make(map[string]*flight)
	}
	f := &flight{done: make(chan struct{})}
	g.calls[key] = f
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		close(f.done)
	}()
	f.row = fn()
	return f.row
}

// filteredDiffForAI is the accessor AI features get on their request
// (ai.Request.FilteredDiff): the same diffs clients see, from the same cache.
func filteredDiffForAI(owner, repo string, number int) func(context.Context, string) (filtereddiff.Result, error) {
	return func(_ context.Context, filter string) (filtereddiff.Result, error) {
		return GetFilteredDiff(owner, repo, number, filter)
	}
}

type ListDiffFiltersArgs struct{}

type ListDiffFiltersReply struct {
	Filters []filtereddiff.Info `json:"filters"`
}

// ListDiffFilters lists the filters GetFilteredDiff takes, in the order
// clients offer them.
func (h *RPCHandler) ListDiffFilters(args *ListDiffFiltersArgs, reply *ListDiffFiltersReply) error {
	reply.Filters = filtereddiff.Describe()
	return nil
}

type GetFilteredDiffArgs struct {
	Owner  string `json:"Owner"`
	Repo   string `json:"Repo"`
	Number int    `json:"Number"`
	Filter string `json:"Filter"`
}

type GetFilteredDiffReply struct {
	filtereddiff.Result
}

// GetFilteredDiff returns the PR's diff narrowed by a filter — the changes
// since the reviewer's last review, say. The diff is rendered like GetPR's,
// but its positions are its own: a comment's position in it is not the
// comment's position in the PR's diff.
func (h *RPCHandler) GetFilteredDiff(args *GetFilteredDiffArgs, reply *GetFilteredDiffReply) error {
	result, err := GetFilteredDiff(args.Owner, args.Repo, args.Number, args.Filter)
	if err != nil {
		return err
	}
	reply.Result = result
	return nil
}
