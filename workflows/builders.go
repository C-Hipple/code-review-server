package workflows

import (
	"fmt"
	"log/slog"
	"strings"
	"crs/config"
	"crs/git_tools"

	"github.com/google/go-github/v48/github"
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
			wf, err = BuildProjectListWorkflow(&raw_workflow, jiraDomain)
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
		Name:         raw.Name,
		Owner:        raw.Owner,
		Repos:        []string{raw.Repo},
		Filters:      filters,
		SectionTitle: raw.SectionTitle,
		IncludeDiff:  raw.IncludeDiff,
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
		Name:         raw.Name,
		Owner:        raw.Owner,
		Repos:        workflowRepos,
		Filters:      filters,
		PRState:      raw.PRState,
		SectionTitle: raw.SectionTitle,
		IncludeDiff:  raw.IncludeDiff,
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
	filters = append([]git_tools.PRFilter{git_tools.FilterMyPRs}, filters...)
	wf := SyncReviewRequestsWorkflow{
		Name:         raw.Name,
		Owner:        raw.Owner,
		Repos:        workflowRepos,
		Filters:      filters,
		PRState:      raw.PRState,
		SectionTitle: raw.SectionTitle,
		IncludeDiff:  raw.IncludeDiff,
	}
	return wf, nil
}

func BuildProjectListWorkflow(raw *config.RawWorkflow, jiraDomain string) (Workflow, error) {
	filters, err := BuildFiltersList(raw)
	if err != nil {
		return nil, err
	}
	wf := ProjectListWorkflow{
		Name:         raw.Name,
		Owner:        raw.Owner,
		Repo:         raw.Repo,
		JiraDomain:   jiraDomain,
		JiraEpic:     raw.JiraEpic,
		Filters:      filters,
		SectionTitle: raw.SectionTitle,
		IncludeDiff:  raw.IncludeDiff,
	}
	return wf, nil
}

var filter_func_map = map[string]func(prs []*github.PullRequest) []*github.PullRequest{
	"FilterMyReviewRequested": git_tools.FilterMyReviewRequested,
	"FilterNotDraft":          git_tools.FilterNotDraft,
	"FilterIsDraft":           git_tools.FilterIsDraft,
	"FilterNotMyPRs":          git_tools.FilterNotMyPRs,
	"FilterMyPRs":             git_tools.FilterMyPRs,
	"FilterCIPassing":         git_tools.FilterCIPassing,
	"FilterCIFailing":         git_tools.FilterCIFailing,
	"FilterStale":             git_tools.FilterStale,
	"FilterNotStale":          git_tools.FilterNotStale,
	"FilterWaitingOnMe":       git_tools.FilterWaitingOnMe,
	"FilterWaitingOnAuthor":    git_tools.FilterWaitingOnAuthor,
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

		filter_func := filter_func_map[filterName]
		if filter_func == nil {
			return nil, fmt.Errorf("unknown filter %q", filterName)
		}
		filters = append(filters, filter_func)
	}
	return filters, nil
}
