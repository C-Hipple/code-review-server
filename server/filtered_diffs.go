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
// PRFilteredDiffs keyed by the commit pair. When the author merged the base
// branch in since the baseline, that compare carries the base branch's
// changes too, and they are taken out (withoutBaseMerges) at the cost of one
// more compare. The post-update hooks warm every filter's diff in the
// background whenever a PR is fetched, updated or opened (warmFilteredDiffs),
// so GetFilteredDiff is a cache read — it computes a diff itself only when a
// client asks before the warm has stored it.

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
	// filteredDiffVersion is how stored diffs are computed; a row stored
	// with another is computed again. 1 leaves out what merging the base
	// branch in brought.
	filteredDiffVersion = 1
)

// rangeDiffSource is the GitHub seam the filtered diffs fetch through; tests
// swap it.
type rangeDiffSource interface {
	// Diff returns the raw unified diff from base to head, or
	// git_tools.ErrNoCommonHistory when GitHub can't relate them.
	Diff(ctx context.Context, owner, repo, base, head string) (string, error)
	// Status reports how head relates to base, as git_tools.CompareStatus.
	Status(ctx context.Context, owner, repo, base, head string) (string, error)
	// Commits lists the PR's commits with their parents, oldest first.
	Commits(ctx context.Context, owner, repo string, number int) ([]filtereddiff.Commit, error)
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

func (githubCompare) Commits(ctx context.Context, owner, repo string, number int) ([]filtereddiff.Commit, error) {
	if !git_tools.HasToken() {
		return nil, errNoToken
	}
	listed, err := git_tools.ListAllPRCommits(ctx, git_tools.GetGithubClient(), owner, repo, number)
	if err != nil {
		return nil, err
	}
	commits := make([]filtereddiff.Commit, 0, len(listed))
	for _, c := range listed {
		commits = append(commits, filtereddiff.Commit{SHA: c.GetSHA(), Parents: git_tools.ParentSHAs(c)})
	}
	return commits, nil
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
	in, fullDiff, err := filteredDiffInputs(db, repo, number)
	return filteredDiff(db, owner, repo, number, f, in, fullDiff, err), nil
}

// filteredDiff answers for one filter from inputs already read (or the error
// reading them), so a caller resolving every filter reads the caches once.
// fullDiff is the PR's cached full diff, raw, which the result's Positions map
// onto.
func filteredDiff(db *database.DB, owner, repo string, number int, f filtereddiff.Filter, in filtereddiff.Inputs, fullDiff string, inputsErr error) filtereddiff.Result {
	result := filtereddiff.Result{Filter: f.ID(), Commits: []string{}, Positions: map[string]map[int]int{}}
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

	stored := rangeDiff(db, owner, repo, number, in, baseline.SHA, fullDiff)
	result.Status = stored.Status
	result.Message = stored.Message
	result.ComputedAt = stored.UpdatedAt
	if stored.Status == filtereddiff.StatusReady {
		result.Diff, result.Positions = renderFilteredDiff(stored.Diff, fullDiff, owner, repo, number, in.HeadSHA)
	}
	return result
}

// filteredDiffInputs reads what the filters judge from — the PR's head and its
// cached reviews, comments and commits, plus who the reviewer is — and the
// PR's cached full diff.
func filteredDiffInputs(db *database.DB, repo string, number int) (filtereddiff.Inputs, string, error) {
	if db == nil {
		return filtereddiff.Inputs{}, "", errors.New("the server has no database")
	}
	fullDiff, head, err := db.GetPullRequest(number, repo)
	if err != nil {
		return filtereddiff.Inputs{}, "", fmt.Errorf("reading the PR's head commit: %w", err)
	}
	if head == "" {
		return filtereddiff.Inputs{}, "", errors.New("the PR's head commit isn't cached yet; open or sync the PR first")
	}
	reviews, err := db.GetPRReviews(number, repo)
	if err != nil {
		return filtereddiff.Inputs{}, "", fmt.Errorf("reading the PR's reviews: %w", err)
	}
	comments, err := db.GetPRComments(number, repo)
	if err != nil {
		return filtereddiff.Inputs{}, "", fmt.Errorf("reading the PR's comments: %w", err)
	}
	commits, err := db.GetPRCommits(number, repo)
	if err != nil {
		return filtereddiff.Inputs{}, "", fmt.Errorf("reading the PR's commits: %w", err)
	}
	in, err := filtereddiff.InputsFromCaches(config.C().GithubUsername, head, reviews, comments, commits)
	return in, fullDiff, err
}

// rangeDiff returns the PR's diff from base to its head: the stored one when
// there is one, else a fresh one, which it stores. A stored failure stands for
// filteredDiffErrorRetry before it is tried again. fullDiff is the PR's
// cached diff, raw.
func rangeDiff(db *database.DB, owner, repo string, number int, in filtereddiff.Inputs, base, fullDiff string) database.FilteredDiff {
	stored, ok, err := db.GetFilteredDiff(owner, repo, number, base, in.HeadSHA)
	if err != nil {
		slog.Warn("Error reading a stored filtered diff", "repo", repo, "pr", number, "error", err)
	}
	if ok && stored.Version == filteredDiffVersion &&
		(stored.Status != filtereddiff.StatusError || time.Since(stored.UpdatedAt) < filteredDiffErrorRetry) {
		return stored
	}
	key := fmt.Sprintf("%s/%s#%d@%s..%s", owner, repo, number, base, in.HeadSHA)
	return rangeDiffFlights.do(key, func() database.FilteredDiff {
		return computeRangeDiff(db, owner, repo, number, in, base, fullDiff)
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
func computeRangeDiff(db *database.DB, owner, repo string, number int, in filtereddiff.Inputs, base, fullDiff string) database.FilteredDiff {
	rangeDiffSlots <- struct{}{}
	defer func() { <-rangeDiffSlots }()

	ctx, cancel := context.WithTimeout(context.Background(), filteredDiffTimeout)
	defer cancel()

	row := database.FilteredDiff{BaseSHA: base, HeadSHA: in.HeadSHA, Version: filteredDiffVersion}
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

	if row.Status == filtereddiff.StatusReady {
		diff, err := withoutBaseMerges(ctx, owner, repo, number, in, base, row.Diff, fullDiff)
		if err != nil {
			row.Status = filtereddiff.StatusError
			row.Diff = ""
			row.Message = fmt.Sprintf("Couldn't leave out the changes merging the base branch brought in: %v", err)
		} else {
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

// withoutBaseMerges takes what merging the base branch in brought out of
// diff, the PR's diff from base to its head, leaving the PR's own changes
// since base (filtereddiff.WithoutMerged). Without such a merge after base the
// diff stands as it is, which the cached commits say for free; when they
// can't — cached before parents were recorded, or a stale list — GitHub is
// asked for the commits. A merge costs one more compare: the PR's diff at
// base, from its merge base with the base branch commit merged in last.
// fullDiff, GitHub's diff of the PR at the head, is the other side; a cache
// without one costs a compare for it too.
func withoutBaseMerges(ctx context.Context, owner, repo string, number int, in filtereddiff.Inputs, base, diff, fullDiff string) (string, error) {
	merged, known := in.MergedAfter(base)
	if !known {
		commits, err := filteredDiffSource.Commits(ctx, owner, repo, number)
		if err != nil {
			return "", fmt.Errorf("listing the PR's commits: %w", err)
		}
		in.Commits = commits
		if merged, known = in.MergedAfter(base); !known {
			// Past the 250 commits GitHub lists: no telling.
			slog.Debug("Can't tell whether the base branch was merged in", "repo", repo, "pr", number, "base", base)
			return diff, nil
		}
	}
	if len(merged) == 0 {
		return diff, nil
	}
	from := merged[len(merged)-1]
	before, err := filteredDiffSource.Diff(ctx, owner, repo, from, base)
	if err != nil {
		return "", fmt.Errorf("fetching the PR's diff at %s: %w", shortSHA(base), err)
	}
	if strings.TrimSpace(fullDiff) == "" {
		if fullDiff, err = filteredDiffSource.Diff(ctx, owner, repo, from, in.HeadSHA); err != nil {
			return "", fmt.Errorf("fetching the PR's diff: %w", err)
		}
	}
	slog.Debug("Leaving the base branch's changes out of a filtered diff", "repo", repo, "pr", number, "base", base, "merged", from)
	return filtereddiff.WithoutMerged(diff, before, fullDiff), nil
}

// renderFilteredDiff renders a stored diff the way GetPRDetails renders the
// PR's full one, so clients parse both alike, and maps its rows onto the full
// diff's (see filtereddiff.Result.Positions).
func renderFilteredDiff(raw, fullDiff, owner, repo string, number int, head string) (string, map[string]map[int]int) {
	if strings.TrimSpace(raw) == "" {
		return "", map[string]map[int]int{}
	}
	parsed, err := utils.Parse(raw)
	if err != nil || parsed == nil {
		return raw, map[string]map[int]int{}
	}
	full, err := utils.Parse(fullDiff)
	if err != nil {
		full = nil
	}
	return formatDiff(parsed, owner, repo, number, head), diffPositions(full, parsed)
}

// diffPositions maps each row of filtered that shows a line of the head onto
// the row of full showing the same line, per file. Both diffs end at the head,
// so a line is the same line when its head-side number is; a removed line
// belongs to whichever commit each diff starts from, and is left out.
func diffPositions(full, filtered *utils.Diff) map[string]map[int]int {
	positions := map[string]map[int]int{}
	if full == nil || filtered == nil {
		return positions
	}
	headRows := map[string]map[int]int{}
	for _, f := range full.Files {
		if f.NewName == "" || f.Mode == utils.DELETED {
			continue
		}
		rows := map[int]int{}
		eachDiffRow(f, func(pos int, line *utils.DiffLine) {
			if line.Mode != utils.REMOVED {
				rows[line.Number] = pos
			}
		})
		headRows[f.NewName] = rows
	}
	for _, f := range filtered.Files {
		rows, ok := headRows[f.NewName]
		if !ok || f.NewName == "" || f.Mode == utils.DELETED {
			continue
		}
		mapped := map[int]int{0: 0}
		eachDiffRow(f, func(pos int, line *utils.DiffLine) {
			if line.Mode == utils.REMOVED {
				return
			}
			if fullPos, ok := rows[line.Number]; ok {
				mapped[pos] = fullPos
			}
		})
		positions[f.NewName] = mapped
	}
	return positions
}

// eachDiffRow visits a file's rows with the positions clients give them in
// the rendered diff: counting from the row after the file's first hunk
// header, with every later hunk header taking a position of its own.
func eachDiffRow(f *utils.DiffFile, visit func(pos int, line *utils.DiffLine)) {
	pos := 0
	for i, h := range f.Hunks {
		if i > 0 {
			pos++
		}
		for _, line := range h.WholeRange.Lines {
			pos++
			visit(pos, line)
		}
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// warmFilteredDiffs stores each registered filter's diff for the PR, so a
// client switching to one finds it stored. The caches are read once for all
// of them, only a commit pair not stored yet reaches GitHub, and nothing is
// rendered: that happens when a diff is asked for. It blocks, so callers run
// it in a goroutine.
func warmFilteredDiffs(owner, repo string, number int) {
	db := config.C().DB
	if db == nil {
		return
	}
	in, fullDiff, err := filteredDiffInputs(db, repo, number)
	if err != nil {
		// Nothing to warm from yet: the PR's caches aren't there.
		slog.Debug("Not warming filtered diffs", "repo", repo, "pr", number, "reason", err)
		return
	}
	for _, f := range filtereddiff.Filters() {
		baseline, err := f.Baseline(in)
		if err != nil || baseline.SHA == in.HeadSHA {
			continue
		}
		rangeDiff(db, owner, repo, number, in, baseline.SHA, fullDiff)
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
// but its positions are its own: the reply's positions map them onto the PR's
// diff, which is what comments are positioned in.
func (h *RPCHandler) GetFilteredDiff(args *GetFilteredDiffArgs, reply *GetFilteredDiffReply) error {
	result, err := GetFilteredDiff(args.Owner, args.Repo, args.Number, args.Filter)
	if err != nil {
		return err
	}
	reply.Result = result
	return nil
}
