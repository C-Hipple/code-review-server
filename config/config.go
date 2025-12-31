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
	Repos          []string
	RawWorkflows   []RawWorkflow
	SleepDuration  time.Duration
	JiraDomain     string
	GithubUsername string
	Plugins        []Plugin
	DB             *database.DB
}

var C Config

// Initialize loads the configuration from the config file and initializes the database.
// This should be called from main() to allow proper error handling.
func Initialize() error {
	var intermediate_config struct {
		Repos          []string
		JiraDomain     string
		SleepDuration  int64
		Workflows      []RawWorkflow
		GithubUsername string
		Plugins        []Plugin
	}
	home_dir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}
	configPath := filepath.Join(home_dir, ".config/codereviewserver.toml")
	the_bytes, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file at %s: %w", configPath, err)
	}
	err = toml.Unmarshal(the_bytes, &intermediate_config)
	if err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	for i := range intermediate_config.Workflows {
		if intermediate_config.Workflows[i].GithubUsername == "" {
			intermediate_config.Workflows[i].GithubUsername = intermediate_config.GithubUsername
		}
	}

	parsed_sleep_duration := time.Duration(1) * time.Minute
	if intermediate_config.SleepDuration != 0 {
		parsed_sleep_duration = time.Duration(intermediate_config.SleepDuration) * time.Minute
	}

	// Initialize database
	dbPath := filepath.Join(home_dir, ".config/codereviewserver.db")
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

	C = Config{
		Repos:          intermediate_config.Repos,
		RawWorkflows:   intermediate_config.Workflows,
		SleepDuration:  parsed_sleep_duration,
		JiraDomain:     intermediate_config.JiraDomain,
		GithubUsername: intermediate_config.GithubUsername,
		Plugins:        intermediate_config.Plugins,
		DB:             db,
	}
	return nil
}
