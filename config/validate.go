package config

import (
	"crs/subprocess"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
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

	problems = append(problems, validateRepos(cfg.Repos)...)

	// 0 is meaningful for both ("never"), so only a negative count is wrong.
	if cfg.UserActiveHours < 0 {
		problems = append(problems, rootError("UserActiveHours",
			"must be 0 (run every user's workflows however long they have been idle) or a positive number of hours"))
	}
	if cfg.UserRetentionDays < 0 {
		problems = append(problems, rootError("UserRetentionDays",
			"must be 0 (never delete idle users) or a positive number of days"))
	}

	problems = append(problems, validateGitHubOAuth(cfg)...)

	problems = append(problems, validateSectionSorting(cfg.SectionSorting)...)
	problems = append(problems, validateWorkflowEntries(cfg.RawWorkflows)...)

	problems = append(problems, validateLegacyLLM(cfg)...)
	problems = append(problems, validatePlugins(cfg)...)
	return append(problems, validateAI(cfg)...)
}

// MaxUserWorkflows caps how many workflows a hosted server keeps for one user.
// Each one is a GitHub fan-out every cycle on the server's shared token, and a
// dashboard is a handful of sections; fifty is far past any real list.
const MaxUserWorkflows = 50

// ValidateUserSettings checks what a registered user sends a hosted server in
// place of a config file — their workflows, fallback Repos and SectionSorting —
// by the rules Validate applies to the same keys of the server's own file, plus
// two of its own: at most MaxUserWorkflows workflows, and no control characters
// in a workflow's name, which ends up inside an ownership key (OwnershipKey
// joins login and name with one). Like Validate it leaves workflow types and
// filters to workflows.ValidateWorkflows.
func ValidateUserSettings(raws []RawWorkflow, repos []string, sorting map[string]string) []ValidationError {
	problems := validateRepos(repos)
	problems = append(problems, validateSectionSorting(sorting)...)
	if len(raws) > MaxUserWorkflows {
		problems = append(problems, rootError("Workflows", "has %d workflows; at most %d are allowed", len(raws), MaxUserWorkflows))
	}
	problems = append(problems, validateWorkflowEntries(raws)...)
	for i, wf := range raws {
		if strings.IndexFunc(wf.Name, unicode.IsControl) >= 0 {
			problems = append(problems, workflowError(i, "Name", "must not contain control characters"))
		}
	}
	return problems
}

func validateRepos(repos []string) []ValidationError {
	var problems []ValidationError
	for i, repo := range repos {
		if err := ValidateRepoEntry(repo); err != nil {
			problems = append(problems, rootError("Repos", "entry %d: %v", i, err))
		}
	}
	return problems
}

func validateSectionSorting(sorting map[string]string) []ValidationError {
	var problems []ValidationError
	for section, method := range sorting {
		if !validSectionSorting[method] {
			problems = append(problems, rootError("SectionSorting",
				"section %q has unknown sorting %q (expected \"newest_first\" or \"oldest_first\")", section, method))
		}
	}
	return problems
}

// githubOAuthScope is one GitHub OAuth scope: "repo", "read:org",
// "public_repo" and the like.
var githubOAuthScope = regexp.MustCompile(`^[a-z0-9_:]+$`)

// validateGitHubOAuth checks the keys GitHub account linking reads. Both are
// sent to GitHub as they are, so a typo would surface only as GitHub refusing
// every user's link.
func validateGitHubOAuth(cfg *Config) []ValidationError {
	var problems []ValidationError
	if strings.IndexFunc(cfg.GitHubOAuthClientID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		problems = append(problems, rootError("GitHubOAuthClientID",
			"must be the app's client ID as GitHub shows it, without spaces"))
	}
	for _, scope := range strings.FieldsFunc(cfg.GitHubOAuthScopes, func(r rune) bool { return r == ' ' || r == ',' }) {
		if !githubOAuthScope.MatchString(scope) {
			problems = append(problems, rootError("GitHubOAuthScopes",
				"%q is not a GitHub OAuth scope; list scopes such as \"repo\" separated by spaces", scope))
		}
	}
	return problems
}

// validateWorkflowEntries checks the fields every workflow needs whatever its
// type, and that no two share a name.
func validateWorkflowEntries(raws []RawWorkflow) []ValidationError {
	var problems []ValidationError
	seenNames := make(map[string]int, len(raws))
	for i, wf := range raws {
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

// validLLMBackends are the LLM backends a [[Plugins]] entry, or the legacy
// file-ordering and review-ease keys, may name: the two HTTP APIs.
var validLLMBackends = map[string]bool{AIProviderGemini: true, AIProviderOpenRouter: true}

// validateLegacyLLM checks the backend the legacy keys give file-ordering and
// review-ease (legacyLLMKeys): a known provider, and a model for OpenRouter
// while either flag that would call it is on. Problems name the key the
// config sets, not the [[AIFeatures]] entry it stands for.
func validateLegacyLLM(cfg *Config) []ValidationError {
	var problems []ValidationError
	p := cfg.legacy.Provider
	if p != "" && !validLLMBackends[p] {
		problems = append(problems, rootError("ExperimentalLLMProvider",
			"unknown provider %q (expected \"gemini\" or \"openrouter\")", p))
	}
	enabled := cfg.legacy.FileOrdering || cfg.legacy.ReviewEase
	if enabled && p == AIProviderOpenRouter && strings.TrimSpace(cfg.legacy.Model) == "" {
		problems = append(problems, rootError("ExperimentalLLMModel",
			"the openrouter provider needs a model, e.g. \"google/gemini-2.5-flash\""))
	}
	return problems
}

// validatePlugins checks each [[Plugins]] entry: a name to show and key its
// results by, a command to run, and its LLM backend — a known provider, and a
// model wherever it names OpenRouter, which has no default. Duplicate names are
// rejected earlier, when the file is parsed.
func validatePlugins(cfg *Config) []ValidationError {
	var problems []ValidationError
	for i, p := range cfg.Plugins {
		field := func(name string) string { return fmt.Sprintf("Plugins[%d].%s", i, name) }
		if strings.TrimSpace(p.Name) == "" {
			problems = append(problems, rootError(field("Name"), "is required"))
		}
		if strings.TrimSpace(p.Command) == "" {
			problems = append(problems, rootError(field("Command"), "is required"))
		}
		if p.Provider != "" && !validLLMBackends[p.Provider] {
			problems = append(problems, rootError(field("Provider"),
				"unknown provider %q (expected \"gemini\" or \"openrouter\")", p.Provider))
		}
		if p.Provider == PluginProviderOpenRouter && strings.TrimSpace(p.Model) == "" {
			problems = append(problems, rootError(field("Model"),
				"the openrouter provider needs a model, e.g. \"anthropic/claude-sonnet-4.5\""))
		}
	}
	return problems
}

// validAIProviders and validAIModes are the names [AI] and [[AIFeatures]] may use.
var (
	validAIProviders = map[string]bool{AIProviderGemini: true, AIProviderOpenRouter: true, AIProviderCommand: true}
	validAIModes     = map[string]bool{AIModeOneShot: true, AIModeAgent: true}
)

// unknownAIProvider is the message for a provider name validAIProviders lacks.
func unknownAIProvider(p string) string {
	return fmt.Sprintf("unknown provider %q (expected \"gemini\", \"openrouter\" or \"command\")", p)
}

// validateAI checks the [AI] table and each [[AIFeatures]] entry for what can
// be judged without the feature registry: required IDs, known provider and
// mode names, command lines that split into words, a command wherever an
// enabled feature would run the command provider, and a model wherever one
// would run the openrouter provider. Whether an ID names a real
// feature, and whether that feature supports the chosen mode, is checked by
// ai.ValidateFeatures, which owns the registry — the same split as workflows.
func validateAI(cfg *Config) []ValidationError {
	var problems []ValidationError
	if p := cfg.AI.DefaultProvider; p != "" && !validAIProviders[p] {
		problems = append(problems, rootError("AI.DefaultProvider", "%s", unknownAIProvider(p)))
	}
	if c := cfg.AI.DefaultCommand; c != "" {
		if _, err := subprocess.SplitCommand(c); err != nil {
			problems = append(problems, rootError("AI.DefaultCommand", "%v", err))
		}
	}

	for i, f := range cfg.AIFeatures {
		field := func(name string) string { return fmt.Sprintf("AIFeatures[%d].%s", i, name) }
		if strings.TrimSpace(f.ID) == "" {
			problems = append(problems, rootError(field("ID"), "is required"))
		}
		if f.Mode != "" && !validAIModes[f.Mode] {
			problems = append(problems, rootError(field("Mode"),
				"unknown mode %q (expected \"oneshot\" or \"agent\")", f.Mode))
		}
		if f.Provider != "" && !validAIProviders[f.Provider] {
			problems = append(problems, rootError(field("Provider"), "%s", unknownAIProvider(f.Provider)))
		}
		if f.Command != "" {
			if _, err := subprocess.SplitCommand(f.Command); err != nil {
				problems = append(problems, rootError(field("Command"), "%v", err))
			}
		}
		// Only an enabled feature ever builds its provider, so a disabled entry
		// left half-configured is not worth blocking a save over.
		if f.Enabled {
			choice := cfg.AIProviderFor(f)
			if choice.Provider == AIProviderCommand && choice.Command == "" {
				problems = append(problems, rootError(field("Command"),
					"the command provider needs a command: set Command here or AI.DefaultCommand"))
			}
			if choice.Provider == AIProviderOpenRouter && strings.TrimSpace(choice.Model) == "" {
				problems = append(problems, rootError(field("Model"),
					"the openrouter provider needs a model: set Model here or AI.DefaultModel"))
			}
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
