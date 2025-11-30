package main

import (
	"codereviewserver/config"
	"codereviewserver/logger"
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

	if *serverFlag {
		go ms.Run(log)
		server.RunServer(log)
	} else {
		ms.Run(log)
	}
}
