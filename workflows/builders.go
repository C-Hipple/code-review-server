package workflows

import (
	"log/slog"
	"strings"
	"crs/config"
	"crs/git_tools"

	"github.com/google/go-github/v48/github"
)

func MatchWorkflows(workflow_maps []config.RawWorkflow, repos *[]string, jiraDomain string) []Workflow {
	workflows := []Workflow{}
	for _, raw_workflow := range workflow_maps {
		if raw_workflow.WorkflowType == "SyncReviewRequestsWorkflow" {
			workflows = append(workflows, BuildSyncReviewRequestWorkflow(&raw_workflow, repos))
		}
		if raw_workflow.WorkflowType == "SingleRepoSyncReviewRequestsWorkflow" {
			workflows = append(workflows, BuildSingleRepoReviewWorkflow(&raw_workflow, repos))
		}
		if raw_workflow.WorkflowType == "ListMyPRsWorkflow" {
			workflows = append(workflows, BuildListMyPRsWorkflow(&raw_workflow, repos))
		}
		if raw_workflow.WorkflowType == "ProjectListWorkflow" {
			workflows = append(workflows, BuildProjectListWorkflow(&raw_workflow, jiraDomain))
		}
	}
	return workflows
}

func BuildSingleRepoReviewWorkflow(raw *config.RawWorkflow, repos *[]string) Workflow {
	wf := SingleRepoSyncReviewRequestsWorkflow{
		Name:                raw.Name,
		Owner:               raw.Owner,
		Repo:                raw.Repo,
		Filters:             BuildFiltersList(raw),
		SectionTitle:        raw.SectionTitle,
		ReleaseCheckCommand: raw.ReleaseCheckCommand,
		Prune:               raw.Prune,
		IncludeDiff:         raw.IncludeDiff,
	}
	return wf
}

func BuildSyncReviewRequestWorkflow(raw *config.RawWorkflow, repos *[]string) Workflow {
	workflowRepos := *repos
	if len(raw.Repos) > 0 {
		workflowRepos = raw.Repos
	}

	wf := SyncReviewRequestsWorkflow{
		Name:                raw.Name,
		Owner:               raw.Owner,
		Repos:               workflowRepos,
		Filters:             BuildFiltersList(raw),
		SectionTitle:        raw.SectionTitle,
		ReleaseCheckCommand: raw.ReleaseCheckCommand,
		Prune:               raw.Prune,
		IncludeDiff:         raw.IncludeDiff,
	}
	return wf
}

func BuildListMyPRsWorkflow(raw *config.RawWorkflow, repos *[]string) Workflow {
	workflowRepos := *repos
	if len(raw.Repos) > 0 {
		workflowRepos = raw.Repos
	}

	wf := ListMyPRsWorkflow{
		Name:                raw.Name,
		Owner:               raw.Owner,
		Repos:               workflowRepos,
		Filters:             BuildFiltersList(raw),
		PRState:             raw.PRState,
		SectionTitle:        raw.SectionTitle,
		ReleaseCheckCommand: raw.ReleaseCheckCommand,
		Prune:               raw.Prune,
		IncludeDiff:         raw.IncludeDiff,
	}
	return wf
}

func BuildProjectListWorkflow(raw *config.RawWorkflow, jiraDomain string) Workflow {
	wf := ProjectListWorkflow{
		Name:                raw.Name,
		Owner:               raw.Owner,
		Repo:                raw.Repo,
		JiraDomain:          jiraDomain,
		JiraEpic:            raw.JiraEpic,
		Filters:             BuildFiltersList(raw),
		SectionTitle:        raw.SectionTitle,
		ReleaseCheckCommand: raw.ReleaseCheckCommand,
		Prune:               raw.Prune,
		IncludeDiff:         raw.IncludeDiff,
	}
	return wf
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

func BuildFiltersList(raw *config.RawWorkflow) []git_tools.PRFilter {
	filters := []git_tools.PRFilter{}

	// Automatically add team filter if Teams is configured
	if len(raw.Teams) > 0 {
		filters = append(filters, git_tools.MakeTeamFilters(raw.Teams))
	}

	for _, name := range raw.Filters {
		filterName, filterArg := ParseFilterString(name)

		if filterName == "FilterByLabel" {
			if filterArg == "" {
				slog.Warn("FilterByLabel requires an argument (e.g. FilterByLabel:bug)", "name", name)
				continue
			}
			filters = append(filters, git_tools.MakeLabelFilter(filterArg))
			continue
		}

		if filterName == "FilterByAuthor" {
			if filterArg == "" {
				slog.Warn("FilterByAuthor requires an argument (e.g. FilterByAuthor:username)", "name", name)
				continue
			}
			filters = append(filters, git_tools.MakeAuthorFilter(filterArg))
			continue
		}

		if filterName == "FilterExcludeAuthor" {
			if filterArg == "" {
				slog.Warn("FilterExcludeAuthor requires an argument (e.g. FilterExcludeAuthor:username)", "name", name)
				continue
			}
			filters = append(filters, git_tools.MakeExcludeAuthorFilter(filterArg))
			continue
		}

		filter_func := filter_func_map[filterName]
		if filter_func == nil {
			slog.Warn("Unmatched filter function", "name", name)
			continue
		}
		filters = append(filters, filter_func)
	}
	return filters
}
