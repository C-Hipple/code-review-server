package config

import (
	"crs/database"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
	"sync"
)

// This struct implements all possible values a workflow can define, then they're written as-needed.
type RawWorkflow struct {
	WorkflowType        string
	Name                string
	Owner               string
	Repo                string
	Repos               []string
	JiraEpic            string
	Filters             []string
	SectionTitle        string
	PRState             string
	GithubUsername      string
	IncludeDiff         bool
	Teams               []string // Teams to filter PRs by when using FilterTeamRequested
	// DesktopNotifications overrides the global DesktopNotifications setting
	// for this workflow only. If nil, the global setting is used.
	DesktopNotifications *bool
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

// Define your classes
type Config struct {
	Repos          []string // List of repositories in "owner/repo" format. Workflows can override this.
	RawWorkflows   []RawWorkflow
	SleepDuration  time.Duration
	JiraDomain     string
	GithubUsername string
	RepoLocation   string
	AutoWorktree         bool
	DesktopNotifications bool              // Send desktop notifications when a PR is added to a section
	SectionPriority map[string]int    // Map of section title to priority (lower is better)
	SectionSorting  map[string]string // Map of section title to sorting method (e.g. "newest_first", "oldest_first")
	Plugins         []Plugin
	RepoConfigs     map[string]RepoConfig // Keyed by "owner/repo"
	// ExperimentalLLMFileOrdering, when true, orders the files in a PR diff via
	// an LLM (integration first, then implementation, then styling, then tests)
	// instead of the default test-files-last sort. Off by default.
	ExperimentalLLMFileOrdering bool
	// ExperimentalLLMReviewEase, when true, rates every PR via an LLM on how
	// difficult it is to review (easy, medium, or hard). The rating is computed
	// alongside the LLM diff file ordering and surfaced as `review_ease` in PR
	// metadata and review list items. Off by default.
	ExperimentalLLMReviewEase bool
	DB              *database.DB
}

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
		Repos               []string
		JiraDomain          string
		SleepDuration       int64
		Workflows           []RawWorkflow
		GithubUsername      string
		RepoLocation        string
		AutoWorktree         bool
		DesktopNotifications bool
		SectionPriority      map[string]int
		SectionSorting      map[string]string
		Plugins             []Plugin
		RepoConfigs         map[string]RepoConfig
		ExperimentalLLMFileOrdering bool
		ExperimentalLLMReviewEase   bool
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
		Repos:              intermediate_config.Repos,
		RawWorkflows:       intermediate_config.Workflows,
		SleepDuration:      parsed_sleep_duration,
		JiraDomain:         intermediate_config.JiraDomain,
		GithubUsername:     intermediate_config.GithubUsername,
		RepoLocation:       repoLocation,
		AutoWorktree:         intermediate_config.AutoWorktree,
		DesktopNotifications: intermediate_config.DesktopNotifications,
		SectionPriority:    intermediate_config.SectionPriority,
		SectionSorting:     intermediate_config.SectionSorting,
		Plugins:            intermediate_config.Plugins,
		RepoConfigs:        repoConfigs,
		ExperimentalLLMFileOrdering: intermediate_config.ExperimentalLLMFileOrdering,
		ExperimentalLLMReviewEase:   intermediate_config.ExperimentalLLMReviewEase,
	}, nil
}

// Initialize loads the configuration from the config file and initializes the database.
// This should be called from main() to allow proper error handling.
func loadConfig() (*Config, error) {
	configHome, err := getXDGConfigHome()
	if err != nil {
		return nil, fmt.Errorf("failed to get config home: %w", err)
	}

	configPath := filepath.Join(configHome, "codereviewserver.toml")
	the_bytes, err := os.ReadFile(configPath)
	if err != nil {
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
