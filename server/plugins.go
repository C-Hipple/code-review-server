package server

import (
	"context"
	"crs/config"
	"crs/subprocess"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const pluginTimeout = 5 * time.Minute

// maxAutomaticPluginProcs caps how many automatic plugin runs execute at once.
// Automatic runs are fan-out work: opening the dashboard warms every visible
// PR, so without a cap one page load can launch dozens of plugin processes,
// each competing for CPU and for the server's single SQLite connection with
// the request the reviewer is actually waiting on. Explicit runs (a rerun, or
// an on-demand plugin someone asked for by name) are never queued behind this
// - someone is watching those. A deferred plugin a section always runs is
// fan-out work too, so its unforced runs queue here like automatic ones.
const maxAutomaticPluginProcs = 4

var automaticPluginSlots = make(chan struct{}, maxAutomaticPluginProcs)

// queuedPluginRuns holds the unforced runs claimed and not finished yet, keyed
// by PR, plugin and head SHA. Such a run writes "pending" only once it has a
// slot, so without this a second dispatch for the same head - the workflow
// writing the PR into a section while its post-update hooks run - would find
// nothing stored and start the plugin again.
var queuedPluginRuns sync.Map

// PluginCallType describes why a plugin is being executed. It is handed to the
// plugin binary via the --call-type flag so a plugin can adapt its behaviour to
// the situation - for instance skipping cached work on an automatic run but
// redoing it from scratch when a reviewer asked for a rerun.
type PluginCallType string

const (
	// PluginCallAutomatic is a run the server triggered on its own, when a PR
	// was fetched or its head SHA changed.
	PluginCallAutomatic PluginCallType = "automatic"
	// PluginCallExplicit is a run of a deferred (OnlyOnDemand) plugin, which
	// only ever executes because someone asked for it by name, or because the
	// PR is in a section that always runs it as though someone had
	// (AlwaysRunPlugins).
	PluginCallExplicit PluginCallType = "explicit"
	// PluginCallRerun is a requested rerun of a plugin that would otherwise
	// have run automatically.
	PluginCallRerun PluginCallType = "rerun"
)

// callTypeFor maps a plugin and the way RunPluginsForce was invoked onto the
// call type reported to the plugin. A deferred plugin never runs
// automatically - it runs because someone asked for it, or because its PR's
// section always runs it as though someone had - so every run of one is an
// explicit request for it.
func callTypeFor(plugin config.Plugin, force bool) PluginCallType {
	switch {
	case plugin.OnlyOnDemand:
		return PluginCallExplicit
	case force:
		return PluginCallRerun
	default:
		return PluginCallAutomatic
	}
}

// RunPlugins executes all configured plugins for a given PR.
// It is intended to run asynchronously.
// Plugins are only executed if the current SHA differs from the SHA for which they were last run.
func RunPlugins(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string) {
	RunPluginsForce(owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch, false, nil)
}

// RunPluginsForce executes plugins with optional force rerun and specific plugin selection.
// force=true bypasses the SHA check and reruns plugins even if SHA hasn't changed.
// plugins=nil or empty means run all configured plugins, apart from the deferred
// ones the PR's sections don't always run; otherwise run only the specified ones.
func RunPluginsForce(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string, force bool, plugins []string) {
	var wg sync.WaitGroup
	pluginMap := make(map[string]bool)
	if len(plugins) > 0 {
		for _, p := range plugins {
			pluginMap[p] = true
		}
	}
	// The deferred plugins the PR's sections always run, which run with the
	// rest when nobody named which plugins to run.
	var sectionRuns map[string]bool
	if len(pluginMap) == 0 {
		sectionRuns = sectionPlugins(owner, repo, number)
	}

	for _, plugin := range config.C().Plugins {
		// If specific plugins requested, skip if not in the list
		if len(pluginMap) > 0 && !pluginMap[plugin.Name] {
			continue
		}
		// Skip on-demand plugins unless explicitly requested by name, or
		// always run by a section the PR is in
		if plugin.OnlyOnDemand && !pluginMap[plugin.Name] && !sectionRuns[plugin.Name] {
			markPluginDeferred(owner, repo, number, plugin.Name, sha)
			continue
		}
		wg.Add(1)
		go func(p config.Plugin) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("Plugin runner panicked", "plugin", p.Name, "panic", r)
				}
			}()
			executePlugin(p, owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch, callTypeFor(p, force), force)
		}(plugin)
	}

	wg.Wait()
}

// markPluginDeferred records a "deferred" status so clients know a plugin
// exists but hasn't been requested yet - unless a result for this head is
// already stored, such as a run someone asked for, which is kept.
func markPluginDeferred(owner, repo string, number int, plugin, sha string) {
	written, err := config.C().DB.MarkPluginDeferred(owner, repo, number, plugin, sha)
	if err != nil {
		slog.Error("Failed to record deferred plugin status", "plugin", plugin, "error", err)
		return
	}
	if written {
		notifyPluginOutputChanged(owner, repo, number, plugin, "deferred")
	}
}

// sectionPlugins returns the deferred plugins the sections holding a PR always
// run (a workflow's AlwaysRunPlugins), as a set. It is nil without reading
// the DB when no section names any.
func sectionPlugins(owner, repo string, number int) map[string]bool {
	cfg := config.C()
	bySection := cfg.SectionPlugins()
	if len(bySection) == 0 || cfg.DB == nil {
		return nil
	}
	// The identifier the workflows give a PR's item: its base repo's full
	// name and its number.
	sections, err := cfg.DB.SectionsHoldingItem(fmt.Sprintf("%s/%s-%d", owner, repo, number))
	if err != nil {
		slog.Error("Failed to look up the sections holding a PR", "repo", repo, "pr", number, "error", err)
		return nil
	}
	plugins := map[string]bool{}
	for _, section := range sections {
		for _, name := range bySection[section] {
			plugins[name] = true
		}
	}
	return plugins
}

// RunSectionPlugins runs the deferred plugins the sections holding a PR always
// run, each that hasn't run at the PR's head yet, as RunPlugins would run them
// with the rest. The workflow layer calls it as it writes a PR into such a
// section (see workflows.SetSectionPluginsHook): a PR's post-update hooks run
// before a cycle has written its sections, so a PR the cycle is adding isn't
// in one yet when they look. Inputs come from the DB caches, the same ones a
// manual run reads; it blocks while the plugins run, so callers run it in a
// goroutine.
func RunSectionPlugins(owner, repo string, number int) {
	always := sectionPlugins(owner, repo, number)
	if len(always) == 0 {
		return
	}
	db := config.C().DB
	head, _, _ := db.GetPullRequestSHAs(number, repo)
	var due []string
	for _, plugin := range config.C().Plugins {
		if !plugin.OnlyOnDemand || !always[plugin.Name] {
			continue
		}
		if sha, status, _ := db.GetPluginResultState(owner, repo, number, plugin.Name); head == "" || sha != head || status == "deferred" {
			due = append(due, plugin.Name)
		}
	}
	if len(due) == 0 {
		return
	}

	details, err := GetPRDetails(owner, repo, number, false)
	if err != nil {
		slog.Error("Error fetching PR details for section plugins", "repo", repo, "pr", number, "error", err)
		return
	}
	commentsJSON := "[]"
	if rawComments, _ := db.GetPRComments(number, repo); rawComments != "" {
		commentsJSON = rawComments
	}
	sha, _, _ := db.GetPullRequestSHAs(number, repo)
	metadataJSON, err := json.Marshal(details.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}
	slog.Info("Running the deferred plugins the PR's sections always run", "repo", repo, "pr", number, "plugins", due)
	RunPluginsForce(owner, repo, number, sha, details.Diff, commentsJSON, string(metadataJSON), details.Metadata.HeadRef, false, due)
}

// executePlugin runs a single plugin binary and stores its result. callType
// says why the run was triggered and is what the plugin is told. force says
// someone is waiting on the run: it executes at once, even when a result for
// this SHA is stored. An unforced run executes once per SHA - a stored
// "deferred" placeholder doesn't count as a run - and waits its turn behind
// the other background runs.
func executePlugin(plugin config.Plugin, owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string, callType PluginCallType, force bool) {
	if !force {
		key := fmt.Sprintf("%s/%s#%d/%s@%s", owner, repo, number, plugin.Name, sha)
		if _, queued := queuedPluginRuns.LoadOrStore(key, struct{}{}); queued {
			slog.Debug("Skipping plugin execution - already queued for this SHA", "plugin", plugin.Name, "sha", sha)
			return
		}
		defer queuedPluginRuns.Delete(key)

		storedSHA, status, err := config.C().DB.GetPluginResultState(owner, repo, number, plugin.Name)
		if err != nil {
			slog.Error("Failed to get stored SHA for plugin", "plugin", plugin.Name, "error", err)
			// Continue anyway - we'll run the plugin
		}
		if storedSHA != "" && storedSHA == sha && status != "deferred" {
			slog.Debug("Skipping plugin execution - SHA unchanged", "plugin", plugin.Name, "sha", sha)
			return
		}
	}

	if force {
		slog.Info("Forcing plugin execution", "plugin", plugin.Name, "call_type", callType)
	} else {
		// Automatic runs wait their turn so a burst of them can't starve the
		// foreground. The slot is held only for the subprocess itself.
		automaticPluginSlots <- struct{}{}
		defer func() { <-automaticPluginSlots }()
	}

	// Set status to pending
	err := storePluginResult(owner, repo, number, plugin.Name, "", "pending", sha)
	if err != nil {
		slog.Error("Failed to set plugin status to pending", "plugin", plugin.Name, "error", err)
	}

	// Construct command using CLI arguments
	args := []string{
		"--owner", owner,
		"--repo", repo,
		"--number", fmt.Sprintf("%d", number),
		"--call-type", string(callType),
	}

	if plugin.IncludeDiff {
		args = append(args, "--diff", diff)
	}
	if plugin.IncludeComments {
		args = append(args, "--comments", commentsJSON)
	}
	if plugin.IncludeHeaders {
		args = append(args, "--headers", metadataJSON)
	}
	if plugin.IncludeBranch {
		args = append(args, "--branch", branch)
	}

	out, err := subprocess.Run(context.Background(), subprocess.Command{
		Name:    plugin.Command,
		Args:    args,
		Env:     plugin.Env(),
		Timeout: pluginTimeout,
	})
	resultStr := out.Stdout
	if out.Stderr != "" {
		slog.Warn("Plugin stderr", "plugin", plugin.Name, "stderr", out.Stderr)
	}
	if err != nil {
		slog.Error("Plugin execution failed", "plugin", plugin.Name, "error", err, "call_type", callType, "stderr", out.Stderr)
		if upsertErr := storePluginResult(owner, repo, number, plugin.Name, fmt.Sprintf("Error: %v\nStderr: %s\nStdout: %s", err, out.Stderr, resultStr), "error", sha); upsertErr != nil {
			slog.Error("Failed to store plugin error result", "plugin", plugin.Name, "error", upsertErr)
		}
		return
	}

	slog.Info("Plugin executed", "plugin", plugin.Name, "result_len", len(resultStr), "sha", sha, "call_type", callType)

	// Store result
	err = storePluginResult(owner, repo, number, plugin.Name, resultStr, "success", sha)
	if err != nil {
		slog.Error("Failed to store plugin result", "plugin", plugin.Name, "error", err)
	}
}

// storePluginResult writes a plugin's result for a PR and tells a subscribed
// client its plugin output changed. Every write of a result goes through
// here, so a client showing the output never has to poll for it.
func storePluginResult(owner, repo string, number int, plugin, result, status, sha string) error {
	if err := config.C().DB.UpsertPluginResult(owner, repo, number, plugin, result, status, sha); err != nil {
		return err
	}
	notifyPluginOutputChanged(owner, repo, number, plugin, status)
	return nil
}
