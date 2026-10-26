package git_tools

import (
	"context"
	"errors"

	"github.com/google/go-github/v74/github"
)

// Statuses CompareStatus reports, as GitHub's compare endpoint names them.
const (
	CompareAhead     = "ahead"
	CompareBehind    = "behind"
	CompareDiverged  = "diverged"
	CompareIdentical = "identical"
	// CompareMissing is a base GitHub no longer has (or never had) a history
	// in common with head — a commit a force-push left behind and that has
	// since been collected. Not a GitHub status.
	CompareMissing = "missing"
)

// ErrNoCommonHistory is CompareDiff's answer when GitHub can't relate the two
// commits at all.
var ErrNoCommonHistory = errors.New("the commits share no history GitHub can compare")

// CompareDiff returns the unified diff between two commits. GitHub only offers
// the three-dot comparison, which diffs head against its merge base with base:
// when base is an ancestor of head — the case filtered diffs ask for — that is
// exactly the changes from base to head.
func CompareDiff(ctx context.Context, client *github.Client, owner, repo, base, head string) (string, error) {
	diff, _, err := client.Repositories.CompareCommitsRaw(ctx, owner, repo, base, head, github.RawOptions{Type: github.Diff})
	if isNotFound(err) {
		return "", ErrNoCommonHistory
	}
	return diff, err
}

// CompareStatus reports how head relates to base: CompareAhead when base is
// one of head's ancestors, CompareDiverged when history was rewritten between
// them, CompareMissing when GitHub can't relate them at all. One page of one
// commit is asked for, since only the status is read.
func CompareStatus(ctx context.Context, client *github.Client, owner, repo, base, head string) (string, error) {
	cmp, _, err := client.Repositories.CompareCommits(ctx, owner, repo, base, head, &github.ListOptions{PerPage: 1})
	if isNotFound(err) {
		return CompareMissing, nil
	}
	if err != nil {
		return "", err
	}
	return cmp.GetStatus(), nil
}
