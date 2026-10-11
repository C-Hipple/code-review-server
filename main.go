package main

import (
	"crs/ai"
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"crs/logger"
	"crs/server"
	"crs/workflows"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

func main() {
	log := logger.New()
	slog.SetDefault(log)
	slog.Info("starting")

	oneOff := flag.Bool("oneoff", false, "Pass oneoff to only run once")
	serverFlag := flag.Bool("server", false, "Run as an RPC server")
	testFlag := flag.Bool("test", false, "Run in test mode")
	listPRs := flag.Bool("list-prs", false, "List all PRs from the database, every user's dashboard included")
	listSections := flag.Bool("list-sections", false, "List all sections from the database, every user's included")
	getPRFlag := flag.String("pr", "", "Get cached details for a specific PR (format: owner/repo/number)")
	printDefaultConfig := flag.Bool("print-default-config", false, "Print the built-in default configuration and exit")
	listenAddr := flag.String("listen", "", "Run the workflows and serve remote clients over TCP on host:port, instead of one client over stdio")
	flag.Parse()

	// Printing the defaults is what someone does before they have a config, a
	// database, or a token, so it happens before anything is initialized.
	if *printDefaultConfig {
		fmt.Print(config.DefaultConfigTOML)
		return
	}
	if *listenAddr != "" && (*serverFlag || *oneOff) {
		fmt.Fprintln(os.Stderr, "error: -listen cannot be combined with -server or -oneoff")
		os.Exit(1)
	}

	// Lets a config that never names a GithubUsername still know who "me" is.
	// Set before Initialize so the very first parse can use it.
	config.LoginResolver = git_tools.GetAuthenticatedLogin

	// Initialize configuration
	if err := config.Initialize(); err != nil {
		slog.Error("Failed to initialize configuration", "error", err)
		os.Exit(1)
	}
	defer config.C().DB.Close()

	if *testFlag {
		content, err := server.GetFullPRResponse("", "C-Hipple", "gtdbot", 9, false, nil)
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
	logConfigProblems(&cfg)
	// The config's workflows and those of the users active now; the manager
	// rebuilds the set every cycle, so this one only seeds the sections and
	// the first (or only, under -oneoff) cycle.
	workflows_list := workflows.RunSet(cfg, time.Now())
	ms := workflows.NewManagerService(
		workflows_list,
		*oneOff,
		cfg.SleepDuration,
	)
	ms.Initialize()

	if *serverFlag || *listenAddr != "" {
		// Compute plugin results and the automatic AI features off the back of
		// the workflow cycle that added or refreshed a PR, so opening a review
		// finds them already cached. Only registered when serving clients
		// (-server or -listen): a workflow-only process exits when its cycle
		// ends and would kill the hooks mid-run.
		workflows.SetPRUpdatedHook(server.WarmPRAnalysis)

		go func() {
			slog.Info("Starting pprof server", "addr", "localhost:6061")
			if err := http.ListenAndServe("localhost:6061", nil); err != nil {
				slog.Error("pprof server exited", "error", err)
			}
		}()
		go ms.Run()
		if *listenAddr != "" {
			logListenSecrets()
			if err := server.Listen(*listenAddr); err != nil {
				slog.Error("Error serving remote clients", "error", err)
				os.Exit(1)
			}
			return
		}
		server.RunServer()
	} else {
		ms.Run()
	}
}

// logListenSecrets says at startup which of the hosted server's two secrets
// are set, since each one missing quietly switches something off: without
// CRS_REGISTRATION_KEY nobody can register, and without CRS_ADMIN_TOKEN nobody
// can authenticate as the server's own identity. Never the values.
func logListenSecrets() {
	if os.Getenv("CRS_REGISTRATION_KEY") == "" {
		slog.Warn("CRS_REGISTRATION_KEY is not set: registration is disabled, so only users already registered can connect")
	}
	if os.Getenv("CRS_ADMIN_TOKEN") == "" {
		slog.Info("CRS_ADMIN_TOKEN is not set: no connection can authenticate as the server's administrator, only users registered as one")
	}
}

// logConfigProblems runs the same validation the config-editing RPCs use and
// logs whatever it finds. The config file is loaded, not rejected: a config that
// was written by hand has never been validated before now, and most problems
// only affect one workflow. They are logged as errors because the visible
// symptom — a section that is simply empty — otherwise gives no hint that
// anything is wrong.
func logConfigProblems(cfg *config.Config) {
	problems := append(config.Validate(cfg), workflows.ValidateWorkflows(cfg.RawWorkflows, cfg.Repos)...)
	problems = append(problems, ai.ValidateFeatures(cfg.AIFeatures)...)
	if len(problems) == 0 {
		return
	}
	slog.Error("Configuration problems found; affected workflows may not behave as configured", "count", len(problems))
	for _, p := range problems {
		if p.Workflow >= 0 && p.Workflow < len(cfg.RawWorkflows) {
			slog.Error("Config problem", "workflow", cfg.RawWorkflows[p.Workflow].Name, "field", p.Field, "problem", p.Message)
			continue
		}
		slog.Error("Config problem", "field", p.Field, "problem", p.Message)
	}
}

// runListPRs lists every dashboard's PRs: the server's own, then each user's
// that has sections. The USER column only appears once there are users, so a
// server only its owner uses lists exactly what it always has.
func runListPRs() error {
	db := config.C().DB
	sections, err := db.GetAllSections()
	if err != nil {
		return err
	}
	owners, showUser := dashboardOwners(sections)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, userColumn(showUser, "USER")+"SECTION\tSTATUS\tREPO\t#\tAUTHOR\tTITLE")
	for _, owner := range owners {
		items, err := server.NewOrgRenderer(db, owner).GetAllReviewItems()
		if err != nil {
			return err
		}
		for _, item := range items {
			fmt.Fprintf(w, "%s%s\t%s\t%s/%s\t%d\t%s\t%s\n",
				userColumn(showUser, owner), item.Section, item.Status, item.Owner, item.Repo,
				item.Number, item.Author, item.Title)
		}
	}
	return w.Flush()
}

// runListSections lists every section in the database, with a USER column
// once there are users' sections to tell apart (see runListPRs).
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

	_, showUser := dashboardOwners(sections)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, userColumn(showUser, "USER")+"SECTION\tPRIORITY\tITEMS")
	for _, section := range sections {
		fmt.Fprintf(w, "%s%s\t%d\t%d\n", userColumn(showUser, section.UserLogin),
			section.SectionName, section.Priority, countBySection[section.ID])
	}
	return w.Flush()
}

// dashboardOwners lists whose dashboards sections belong to, in GetAllSections'
// order — the server's own ("") first — and reports whether any is a user's.
func dashboardOwners(sections []*database.Section) (owners []string, anyUser bool) {
	for _, section := range sections {
		if !slices.Contains(owners, section.UserLogin) {
			owners = append(owners, section.UserLogin)
		}
		if section.UserLogin != "" {
			anyUser = true
		}
	}
	return owners, anyUser
}

// userColumn is a row's USER cell and its separator when the column is shown,
// and nothing when it is not. The server's own rows leave the cell empty.
func userColumn(show bool, value string) string {
	if !show {
		return ""
	}
	return value + "\t"
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

	// The server's own drafts, as local mode has always shown.
	details, err := server.GetPRDetails("", owner, repo, number, false)
	if err != nil {
		return err
	}
	content, err := server.GetFullPRResponse("", owner, repo, number, false, details)
	if err != nil {
		return err
	}
	fmt.Println(content)
	return nil
}
