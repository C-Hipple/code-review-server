package workflows

import (
	"fmt"
	"log/slog"
	"strings"
	"crs/config"
	"crs/git_tools"

	"github.com/google/go-github/v74/github"
)

func MatchWorkflows(workflow_maps []config.RawWorkflow, repos *[]string, jiraDomain string) []Workflow {
	workflows := []Workflow{}
	for _, raw_workflow := range workflow_maps {
		var wf Workflow
		var err error
		switch raw_workflow.WorkflowType {
		case "SyncReviewRequestsWorkflow":
			wf, err = BuildSyncReviewRequestWorkflow(&raw_workflow, repos)
		case "SingleRepoSyncReviewRequestsWorkflow":
			wf, err = BuildSingleRepoReviewWorkflow(&raw_workflow, repos)
		case "ListMyPRsWorkflow":
			wf, err = BuildListMyPRsWorkflow(&raw_workflow, repos)
		case "ProjectListWorkflow":
			wf, err = BuildProjectListWorkflow(&raw_workflow, repos, jiraDomain)
		default:
			slog.Warn("Skipping workflow with unknown WorkflowType", "name", raw_workflow.Name, "type", raw_workflow.WorkflowType)
			continue
		}
		if err != nil {
			slog.Warn("Skipping improperly configured workflow", "name", raw_workflow.Name, "error", err)
			continue
		}
		workflows = append(workflows, wf)
	}
	return workflows
}

func BuildSingleRepoReviewWorkflow(raw *config.RawWorkflow, repos *[]string) (Workflow, error) {
	slog.Warn("SingleRepoSyncReviewRequestsWorkflow is deprecated; use SyncReviewRequestsWorkflow with a single-element Repos list instead", "workflow", raw.Name)
	filters, err := BuildFiltersList(raw)
	if err != nil {
		return nil, err
	}
	wf := SyncReviewRequestsWorkflow{
		Name:                 raw.Name,
		Owner:                raw.Owner,
		Repos:                []string{raw.Repo},
		Filters:              filters,
		SectionTitle:         raw.SectionTitle,
		IncludeDiff:          raw.IncludeDiff,
		AuxDataReq:           computeAuxRequirements(raw.Filters, raw.IncludeDiff),
		PostFilterAux:        postFilterAuxRequirements(raw.Filters),
		DesktopNotifications: raw.DesktopNotifications,
	}
	return wf, nil
}

func BuildSyncReviewRequestWorkflow(raw *config.RawWorkflow, repos *[]string) (Workflow, error) {
	workflowRepos := *repos
	if len(raw.Repos) > 0 {
		workflowRepos = raw.Repos
	}

	filters, err := BuildFiltersList(raw)
	if err != nil {
		return nil, err
	}
	wf := SyncReviewRequestsWorkflow{
		Name:                 raw.Name,
		Owner:                raw.Owner,
		Repos:                workflowRepos,
		Filters:              filters,
		PRState:              raw.PRState,
		SectionTitle:         raw.SectionTitle,
		IncludeDiff:          raw.IncludeDiff,
		AuxDataReq:           computeAuxRequirements(raw.Filters, raw.IncludeDiff),
		PostFilterAux:        postFilterAuxRequirements(raw.Filters),
		DesktopNotifications: raw.DesktopNotifications,
	}
	return wf, nil
}

func BuildListMyPRsWorkflow(raw *config.RawWorkflow, repos *[]string) (Workflow, error) {
	slog.Warn("ListMyPRsWorkflow is deprecated; use SyncReviewRequestsWorkflow with FilterMyPRs in the filters list instead", "workflow", raw.Name)
	workflowRepos := *repos
	if len(raw.Repos) > 0 {
		workflowRepos = raw.Repos
	}

	filters, err := BuildFiltersList(raw)
	if err != nil {
		return nil, err
	}
	// ListMyPRsWorkflow always filters to the authenticated user's PRs.
	// Prepend it so user-supplied filters further narrow the result.
	login := strings.TrimSpace(raw.GithubUsername)
	if login == "" {
		return nil, fmt.Errorf("ListMyPRsWorkflow requires GithubUsername to be set (in this workflow or at the root of the config); without it it matches nothing")
	}
	filters = append([]git_tools.PRFilter{git_tools.MakeAuthorFilter(login)}, filters...)
	wf := SyncReviewRequestsWorkflow{
		Name:                 raw.Name,
		Owner:                raw.Owner,
		Repos:                workflowRepos,
		Filters:              filters,
		PRState:              raw.PRState,
		SectionTitle:         raw.SectionTitle,
		IncludeDiff:          raw.IncludeDiff,
		AuxDataReq:           computeAuxRequirements(raw.Filters, raw.IncludeDiff),
		PostFilterAux:        postFilterAuxRequirements(raw.Filters),
		DesktopNotifications: raw.DesktopNotifications,
	}
	return wf, nil
}

func BuildProjectListWorkflow(raw *config.RawWorkflow, repos *[]string, jiraDomain string) (Workflow, error) {
	workflowRepos := *repos
	if len(raw.Repos) > 0 {
		workflowRepos = raw.Repos
	} else if raw.Owner != "" && raw.Repo != "" {
		// Backward compat for configs using singular Owner/Repo.
		workflowRepos = []string{fmt.Sprintf("%s/%s", raw.Owner, raw.Repo)}
	}

	filters, err := BuildFiltersList(raw)
	if err != nil {
		return nil, err
	}
	wf := ProjectListWorkflow{
		Name:                 raw.Name,
		Repos:                workflowRepos,
		JiraDomain:           jiraDomain,
		JiraEpic:             raw.JiraEpic,
		Filters:              filters,
		SectionTitle:         raw.SectionTitle,
		IncludeDiff:          raw.IncludeDiff,
		DesktopNotifications: raw.DesktopNotifications,
	}
	return wf, nil
}

// computeAuxRequirements determines which metadata the manager pre-fetches by
// inspecting the filter names configured for a workflow. The pre-fetch pass
// runs BEFORE filtering, so anything requested here is fetched for every
// candidate PR of the workflow's repos on every cycle — on a repo with
// hundreds of open PRs that is hundreds of API calls per field per cycle.
// Only data a filter actually reads out of the AuxDataStore belongs here;
// Details() falls back to the API for anything else.
//
// FilterWaitingOnMe and FilterWaitingOnAuthor deliberately request nothing:
// they cannot read the AuxDataStore (they live in git_tools, which the store
// is not visible to) and instead fetch interaction data themselves for the
// few PRs that survive their cheap search prefilter — see
// git_tools/interaction_prefilter.go. The display data their sections need is
// fetched after filtering, for survivors only (postFilterAuxRequirements).
func computeAuxRequirements(filterNames []string, includeDiff bool) AuxDataRequirement {
	req := AuxDataRequirement{Diff: includeDiff}
	for _, raw := range filterNames {
		name, _ := ParseFilterString(raw)
		switch name {
		case "FilterCIPassing", "FilterCIFailing":
			req.CIStatus = true
		case "FilterMyReviewRequested":
			// filterMyReviewRequestedFor reads aux.Reviews from the store to
			// catch dismissed reviews.
			req.Reviews = true
		}
	}
	return req
}

// postFilterAuxRequirements returns the aux data to fetch for the PRs that
// SURVIVE a workflow's filters. The interaction filters keep the manager from
// pre-fetching anything for their candidate set (see computeAuxRequirements),
// but the handful of PRs that end up in the section still need comments,
// reviews, and commits — for the section display (reviewer summary, comment
// counts) and for GetPRDetails' DB caches — the same data the pre-fetch pass
// provided before the prefilter existed. A zero value means the workflow has
// no interaction filter and the manager's pre-fetch already covered it.
func postFilterAuxRequirements(filterNames []string) AuxDataRequirement {
	for _, raw := range filterNames {
		name, _ := ParseFilterString(raw)
		if name == "FilterWaitingOnMe" || name == "FilterWaitingOnAuthor" {
			return AuxDataRequirement{Comments: true, Reviews: true, Commits: true}
		}
	}
	return AuxDataRequirement{}
}

var filter_func_map = map[string]func(prs []*github.PullRequest) []*github.PullRequest{
	"FilterNotDraft":  git_tools.FilterNotDraft,
	"FilterIsDraft":   git_tools.FilterIsDraft,
	"FilterCIPassing": git_tools.FilterCIPassing,
	"FilterCIFailing": git_tools.FilterCIFailing,
	"FilterStale":     git_tools.FilterStale,
	"FilterNotStale":  git_tools.FilterNotStale,
}

// identityFilters are the filters that compare PR data against *my* GitHub
// login. They are built separately from filter_func_map because they need that
// login injected: with an empty one every comparison inside them is against "",
// so they match nothing and leave a section silently empty. Binding the login at
// build time turns a missing GithubUsername into a startup error instead.
var identityFilters = map[string]func(login string) git_tools.PRFilter{
	"FilterMyReviewRequested": makeMyReviewRequestedFilter,
	"FilterNotMyPRs":          git_tools.MakeExcludeAuthorFilter,
	"FilterMyPRs":             git_tools.MakeAuthorFilter,
	"FilterWaitingOnMe":       git_tools.MakeWaitingOnMeFilter,
	"FilterWaitingOnAuthor":   git_tools.MakeWaitingOnAuthorFilter,
}

// IsIdentityFilter reports whether the named filter needs a GithubUsername to
// mean anything. Validation uses it to explain why a filter can't work.
func IsIdentityFilter(name string) bool {
	_, ok := identityFilters[name]
	return ok
}

func ParseFilterString(raw string) (string, string) {
	if strings.Contains(raw, ":") {
		parts := strings.SplitN(raw, ":", 2)
		return parts[0], parts[1]
	}
	return raw, ""
}

func BuildFiltersList(raw *config.RawWorkflow) ([]git_tools.PRFilter, error) {
	filters := []git_tools.PRFilter{}

	// Automatically add team filter if Teams is configured
	if len(raw.Teams) > 0 {
		filters = append(filters, git_tools.MakeTeamFilters(raw.Teams))
	}

	for _, name := range raw.Filters {
		filterName, filterArg := ParseFilterString(name)

		if filterName == "FilterByLabel" {
			if filterArg == "" {
				return nil, fmt.Errorf("FilterByLabel requires an argument (e.g. FilterByLabel:bug)")
			}
			filters = append(filters, git_tools.MakeLabelFilter(filterArg))
			continue
		}

		if filterName == "FilterByAuthor" {
			if filterArg == "" {
				return nil, fmt.Errorf("FilterByAuthor requires an argument (e.g. FilterByAuthor:username)")
			}
			filters = append(filters, git_tools.MakeAuthorFilter(filterArg))
			continue
		}

		if filterName == "FilterExcludeAuthor" {
			if filterArg == "" {
				return nil, fmt.Errorf("FilterExcludeAuthor requires an argument (e.g. FilterExcludeAuthor:username)")
			}
			filters = append(filters, git_tools.MakeExcludeAuthorFilter(filterArg))
			continue
		}

		if makeFilter, ok := identityFilters[filterName]; ok {
			// raw.GithubUsername is the workflow's own setting, defaulted to the
			// root-level one when the workflow doesn't override it (see
			// config.parseConfig). Until this lookup existed the per-workflow field
			// was parsed and written back but never actually read by a filter.
			login := strings.TrimSpace(raw.GithubUsername)
			if login == "" {
				return nil, fmt.Errorf("%s requires GithubUsername to be set (in this workflow or at the root of the config); without it the filter matches nothing", filterName)
			}
			filters = append(filters, makeFilter(login))
			continue
		}

		filter_func := filter_func_map[filterName]
		if filter_func == nil {
			return nil, fmt.Errorf("unknown filter %q", filterName)
		}
		filters = append(filters, filter_func)
	}
	return filters, nil
}
