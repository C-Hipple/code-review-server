package config

import (
	"fmt"
	"strings"
	"time"
)

// ValidationError describes one problem with a proposed configuration.
// Workflow is the index of the offending [[Workflows]] entry, or -1 when the
// problem is with a root-level setting.
type ValidationError struct {
	Workflow int    `json:"workflow"`
	Field    string `json:"field"`
	Message  string `json:"message"`
}

func (e ValidationError) Error() string {
	if e.Workflow >= 0 {
		return fmt.Sprintf("Workflows[%d].%s: %s", e.Workflow, e.Field, e.Message)
	}
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// rootError builds a ValidationError for a root-level setting.
func rootError(field, format string, args ...any) ValidationError {
	return ValidationError{Workflow: -1, Field: field, Message: fmt.Sprintf(format, args...)}
}

// workflowError builds a ValidationError for a single workflow entry.
func workflowError(index int, field, format string, args ...any) ValidationError {
	return ValidationError{Workflow: index, Field: field, Message: fmt.Sprintf(format, args...)}
}

// maxSleepDuration caps how long a workflow cycle may sleep. A day between
// syncs is already far past useful; anything larger is a typo (e.g. minutes
// entered as seconds).
const maxSleepDuration = 24 * time.Hour

// validSectionSorting lists the sorting methods the renderer understands.
var validSectionSorting = map[string]bool{"newest_first": true, "oldest_first": true}

// Validate checks the parts of a configuration that don't depend on the
// workflow registry: root-level settings plus the fields every workflow needs
// regardless of its type. Workflow types and filters are validated by
// workflows.ValidateWorkflows, which owns those registries; server-side callers
// run both.
func Validate(cfg *Config) []ValidationError {
	problems := []ValidationError{}
	if cfg == nil {
		return append(problems, rootError("", "configuration is empty"))
	}

	if cfg.SleepDuration <= 0 {
		problems = append(problems, rootError("SleepDuration", "must be a positive number of minutes"))
	} else if cfg.SleepDuration > maxSleepDuration {
		problems = append(problems, rootError("SleepDuration", "must be at most %d minutes (24 hours)", int(maxSleepDuration.Minutes())))
	}

	for i, repo := range cfg.Repos {
		if err := ValidateRepoEntry(repo); err != nil {
			problems = append(problems, rootError("Repos", "entry %d: %v", i, err))
		}
	}

	for section, sorting := range cfg.SectionSorting {
		if !validSectionSorting[sorting] {
			problems = append(problems, rootError("SectionSorting",
				"section %q has unknown sorting %q (expected \"newest_first\" or \"oldest_first\")", section, sorting))
		}
	}

	seenNames := make(map[string]int, len(cfg.RawWorkflows))
	for i, wf := range cfg.RawWorkflows {
		if strings.TrimSpace(wf.Name) == "" {
			problems = append(problems, workflowError(i, "Name", "is required"))
		} else if first, dup := seenNames[wf.Name]; dup {
			problems = append(problems, workflowError(i, "Name",
				"duplicates the name of workflow %d; workflow names identify which workflow owns an item and must be unique", first))
		} else {
			seenNames[wf.Name] = i
		}

		if strings.TrimSpace(wf.WorkflowType) == "" {
			problems = append(problems, workflowError(i, "WorkflowType", "is required"))
		}
		if strings.TrimSpace(wf.SectionTitle) == "" {
			problems = append(problems, workflowError(i, "SectionTitle", "is required"))
		}
	}

	return problems
}

// ValidateRepoEntry checks that a repository entry is in "owner/repo" form.
// git_tools.ParseRepoName does the same check at run time, but it imports this
// package, so the rule is duplicated here rather than inverted.
func ValidateRepoEntry(entry string) error {
	trimmed := strings.TrimSpace(entry)
	if trimmed == "" {
		return fmt.Errorf("repository entry is empty")
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("%q is not in \"owner/repo\" form", entry)
	}
	return nil
}
