package server

import (
	"crs/config"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
)

// RunPlugins executes all configured plugins for a given PR.
// It is intended to run asynchronously.
// Plugins are only executed if the current SHA differs from the SHA for which they were last run.
func RunPlugins(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string) {
	RunPluginsForce(owner, repo, number, sha, diff, commentsJSON, metadataJSON, false, nil)
}

// RunPluginsForce executes plugins with optional force rerun and specific plugin selection.
// force=true bypasses the SHA check and reruns plugins even if SHA hasn't changed.
// plugins=nil or empty means run all configured plugins; otherwise run only the specified ones.
func RunPluginsForce(owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, force bool, plugins []string) {
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
			config.C().DB.UpsertPluginResult(owner, repo, number, plugin.Name, "", "deferred", sha)
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
			executePluginForce(p, owner, repo, number, sha, diff, commentsJSON, metadataJSON, force)
		}(plugin)
	}

	wg.Wait()
}

func executePlugin(plugin config.Plugin, owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string) {
	executePluginForce(plugin, owner, repo, number, sha, diff, commentsJSON, metadataJSON, false)
}

func executePluginForce(plugin config.Plugin, owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string, force bool) {
	// Check if we need to rerun this plugin
	storedSHA, err := config.C().DB.GetPluginResultSHA(owner, repo, number, plugin.Name)
	if err != nil {
		slog.Error("Failed to get stored SHA for plugin", "plugin", plugin.Name, "error", err)
		// Continue anyway - we'll run the plugin
	}

	// Skip execution if SHA hasn't changed (unless force is true)
	if !force && storedSHA != "" && storedSHA == sha {
		slog.Info("Skipping plugin execution - SHA unchanged", "plugin", plugin.Name, "sha", sha)
		return
	}

	if force {
		slog.Info("Forcing plugin execution", "plugin", plugin.Name)
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

	cmd := exec.Command(plugin.Command, args...)

	output, err := cmd.CombinedOutput()
	resultStr := string(output)
	if err != nil {
		slog.Error("Plugin execution failed", "plugin", plugin.Name, "error", err, "output", resultStr)
		config.C().DB.UpsertPluginResult(owner, repo, number, plugin.Name, fmt.Sprintf("Error: %v\nOutput: %s", err, resultStr), "error", sha)
		return
	}

	slog.Info("Plugin executed", "plugin", plugin.Name, "result_len", len(resultStr), "sha", sha)

	// Store result
	err = config.C().DB.UpsertPluginResult(owner, repo, number, plugin.Name, resultStr, "success", sha)
	if err != nil {
		slog.Error("Failed to store plugin result", "plugin", plugin.Name, "error", err)
	}
}
