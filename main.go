package main

import (
	"flag"
	"gtdbot/config"
	"gtdbot/logger"
	"gtdbot/org"
	"gtdbot/workflows"
	"log/slog"
)

func main() {
	log := logger.New()
	slog.SetDefault(log)
	slog.Info("Starting!")
	defer config.C.DB.Close()

	oneOff := flag.Bool("oneoff", false, "Pass oneoff to only run once")
	initOnly := flag.Bool("init", false, "Pass init to only only setup the org file.")
	flag.Parse()

	workflows_list := workflows.MatchWorkflows(config.C.RawWorkflows, &config.C.Repos, config.C.JiraDomain)
	ms := workflows.NewManagerService(
		workflows_list,
		*oneOff,
		config.C.SleepDuration,
	)
	ms.Initialize()
	if *initOnly {
		slog.Info("Finished Initilization, Exiting.")
		// Render org files after initialization
		renderer := org.NewOrgRenderer(config.C.DB, org.BaseOrgSerializer{})
		if err := renderer.RenderAllFiles(config.C.OrgFileDir); err != nil {
			slog.Error("Error rendering org files", "error", err)
		}
		return
	}

	ms.Run(log)
}
