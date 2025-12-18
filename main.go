package main

import (
	"crs/config"
	"crs/logger"
	"crs/server"
	"crs/workflows"
	"flag"
	"fmt"
	"log/slog"
	"os"
)

func main() {
	fmt.Println("here")
	log := logger.New()
	slog.SetDefault(log)
	slog.Info("starting")

	// Initialize configuration
	if err := config.Initialize(); err != nil {
		slog.Error("Failed to initialize configuration", "error", err)
		os.Exit(1)
	}
	defer config.C.DB.Close()

	oneOff := flag.Bool("oneoff", false, "Pass oneoff to only run once")
	serverFlag := flag.Bool("server", false, "Run as an RPC server")
	testFlag := flag.Bool("test", false, "Run in test mode")
	flag.Parse()

	if *testFlag {
		content, err := server.GetFullPRResponse("C-Hipple", "gtdbot", 9, false)
		if err != nil {
			slog.Error("Error getting PR response", "error", err)
			os.Exit(1)
		}
		fmt.Println(content)
		return
	}

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
