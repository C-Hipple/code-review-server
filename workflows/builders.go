package workflows

import (
	"fmt"
	"gtdbot/config"
	"gtdbot/git_tools"

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
		Filters:             BuildFiltersList(raw.Filters),
		OrgFileName:         raw.OrgFileName,
		SectionTitle:        raw.SectionTitle,
		ReleaseCheckCommand: raw.ReleaseCheckCommand,
		Prune:               raw.Prune,
		IncludeDiff:         raw.IncludeDiff,
	}
	return wf
}

func BuildSyncReviewRequestWorkflow(raw *config.RawWorkflow, repos *[]string) Workflow {
	wf := SyncReviewRequestsWorkflow{
		Name:                raw.Name,
		Owner:               raw.Owner,
		Repos:               *repos,
		Filters:             BuildFiltersList(raw.Filters),
		OrgFileName:         raw.OrgFileName,
		SectionTitle:        raw.SectionTitle,
		ReleaseCheckCommand: raw.ReleaseCheckCommand,
		Prune:               raw.Prune,
		IncludeDiff:         raw.IncludeDiff,
	}
	return wf
}

func BuildListMyPRsWorkflow(raw *config.RawWorkflow, repos *[]string) Workflow {
	wf := ListMyPRsWorkflow{
		Name:                raw.Name,
		Owner:               raw.Owner,
		Repos:               *repos,
		Filters:             BuildFiltersList(raw.Filters),
		PRState:             raw.PRState,
		OrgFileName:         raw.OrgFileName,
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
		Filters:             BuildFiltersList(raw.Filters),
		OrgFileName:         raw.OrgFileName,
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
	"FilterMyTeamRequested":   git_tools.FilterMyTeamRequested,
	"FilterNotMyPRs":          git_tools.FilterNotMyPRs,
}

func BuildFiltersList(names []string) []git_tools.PRFilter {
	filters := []git_tools.PRFilter{}
	for _, name := range names {
		filter_func := filter_func_map[name]
		if filter_func == nil {
			fmt.Println("Warning: Unmatched filter function ", name)
			continue
		}
		filters = append(filters, filter_func)
	}
	return filters
}
