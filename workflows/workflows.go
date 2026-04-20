package workflows

import (
	"crs/config"
	"crs/git_tools"
	"crs/jira"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/google/go-github/v48/github"
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

	// org output info
	SectionTitle string
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

func (w SyncReviewRequestsWorkflow) Run(log *slog.Logger, prs []*github.PullRequest, c chan FileChanges, file_change_wg *sync.WaitGroup) (RunResult, error) {
	prs = git_tools.ApplyPRFilters(prs, w.Filters)
	db := config.C().DB
	section, err := db.GetOrCreateSection(w.SectionTitle, config.C().SectionPriority[w.SectionTitle])
	if err != nil {
		log.Error("Error getting section", "error", err, "section", w.SectionTitle)
		return RunResult{}, errors.New("Section Not Found")
	}
	log.Info("Got section: " + strconv.FormatInt(section.ID, 10) + " + " + section.SectionName)

	beforeCount, _ := db.GetItemCount()
	log.Info("Starting workflow", "items_before", beforeCount)
	result := ProcessPRsDB(log, prs, c, db, section, file_change_wg, w.IncludeDiff)
	afterCount, _ := db.GetItemCount()
	log.Info("Finished workflow", "items_after", afterCount)
	return result, nil
}

func (w SyncReviewRequestsWorkflow) GetName() string {
	return w.Name
}

func (w SyncReviewRequestsWorkflow) GetOrgSectionName() string {
	return w.SectionTitle
}

type ProjectListWorkflow struct {
	Name         string
	Owner        string
	Repo         string
	Filters      []git_tools.PRFilter
	SectionTitle string
	JiraDomain   string
	JiraEpic     string
	IncludeDiff  bool
}

func (w ProjectListWorkflow) GetName() string {
	return w.Name
}

func (w ProjectListWorkflow) GetOrgSectionName() string {
	return w.SectionTitle
}

func (w ProjectListWorkflow) GetPRRequirements() []PRRequirement {
	// Reverted to manual fetching to avoid long-running Jira lookups in the manager collection phase.
	return nil
}

func (w ProjectListWorkflow) Run(log *slog.Logger, prs []*github.PullRequest, c chan FileChanges, file_change_wg *sync.WaitGroup) (RunResult, error) {
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
	projectPRs := jira.GetProjectPRKeys(w.JiraDomain, w.JiraEpic, w.Repo)

	prs, err = git_tools.GetSpecificPRs(client, w.Owner, w.Repo, projectPRs)
	if err != nil {
		log.Error("Error getting specific PRs", "error", err)
		return RunResult{}, err
	}

	beforeCount, _ := db.GetItemCount()
	log.Info("Starting workflow", "items_before", beforeCount)
	result := ProcessPRsDB(log, prs, c, db, section, file_change_wg, w.IncludeDiff)
	afterCount, _ := db.GetItemCount()
	log.Info("Finished workflow", "items_after", afterCount)
	return result, nil
}
