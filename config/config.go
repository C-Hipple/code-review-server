package config

import (
	"crs/database"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"sync"
)

// This struct implements all possible values a workflow can define, then they're written as-needed.
//
// The toml tags only matter when the server writes the config back out (see
// Update.Render): omitempty keeps a workflow's unused fields out of the file
// so a round-trip through the config RPCs doesn't litter it with empty
// strings and false booleans.
type RawWorkflow struct {
	WorkflowType   string   `toml:"WorkflowType"`
	Name           string   `toml:"Name"`
	Owner          string   `toml:"Owner,omitempty"`
	Repo           string   `toml:"Repo,omitempty"`
	Repos          []string `toml:"Repos,omitempty"`
	JiraEpic       string   `toml:"JiraEpic,omitempty"`
	Filters        []string `toml:"Filters,omitempty"`
	SectionTitle   string   `toml:"SectionTitle"`
	PRState        string   `toml:"PRState,omitempty"`
	GithubUsername string   `toml:"GithubUsername,omitempty"`
	IncludeDiff    bool     `toml:"IncludeDiff,omitempty"`
	Teams          []string `toml:"Teams,omitempty"` // Teams to filter PRs by when using FilterTeamRequested
	// DesktopNotifications overrides the global DesktopNotifications setting
	// for this workflow only. If nil, the global setting is used.
	DesktopNotifications *bool `toml:"DesktopNotifications,omitempty"`
}

// RepoConfig holds per-repository configuration settings.
type RepoConfig struct {
	ReleaseCheckCommand string
}

// Plugin defines the configuration for an installed plugin.
//
// As on RawWorkflow, the toml tags only matter when the server writes the
// config back out: omitempty keeps unset options out of the file.
type Plugin struct {
	Name            string `toml:"Name"`
	Command         string `toml:"Command"`
	IncludeDiff     bool   `toml:"IncludeDiff,omitempty"`
	IncludeHeaders  bool   `toml:"IncludeHeaders,omitempty"`
	IncludeComments bool   `toml:"IncludeComments,omitempty"`
	IncludeBranch   bool   `toml:"IncludeBranch,omitempty"`
	OnlyOnDemand    bool   `toml:"OnlyOnDemand,omitempty"`
	// Provider and Model say which LLM backend the plugin should call:
	// PluginProviderGemini or PluginProviderOpenRouter, and for OpenRouter the
	// model to ask for. The server hands them to the plugin in the
	// CRS_LLM_PROVIDER and CRS_LLM_MODEL environment variables; the bundled
	// plugins honor them, and a plugin that calls no model ignores them. Empty
	// leaves the variables to whatever the server's own environment says.
	Provider string `toml:"Provider,omitempty"`
	Model    string `toml:"Model,omitempty"`
}

// LLM backends a [[Plugins]] entry can name. A plugin is itself a command, so
// the AI features' command provider has no counterpart here.
const (
	PluginProviderGemini     = AIProviderGemini
	PluginProviderOpenRouter = AIProviderOpenRouter
)

// Environment variables a plugin's Provider and Model reach it through.
const (
	PluginProviderEnv = "CRS_LLM_PROVIDER"
	PluginModelEnv    = "CRS_LLM_MODEL"
)

// Env is what the server adds to the plugin's environment: its Provider and
// Model, each only when set.
func (p Plugin) Env() []string {
	var env []string
	if p.Provider != "" {
		env = append(env, PluginProviderEnv+"="+p.Provider)
	}
	if p.Model != "" {
		env = append(env, PluginModelEnv+"="+p.Model)
	}
	return env
}

// AI providers and execution modes a [[AIFeatures]] entry can name. They live
// here, beside the validation that checks them, because config cannot import
// the ai package (ai reads config).
const (
	AIProviderGemini     = "gemini"
	AIProviderOpenRouter = "openrouter"
	AIProviderCommand    = "command"

	// AIModeOneShot answers from a single model call.
	AIModeOneShot = "oneshot"
	// AIModeAgent runs a multi-turn loop in which the model may call tools.
	AIModeAgent = "agent"
)

// AISettings is the [AI] table: defaults every [[AIFeatures]] entry inherits.
// It enables nothing on its own — each feature is off until its entry says
// Enabled = true.
type AISettings struct {
	// DefaultProvider is the provider a feature uses when its entry sets
	// neither Provider nor Command: "gemini" (the Gemini API, which needs
	// GEMINI_API_KEY), "openrouter" (any model OpenRouter serves, which needs
	// OPENROUTER_API_KEY and a model) or "command" (a program on this machine).
	// Empty picks "command" when DefaultCommand is set and "gemini" otherwise.
	// The whole order is on AIProviderFor.
	DefaultProvider string `toml:"DefaultProvider,omitempty"`
	// DefaultCommand is the command line the command provider runs, e.g.
	// "claude -p". It is split into words like a shell would (quotes work, pipes
	// and variables do not), receives the prompt on stdin, and must print its
	// answer on stdout.
	DefaultCommand string `toml:"DefaultCommand,omitempty"`
	// DefaultModel is the model the openrouter provider asks for, as OpenRouter
	// names it, e.g. "anthropic/claude-sonnet-4.5". Only that provider reads it:
	// Gemini stays on gemini-flash-latest, and a command picks its own model.
	DefaultModel string `toml:"DefaultModel,omitempty"`
}

// AIFeature is one [[AIFeatures]] entry: it switches a registered AI feature on
// and says how it runs.
type AIFeature struct {
	// ID names the feature, e.g. "comments-addressed".
	ID      string `toml:"ID"`
	Enabled bool   `toml:"Enabled"`
	// Mode is AIModeOneShot or AIModeAgent; empty uses the feature's default.
	Mode string `toml:"Mode,omitempty"`
	// Automatic also runs the feature after a PR is fetched or updated, the way
	// plugins run, instead of only when a client asks for it. It has no effect
	// unless Enabled is set too.
	Automatic bool `toml:"Automatic,omitempty"`
	// Provider, Command and Model override the [AI] defaults for this feature.
	// A Command on its own also picks the command provider, over any [AI]
	// DefaultProvider; a Model picks nothing. See AIProviderFor.
	Provider string `toml:"Provider,omitempty"`
	Command  string `toml:"Command,omitempty"`
	Model    string `toml:"Model,omitempty"`
}

// AIProviderChoice is how a feature reaches a model, as AIProviderFor resolves
// it from config.
type AIProviderChoice struct {
	// Provider is AIProviderGemini, AIProviderOpenRouter or AIProviderCommand.
	Provider string
	// Command is the command line the command provider runs; only it reads it.
	Command string
	// Model is the model the openrouter provider asks for; only it reads it.
	Model string
}

// AIProviderFor resolves the provider a feature runs with, and the command
// and model that provider reads. The first of these that is set picks the
// provider:
//
//  1. the feature's Provider
//  2. the feature's Command, which picks "command"
//  3. [AI] DefaultProvider
//  4. [AI] DefaultCommand, which picks "command"
//  5. otherwise "gemini"
//
// That is, the feature's own settings beat the [AI] defaults, and at each
// level a named provider beats the one a command implies. No API key plays a
// part in the choice: GEMINI_API_KEY and OPENROUTER_API_KEY are read only once
// their provider has been picked. The command is the feature's Command, else
// [AI] DefaultCommand, and the model the feature's Model, else [AI]
// DefaultModel.
func (c Config) AIProviderFor(f AIFeature) AIProviderChoice {
	choice := AIProviderChoice{Command: f.Command, Model: f.Model}
	if choice.Command == "" {
		choice.Command = c.AI.DefaultCommand
	}
	if choice.Model == "" {
		choice.Model = c.AI.DefaultModel
	}
	switch {
	case f.Provider != "":
		choice.Provider = f.Provider
	case f.Command != "":
		choice.Provider = AIProviderCommand
	case c.AI.DefaultProvider != "":
		choice.Provider = c.AI.DefaultProvider
	case c.AI.DefaultCommand != "":
		choice.Provider = AIProviderCommand
	default:
		choice.Provider = AIProviderGemini
	}
	return choice
}

// IDs of the AI features the legacy root-level keys configure. config cannot
// import the ai package for them (ai reads config), so they are spelled out
// here; ai's tests check them against its registry.
const (
	legacyFileOrderingID = "file-ordering"
	legacyReviewEaseID   = "review-ease"
)

// legacyLLMKeys are the root-level keys that configured the diff file ordering
// and the review-ease rating before those were AI features. A config that
// still sets them keeps working: for either feature without an [[AIFeatures]]
// entry of its own, a flag that is on stands for an entry that enables it
// automatically on Provider (Gemini unless it names another) with Model — how
// the flags always ran, apart from any [AI] defaults.
type legacyLLMKeys struct {
	FileOrdering bool   // ExperimentalLLMFileOrdering
	ReviewEase   bool   // ExperimentalLLMReviewEase
	Provider     string // ExperimentalLLMProvider: "gemini" or "openrouter"
	Model        string // ExperimentalLLMModel: the model the openrouter provider asks for
}

// entry is the [[AIFeatures]] entry for the feature id that a legacy flag
// stands for, and false when no flag that is on stands for one.
func (k legacyLLMKeys) entry(id string) (AIFeature, bool) {
	on := (id == legacyFileOrderingID && k.FileOrdering) || (id == legacyReviewEaseID && k.ReviewEase)
	if !on {
		return AIFeature{}, false
	}
	provider := k.Provider
	if provider == "" {
		provider = AIProviderGemini
	}
	return AIFeature{ID: id, Enabled: true, Automatic: true, Provider: provider, Model: k.Model}, true
}

// AIFeatureSettings returns the [[AIFeatures]] entry for id, and false when
// the config has none — which leaves the feature disabled. For file-ordering
// and review-ease, a legacy flag that is on stands in for a missing entry
// (legacyLLMKeys), so code asking whether a feature is enabled goes through
// here rather than reading AIFeatures.
func (c Config) AIFeatureSettings(id string) (AIFeature, bool) {
	if f, ok := c.fileEntry(id); ok {
		return f, true
	}
	return c.legacy.entry(id)
}

// LegacyAIFeature returns the root-level key that switches the feature id on
// in the config file, and the [[AIFeatures]] entry it stands for, whether or
// not the file also has an entry of its own (which would win). ok is false
// when no legacy flag that is on stands for id.
func (c Config) LegacyAIFeature(id string) (key string, entry AIFeature, ok bool) {
	entry, ok = c.legacy.entry(id)
	if !ok {
		return "", AIFeature{}, false
	}
	key = "ExperimentalLLMFileOrdering"
	if id == legacyReviewEaseID {
		key = "ExperimentalLLMReviewEase"
	}
	return key, entry, true
}

// AutomaticAIFeatures returns the IDs of the AI features config enables to run
// automatically: the [[AIFeatures]] entries that say so, in file order, then
// the features a legacy flag stands in for.
func (c Config) AutomaticAIFeatures() []string {
	var ids []string
	for _, f := range c.AIFeatures {
		if f.Enabled && f.Automatic {
			ids = append(ids, f.ID)
		}
	}
	for _, id := range []string{legacyFileOrderingID, legacyReviewEaseID} {
		if _, own := c.fileEntry(id); own {
			continue
		}
		if f, ok := c.legacy.entry(id); ok && f.Enabled && f.Automatic {
			ids = append(ids, id)
		}
	}
	return ids
}

// fileEntry is the [[AIFeatures]] entry the config file has for id.
func (c Config) fileEntry(id string) (AIFeature, bool) {
	for _, f := range c.AIFeatures {
		if f.ID == id {
			return f, true
		}
	}
	return AIFeature{}, false
}

// The APIs a [GitHubAPI] key can send a lookup to. They live here, beside the
// validation that checks them, because config cannot import git_tools
// (git_tools reads config).
const (
	GitHubAPIREST    = "rest"
	GitHubAPIGraphQL = "graphql"
)

// GitHubAPISettings is the [GitHubAPI] table: which of GitHub's two APIs
// answers each lookup that both of them can answer. A key left unset means
// REST. The server revalidates a REST reply rather than refetching it, so
// asking again about data that hasn't changed costs nothing against the rate
// limit; a GraphQL query is charged every time, but against a budget of its
// own, and some lookups answer more completely there — each key says how.
// git_tools.RouteFor is what reads it.
type GitHubAPISettings struct {
	// ReviewRequestHistory is every team and user ever asked to review a PR,
	// behind the required-team chips on the review list. REST reads it from
	// the PR's issue events, GraphQL from its timeline; both answer fully.
	ReviewRequestHistory string `toml:"ReviewRequestHistory,omitempty"`
	// Reactions is who reacted to each comment and review. REST spends a
	// request per comment somebody reacted to, and has no endpoint for
	// reactions on a review's own body; GraphQL answers both in one query.
	Reactions string `toml:"Reactions,omitempty"`
	// Mergeability is whether each PR on the review list merges cleanly. REST
	// asks about one PR per request, GraphQL about fifty.
	Mergeability string `toml:"Mergeability,omitempty"`
}

// Routes maps each [GitHubAPI] key to the API it names, "" for a key left
// unset. Validation and git_tools.RouteFor both read the keys from here, so a
// new lookup is added in one place.
func (s GitHubAPISettings) Routes() map[string]string {
	return map[string]string{
		"ReviewRequestHistory": s.ReviewRequestHistory,
		"Reactions":            s.Reactions,
		"Mergeability":         s.Mergeability,
	}
}

// Define your classes
type Config struct {
	Repos                []string // List of repositories in "owner/repo" format. Workflows can override this.
	RawWorkflows         []RawWorkflow
	SleepDuration        time.Duration
	JiraDomain           string
	GithubUsername       string
	RepoLocation         string
	AutoWorktree         bool
	DesktopNotifications bool              // Send desktop notifications when a PR is added to a section
	SectionPriority      map[string]int    // Map of section title to priority (lower is better)
	SectionSorting       map[string]string // Map of section title to sorting method (e.g. "newest_first", "oldest_first")
	Plugins              []Plugin
	RepoConfigs          map[string]RepoConfig // Keyed by "owner/repo"
	// AI holds the defaults for the AI features, and AIFeatures switches them
	// on one by one. Both are absent from the built-in defaults, so the AI layer
	// does nothing until a config file enables a feature. AIFeatures holds only
	// the entries the file has: ask AIFeatureSettings whether a feature is on.
	AI         AISettings
	AIFeatures []AIFeature
	// GitHubAPI picks REST or GraphQL for the lookups both can answer.
	GitHubAPI GitHubAPISettings
	// legacy holds the root-level keys file ordering and review ease were
	// configured by before they were AI features; see legacyLLMKeys.
	legacy legacyLLMKeys
	// UsingDefaults is true when no config file exists and the server is
	// running DefaultConfigTOML. Nothing behaves differently because of it; it
	// exists so clients can say so.
	UsingDefaults bool
	DB            *database.DB
}

// LoginResolver looks up the GitHub login the configured API token belongs to,
// so a config that never names a user still has an identity for the filters
// that compare PRs against "me". main wires this to
// git_tools.GetAuthenticatedLogin; it is a variable because config cannot
// import git_tools (git_tools reads config). A nil resolver, or one that
// returns "", simply leaves GithubUsername empty — the behavior before this
// existed.
var LoginResolver func() string

var (
	c  Config
	mu sync.RWMutex
)

// C returns a copy of the current configuration.
func C() Config {
	mu.RLock()
	defer mu.RUnlock()
	return c
}

// SetC updates the current configuration. Primarily for tests.
func SetC(newCfg Config) {
	mu.Lock()
	defer mu.Unlock()
	c = newCfg
}

// GetReleaseCheckCommand returns the release check command for a given repo (owner/repo format).
func (c Config) GetReleaseCheckCommand(ownerRepo string) string {
	if rc, ok := c.RepoConfigs[ownerRepo]; ok {
		return rc.ReleaseCheckCommand
	}
	return ""
}

var UserHomeDir = os.UserHomeDir

func getCRSHome() (string, error) {
	return GetCRSHome()
}

// GetCRSHome returns the CRS home directory (respects CRS_HOME env override).
func GetCRSHome() (string, error) {
	if crsHome := os.Getenv("CRS_HOME"); crsHome != "" {
		return crsHome, nil
	}
	home, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".crs"), nil
}

func getXDGConfigHome() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg, nil
	}
	home, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// ParseConfigForTest is an exported wrapper around parseConfig for use in tests.
func ParseConfigForTest(data []byte) (*Config, error) {
	return parseConfig(data)
}

// parseConfig parses the configuration from bytes and returns a Config struct.
// It does NOT initialize the database.
func parseConfig(data []byte) (*Config, error) {
	var intermediate_config struct {
		Repos                []string
		JiraDomain           string
		SleepDuration        int64
		Workflows            []RawWorkflow
		GithubUsername       string
		RepoLocation         string
		AutoWorktree         bool
		DesktopNotifications bool
		SectionPriority      map[string]int
		SectionSorting       map[string]string
		Plugins              []Plugin
		RepoConfigs          map[string]RepoConfig
		AI                   AISettings
		AIFeatures           []AIFeature
		GitHubAPI            GitHubAPISettings
		// The legacy keys; see legacyLLMKeys.
		LegacyFileOrdering bool   `toml:"ExperimentalLLMFileOrdering"`
		LegacyReviewEase   bool   `toml:"ExperimentalLLMReviewEase"`
		LegacyProvider     string `toml:"ExperimentalLLMProvider"`
		LegacyModel        string `toml:"ExperimentalLLMModel"`
	}

	err := toml.Unmarshal(data, &intermediate_config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	pluginNames := make(map[string]bool)
	for _, p := range intermediate_config.Plugins {
		if pluginNames[p.Name] {
			return nil, fmt.Errorf("duplicate plugin name found: %s", p.Name)
		}
		pluginNames[p.Name] = true
	}

	// Same rule as plugins: two entries for one feature would leave it
	// ambiguous which settings apply.
	aiFeatureIDs := make(map[string]bool)
	for _, f := range intermediate_config.AIFeatures {
		if aiFeatureIDs[f.ID] {
			return nil, fmt.Errorf("duplicate AI feature ID found: %s", f.ID)
		}
		aiFeatureIDs[f.ID] = true
	}

	// Fill in who "me" is from the API token when the config doesn't say. This
	// runs before the per-workflow copy below, so a resolved login reaches the
	// identity filters the same way a configured one does. Nothing is written
	// back to disk: the file stays as the user wrote it.
	if intermediate_config.GithubUsername == "" && LoginResolver != nil {
		if login := strings.TrimSpace(LoginResolver()); login != "" {
			slog.Debug("Using the API token's user as GithubUsername", "login", login)
			intermediate_config.GithubUsername = login
		}
	}

	for i := range intermediate_config.Workflows {
		if intermediate_config.Workflows[i].GithubUsername == "" {
			intermediate_config.Workflows[i].GithubUsername = intermediate_config.GithubUsername
		}
	}

	repoLocation := intermediate_config.RepoLocation
	if repoLocation == "" {
		repoLocation = "~/"
	}

	parsed_sleep_duration := time.Duration(10) * time.Minute
	if intermediate_config.SleepDuration != 0 {
		parsed_sleep_duration = time.Duration(intermediate_config.SleepDuration) * time.Minute
	}

	repoConfigs := intermediate_config.RepoConfigs
	if repoConfigs == nil {
		repoConfigs = make(map[string]RepoConfig)
	}

	return &Config{
		Repos:                intermediate_config.Repos,
		RawWorkflows:         intermediate_config.Workflows,
		SleepDuration:        parsed_sleep_duration,
		JiraDomain:           intermediate_config.JiraDomain,
		GithubUsername:       intermediate_config.GithubUsername,
		RepoLocation:         repoLocation,
		AutoWorktree:         intermediate_config.AutoWorktree,
		DesktopNotifications: intermediate_config.DesktopNotifications,
		SectionPriority:      intermediate_config.SectionPriority,
		SectionSorting:       intermediate_config.SectionSorting,
		Plugins:              intermediate_config.Plugins,
		RepoConfigs:          repoConfigs,
		AI:                   intermediate_config.AI,
		AIFeatures:           intermediate_config.AIFeatures,
		GitHubAPI:            intermediate_config.GitHubAPI,
		legacy: legacyLLMKeys{
			FileOrdering: intermediate_config.LegacyFileOrdering,
			ReviewEase:   intermediate_config.LegacyReviewEase,
			Provider:     intermediate_config.LegacyProvider,
			Model:        intermediate_config.LegacyModel,
		},
	}, nil
}

// Initialize loads the configuration from the config file and initializes the database.
// This should be called from main() to allow proper error handling.
func loadConfig() (*Config, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	the_bytes, err := os.ReadFile(configPath)
	if err != nil {
		// A missing config file is a first run, not a failure: the built-in
		// defaults need nothing but a token, so the server comes up and starts
		// filling a dashboard instead of exiting with a message about a file
		// the user has never heard of.
		if errors.Is(err, os.ErrNotExist) {
			slog.Warn("No configuration file found; running with the built-in defaults",
				"path", configPath,
				"sections", "Waiting On Me, Review Requested",
				"customize", "codereviewserver -print-default-config > "+configPath)
			return DefaultConfig()
		}
		return nil, fmt.Errorf("failed to read config file at %s: %w", configPath, err)
	}

	return parseConfig(the_bytes)
}

// Reload reloads the configuration from the config file.
// It updates the global c struct but maintains the existing DB connection.
func Reload() error {
	newCfg, err := loadConfig()
	if err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()
	// Persist the database connection
	newCfg.DB = c.DB
	c = *newCfg
	slog.Info("Configuration reloaded successfully")
	return nil
}

// Initialize loads the configuration from the config file and initializes the database.
// This should be called from main() to allow proper error handling.
func Initialize() error {
	config, err := loadConfig()
	if err != nil {
		return err
	}

	// Initialize database
	crsHome, err := getCRSHome()
	if err != nil {
		return fmt.Errorf("failed to get CRS home: %w", err)
	}
	dbPath := filepath.Join(crsHome, "codereviewserver.db")

	// Attempt to migrate legacy database if it exists
	homeDir, err := UserHomeDir()
	if err == nil {
		legacyPaths := []string{
			filepath.Join(homeDir, ".config/codereviewserver.db"),
			filepath.Join(homeDir, ".config/codereviewserver/codereviewserver.db"),
		}

		for _, legacyDBPath := range legacyPaths {
			if _, err := os.Stat(legacyDBPath); err == nil {
				// Legacy DB exists
				if _, err := os.Stat(dbPath); os.IsNotExist(err) {
					// New DB does not exist, migrate
					slog.Info("Migrating database to new location", "old", legacyDBPath, "new", dbPath)
					if err := os.MkdirAll(crsHome, 0755); err != nil {
						slog.Error("Failed to create new CRS directory", "error", err)
					} else {
						if err := os.Rename(legacyDBPath, dbPath); err != nil {
							slog.Warn("Failed to move legacy database, falling back to legacy path", "error", err)
							dbPath = legacyDBPath
						}
					}
					break // Only migrate the first found legacy DB
				}
			}
		}
	}

	if _, err := os.Stat(dbPath); err == nil {
		slog.Info("Found database file", "path", dbPath)
	} else {
		slog.Info("Setting up database file", "path", dbPath)
	}
	db, err := database.NewDB(dbPath)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	slog.Info("Database initialized successfully")

	config.DB = db
	mu.Lock()
	defer mu.Unlock()
	c = *config
	return nil
}
