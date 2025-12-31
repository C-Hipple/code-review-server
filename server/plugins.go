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
func RunPlugins(owner, repo string, number int, diff string, commentsJSON string, metadataJSON string) {
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
			executePlugin(p, owner, repo, number, diff, commentsJSON, metadataJSON)
		}(plugin)
	}

	wg.Wait()
}

func executePlugin(plugin config.Plugin, owner, repo string, number int, diff string, commentsJSON string, metadataJSON string) {
	// Set status to pending
	err := config.C.DB.UpsertPluginResult(owner, repo, number, plugin.Name, "", "pending")
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
		config.C.DB.UpsertPluginResult(owner, repo, number, plugin.Name, fmt.Sprintf("Error: %v\nOutput: %s", err, resultStr), "error")
		return
	}

	slog.Info("Plugin executed", "plugin", plugin.Name, "result_len", len(resultStr))

	// Store result
	err = config.C.DB.UpsertPluginResult(owner, repo, number, plugin.Name, resultStr, "success")
	if err != nil {
		slog.Error("Failed to store plugin result", "plugin", plugin.Name, "error", err)
	}
}
