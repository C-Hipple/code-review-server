package workflows

import (
	"crs/config"
	"crs/git_tools"
	"crs/jira"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/google/go-github/v74/github"
)

type RunResult struct {
	Added   int
	Updated int
	Deleted int
	Skipped int
}

func (rr *RunResult) Process(output *FileChanges, c chan FileChanges, wg *sync.WaitGroup) {
	if output.ChangeType != "No Change" {
		if output.ChangeType == "Update" {
			rr.Updated += 1
		} else if output.ChangeType == "Addition" {
			rr.Added += 1
		} else if output.ChangeType == "Delete" {
			rr.Deleted += 1
		}
		wg.Add(1)
		func() {
			defer func() {
				if recover() != nil {
					// Channel closed (workflow goroutine outlived its cycle due to
					// the 240 s timeout in RunOnce). Undo the wg.Add so the
					// WaitGroup can still reach zero.
					wg.Done()
				}
			}()
			c <- *output
		}()
	} else {
		rr.Skipped += 1
	}
}

func (rr *RunResult) Report() string {
	return fmt.Sprintf("A: %d; U: %d; R: %d; S: %d", rr.Added, rr.Updated, rr.Deleted, rr.Skipped)
}

type SyncReviewRequestsWorkflow struct {
	// Github repo info
	Name        string
	Owner       string
	Repos       []string
	Filters     []git_tools.PRFilter
	IncludeDiff bool
	PRState     string // defaults to "open"
	AuxDataReq  AuxDataRequirement

	// PostFilterAux, when nonzero, is fetched for the PRs that survive this
	// workflow's filters instead of being pre-fetched for every candidate PR
	// by the manager. See postFilterAuxRequirements in builders.go.
	PostFilterAux AuxDataRequirement

	// org output info
	SectionTitle string

	// DesktopNotifications overrides the global setting for this workflow.
	// nil means inherit the global config.
	DesktopNotifications *bool
}

func (w SyncReviewRequestsWorkflow) GetPRRequirements() []PRRequirement {
	state := w.PRState
	if state == "" {
		state = "open"
	}
	auxData := w.AuxDataReq
	auxData.Diff = w.IncludeDiff
	reqs := []PRRequirement{}
	for _, repoEntry := range w.Repos {
		owner, repo, err := git_tools.ParseRepoName(repoEntry)
		if err == nil {
			reqs = append(reqs, PRRequirement{
				Owner:   owner,
				Repo:    repo,
				State:   state,
				AuxData: auxData,
			})
		}
	}
	return reqs
}

func (w SyncReviewRequestsWorkflow) Run(prs []*github.PullRequest, c chan FileChanges, file_change_wg *sync.WaitGroup) (RunResult, error) {
	prs = git_tools.ApplyPRFilters(prs, w.Filters)
	// Aux data the filters didn't need but the section display and the
	// GetPRDetails caches do is fetched for the survivors only — the whole
	// point of the interaction prefilter is that the candidate set is large
	// and the surviving set is small. See postFilterAuxRequirements.
	if w.PostFilterAux != (AuxDataRequirement{}) {
		prefetchAuxForFilteredPRs(prs, w.PostFilterAux)
	}
	db := config.C().DB
	section, err := db.GetOrCreateSection(w.SectionTitle, config.C().SectionPriority[w.SectionTitle])
	if err != nil {
		slog.Error("Error getting section", "error", err, "section", w.SectionTitle)
		return RunResult{}, errors.New("Section Not Found")
	}
	slog.Debug("Got section", "id", section.ID, "name", section.SectionName)

	beforeCount, _ := db.GetItemCount()
	slog.Info("Starting workflow", "items_before", beforeCount)
	result := ProcessPRsDB(w.Name, prs, c, db, section, file_change_wg, w.IncludeDiff, w.ShouldNotify())
	afterCount, _ := db.GetItemCount()
	slog.Info("Finished workflow", "items_after", afterCount)
	return result, nil
}

// ShouldNotify resolves the effective desktop-notification setting for this
// workflow: the per-workflow override if set, otherwise the global config.
func (w SyncReviewRequestsWorkflow) ShouldNotify() bool {
	if w.DesktopNotifications != nil {
		return *w.DesktopNotifications
	}
	return config.C().DesktopNotifications
}

func (w SyncReviewRequestsWorkflow) GetName() string {
	return w.Name
}

func (w SyncReviewRequestsWorkflow) GetOrgSectionName() string {
	return w.SectionTitle
}

type ProjectListWorkflow struct {
	Name         string
	Repos        []string
	Filters      []git_tools.PRFilter
	SectionTitle string
	JiraDomain   string
	JiraEpic     string
	IncludeDiff  bool

	// DesktopNotifications overrides the global setting for this workflow.
	// nil means inherit the global config.
	DesktopNotifications *bool
}

// ShouldNotify resolves the effective desktop-notification setting for this
// workflow: the per-workflow override if set, otherwise the global config.
func (w ProjectListWorkflow) ShouldNotify() bool {
	if w.DesktopNotifications != nil {
		return *w.DesktopNotifications
	}
	return config.C().DesktopNotifications
}

func (w ProjectListWorkflow) GetName() string {
	return w.Name
}

func (w ProjectListWorkflow) GetOrgSectionName() string {
	return w.SectionTitle
}

func (w ProjectListWorkflow) GetPRRequirements() []PRRequirement {
	// Reverted to manual fetching to avoid long-running Jira lookups in the manager collection phase.
	// PRs for this workflow are fetched in Run() and aux data (including the
	// diff) is pre-fetched and persisted there via prefetchAuxDataForPRs.
	return nil
}

func (w ProjectListWorkflow) Run(prs []*github.PullRequest, c chan FileChanges, file_change_wg *sync.WaitGroup) (RunResult, error) {
	client := git_tools.GetGithubClient()
	db := config.C().DB
	section, err := db.GetOrCreateSection(w.SectionTitle, config.C().SectionPriority[w.SectionTitle])
	if err != nil {
		return RunResult{}, errors.New("Section Not Found")
	}
	if w.JiraEpic == "" {
		// I used to let just define []int for PR #s in config, could easily bring that back
		return RunResult{}, errors.New("ProjectList requires Jira Epic")
	}
	if len(w.Repos) == 0 {
		return RunResult{}, errors.New("ProjectList requires at least one repo")
	}

	// Resolve each configured "owner/repo" entry so the Jira lookup can match the
	// PR links it finds and we can fetch each PR from the right repo.
	refs := resolveRepoRefs(w.Repos)
	if len(refs) == 0 {
		return RunResult{}, errors.New("ProjectList has no valid repos")
	}

	prsByRepo := jira.GetProjectPRKeys(w.JiraDomain, w.JiraEpic, refs)

	// Walk the configured order rather than the map so each cycle processes the
	// repos the same way.
	var allPRs []*github.PullRequest
	for _, ref := range refs {
		nums := prsByRepo[ref]
		if len(nums) == 0 {
			continue
		}
		repoPRs, err := git_tools.GetSpecificPRs(client, ref.Owner, ref.Repo, nums)
		if err != nil {
			slog.Error("Error getting specific PRs", "owner", ref.Owner, "repo", ref.Repo, "error", err)
			continue
		}
		// Because GetPRRequirements returns nil, the manager's prefetch pass skips
		// these PRs entirely — leaving the diff (and other aux) caches unpopulated
		// when GetPRDetails is later called from the web UI. Pre-fetch all aux data
		// here so it lands in the DB caches and the global AuxDataStore.
		prefetchAuxDataForPRs(client, ref.Owner, ref.Repo, repoPRs, AuxDataRequirement{
			Comments: true,
			CIStatus: true,
			Diff:     true,
			Reviews:  true,
			Commits:  true,
		})
		allPRs = append(allPRs, repoPRs...)
	}

	allPRs = git_tools.ApplyPRFilters(allPRs, w.Filters)

	beforeCount, _ := db.GetItemCount()
	slog.Info("Starting workflow", "items_before", beforeCount)
	result := ProcessPRsDB(w.Name, allPRs, c, db, section, file_change_wg, w.IncludeDiff, w.ShouldNotify())
	afterCount, _ := db.GetItemCount()
	slog.Info("Finished workflow", "items_after", afterCount)
	return result, nil
}

// resolveRepoRefs turns configured "owner/repo" entries into repo refs, keeping
// the configured order, dropping malformed entries and collapsing duplicates so
// a repo listed twice is only fetched once.
func resolveRepoRefs(entries []string) []jira.RepoRef {
	refs := make([]jira.RepoRef, 0, len(entries))
	seen := make(map[jira.RepoRef]struct{}, len(entries))
	for _, entry := range entries {
		owner, repo, err := git_tools.ParseRepoName(strings.TrimSpace(entry))
		if err != nil {
			slog.Error("Skipping invalid repo entry", "entry", entry, "error", err)
			continue
		}
		ref := jira.RepoRef{Owner: owner, Repo: repo}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

// prefetchAuxDataForPRs fetches the requested auxiliary data for the given
// PRs and persists it to the DB caches and the current global AuxDataStore.
// Used by workflows whose PR set isn't known during the manager's collection
// phase (ProjectListWorkflow, which resolves PR numbers via Jira at run time)
// and for post-filter fetches (prefetchAuxForFilteredPRs).
func prefetchAuxDataForPRs(client *github.Client, owner, repo string, prs []*github.PullRequest, auxReq AuxDataRequirement) {
	if len(prs) == 0 {
		return
	}
	store := GetCurrentAuxDataStore()
	if store == nil {
		store = NewAuxDataStore()
		SetCurrentAuxDataStore(store)
	}
	apiCalls := &apiCallCounter{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, prefetchConcurrency)
	for _, pr := range prs {
		if pr == nil || pr.Number == nil {
			continue
		}
		wg.Add(1)
		go func(pr *github.PullRequest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			key := PRKey{Owner: owner, Repo: repo, Number: *pr.Number}
			data := fetchAuxDataForPR(client, apiCalls, key, auxReq, pr)
			// MergeSet, not Set: auxReq may be a subset of the fields another
			// pass already stored for this PR (e.g. the manager pre-fetched
			// its diff), and those must survive.
			store.MergeSet(key, data)
		}(pr)
	}
	wg.Wait()
	slog.Info("Pre-fetched aux data for runtime-resolved PRs", "count", len(prs))
}

// prefetchAuxForFilteredPRs groups prs — which may span several repos — by
// their base repo and fetches auxReq for each group. Called after a
// workflow's filters have run, so the PR count here is the section's size,
// not the candidate set's; see postFilterAuxRequirements in builders.go for
// why interaction-filter workflows defer this data until after filtering.
func prefetchAuxForFilteredPRs(prs []*github.PullRequest, auxReq AuxDataRequirement) {
	if len(prs) == 0 {
		return
	}
	client := git_tools.GetGithubClient()
	type repoRef struct{ owner, repo string }
	groups := map[repoRef][]*github.PullRequest{}
	for _, pr := range prs {
		if pr == nil || pr.Base == nil || pr.Base.Repo == nil || pr.Base.Repo.Owner == nil ||
			pr.Base.Repo.Owner.Login == nil || pr.Base.Repo.Name == nil {
			continue
		}
		ref := repoRef{owner: *pr.Base.Repo.Owner.Login, repo: *pr.Base.Repo.Name}
		groups[ref] = append(groups[ref], pr)
	}
	for ref, group := range groups {
		prefetchAuxDataForPRs(client, ref.owner, ref.repo, group, auxReq)
	}
}
