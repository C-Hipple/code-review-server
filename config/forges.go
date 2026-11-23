package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// A forge is the service a workflow's pull requests live on: GitHub today,
// GitLab or a self-hosted instance of either one day. Each one the config can
// name has an ID, and each ID has a kind, which picks the driver that talks to
// it (the forge package holds the drivers; config can't import it, since it
// reads config). The built-in forge, ForgeGitHub, needs no table, and is the
// default unless DefaultForge names another; a workflow that names no Forge
// talks to the default.
//
//	DefaultForge = "github"   # optional; this is the default
//
//	[Forges.github]           # optional; the built-in forge
//	Kind = "github"
//
//	[[Workflows]]
//	Name = "Team PRs"
//	Forge = "github"          # optional; DefaultForge when left out
const (
	// ForgeKindGitHub is GitHub: github.com, reached with CRS_GITHUB_TOKEN.
	ForgeKindGitHub = "github"

	// ForgeGitHub is the ID of the built-in forge, of kind ForgeKindGitHub.
	// It is defined whether or not the file has a [Forges.github] table.
	ForgeGitHub = "github"
)

// ForgeKinds are the kinds a [Forges.<id>] table may name: those this build has
// a driver for. The forge package's tests check each one against the drivers
// registered with it.
var ForgeKinds = []string{ForgeKindGitHub}

// ForgeSettings is one [Forges.<id>] table. It holds only the kind for now;
// what a driver needs to reach its forge — a base URL, the environment
// variable its token is in — belongs here as drivers that need it arrive.
type ForgeSettings struct {
	// Kind picks the driver: one of ForgeKinds. Empty means the table's ID, so
	// [Forges.github] needs no Kind.
	Kind string `toml:"Kind,omitempty"`
}

// DefaultForgeID is the ID of the forge a workflow that names none talks to:
// DefaultForge, else ForgeGitHub.
func (c Config) DefaultForgeID() string {
	if id := strings.TrimSpace(c.DefaultForge); id != "" {
		return id
	}
	return ForgeGitHub
}

// ForgeID resolves a forge ID as a workflow or a client gives it: trimmed, and
// the default forge when empty.
func (c Config) ForgeID(id string) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	return c.DefaultForgeID()
}

// WorkflowForgeID is the ID of the forge the workflow talks to: its Forge,
// else the default.
func (c Config) WorkflowForgeID(wf RawWorkflow) string {
	return c.ForgeID(wf.Forge)
}

// Forge returns the settings of the forge id, with Kind filled in, and false
// when the config defines no forge by that ID. ForgeGitHub is always defined.
func (c Config) Forge(id string) (ForgeSettings, bool) {
	s, ok := c.Forges[id]
	if !ok && id != ForgeGitHub {
		return ForgeSettings{}, false
	}
	s.Kind = strings.TrimSpace(s.Kind)
	if s.Kind == "" {
		s.Kind = id
	}
	return s, true
}

// ForgeIDs are the IDs of every forge the config defines: the built-in one,
// then the [Forges] tables by ID.
func (c Config) ForgeIDs() []string {
	ids := []string{ForgeGitHub}
	for _, id := range slices.Sorted(maps.Keys(c.Forges)) {
		if id != ForgeGitHub {
			ids = append(ids, id)
		}
	}
	return ids
}

// ForgeTable names the table that configures the forge id, for messages.
func ForgeTable(id string) string {
	return "[Forges." + tomlKey(id) + "]"
}

// validateForges checks that DefaultForge and every workflow's Forge name a
// forge the config defines, and that each [Forges] table names a kind this
// build can talk to. A workflow on a forge that can't be opened is skipped
// when the workflows are built, so this is where the mistake gets explained.
func validateForges(cfg *Config) []ValidationError {
	var problems []ValidationError
	kinds := strings.Join(ForgeKinds, ", ")
	for _, id := range slices.Sorted(maps.Keys(cfg.Forges)) {
		if strings.TrimSpace(id) == "" {
			problems = append(problems, rootError("Forges", "a forge's ID can't be empty"))
			continue
		}
		s, _ := cfg.Forge(id)
		field := "Forges." + tomlKey(id) + ".Kind"
		switch {
		case !slices.Contains(ForgeKinds, s.Kind):
			problems = append(problems, rootError(field,
				"unknown kind %q (this build can talk to: %s)", s.Kind, kinds))
		case id == ForgeGitHub && s.Kind != ForgeKindGitHub:
			problems = append(problems, rootError(field,
				"%q is the built-in GitHub forge; give a forge of another kind its own ID", id))
		}
	}
	if id := cfg.DefaultForgeID(); !cfg.forgeDefined(id) {
		problems = append(problems, rootError("DefaultForge", "%s", undefinedForge(id)))
	}
	for i, wf := range cfg.RawWorkflows {
		if strings.TrimSpace(wf.Forge) == "" {
			continue
		}
		if id := cfg.WorkflowForgeID(wf); !cfg.forgeDefined(id) {
			problems = append(problems, workflowError(i, "Forge", "%s", undefinedForge(id)))
		}
	}
	return problems
}

func (c Config) forgeDefined(id string) bool {
	_, ok := c.Forge(id)
	return ok
}

func undefinedForge(id string) string {
	return fmt.Sprintf("there is no forge %q: define it in %s, or use %q for GitHub", id, ForgeTable(id), ForgeGitHub)
}
