package main

import (
	"bufio"
	"codereviewserver/config"
	"codereviewserver/logger"
	"codereviewserver/org"
	"codereviewserver/workflows"
	"flag"
	"fmt"
	"log/slog"
	"os"
)

func main() {
	log := logger.New()
	slog.SetDefault(log)
	slog.Info("Starting!")
	defer config.C.DB.Close()

	oneOff := flag.Bool("oneoff", false, "Pass oneoff to only run once")
	initOnly := flag.Bool("init", false, "Pass init to only only setup the org file.")
	server := flag.Bool("server", false, "Run as an RPC server")
	flag.Parse()

	if *oneOff && *server {
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

	if *server {
		go ms.Run(log)
		runServer(log)
	} else {
		ms.Run(log)
	}
}

func runServer(log *slog.Logger) {
	log.Info("Starting RPC Server on Stdin/Stdout")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "hello" {
			var count int
			err := config.C.DB.QueryRow("SELECT COUNT(*) FROM sections").Scan(&count)
			if err != nil {
				log.Error("Error counting items", "error", err)
				continue
			}
			fmt.Printf("hello %d\n", count)
		} else if line == "getReviews" {
			renderer := org.NewOrgRenderer(config.C.DB, org.BaseOrgSerializer{})
			content, err := renderer.RenderAllFilesToString()
			if err != nil {
				log.Error("Error rendering org files", "error", err)
				fmt.Printf("ERROR: %s\n", err.Error())
				continue
			}
			fmt.Print(content)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Error("Error reading from stdin", "error", err)
	}
}
