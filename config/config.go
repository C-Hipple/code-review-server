package config

import (
	"crs/database"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
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
	ReleaseCheckCommand string
	Prune               string
	GithubUsername      string
	IncludeDiff         bool
	Teams               []string // Teams to filter PRs by when using FilterTeamRequested
}

// Plugin defines the configuration for an installed plugin
type Plugin struct {
	Name            string
	Command         string
	IncludeDiff     bool
	IncludeHeaders  bool
	IncludeComments bool
}

// Define your classes
type Config struct {
	Repos          []string // List of repositories in "owner/repo" format. Workflows can override this.
	RawWorkflows   []RawWorkflow
	SleepDuration  time.Duration
	JiraDomain     string
	GithubUsername string
	RepoLocation   string
	AutoWorktree   bool
	SectionPriority map[string]int // Map of section title to priority (lower is better)
	Plugins         []Plugin
	DB              *database.DB
}

var C Config

var UserHomeDir = os.UserHomeDir

func getCRSHome() (string, error) {
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

// parseConfig parses the configuration from bytes and returns a Config struct.
// It does NOT initialize the database.
func parseConfig(data []byte) (*Config, error) {
	var intermediate_config struct {
		Repos           []string
		JiraDomain      string
		SleepDuration   int64
		Workflows       []RawWorkflow
		GithubUsername  string
		RepoLocation    string
		AutoWorktree    bool
		SectionPriority map[string]int
		Plugins         []Plugin
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

	return &Config{
		Repos:           intermediate_config.Repos,
		RawWorkflows:    intermediate_config.Workflows,
		SleepDuration:   parsed_sleep_duration,
		JiraDomain:      intermediate_config.JiraDomain,
		GithubUsername:  intermediate_config.GithubUsername,
		RepoLocation:    repoLocation,
		AutoWorktree:    intermediate_config.AutoWorktree,
		SectionPriority: intermediate_config.SectionPriority,
		Plugins:         intermediate_config.Plugins,
	}, nil
}

// Initialize loads the configuration from the config file and initializes the database.
// This should be called from main() to allow proper error handling.
func Initialize() error {
	configHome, err := getXDGConfigHome()
	if err != nil {
		return fmt.Errorf("failed to get config home: %w", err)
	}

	configPath := filepath.Join(configHome, "codereviewserver.toml")
	the_bytes, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file at %s: %w", configPath, err)
	}

	config, err := parseConfig(the_bytes)
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
	C = *config
	return nil
}
