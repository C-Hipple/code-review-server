package workflows

import (
	"crs/config"
	"crs/git_tools"
	"fmt"
	"strings"
)

// WorkflowTypeInfo describes a workflow type so clients can offer it in a
// picker without hard-coding the list. RequiredFields and OptionalFields are
// the type-specific fields; Name, WorkflowType and SectionTitle are required by
// every type and are left out.
type WorkflowTypeInfo struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Deprecated     bool     `json:"deprecated"`
	DeprecatedBy   string   `json:"deprecated_by,omitempty"`
	RequiredFields []string `json:"required_fields"`
	OptionalFields []string `json:"optional_fields"`
}

// workflowTypes is the registry backing MatchWorkflows' type switch. Keep the
// two in sync: a type listed here must build, and a buildable type belongs
// here so clients can offer it.
var workflowTypes = []WorkflowTypeInfo{
	{
		Name:           "SyncReviewRequestsWorkflow",
		Description:    "Sync pull requests from the configured repositories into a section, narrowed by filters.",
		RequiredFields: []string{},
		OptionalFields: []string{"Repos", "Filters", "Teams", "PRState", "IncludeDiff", "DesktopNotifications"},
	},
	{
		Name:           "SingleRepoSyncReviewRequestsWorkflow",
		Description:    "Sync pull requests from a single repository.",
		Deprecated:     true,
		DeprecatedBy:   "SyncReviewRequestsWorkflow with a single-element Repos list",
		RequiredFields: []string{"Repo"},
		OptionalFields: []string{"Filters", "Teams", "IncludeDiff", "DesktopNotifications"},
	},
	{
		Name:           "ListMyPRsWorkflow",
		Description:    "Sync pull requests you authored.",
		Deprecated:     true,
		DeprecatedBy:   "SyncReviewRequestsWorkflow with FilterMyPRs in Filters",
		RequiredFields: []string{},
		OptionalFields: []string{"Repos", "Filters", "PRState", "IncludeDiff", "DesktopNotifications"},
	},
	{
		Name:           "ProjectListWorkflow",
		Description:    "Sync the pull requests linked to the children of a Jira epic. Repos takes a list, so one workflow can cover a project spanning several repositories. Requires JiraDomain plus the JIRA_API_TOKEN and JIRA_API_EMAIL environment variables.",
		RequiredFields: []string{"JiraEpic"},
		OptionalFields: []string{"Repos", "Filters", "IncludeDiff", "DesktopNotifications"},
	},
}

// WorkflowTypes returns the workflow types a config may use.
func WorkflowTypes() []WorkflowTypeInfo {
	out := make([]WorkflowTypeInfo, len(workflowTypes))
	copy(out, workflowTypes)
	return out
}

// IsKnownWorkflowType reports whether MatchWorkflows can build the named type.
func IsKnownWorkflowType(name string) bool {
	for _, wt := range workflowTypes {
		if wt.Name == name {
			return true
		}
	}
	return false
}

// FilterInfo describes a filter clients can offer in a picker. Filters that
// take an argument are written as "FilterName:argument" in the config.
type FilterInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	RequiresArg bool   `json:"requires_arg"`
	ArgLabel    string `json:"arg_label,omitempty"`
}

// filterTypes describes every filter BuildFiltersList accepts. The no-argument
// entries mirror filter_func_map; the three argument-taking filters are handled
// explicitly in BuildFiltersList and have no entry there.
var filterTypes = []FilterInfo{
	{Name: "FilterMyReviewRequested", Description: "PRs where you are personally requested as a reviewer"},
	{Name: "FilterNotDraft", Description: "Exclude draft PRs"},
	{Name: "FilterIsDraft", Description: "Only include draft PRs"},
	{Name: "FilterNotMyPRs", Description: "Exclude PRs you authored"},
	{Name: "FilterMyPRs", Description: "Only include PRs you authored"},
	{Name: "FilterCIPassing", Description: "Only include PRs with passing CI"},
	{Name: "FilterCIFailing", Description: "Only include PRs with failing CI"},
	{Name: "FilterStale", Description: "PRs with no activity for more than 3 days"},
	{Name: "FilterNotStale", Description: "PRs with activity within the last 3 days"},
	{Name: "FilterWaitingOnMe", Description: "PRs where you are a requested reviewer and need to act"},
	{Name: "FilterWaitingOnAuthor", Description: "PRs where you acted last and are waiting on the author"},
	{Name: "FilterByLabel", Description: "Only include PRs carrying a label", RequiresArg: true, ArgLabel: "label"},
	{Name: "FilterByAuthor", Description: "Only include PRs authored by a user", RequiresArg: true, ArgLabel: "username"},
	{Name: "FilterExcludeAuthor", Description: "Exclude PRs authored by a user", RequiresArg: true, ArgLabel: "username"},
}

// FilterTypes returns the filters a workflow may use.
func FilterTypes() []FilterInfo {
	out := make([]FilterInfo, len(filterTypes))
	copy(out, filterTypes)
	return out
}

// lookupFilter finds a filter by name.
func lookupFilter(name string) (FilterInfo, bool) {
	for _, f := range filterTypes {
		if f.Name == name {
			return f, true
		}
	}
	return FilterInfo{}, false
}

// validPRStates are the values PRState may take; it is passed straight through
// to the GitHub list-pull-requests API.
var validPRStates = map[string]bool{"open": true, "closed": true, "all": true}

// ValidateWorkflows checks each workflow against the registries this package
// owns: the workflow types MatchWorkflows can build and the filters
// BuildFiltersList accepts. globalRepos is the root-level Repos list, which
// workflows inherit when they don't define their own.
//
// It complements config.Validate, which covers the fields (Name, SectionTitle,
// …) that every workflow needs regardless of type.
func ValidateWorkflows(raws []config.RawWorkflow, globalRepos []string) []config.ValidationError {
	problems := []config.ValidationError{}

	for i, raw := range raws {
		if raw.WorkflowType != "" && !IsKnownWorkflowType(raw.WorkflowType) {
			problems = append(problems, config.ValidationError{
				Workflow: i,
				Field:    "WorkflowType",
				Message:  fmt.Sprintf("unknown workflow type %q (expected one of: %s)", raw.WorkflowType, strings.Join(workflowTypeNames(), ", ")),
			})
		}

		problems = append(problems, validateFilters(i, raw.Filters)...)
		problems = append(problems, validateIdentityFilters(i, raw)...)

		for j, repo := range raw.Repos {
			if _, _, err := git_tools.ParseRepoName(strings.TrimSpace(repo)); err != nil {
				problems = append(problems, config.ValidationError{
					Workflow: i, Field: "Repos",
					Message: fmt.Sprintf("entry %d: %q is not in \"owner/repo\" form", j, repo),
				})
			}
		}

		if raw.PRState != "" && !validPRStates[raw.PRState] {
			problems = append(problems, config.ValidationError{
				Workflow: i, Field: "PRState",
				Message: fmt.Sprintf("unknown state %q (expected \"open\", \"closed\", or \"all\")", raw.PRState),
			})
		}

		for j, team := range raw.Teams {
			if strings.TrimSpace(team) == "" {
				problems = append(problems, config.ValidationError{
					Workflow: i, Field: "Teams",
					Message: fmt.Sprintf("entry %d is empty", j),
				})
			}
		}

		problems = append(problems, validateTypeSpecific(i, raw, globalRepos)...)
	}

	return problems
}

// validateFilters checks each configured filter string against the registry,
// including the "FilterName:argument" form.
func validateFilters(index int, filters []string) []config.ValidationError {
	problems := []config.ValidationError{}
	for _, entry := range filters {
		name, arg := ParseFilterString(strings.TrimSpace(entry))
		info, known := lookupFilter(name)
		if !known {
			problems = append(problems, config.ValidationError{
				Workflow: index, Field: "Filters",
				Message: fmt.Sprintf("unknown filter %q", name),
			})
			continue
		}
		if info.RequiresArg && strings.TrimSpace(arg) == "" {
			problems = append(problems, config.ValidationError{
				Workflow: index, Field: "Filters",
				Message: fmt.Sprintf("%s requires an argument (e.g. %s:<%s>)", name, name, info.ArgLabel),
			})
		}
		if !info.RequiresArg && arg != "" {
			problems = append(problems, config.ValidationError{
				Workflow: index, Field: "Filters",
				Message: fmt.Sprintf("%s does not take an argument, got %q", name, arg),
			})
		}
	}
	return problems
}

// validateIdentityFilters reports filters that compare against my GitHub login
// when no login is available. config.parseConfig copies the root-level
// GithubUsername into every workflow that doesn't set its own, so an empty value
// here means neither was configured — and the filter would match nothing at all,
// leaving its section permanently empty with nothing to explain it.
func validateIdentityFilters(index int, raw config.RawWorkflow) []config.ValidationError {
	if strings.TrimSpace(raw.GithubUsername) != "" {
		return nil
	}
	problems := []config.ValidationError{}
	needsIdentity := raw.WorkflowType == "ListMyPRsWorkflow"
	for _, entry := range raw.Filters {
		name, _ := ParseFilterString(strings.TrimSpace(entry))
		if IsIdentityFilter(name) {
			problems = append(problems, config.ValidationError{
				Workflow: index, Field: "Filters",
				Message: fmt.Sprintf("%s compares PRs against your GitHub login, but GithubUsername is not set (here or at the root of the config); the filter would match nothing", name),
			})
		}
	}
	if needsIdentity {
		problems = append(problems, config.ValidationError{
			Workflow: index, Field: "GithubUsername",
			Message: "is required by ListMyPRsWorkflow (set it here or at the root of the config)",
		})
	}
	return problems
}

// validateTypeSpecific checks the fields a particular workflow type needs.
func validateTypeSpecific(index int, raw config.RawWorkflow, globalRepos []string) []config.ValidationError {
	problems := []config.ValidationError{}

	switch raw.WorkflowType {
	case "SingleRepoSyncReviewRequestsWorkflow":
		if _, _, err := git_tools.ParseRepoName(strings.TrimSpace(raw.Repo)); err != nil {
			problems = append(problems, config.ValidationError{
				Workflow: index, Field: "Repo",
				Message: "is required and must be in \"owner/repo\" form",
			})
		}
	case "ProjectListWorkflow":
		if strings.TrimSpace(raw.JiraEpic) == "" {
			problems = append(problems, config.ValidationError{
				Workflow: index, Field: "JiraEpic",
				Message: "is required (the Jira epic key, e.g. BOARD-123)",
			})
		}
		if len(raw.Repos) == 0 && len(globalRepos) == 0 && (raw.Owner == "" || raw.Repo == "") {
			problems = append(problems, config.ValidationError{
				Workflow: index, Field: "Repos",
				Message: "is required when no root-level Repos list is configured",
			})
		}
	case "SyncReviewRequestsWorkflow", "ListMyPRsWorkflow":
		if len(raw.Repos) == 0 && len(globalRepos) == 0 {
			problems = append(problems, config.ValidationError{
				Workflow: index, Field: "Repos",
				Message: "is required when no root-level Repos list is configured",
			})
		}
	}

	return problems
}

// workflowTypeNames lists the registered type names, for error messages.
func workflowTypeNames() []string {
	names := make([]string, 0, len(workflowTypes))
	for _, wt := range workflowTypes {
		names = append(names, wt.Name)
	}
	return names
}
