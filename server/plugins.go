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
	var wg sync.WaitGroup

	for _, plugin := range config.C.Plugins {
		wg.Add(1)
		go func(p config.Plugin) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("Plugin runner panicked", "plugin", p.Name, "panic", r)
				}
			}()
			executePlugin(p, owner, repo, number, sha, diff, commentsJSON, metadataJSON)
		}(plugin)
	}

	wg.Wait()
}

func executePlugin(plugin config.Plugin, owner, repo string, number int, sha string, diff string, commentsJSON string, metadataJSON string) {
	// Check if we need to rerun this plugin
	storedSHA, err := config.C.DB.GetPluginResultSHA(owner, repo, number, plugin.Name)
	if err != nil {
		slog.Error("Failed to get stored SHA for plugin", "plugin", plugin.Name, "error", err)
		// Continue anyway - we'll run the plugin
	}
	
	// Skip execution if SHA hasn't changed
	if storedSHA != "" && storedSHA == sha {
		slog.Info("Skipping plugin execution - SHA unchanged", "plugin", plugin.Name, "sha", sha)
		return
	}

	// Set status to pending
	err = config.C.DB.UpsertPluginResult(owner, repo, number, plugin.Name, "", "pending", sha)
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
		config.C.DB.UpsertPluginResult(owner, repo, number, plugin.Name, fmt.Sprintf("Error: %v\nOutput: %s", err, resultStr), "error", sha)
		return
	}

	slog.Info("Plugin executed", "plugin", plugin.Name, "result_len", len(resultStr), "sha", sha)

	// Store result
	err = config.C.DB.UpsertPluginResult(owner, repo, number, plugin.Name, resultStr, "success", sha)
	if err != nil {
		slog.Error("Failed to store plugin result", "plugin", plugin.Name, "error", err)
	}
}
