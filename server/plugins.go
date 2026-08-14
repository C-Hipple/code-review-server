package server

import (
	"bytes"
	"context"
	"crs/config"
	"fmt"
	"log/slog"
	"os/exec"
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
// - someone is watching those.
const maxAutomaticPluginProcs = 4

var automaticPluginSlots = make(chan struct{}, maxAutomaticPluginProcs)

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
	// only ever executes because someone asked for it by name.
	PluginCallExplicit PluginCallType = "explicit"
	// PluginCallRerun is a requested rerun of a plugin that would otherwise
	// have run automatically.
	PluginCallRerun PluginCallType = "rerun"
)

// callTypeFor maps a plugin and the way the run was invoked onto the call type
// reported to the plugin. A forced run of a deferred plugin is always an
// explicit request for it; a section-driven pre-computation (includeOnDemand)
// is not — nobody asked for it, so it reports itself as the automatic run it is.
func callTypeFor(plugin config.Plugin, force bool) PluginCallType {
	if !force {
		return PluginCallAutomatic
	}
	if plugin.OnlyOnDemand {
		return PluginCallExplicit
	}
	return PluginCallRerun
}

// RunPlugins executes all configured plugins for a given PR.
// It is intended to run asynchronously.
// Plugins are only executed if the current SHA differs from the SHA for which they were last run.
func RunPlugins(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string) {
	runPlugins(owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch, false, nil, false)
}

// RunPluginsIncludingOnDemand is the automatic run for a PR in a section
// configured with ForceRunOnDemand: the deferred (OnlyOnDemand) plugins are
// treated as ordinary ones and computed up front, rather than sitting at
// "deferred" until a reviewer asks for them.
//
// It is still an automatic run in every other respect. In particular each
// plugin keeps its per-SHA skip, so the repeated calls a workflow cycle makes
// for an unchanged PR cost a couple of DB reads and nothing more.
func RunPluginsIncludingOnDemand(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string) {
	runPlugins(owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch, false, nil, true)
}

// RunPluginsForce executes plugins with optional force rerun and specific plugin selection.
// force=true bypasses the SHA check and reruns plugins even if SHA hasn't changed.
// plugins=nil or empty means run all configured plugins; otherwise run only the specified ones.
func RunPluginsForce(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string, force bool, plugins []string) {
	runPlugins(owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch, force, plugins, false)
}

// runPlugins is the single implementation behind the three entry points above.
// includeOnDemand promotes the deferred plugins into this run without making it
// a forced one.
func runPlugins(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string, force bool, plugins []string, includeOnDemand bool) {
	var wg sync.WaitGroup
	pluginMap := make(map[string]bool)
	if len(plugins) > 0 {
		for _, p := range plugins {
			pluginMap[p] = true
		}
	}

	for _, plugin := range config.C().Plugins {
		// If specific plugins requested, skip if not in the list
		if len(pluginMap) > 0 && !pluginMap[plugin.Name] {
			continue
		}
		// Skip on-demand plugins unless explicitly requested by name, or unless
		// this run was asked to cover them.
		if plugin.OnlyOnDemand && !pluginMap[plugin.Name] && !includeOnDemand {
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
			executePlugin(p, owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch, callTypeFor(p, force))
		}(plugin)
	}

	wg.Wait()
}

// markPluginDeferred records a "deferred" status so clients know the plugin
// exists but hasn't been requested yet.
//
// It leaves an existing row for this SHA alone: in a ForceRunOnDemand section
// the same PR is dispatched twice — once as an ordinary warm, once to cover the
// deferred plugins — and the ordinary warm must not stamp "deferred" over a
// result the other dispatch already produced or is producing.
func markPluginDeferred(owner, repo string, number int, pluginName string, sha string) {
	storedSHA, status, err := config.C().DB.GetPluginResultState(owner, repo, number, pluginName)
	if err != nil {
		slog.Error("Failed to read plugin state before recording deferred status", "plugin", pluginName, "error", err)
	} else if storedSHA == sha && status != "" && status != "deferred" {
		return
	}
	if err := config.C().DB.UpsertPluginResult(owner, repo, number, pluginName, "", "deferred", sha); err != nil {
		slog.Error("Failed to record deferred plugin status", "plugin", pluginName, "error", err)
	}
}

// OnDemandPluginsPending reports whether any deferred (OnlyOnDemand) plugin has
// yet to run for sha. It reads only the DB, so a caller can use it to decide
// whether a pre-computation dispatch is worth the PR load behind it — which
// matters because a ForceRunOnDemand section re-emits an "Update" for every PR
// it holds on every workflow cycle, and the steady-state answer here is no.
func OnDemandPluginsPending(owner, repo string, number int, sha string) bool {
	for _, plugin := range config.C().Plugins {
		if !plugin.OnlyOnDemand {
			continue
		}
		storedSHA, status, err := config.C().DB.GetPluginResultState(owner, repo, number, plugin.Name)
		if err != nil {
			slog.Warn("Failed to read plugin state while checking for pending on-demand work",
				"plugin", plugin.Name, "repo", repo, "pr", number, "error", err)
			return true
		}
		if pluginNeedsRun(storedSHA, status, sha) {
			return true
		}
	}
	return false
}

// pluginNeedsRun decides whether a plugin whose last row is (storedSHA, status)
// still has work to do for sha. A "deferred" row is a placeholder rather than a
// result, so it never counts as done; an "error" row does, for the same reason
// a successful one does — the automatic path does not retry a revision it has
// already attempted.
func pluginNeedsRun(storedSHA, status, sha string) bool {
	if storedSHA == "" || storedSHA != sha {
		return true
	}
	return status == "deferred"
}

// executePlugin runs a single plugin binary and stores its result. callType
// says why the run was triggered; anything other than PluginCallAutomatic
// bypasses the per-SHA skip so a requested run always executes.
func executePlugin(plugin config.Plugin, owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string, callType PluginCallType) {
	force := callType != PluginCallAutomatic

	// Check if we need to rerun this plugin
	storedSHA, storedStatus, err := config.C().DB.GetPluginResultState(owner, repo, number, plugin.Name)
	if err != nil {
		slog.Error("Failed to get stored SHA for plugin", "plugin", plugin.Name, "error", err)
		// Continue anyway - we'll run the plugin
	}

	// Skip execution if the plugin already ran for this SHA (unless force is
	// true). A "deferred" row carries the current SHA but means the opposite —
	// the plugin was skipped, not run — so it does not stand in for a result.
	if !force && !pluginNeedsRun(storedSHA, storedStatus, sha) {
		slog.Debug("Skipping plugin execution - SHA unchanged", "plugin", plugin.Name, "sha", sha)
		return
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
	err = config.C().DB.UpsertPluginResult(owner, repo, number, plugin.Name, "", "pending", sha)
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

	ctx, cancel := context.WithTimeout(context.Background(), pluginTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, plugin.Command, args...)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	err = cmd.Run()
	resultStr := stdoutBuf.String()
	if stderrBuf.Len() > 0 {
		slog.Warn("Plugin stderr", "plugin", plugin.Name, "stderr", stderrBuf.String())
	}
	if err != nil {
		slog.Error("Plugin execution failed", "plugin", plugin.Name, "error", err, "call_type", callType, "stderr", stderrBuf.String())
		if upsertErr := config.C().DB.UpsertPluginResult(owner, repo, number, plugin.Name, fmt.Sprintf("Error: %v\nStderr: %s\nStdout: %s", err, stderrBuf.String(), resultStr), "error", sha); upsertErr != nil {
			slog.Error("Failed to store plugin error result", "plugin", plugin.Name, "error", upsertErr)
		}
		return
	}

	slog.Info("Plugin executed", "plugin", plugin.Name, "result_len", len(resultStr), "sha", sha, "call_type", callType)

	// Store result
	err = config.C().DB.UpsertPluginResult(owner, repo, number, plugin.Name, resultStr, "success", sha)
	if err != nil {
		slog.Error("Failed to store plugin result", "plugin", plugin.Name, "error", err)
	}
}
