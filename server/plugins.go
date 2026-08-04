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

// callTypeFor maps a plugin and the way RunPluginsForce was invoked onto the
// call type reported to the plugin. Automatic runs never reach deferred
// plugins, so a forced run of one is always an explicit request for it.
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
	RunPluginsForce(owner, repo, number, sha, diff, commentsJSON, metadataJSON, branch, false, nil)
}

// RunPluginsForce executes plugins with optional force rerun and specific plugin selection.
// force=true bypasses the SHA check and reruns plugins even if SHA hasn't changed.
// plugins=nil or empty means run all configured plugins; otherwise run only the specified ones.
func RunPluginsForce(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string, force bool, plugins []string) {
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
		// Skip on-demand plugins unless explicitly requested by name
		if plugin.OnlyOnDemand && !pluginMap[plugin.Name] {
			// Record a "deferred" status so clients know this plugin exists but hasn't been requested yet
			if err := config.C().DB.UpsertPluginResult(owner, repo, number, plugin.Name, "", "deferred", sha); err != nil {
				slog.Error("Failed to record deferred plugin status", "plugin", plugin.Name, "error", err)
			}
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

// executePlugin runs a single plugin binary and stores its result. callType
// says why the run was triggered; anything other than PluginCallAutomatic
// bypasses the per-SHA skip so a requested run always executes.
func executePlugin(plugin config.Plugin, owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, branch string, callType PluginCallType) {
	force := callType != PluginCallAutomatic

	// Check if we need to rerun this plugin
	storedSHA, err := config.C().DB.GetPluginResultSHA(owner, repo, number, plugin.Name)
	if err != nil {
		slog.Error("Failed to get stored SHA for plugin", "plugin", plugin.Name, "error", err)
		// Continue anyway - we'll run the plugin
	}

	// Skip execution if SHA hasn't changed (unless force is true)
	if !force && storedSHA != "" && storedSHA == sha {
		slog.Debug("Skipping plugin execution - SHA unchanged", "plugin", plugin.Name, "sha", sha)
		return
	}

	if force {
		slog.Info("Forcing plugin execution", "plugin", plugin.Name, "call_type", callType)
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
