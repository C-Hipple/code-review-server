package main

import (
	"crs/config"
	"crs/logger"
	"crs/server"
	"crs/workflows"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

func main() {
	log := logger.New()
	slog.SetDefault(log)
	slog.Info("starting")

	// Initialize configuration
	if err := config.Initialize(); err != nil {
		slog.Error("Failed to initialize configuration", "error", err)
		os.Exit(1)
	}
	defer config.C().DB.Close()

	oneOff := flag.Bool("oneoff", false, "Pass oneoff to only run once")
	serverFlag := flag.Bool("server", false, "Run as an RPC server")
	testFlag := flag.Bool("test", false, "Run in test mode")
	listPRs := flag.Bool("list-prs", false, "List all PRs from the database")
	listSections := flag.Bool("list-sections", false, "List all sections from the database")
	getPRFlag := flag.String("pr", "", "Get cached details for a specific PR (format: owner/repo/number)")
	flag.Parse()

	if *testFlag {
		content, err := server.GetFullPRResponse("C-Hipple", "gtdbot", 9, false, nil)
		if err != nil {
			slog.Error("Error getting PR response", "error", err)
			os.Exit(1)
		}
		fmt.Println(content)
		return
	}

	if *listPRs {
		if err := runListPRs(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *listSections {
		if err := runListSections(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *getPRFlag != "" {
		if err := runGetPR(*getPRFlag); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *oneOff && *serverFlag {
		slog.Error("Cannot run in both server and oneoff mode")
		os.Exit(1)
	}

	cfg := config.C()
	workflows_list := workflows.MatchWorkflows(cfg.RawWorkflows, &cfg.Repos, cfg.JiraDomain)
	ms := workflows.NewManagerService(
		workflows_list,
		*oneOff,
		cfg.SleepDuration,
	)
	ms.Initialize()

	if *serverFlag {
		go func() {
			slog.Info("Starting pprof server", "addr", "localhost:6060")
			if err := http.ListenAndServe("localhost:6060", nil); err != nil {
				slog.Error("pprof server exited", "error", err)
			}
		}()
		go ms.Run(log)
		server.RunServer(log)
	} else {
		ms.Run(log)
	}
}

func runListPRs() error {
	renderer := server.NewOrgRenderer(config.C().DB)
	items, err := renderer.GetAllReviewItems()
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SECTION\tSTATUS\tREPO\t#\tAUTHOR\tTITLE")
	for _, item := range items {
		fmt.Fprintf(w, "%s\t%s\t%s/%s\t%d\t%s\t%s\n",
			item.Section, item.Status, item.Owner, item.Repo,
			item.Number, item.Author, item.Title)
	}
	return w.Flush()
}

func runListSections() error {
	db := config.C().DB
	sections, err := db.GetAllSections()
	if err != nil {
		return err
	}

	allItems, err := db.GetAllItems()
	if err != nil {
		return err
	}

	countBySection := make(map[int64]int)
	for _, item := range allItems {
		countBySection[item.SectionID]++
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SECTION\tPRIORITY\tITEMS")
	for _, section := range sections {
		fmt.Fprintf(w, "%s\t%d\t%d\n", section.SectionName, section.Priority, countBySection[section.ID])
	}
	return w.Flush()
}

func runGetPR(prArg string) error {
	// Expected format: owner/repo/number
	parts := strings.SplitN(prArg, "/", 3)
	if len(parts) != 3 {
		return fmt.Errorf("invalid PR format %q, expected owner/repo/number", prArg)
	}
	owner, repo, numStr := parts[0], parts[1], parts[2]
	number, err := strconv.Atoi(numStr)
	if err != nil {
		return fmt.Errorf("invalid PR number %q: %w", numStr, err)
	}

	details, err := server.GetPRDetails(owner, repo, number, false)
	if err != nil {
		return err
	}
	content, err := server.GetFullPRResponse(owner, repo, number, false, details)
	if err != nil {
		return err
	}
	fmt.Println(content)
	return nil
}
