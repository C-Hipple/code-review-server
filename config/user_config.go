package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// UserConfig is what a client registering with a hosted server sends from its
// local config file in place of running it: the server builds the user's
// dashboard from these keys alone. JSON keys are the Go field names, as
// RegisterUser takes them, and RawWorkflow's are its TOML keys.
type UserConfig struct {
	Workflows       []RawWorkflow
	Repos           []string
	SectionPriority map[string]int
	SectionSorting  map[string]string
}

// ReadUserConfig reads the keys a registration sends from the config file at
// path, exactly as written: a workflow's empty GithubUsername stays empty
// rather than inheriting the root one, since the server fills it from the
// registered login instead. DesktopNotifications is dropped from every
// workflow, as the server would drop it — a popup there would land on the
// server's machine. Lists and maps the file leaves out come back empty, never
// nil, so they encode as [] and {}.
//
// A missing file is an error rather than the built-in defaults: registering
// with defaults would hand the server a dashboard nobody wrote.
func ReadUserConfig(path string) (UserConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return UserConfig{}, fmt.Errorf("no config file at %s; registering sends its [[Workflows]], Repos, SectionPriority and SectionSorting, so create it first (crs -print-default-config > %s)", path, path)
		}
		return UserConfig{}, fmt.Errorf("could not read %s: %w", path, err)
	}

	var uc UserConfig
	if err := toml.Unmarshal(data, &uc); err != nil {
		return UserConfig{}, fmt.Errorf("could not parse %s: %w", path, err)
	}
	if uc.Workflows == nil {
		uc.Workflows = []RawWorkflow{}
	}
	for i := range uc.Workflows {
		uc.Workflows[i].DesktopNotifications = nil
	}
	if uc.Repos == nil {
		uc.Repos = []string{}
	}
	if uc.SectionPriority == nil {
		uc.SectionPriority = map[string]int{}
	}
	if uc.SectionSorting == nil {
		uc.SectionSorting = map[string]string{}
	}
	return uc, nil
}

// UserConfigJSON is ReadUserConfig of the config file at ConfigPath, as the
// JSON a registration sends. It touches nothing else — no database, no token,
// no GitHub — so a client without a TOML parser can run it before it has
// anything else set up.
func UserConfigJSON() ([]byte, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	uc, err := ReadUserConfig(path)
	if err != nil {
		return nil, err
	}
	return json.Marshal(uc)
}
