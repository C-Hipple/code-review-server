package config

import (
	"codereviewserver/database"
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
}

// Define your classes
type Config struct {
	Repos          []string
	RawWorkflows   []RawWorkflow
	SleepDuration  time.Duration
	JiraDomain     string
	GithubUsername string
	DB             *database.DB
}

var C Config

func init() {

	var intermediate_config struct {
		Repos          []string
		JiraDomain     string
		SleepDuration  int64
		Workflows      []RawWorkflow
		GithubUsername string
	}
	home_dir, err := os.UserHomeDir()
	configPath := "~/.config/codereviewserver.toml"
	the_bytes, err := os.ReadFile(configPath)
	if err != nil {
		// Fallback to home directory
		the_bytes, err = os.ReadFile(filepath.Join(home_dir, ".config/codereviewserver.toml"))
		if err != nil {
			panic(err)
		}
	}
	err = toml.Unmarshal(the_bytes, &intermediate_config)
	if err != nil {
		panic(err)
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
	db, err := database.NewDB(dbPath)
	if err != nil {
		panic(err)
	}

	C = Config{
		Repos:          intermediate_config.Repos,
		RawWorkflows:   intermediate_config.Workflows,
		SleepDuration:  parsed_sleep_duration,
		JiraDomain:     intermediate_config.JiraDomain,
		GithubUsername: intermediate_config.GithubUsername,
		DB:             db,
	}
}
