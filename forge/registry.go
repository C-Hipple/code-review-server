package forge

import (
	"crs/config"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
)

// Driver opens the forge the config calls id, of the kind the driver was
// registered for, from its [Forges.<id>] settings (the zero settings, with
// Kind filled in, when it has no table).
type Driver func(id string, settings config.ForgeSettings) (Forge, error)

var registry struct {
	sync.Mutex
	drivers map[string]Driver
	// open holds each forge opened so far, by ID, with the settings it was
	// opened from: a config reload that changes them opens it again.
	open map[string]opened
	// repos is which forge each repository's PRs come from, by lowercased
	// "owner/repo" (Remember).
	repos map[string]string
	// warned is the forge IDs a fallback has been logged for, so a broken
	// config says so once rather than on every lookup.
	warned map[string]bool
}

type opened struct {
	settings config.ForgeSettings
	forge    Forge
}

// Register makes a kind of forge available. Each driver registers itself
// from an init function; registering a kind twice is a programming error and
// panics.
func Register(kind string, driver Driver) {
	registry.Lock()
	defer registry.Unlock()
	if registry.drivers == nil {
		registry.drivers = map[string]Driver{}
	}
	if _, dup := registry.drivers[kind]; dup {
		panic(fmt.Sprintf("forge: kind %q registered twice", kind))
	}
	registry.drivers[kind] = driver
}

// Kinds are the kinds a driver is registered for, sorted.
func Kinds() []string {
	registry.Lock()
	defer registry.Unlock()
	return slices.Sorted(maps.Keys(registry.drivers))
}

// For returns the forge the config calls id, "" meaning the default one,
// opening it the first time it is asked for. It fails when the config
// defines no such forge or no driver serves its kind, both of which config
// validation reports.
func For(id string) (Forge, error) {
	cfg := config.C()
	id = cfg.ForgeID(id)
	settings, ok := cfg.Forge(id)
	if !ok {
		return nil, fmt.Errorf("there is no forge %q: define it in %s", id, config.ForgeTable(id))
	}
	registry.Lock()
	defer registry.Unlock()
	if o, ok := registry.open[id]; ok && o.settings == settings {
		return o.forge, nil
	}
	driver, ok := registry.drivers[settings.Kind]
	if !ok {
		return nil, fmt.Errorf("forge %q is of kind %q, which this build has no driver for", id, settings.Kind)
	}
	f, err := driver(id, settings)
	if err != nil {
		return nil, fmt.Errorf("opening forge %q: %w", id, err)
	}
	if registry.open == nil {
		registry.open = map[string]opened{}
	}
	registry.open[id] = opened{settings: settings, forge: f}
	return f, nil
}

// Default returns the forge workflows that name none talk to. When the one
// the config names can't be opened it says so, once, and returns the
// built-in GitHub forge, which is what every workflow talked to before
// forges could be named.
func Default() Forge {
	return orBuiltIn(config.C().DefaultForgeID())
}

// orBuiltIn opens the forge id, falling back to the built-in GitHub forge.
func orBuiltIn(id string) Forge {
	f, err := For(id)
	if err == nil {
		return f
	}
	warnOnce(id, "Forge can't be opened; using GitHub", err)
	f, builtInErr := For(config.ForgeGitHub)
	if builtInErr != nil {
		// Only a binary that never linked the GitHub driver gets here.
		panic(fmt.Sprintf("forge: the built-in GitHub forge can't be opened: %v", builtInErr))
	}
	return f
}

func warnOnce(id, msg string, err error) {
	registry.Lock()
	defer registry.Unlock()
	if registry.warned[id] {
		return
	}
	if registry.warned == nil {
		registry.warned = map[string]bool{}
	}
	registry.warned[id] = true
	slog.Error(msg, "forge", id, "error", err)
}

// Remember records that owner/repo's PRs come from the forge id, so ForRepo
// sends what a client asks about it there. The workflows remember the
// repositories they list as they are built, and the PRs a search turns up as
// it finds them. The last forge remembered for a repository wins.
func Remember(owner, repo, id string) {
	if owner == "" || repo == "" {
		return
	}
	id = config.C().ForgeID(id)
	registry.Lock()
	defer registry.Unlock()
	if registry.repos == nil {
		registry.repos = map[string]string{}
	}
	registry.repos[repoKey(owner, repo)] = id
}

// IDForRepo is the ID of the forge owner/repo's PRs come from: the one
// remembered for it, else the default.
func IDForRepo(owner, repo string) string {
	registry.Lock()
	id, ok := registry.repos[repoKey(owner, repo)]
	registry.Unlock()
	if ok {
		return id
	}
	return config.C().DefaultForgeID()
}

// ForRepo returns the forge owner/repo's PRs come from (IDForRepo). It never
// fails: a forge that can't be opened falls back as Default does.
func ForRepo(owner, repo string) Forge {
	return orBuiltIn(IDForRepo(owner, repo))
}

// ForPR is ForRepo for the repository a PR belongs to: its base's.
func ForPR(pr *PullRequest) Forge {
	if pr == nil || pr.Base == nil || pr.Base.Repo == nil {
		return Default()
	}
	return ForRepo(pr.Base.Repo.GetOwner().GetLogin(), pr.Base.Repo.GetName())
}

func repoKey(owner, repo string) string {
	return strings.ToLower(owner + "/" + repo)
}
