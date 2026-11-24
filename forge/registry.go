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
// Kind filled in, when it has no table). It reaches its forge from the
// settings alone, so two IDs with equal settings are one service (Same).
type Driver func(id string, settings config.ForgeSettings) (Forge, error)

var registry struct {
	sync.Mutex
	drivers map[string]Driver
	// open holds each forge opened so far, by ID, with the settings it was
	// opened from: a config reload that changes them opens it again.
	open map[string]opened
	// listed is which forge each repository a workflow lists is on, by
	// lowercased "owner/repo" (SetListedRepos), and found which forge a
	// search last turned each repository up on (Remember). A listed
	// repository's forge outranks a found one's.
	listed map[string]string
	found  map[string]string
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
	return builtIn()
}

// builtInSettings are the built-in GitHub forge's settings when the config
// has no [Forges.github] table.
var builtInSettings = config.ForgeSettings{Kind: config.ForgeKindGitHub}

// builtIn is the built-in GitHub forge, opened from the GitHub driver with
// builtInSettings rather than through For: a [Forges.github] table naming
// another kind makes For fail for it too, and the fallback must not.
func builtIn() Forge {
	registry.Lock()
	defer registry.Unlock()
	if o, ok := registry.open[config.ForgeGitHub]; ok && o.settings == builtInSettings {
		return o.forge
	}
	driver, ok := registry.drivers[config.ForgeKindGitHub]
	if !ok {
		// Only a binary that never linked the GitHub driver gets here.
		panic("forge: the built-in GitHub forge can't be opened: no driver is registered for kind " + config.ForgeKindGitHub)
	}
	f, err := driver(config.ForgeGitHub, builtInSettings)
	if err != nil {
		panic(fmt.Sprintf("forge: the built-in GitHub forge can't be opened: %v", err))
	}
	if registry.open == nil {
		registry.open = map[string]opened{}
	}
	registry.open[config.ForgeGitHub] = opened{settings: builtInSettings, forge: f}
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

// SetListedRepos records which forge each repository the config's workflows
// list is on, keyed by "owner/repo", so ForRepo sends what a client asks about
// its PRs there. It replaces what the last build of the workflows recorded:
// a repository no workflow lists any more is back on the default.
// MatchWorkflows calls it as it builds them.
func SetListedRepos(repos map[string]string) {
	cfg := config.C()
	listed := make(map[string]string, len(repos))
	for entry, id := range repos {
		listed[strings.ToLower(strings.TrimSpace(entry))] = cfg.ForgeID(id)
	}
	registry.Lock()
	defer registry.Unlock()
	registry.listed = listed
}

// Remember records that a search on the forge id turned up owner/repo, so
// ForRepo sends what a client asks about its PRs there — unless a workflow
// lists the repository (SetListedRepos), whose forge outranks it. The last
// search to find a repository wins.
func Remember(owner, repo, id string) {
	if owner == "" || repo == "" {
		return
	}
	id = config.C().ForgeID(id)
	registry.Lock()
	defer registry.Unlock()
	if registry.found == nil {
		registry.found = map[string]string{}
	}
	registry.found[repoKey(owner, repo)] = id
}

// IDForRepo is the ID of the forge owner/repo's PRs come from: the one a
// workflow lists it on, else the one a search last found it on, else the
// default. A recorded forge the config no longer defines is passed over.
func IDForRepo(owner, repo string) string {
	key := repoKey(owner, repo)
	registry.Lock()
	listed, isListed := registry.listed[key]
	found, isFound := registry.found[key]
	registry.Unlock()
	cfg := config.C()
	if _, defined := cfg.Forge(listed); isListed && defined {
		return listed
	}
	if _, defined := cfg.Forge(found); isFound && defined {
		return found
	}
	return cfg.DefaultForgeID()
}

// Same reports whether the forges the config calls a and b ("" meaning the
// default) are one service: the same ID, or two IDs with equal settings. A
// driver reaches its forge from its settings alone, so equal settings are one
// service under two names — as every forge of kind github is github.com
// today — and a PR a search on one of them turns up is on the other too.
func Same(a, b string) bool {
	cfg := config.C()
	a, b = cfg.ForgeID(a), cfg.ForgeID(b)
	if a == b {
		return true
	}
	sa, okA := cfg.Forge(a)
	sb, okB := cfg.Forge(b)
	return okA && okB && sa == sb
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
