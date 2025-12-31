package workflows

import (
	"crs/config"
	"crs/git_tools"
	"crs/jira"
	"crs/org"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
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
		c <- *output
	} else {
		rr.Skipped += 1
	}
}

func (rr *RunResult) Report() string {
	return fmt.Sprintf("A: %d; U: %d; R: %d; S: %d", rr.Added, rr.Updated, rr.Deleted, rr.Skipped)
}

type SingleRepoSyncReviewRequestsWorkflow struct {
	Name                string
	Owner               string
	Repo                string
	Filters             []git_tools.PRFilter
	OrgFileName         string
	SectionTitle        string
	ReleaseCheckCommand string
	Prune               string
	IncludeDiff         bool
}

func (w SingleRepoSyncReviewRequestsWorkflow) GetName() string {
	return w.Name
}

func (w SingleRepoSyncReviewRequestsWorkflow) GetOrgFilename() string {
	return w.OrgFileName
}

func (w SingleRepoSyncReviewRequestsWorkflow) GetOrgSectionName() string {
	return w.SectionTitle
}

func (w SingleRepoSyncReviewRequestsWorkflow) Run(log *slog.Logger, c chan FileChanges, file_change_wg *sync.WaitGroup) (RunResult, error) {
	prs := git_tools.GetPRs(
		git_tools.GetGithubClient(),
		"open",
		w.Owner,
		w.Repo,
	)

	prs = git_tools.ApplyPRFilters(prs, w.Filters)
	db := config.C.DB
	doc := org.NewDBClient(db, org.BaseOrgSerializer{ReleaseCheckCommand: w.ReleaseCheckCommand})
	section, err := doc.GetSection(w.SectionTitle)
	if err != nil {
		log.Error("Error getting section", "error", err, "section", w.SectionTitle)
		return RunResult{}, errors.New("Section Not Found")
	}

	result := ProcessPRsDB(log, prs, c, doc, section, file_change_wg, w.Prune, w.IncludeDiff)
	return result, nil
}

type SyncReviewRequestsWorkflow struct {
	// Github repo info
	Name        string
	Owner       string
	Repos       []string
	Filters     []git_tools.PRFilter
	Prune       string
	IncludeDiff bool

	// org output info
	OrgFileName         string
	SectionTitle        string
	ReleaseCheckCommand string
}

func (w SyncReviewRequestsWorkflow) Run(log *slog.Logger, c chan FileChanges, file_change_wg *sync.WaitGroup) (RunResult, error) {
	client := git_tools.GetGithubClient()
	prs := git_tools.GetManyRepoPRs(client, "open", w.Owner, w.Repos)
	prs = git_tools.ApplyPRFilters(prs, w.Filters)
	db := config.C.DB
	doc := org.NewDBClient(db, org.BaseOrgSerializer{ReleaseCheckCommand: w.ReleaseCheckCommand})
	section, err := doc.GetSection(w.SectionTitle)
	if err != nil {
		log.Error("Error getting section", "error", err, "section", w.SectionTitle)
		return RunResult{}, errors.New("Section Not Found")
	}
	log.Info("Got section: " + strconv.FormatInt(section.ID, 10) + " + " + section.SectionName)
	result := ProcessPRsDB(log, prs, c, doc, section, file_change_wg, w.Prune, w.IncludeDiff)
	return result, nil
}

func (w SyncReviewRequestsWorkflow) GetName() string {
	return w.Name
}

func (w SyncReviewRequestsWorkflow) GetOrgFilename() string {
	return w.OrgFileName
}

func (w SyncReviewRequestsWorkflow) GetOrgSectionName() string {
	return w.SectionTitle
}

type ListMyPRsWorkflow struct {
	Name                string
	Owner               string
	Repos               []string
	Filters             []git_tools.PRFilter
	OrgFileName         string
	SectionTitle        string
	PRState             string
	ReleaseCheckCommand string
	Prune               string
	IncludeDiff         bool
}

func (w ListMyPRsWorkflow) GetName() string {
	return w.Name
}

func (w ListMyPRsWorkflow) GetOrgFilename() string {
	return w.OrgFileName
}

func (w ListMyPRsWorkflow) GetOrgSectionName() string {
	return w.SectionTitle
}

func (w ListMyPRsWorkflow) Run(log *slog.Logger, c chan FileChanges, file_change_wg *sync.WaitGroup) (RunResult, error) {
	client := git_tools.GetGithubClient()
	prs := git_tools.GetManyRepoPRs(client, w.PRState, w.Owner, w.Repos)

	prs = git_tools.ApplyPRFilters(prs, w.Filters)
	db := config.C.DB
	doc := org.NewDBClient(db, org.BaseOrgSerializer{ReleaseCheckCommand: w.ReleaseCheckCommand})
	section, err := doc.GetSection(w.SectionTitle)
	if err != nil {
		log.Error("Error getting section", "error", err, "section", w.SectionTitle)
		return RunResult{}, errors.New("Section Not Found")
	}
	prs = git_tools.ApplyPRFilters(prs, []git_tools.PRFilter{git_tools.MyPRs})
	result := ProcessPRsDB(log, prs, c, doc, section, file_change_wg, w.Prune, w.IncludeDiff)
	return result, nil
}

type ProjectListWorkflow struct {
	Name                string
	Owner               string
	Repo                string
	Filters             []git_tools.PRFilter
	SectionTitle        string
	JiraDomain          string
	JiraEpic            string
	ReleaseCheckCommand string
	Prune               string
	IncludeDiff         bool
}

func (w ProjectListWorkflow) GetName() string {
	return w.Name
}

func (w ProjectListWorkflow) GetOrgSectionName() string {
	return w.SectionTitle
}

func (w ProjectListWorkflow) Run(log *slog.Logger, c chan FileChanges, file_change_wg *sync.WaitGroup) (RunResult, error) {
	client := git_tools.GetGithubClient()
	db := config.C.DB
	doc := org.NewDBClient(db, org.BaseOrgSerializer{ReleaseCheckCommand: w.ReleaseCheckCommand})

	section, err := doc.GetSection(w.SectionTitle)
	if err != nil {
		return RunResult{}, errors.New("Section Not Found")
	}
	if w.JiraEpic == "" {
		// I used to let just define []int for PR #s in config, could easily bring that back
		return RunResult{}, errors.New("ProjectList requires Jira Epic")
	}
	projectPRs := jira.GetProjectPRKeys(w.JiraDomain, w.JiraEpic, w.Repo)

	prs := git_tools.GetSpecificPRs(client, w.Owner, w.Repo, projectPRs)
	result := ProcessPRsDB(log, prs, c, doc, section, file_change_wg, w.Prune, w.IncludeDiff)
	return result, nil
}
