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

// Plugin defines the configuration for an installed plugin
type Plugin struct {
	Name            string
	Command         string
	IncludeDiff     bool
	IncludeHeaders  bool
	IncludeComments bool
	IncludeBranch   bool
	OnlyOnDemand    bool
}

// AI providers and execution modes a [[AIFeatures]] entry can name. They live
// here, beside the validation that checks them, because config cannot import
// the ai package (ai reads config).
const (
	AIProviderGemini  = "gemini"
	AIProviderCommand = "command"

	// AIModeOneShot answers from a single model call.
	AIModeOneShot = "oneshot"
	// AIModeAgent runs a multi-turn loop in which the model may call tools.
	AIModeAgent = "agent"
)

// AISettings is the [AI] table: defaults every [[AIFeatures]] entry inherits.
// It enables nothing on its own — each feature is off until its entry says
// Enabled = true.
type AISettings struct {
	// DefaultProvider is the provider a feature uses when its entry names none:
	// "gemini" (needs GEMINI_API_KEY) or "command". Empty picks "command" when a
	// command is configured and "gemini" otherwise; neither is preferred beyond
	// that.
	DefaultProvider string
	// DefaultCommand is the command line the command provider runs, e.g.
	// "claude -p". It is split into words like a shell would (quotes work, pipes
	// and variables do not), receives the prompt on stdin, and must print its
	// answer on stdout.
	DefaultCommand string
}

// AIFeature is one [[AIFeatures]] entry: it switches a registered AI feature on
// and says how it runs.
type AIFeature struct {
	// ID names the feature, e.g. "comments-addressed".
	ID      string
	Enabled bool
	// Mode is AIModeOneShot or AIModeAgent; empty uses the feature's default.
	Mode string
	// Automatic also runs the feature after a PR is fetched or updated, the way
	// plugins run, instead of only when a client asks for it. It has no effect
	// unless Enabled is set too.
	Automatic bool
	// Provider and Command override the [AI] defaults for this feature.
	Provider string
	Command  string
}

// AIProviderFor resolves the provider and command a feature runs with: its own
// settings, then the [AI] defaults, then — when neither names a provider —
// "command" if a command is configured anywhere and "gemini" otherwise.
func (c Config) AIProviderFor(f AIFeature) (provider, command string) {
	command = f.Command
	if command == "" {
		command = c.AI.DefaultCommand
	}
	provider = f.Provider
	if provider == "" {
		provider = c.AI.DefaultProvider
	}
	if provider == "" {
		if command != "" {
			provider = AIProviderCommand
		} else {
			provider = AIProviderGemini
		}
	}
	return provider, command
}

// AIFeatureSettings returns the [[AIFeatures]] entry for id, and false when
// the config has none — which leaves the feature disabled.
func (c Config) AIFeatureSettings(id string) (AIFeature, bool) {
	for _, f := range c.AIFeatures {
		if f.ID == id {
			return f, true
		}
	}
	return AIFeature{}, false
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
	// ExperimentalLLMFileOrdering, when true, orders the files in a PR diff via
	// an LLM (integration first, then implementation, then styling, then tests)
	// instead of the default test-files-last sort. Off by default.
	ExperimentalLLMFileOrdering bool
	// ExperimentalLLMReviewEase, when true, rates how easy each PR is to review
	// ("easy", "medium", or "hard") in the same LLM call that computes the diff
	// file ordering. The rating is exposed as the review_ease field in PR
	// metadata and review list items. Off by default; requires GEMINI_API_KEY.
	ExperimentalLLMReviewEase bool
	// AI holds the defaults for the AI features, and AIFeatures switches them
	// on one by one. Both are absent from the built-in defaults, so the AI layer
	// does nothing until a config file enables a feature.
	AI         AISettings
	AIFeatures []AIFeature
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
		Repos                       []string
		JiraDomain                  string
		SleepDuration               int64
		Workflows                   []RawWorkflow
		GithubUsername              string
		RepoLocation                string
		AutoWorktree                bool
		DesktopNotifications        bool
		SectionPriority             map[string]int
		SectionSorting              map[string]string
		Plugins                     []Plugin
		RepoConfigs                 map[string]RepoConfig
		ExperimentalLLMFileOrdering bool
		ExperimentalLLMReviewEase   bool
		AI                          AISettings
		AIFeatures                  []AIFeature
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
		Repos:                       intermediate_config.Repos,
		RawWorkflows:                intermediate_config.Workflows,
		SleepDuration:               parsed_sleep_duration,
		JiraDomain:                  intermediate_config.JiraDomain,
		GithubUsername:              intermediate_config.GithubUsername,
		RepoLocation:                repoLocation,
		AutoWorktree:                intermediate_config.AutoWorktree,
		DesktopNotifications:        intermediate_config.DesktopNotifications,
		SectionPriority:             intermediate_config.SectionPriority,
		SectionSorting:              intermediate_config.SectionSorting,
		Plugins:                     intermediate_config.Plugins,
		RepoConfigs:                 repoConfigs,
		ExperimentalLLMFileOrdering: intermediate_config.ExperimentalLLMFileOrdering,
		ExperimentalLLMReviewEase:   intermediate_config.ExperimentalLLMReviewEase,
		AI:                          intermediate_config.AI,
		AIFeatures:                  intermediate_config.AIFeatures,
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
