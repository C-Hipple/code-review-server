package workflows

import (
	"crs/config"
	"crs/git_tools"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/google/go-github/v74/github"
)

// SearchMode selects which GitHub searches a SearchWorkflow gets its candidate
// PRs from.
type SearchMode string

const (
	// ModeReviewRequested mirrors github.com's own "Review requests" list:
	// every open PR whose review was requested from you, or from a team you
	// belong to.
	ModeReviewRequested SearchMode = "review_requested"

	// ModeWaitingOnMe narrows the same candidates — plus the PRs you have
	// already reviewed or commented on — down to the ones actually in your
	// court, via FilterWaitingOnMe.
	ModeWaitingOnMe SearchMode = "waiting_on_me"
)

// SearchWorkflow builds its section from GitHub search rather than from a
// configured repository list, which is what lets the server run with no
// configuration at all (see config.DefaultConfigTOML). The identity it searches
// for comes from the config's GithubUsername, which the server fills in from the
// API token when it isn't set.
type SearchWorkflow struct {
	Name         string
	SectionTitle string
	Mode         SearchMode
	// Login is the GitHub user the searches are about.
	Login       string
	Filters     []git_tools.PRFilter
	IncludeDiff bool

	// AuxDataReq is data this workflow's own filters read out of the
	// AuxDataStore, so it has to be fetched before filtering.
	AuxDataReq AuxDataRequirement
	// PostFilterAux is the display data fetched for the PRs that survive the
	// filters — the section's reviewer summary and GetPRDetails' caches.
	PostFilterAux AuxDataRequirement

	// DesktopNotifications overrides the global setting for this workflow.
	// nil means inherit the global config.
	DesktopNotifications *bool
}

func (w SearchWorkflow) GetName() string {
	return w.Name
}

func (w SearchWorkflow) GetOrgSectionName() string {
	return w.SectionTitle
}

// GetPRRequirements returns nothing: this workflow's PRs aren't known until its
// searches run, so the manager's collection phase has nothing to pre-fetch and
// Run does its own fetching — the same arrangement ProjectListWorkflow uses.
func (w SearchWorkflow) GetPRRequirements() []PRRequirement {
	return nil
}

// ShouldNotify resolves the effective desktop-notification setting for this
// workflow: the per-workflow override if set, otherwise the global config.
func (w SearchWorkflow) ShouldNotify() bool {
	if w.DesktopNotifications != nil {
		return *w.DesktopNotifications
	}
	return config.C().DesktopNotifications
}

func (w SearchWorkflow) Run(_ []*github.PullRequest, c chan FileChanges, file_change_wg *sync.WaitGroup) (RunResult, error) {
	login := strings.TrimSpace(w.Login)
	if login == "" {
		return RunResult{}, fmt.Errorf("%s needs a GitHub login: set GithubUsername in the config, or give the server a token whose user it can look up", w.Name)
	}

	refs, err := w.candidateRefs(login)
	if err != nil {
		// Every early return in here leaves the section as it is. Falling
		// through to ProcessPRsDB with an empty list would read as "none of
		// these PRs are mine any more" and delete the whole section.
		return RunResult{}, fmt.Errorf("finding candidate PRs for %s: %w", w.Name, err)
	}
	slog.Info("Search workflow candidates", "workflow", w.Name, "mode", w.Mode, "candidates", len(refs))

	prs, err := git_tools.GetPRsForRefs(git_tools.GetGithubClient(), refs)
	if err != nil {
		return RunResult{}, fmt.Errorf("fetching candidate PRs for %s: %w", w.Name, err)
	}

	// Pre-filter fetch: only what this workflow's filters read from the store.
	if w.AuxDataReq != (AuxDataRequirement{}) {
		prefetchAuxForFilteredPRs(w.Name, prs, w.AuxDataReq)
	}

	prs = git_tools.ApplyPRFilters(prs, w.filters(login))

	// Post-filter fetch: display data, for the survivors only.
	if w.PostFilterAux != (AuxDataRequirement{}) {
		prefetchAuxForFilteredPRs(w.Name, prs, w.PostFilterAux)
	}

	db := config.C().DB
	section, err := db.GetOrCreateSection(w.SectionTitle, config.C().SectionPriority[w.SectionTitle])
	if err != nil {
		slog.Error("Error getting section", "error", err, "section", w.SectionTitle)
		return RunResult{}, errors.New("Section Not Found")
	}

	beforeCount, _ := db.GetItemCount()
	slog.Info("Starting workflow", "items_before", beforeCount)
	result := ProcessPRsDB(w.Name, prs, c, db, section, file_change_wg, w.IncludeDiff, w.ShouldNotify())
	afterCount, _ := db.GetItemCount()
	slog.Info("Finished workflow", "items_after", afterCount)
	return result, nil
}

// filters returns the mode's own filter followed by the configured ones.
func (w SearchWorkflow) filters(login string) []git_tools.PRFilter {
	if w.Mode != ModeWaitingOnMe {
		return w.Filters
	}
	return append([]git_tools.PRFilter{git_tools.MakeWaitingOnMeFilter(login)}, w.Filters...)
}

// candidateRefs runs the searches this mode draws its PRs from.
func (w SearchWorkflow) candidateRefs(login string) ([]git_tools.PRRef, error) {
	switch w.Mode {
	case ModeReviewRequested:
		return git_tools.ReviewRequestedRefs(login)

	case ModeWaitingOnMe:
		// A PR is waiting on me either because I have an open review request
		// — including one I was asked for through a team — or because
		// something I already did on it needs following up: a dismissed
		// approval, or a thread of mine awaiting my reply. The second group
		// only exists among PRs I have reviewed or commented on, so the union
		// of the two searches is the complete candidate set. See
		// git_tools/interaction_prefilter.go for the full argument.
		requested, err := git_tools.ReviewRequestedRefs(login)
		if err != nil {
			return nil, err
		}
		interacted, err := git_tools.MyInteractionRefs(login)
		if err != nil {
			return nil, err
		}
		return git_tools.DedupeRefs(append(requested, interacted...)), nil
	}
	return nil, fmt.Errorf("unknown search mode %q", w.Mode)
}
