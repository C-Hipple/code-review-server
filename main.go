package main

import (
	"codereviewserver/config"
	"codereviewserver/logger"
	"codereviewserver/org"
	"codereviewserver/server"
	"codereviewserver/workflows"
	"flag"
	"log/slog"
	"os"
)

func main() {
	log := logger.New()
	slog.SetDefault(log)
	defer config.C.DB.Close()

	oneOff := flag.Bool("oneoff", false, "Pass oneoff to only run once")
	initOnly := flag.Bool("init", false, "Pass init to only only setup the org file.")
	serverFlag := flag.Bool("server", false, "Run as an RPC server")
	flag.Parse()

	if *oneOff && *serverFlag {
		slog.Error("Cannot run in both server and oneoff mode")
		os.Exit(1)
	}

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

	if *serverFlag {
		go ms.Run(log)
		server.RunServer(log)
	} else {
		ms.Run(log)
	}
}
