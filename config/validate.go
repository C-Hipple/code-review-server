package config

import (
	"crs/subprocess"
	"fmt"
	"net"
	"path/filepath"
	"slices"
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
		problems = append(problems, validateAlwaysRunPlugins(cfg, i, wf)...)
	}

	problems = append(problems, validateLegacyLLM(cfg)...)
	problems = append(problems, validatePlugins(cfg)...)
	problems = append(problems, validateGitHubAPI(cfg)...)
	problems = append(problems, validateWebhooks(cfg)...)
	return append(problems, validateAI(cfg)...)
}

// validateWebhooks checks the [Webhooks] table. A table that asks for webhooks
// but can't have them — no secret, an address that won't parse — still runs:
// the server just keeps polling. That fallback is silent apart from a log line,
// which is why it is worth catching here.
func validateWebhooks(cfg *Config) []ValidationError {
	var problems []ValidationError
	w := cfg.Webhooks
	// With a Pub/Sub subscription nothing listens, so the address and secret
	// don't matter; validatePubSub says to remove the address instead.
	if addr := strings.TrimSpace(w.ListenAddress); addr != "" && !cfg.PubSubConfigured() {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			problems = append(problems, rootError("Webhooks.ListenAddress",
				"%q is not host:port (e.g. \":8790\", or \"127.0.0.1:8790\" behind a tunnel)", w.ListenAddress))
		}
		if cfg.WebhookSecret() == "" {
			problems = append(problems, rootError("Webhooks.Secret",
				"needs the secret the GitHub webhook was created with: set Secret here or export %s. Without it deliveries can't be verified, so the server keeps polling",
				WebhookSecretEnv))
		}
	}
	if p := strings.TrimSpace(w.Path); p != "" && !strings.HasPrefix(p, "/") {
		problems = append(problems, rootError("Webhooks.Path", "must start with \"/\", e.g. %q", DefaultWebhookPath))
	}
	for i, repo := range w.Repos {
		if err := validateWebhookRepo(repo); err != nil {
			problems = append(problems, rootError("Webhooks.Repos", "entry %d: %v", i, err))
		}
	}
	if w.FullRefreshInterval < 0 {
		problems = append(problems, rootError("Webhooks.FullRefreshInterval",
			"must be a positive number of minutes, or 0 for the default (%d)", int(DefaultWebhookFullRefresh.Minutes())))
	}
	return append(problems, validatePubSub(cfg)...)
}

// validatePubSub checks the [Webhooks.PubSub] table. Like the rest of
// [Webhooks], a subscription that can't be pulled from only leaves the server
// polling, with a log line saying why.
func validatePubSub(cfg *Config) []ValidationError {
	var problems []ValidationError
	p := cfg.Webhooks.PubSub
	sub := strings.TrimSpace(p.Subscription)
	if sub == "" {
		if p != (PubSubSettings{}) {
			problems = append(problems, rootError("Webhooks.PubSub.Subscription",
				"is required for the rest of [Webhooks.PubSub] to do anything, e.g. \"projects/my-project/subscriptions/crs-github-events\""))
		}
		return problems
	}
	if !validPubSubSubscription(sub) {
		problems = append(problems, rootError("Webhooks.PubSub.Subscription",
			"%q is not \"projects/<project>/subscriptions/<subscription>\"", p.Subscription))
	}
	if strings.TrimSpace(cfg.Webhooks.ListenAddress) != "" {
		problems = append(problems, rootError("Webhooks.ListenAddress",
			"is ignored while Webhooks.PubSub.Subscription is set: deliveries come from one or the other, so remove one of them"))
	}
	// Whether the file is there is the subscriber's to report, when it reads
	// it: a key missing on one machine shouldn't stop the config being saved.
	if path := strings.TrimSpace(p.CredentialsFile); path != "" && !filepath.IsAbs(path) && path != "~" && !strings.HasPrefix(path, "~/") {
		problems = append(problems, rootError("Webhooks.PubSub.CredentialsFile",
			"%q must be an absolute path, or start with ~/: a relative one would depend on where the server was started", p.CredentialsFile))
	}
	return problems
}

// validPubSubSubscription reports whether name is a subscription's full
// resource name, "projects/<project>/subscriptions/<subscription>".
func validPubSubSubscription(name string) bool {
	parts := strings.Split(name, "/")
	return len(parts) == 4 && parts[0] == "projects" && parts[1] != "" &&
		parts[2] == "subscriptions" && parts[3] != ""
}

// validateWebhookRepo accepts what a [Webhooks] Repos entry may be: a
// repository, or every repository of an owner ("owner/*").
func validateWebhookRepo(entry string) error {
	owner, repo, _ := strings.Cut(strings.TrimSpace(entry), "/")
	wildcardOwner := strings.Contains(owner, "*")
	if !wildcardOwner && repo == "*" && owner != "" {
		return nil
	}
	if wildcardOwner || strings.Contains(repo, "*") || ValidateRepoEntry(entry) != nil {
		return fmt.Errorf("%q is not \"owner/repo\" or \"owner/*\"", entry)
	}
	return nil
}

// validGitHubAPIs are the APIs a [GitHubAPI] key may name.
var validGitHubAPIs = map[string]bool{GitHubAPIREST: true, GitHubAPIGraphQL: true}

// validateGitHubAPI checks that each [GitHubAPI] key names an API. An unset key
// is fine — it means REST — and so, at run time, is a misspelled one, which is
// why it is worth catching here.
func validateGitHubAPI(cfg *Config) []ValidationError {
	var problems []ValidationError
	routes := cfg.GitHubAPI.Routes()
	keys := make([]string, 0, len(routes))
	for key := range routes {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if api := routes[key]; api != "" && !validGitHubAPIs[api] {
			problems = append(problems, rootError("GitHubAPI."+key,
				"unknown API %q (expected \"rest\" or \"graphql\")", api))
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

// validateAlwaysRunPlugins checks that each plugin a workflow always runs in
// its section is configured: a name with no [[Plugins]] entry runs nothing.
// Any plugin will do. A deferred one runs there and nowhere else unasked, and
// one that runs on its own gets an explicit run there in place of its
// automatic one, which a plugin can treat differently (--call-type).
func validateAlwaysRunPlugins(cfg *Config, index int, wf RawWorkflow) []ValidationError {
	var problems []ValidationError
	for _, name := range wf.AlwaysRunPlugins {
		if !slices.ContainsFunc(cfg.Plugins, func(p Plugin) bool { return p.Name == name }) {
			problems = append(problems, workflowError(index, "AlwaysRunPlugins",
				"%q is not the Name of a [[Plugins]] entry", name))
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
