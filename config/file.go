package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// configFileName is the name of the TOML file inside the XDG config home.
const configFileName = "codereviewserver.toml"

// writeMu serializes read-modify-write cycles on the config file so two
// concurrent Apply calls can't clobber each other's changes.
var writeMu sync.Mutex

// ConfigPath returns the absolute path of the TOML config file.
func ConfigPath() (string, error) {
	configHome, err := getXDGConfigHome()
	if err != nil {
		return "", fmt.Errorf("failed to get config home: %w", err)
	}
	return filepath.Join(configHome, configFileName), nil
}

// Update is a partial change to the config file. A nil field is left as it is
// on disk, so a client can send only the settings it means to change.
type Update struct {
	Repos                       *[]string
	SleepDuration               *int // minutes
	JiraDomain                  *string
	GithubUsername              *string
	RepoLocation                *string
	AutoWorktree                *bool
	DesktopNotifications        *bool
	SectionPriority             *map[string]int
	SectionSorting              *map[string]string
	Workflows                   *[]RawWorkflow
	ExperimentalLLMFileOrdering *bool
	ExperimentalLLMReviewEase   *bool
}

// IsEmpty reports whether the update carries no changes at all.
func (u Update) IsEmpty() bool {
	return u.Repos == nil && u.SleepDuration == nil && u.JiraDomain == nil &&
		u.GithubUsername == nil && u.RepoLocation == nil && u.AutoWorktree == nil &&
		u.DesktopNotifications == nil && u.SectionPriority == nil && u.SectionSorting == nil &&
		u.Workflows == nil && u.ExperimentalLLMFileOrdering == nil && u.ExperimentalLLMReviewEase == nil
}

// setKey writes v into the TOML document when the update supplies it.
func setKey[T any](doc map[string]any, key string, v *T) {
	if v != nil {
		doc[key] = *v
	}
}

// Render merges the update into the config file currently on disk and returns
// the TOML bytes that would replace it along with the Config those bytes parse
// into. Keys the server does not model (and any settings the update leaves
// alone) are carried over untouched; comments and the original key ordering are
// not preserved, which is why save keeps a .bak copy.
//
// Nothing is written here — callers validate the returned Config first and then
// hand the bytes to save. Apply does both under one lock.
func (u Update) Render() ([]byte, *Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, nil, err
	}

	doc := map[string]any{}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("failed to read config file at %s: %w", path, err)
	}
	if err == nil {
		if err := toml.Unmarshal(existing, &doc); err != nil {
			return nil, nil, fmt.Errorf("failed to parse config file at %s: %w", path, err)
		}
		if doc == nil {
			doc = map[string]any{}
		}
	}

	setKey(doc, "Repos", u.Repos)
	setKey(doc, "SleepDuration", u.SleepDuration)
	setKey(doc, "JiraDomain", u.JiraDomain)
	setKey(doc, "GithubUsername", u.GithubUsername)
	setKey(doc, "RepoLocation", u.RepoLocation)
	setKey(doc, "AutoWorktree", u.AutoWorktree)
	setKey(doc, "DesktopNotifications", u.DesktopNotifications)
	setKey(doc, "SectionPriority", u.SectionPriority)
	setKey(doc, "SectionSorting", u.SectionSorting)
	setKey(doc, "ExperimentalLLMFileOrdering", u.ExperimentalLLMFileOrdering)
	setKey(doc, "ExperimentalLLMReviewEase", u.ExperimentalLLMReviewEase)
	if u.Workflows != nil {
		doc["Workflows"] = stripInheritedUsernames(*u.Workflows, docString(doc, "GithubUsername"))
	}

	data, err := toml.Marshal(doc)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to serialize config: %w", err)
	}

	cfg, err := parseConfig(data)
	if err != nil {
		return nil, nil, err
	}
	return data, cfg, nil
}

// docString reads a string key out of a decoded TOML document.
func docString(doc map[string]any, key string) string {
	if s, ok := doc[key].(string); ok {
		return s
	}
	return ""
}

// stripInheritedUsernames drops each workflow's GithubUsername when it merely
// repeats the root-level one. parseConfig copies the root value into every
// workflow that omits it, so without this a client that reads the config and
// writes it straight back would bake that inherited value into the file.
func stripInheritedUsernames(wfs []RawWorkflow, rootUsername string) []RawWorkflow {
	out := make([]RawWorkflow, len(wfs))
	copy(out, wfs)
	if rootUsername == "" {
		return out
	}
	for i := range out {
		if out[i].GithubUsername == rootUsername {
			out[i].GithubUsername = ""
		}
	}
	return out
}

// save atomically replaces the config file with data, keeping the previous
// contents in <path>.bak. Callers go through Apply, which holds writeMu across
// the whole read-modify-write.
func save(data []byte) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create config directory %s: %w", dir, err)
	}

	if existing, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+".bak", existing, 0o644); err != nil {
			slog.Warn("Failed to write config backup", "path", path+".bak", "error", err)
		}
	}

	tmp, err := os.CreateTemp(dir, configFileName+".tmp*")
	if err != nil {
		return fmt.Errorf("failed to create temp config file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write temp config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temp config file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		slog.Warn("Failed to set config file permissions", "path", tmpPath, "error", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to replace config file at %s: %w", path, err)
	}
	slog.Info("Wrote configuration file", "path", path)
	return nil
}

// Apply renders an update, runs validate against the configuration it produces,
// and — only when validation reports nothing — writes the file and reloads the
// in-memory config. The whole cycle holds writeMu so concurrent callers can't
// interleave.
//
// A non-empty []ValidationError means the config was rejected and the file on
// disk is untouched. A non-nil error means rendering, writing, or reloading
// failed. validate may be nil.
func Apply(u Update, validate func(*Config) []ValidationError) ([]ValidationError, error) {
	writeMu.Lock()
	defer writeMu.Unlock()

	data, cfg, err := u.Render()
	if err != nil {
		return nil, err
	}
	if validate != nil {
		if problems := validate(cfg); len(problems) > 0 {
			return problems, nil
		}
	}
	if err := save(data); err != nil {
		return nil, err
	}
	return nil, Reload()
}
