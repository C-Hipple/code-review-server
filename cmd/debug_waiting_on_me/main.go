// Command debug_waiting_on_me explains, PR by PR, what the FilterWaitingOnMe
// filter decides and why.
//
// An empty "Waiting on Me" section has several possible causes that all look
// identical from the outside: no GithubUsername, a repo the token can't read
// (which makes the manager skip the whole workflow for that cycle), the
// interaction prefilter skipping a PR the search index hasn't caught up on
// (see git_tools/interaction_prefilter.go), or simply no outstanding
// requests. This runs the same checks the filter does — including the
// prefilter — against the live config and prints the decision for every
// open PR.
//
//	go run ./cmd/debug_waiting_on_me              # repos from the config
//	go run ./cmd/debug_waiting_on_me owner/repo   # or an explicit list
package main

import (
	"crs/config"
	"crs/git_tools"
	"crs/workflows"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/google/go-github/v74/github"
)

func main() {
	// Same identity fallback the server uses, so this tool diagnoses the
	// configuration that actually runs rather than the file's literal contents.
	config.LoginResolver = git_tools.GetAuthenticatedLogin

	if err := config.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize configuration: %v\n", err)
		os.Exit(1)
	}
	defer config.C().DB.Close()

	cfg := config.C()

	fmt.Println("== Configuration ==")
	if cfg.UsingDefaults {
		fmt.Println("No config file — running the built-in defaults.")
	}
	fmt.Printf("GithubUsername (root): %q\n", cfg.GithubUsername)
	if cfg.GithubUsername == "" {
		fmt.Println("  !! No login available: GithubUsername is unset and the API token could")
		fmt.Println("     not be resolved to a user. Every identity filter (FilterWaitingOnMe,")
		fmt.Println("     FilterMyPRs, FilterNotMyPRs, FilterMyReviewRequested) then compares")
		fmt.Println("     PRs against an empty login, matching nothing.")
	}
	hasToken := git_tools.HasToken()
	if !hasToken {
		fmt.Println("  !! CRS_GITHUB_TOKEN is not set in this shell.")
	}

	login := reportWorkflows(cfg)

	repos := os.Args[1:]
	if len(repos) == 0 {
		repos = resolveRepos(cfg)
	}
	// No repos is normal now: WaitingOnMeWorkflow finds its PRs by search
	// instead, so fall back to the same candidate set it uses.
	useSearch := len(repos) == 0
	if login == "" {
		login = cfg.GithubUsername
	}
	if login == "" {
		fmt.Println("\nNo GitHub username available, so there is nothing to compare PRs against.")
		os.Exit(1)
	}

	if !hasToken {
		// GetGithubClient exits the process on a missing token, which would look
		// like the tool itself crashing.
		fmt.Println("\nCRS_GITHUB_TOKEN is not set, so no PRs can be fetched. Export it and re-run")
		fmt.Println("to see the per-PR decisions.")
		os.Exit(1)
	}

	// The same two-search interaction set the filter consults; a nil set means
	// the filter is running in its fail-open mode (checking every PR).
	interacted := git_tools.SearchMyOpenInteractions(login)
	if interacted == nil {
		fmt.Println("\n!! Interaction prefilter unavailable (search failed?) — the filter falls")
		fmt.Println("   back to checking every PR. Decisions below reflect that fallback.")
	} else {
		fmt.Printf("\nInteraction prefilter: search found %d open PRs with reviews or comments from %q.\n",
			len(interacted), login)
	}

	fmt.Printf("\n== Open PRs (matching as %q) ==\n", login)
	client := git_tools.GetGithubClient()
	totalPRs, totalMatched := 0, 0

	if useSearch {
		fmt.Println("\nNo repositories configured, so these are the search candidates")
		fmt.Println("WaitingOnMeWorkflow works from. Pass owner/repo arguments to scan a")
		fmt.Println("repository's whole open PR list instead.")
		prs, err := searchCandidates(login)
		if err != nil {
			fmt.Printf("\nSEARCH FAILED — %v\n", err)
			fmt.Println("A workflow hitting this leaves its section untouched for the cycle.")
			os.Exit(1)
		}
		matched, total := printDecisions("search candidates", prs, login, interacted)
		totalMatched, totalPRs = matched, total
	}

	for _, entry := range repos {
		owner, repo, err := git_tools.ParseRepoName(entry)
		if err != nil {
			fmt.Printf("\n%s: SKIPPED — %v\n", entry, err)
			continue
		}

		prs, err := git_tools.GetPRs(client, "open", owner, repo)
		if err != nil {
			// This is the failure that silently empties a section: the manager
			// skips any workflow with an unfetchable repo for the whole cycle.
			fmt.Printf("\n%s: FETCH FAILED — %v\n", entry, err)
			fmt.Println("  A workflow covering this repo is skipped entirely on every cycle")
			fmt.Println("  (\"Skipping workflow due to PR fetch error\" in the log), which empties")
			fmt.Println("  its section as items expire.")
			continue
		}

		matched, total := printDecisions(entry, prs, login, interacted)
		totalPRs += total
		totalMatched += matched
	}

	fmt.Printf("\n== Summary ==\n%d of %d open PRs are waiting on %s\n", totalMatched, totalPRs, login)
	if totalMatched == 0 && totalPRs > 0 {
		fmt.Println("\nNothing matched. Check that the logins in the REQUESTED REVIEWERS column")
		fmt.Println("include yours — if your review was requested through a team, it shows in")
		fmt.Println("TEAMS instead and FilterWaitingOnMe does not match it (use the workflow's")
		fmt.Println("Teams field for that).")
	}
}

// printDecisions prints one table of per-PR decisions and returns how many
// matched out of how many were checked.
func printDecisions(label string, prs []*github.PullRequest, login string, interacted git_tools.InteractionSet) (int, int) {
	fmt.Printf("\n%s: %d open PRs\n", label, len(prs))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  MATCH\t#\tAUTHOR\tREQUESTED REVIEWERS\tTEAMS\tREASON")
	matched := 0
	for _, pr := range prs {
		decision, reason := explain(pr, login, interacted)
		mark := "-"
		if decision {
			matched++
			mark = "YES"
		}
		fmt.Fprintf(w, "  %s\t%d\t%s\t%s\t%s\t%s\n",
			mark, pr.GetNumber(), pr.GetUser().GetLogin(),
			joinReviewers(pr), joinTeams(pr), reason)
	}
	w.Flush()
	fmt.Printf("  %d of %d matched\n", matched, len(prs))
	return matched, len(prs)
}

// explain reruns the filter's checks for one PR — in the same order the
// filter takes them, so a PR the prefilter skips is reported as skipped even
// if a full interaction-state check would have matched it (that divergence is
// exactly the thing worth surfacing when debugging a missing PR).
func explain(pr *github.PullRequest, login string, interacted git_tools.InteractionSet) (bool, string) {
	if pr == nil || pr.Base == nil || pr.Base.Repo == nil ||
		pr.Base.Repo.Owner == nil || pr.Base.Repo.Owner.Login == nil || pr.Base.Repo.Name == nil {
		return false, "incomplete PR data"
	}

	requested := false
	for _, r := range pr.RequestedReviewers {
		if r != nil && r.Login != nil && strings.EqualFold(*r.Login, login) {
			requested = true
			break
		}
	}
	if requested {
		return true, "review requested from you"
	}

	if !interacted.MayHaveInteracted(pr) {
		return false, "prefiltered: search found no reviews or comments from you"
	}

	state := git_tools.GetInteractionState(*pr.Base.Repo.Owner.Login, *pr.Base.Repo.Name, pr)

	reasons := []string{}
	if state.MyReviewDismissed {
		reasons = append(reasons, "your last review was dismissed")
	}
	if state.HasUnrespondedComments {
		reasons = append(reasons, "unresponded comments")
	}
	if len(reasons) == 0 {
		return false, "no open request, no dismissal, no unresponded comments"
	}
	return true, strings.Join(reasons, "; ")
}

// usesWaitingOnMe reports whether a workflow applies FilterWaitingOnMe, either
// because it lists the filter or because it is the search-driven workflow type
// built around it.
func usesWaitingOnMe(raw config.RawWorkflow) bool {
	if raw.WorkflowType == "WaitingOnMeWorkflow" {
		return true
	}
	for _, entry := range raw.Filters {
		name, _ := workflows.ParseFilterString(strings.TrimSpace(entry))
		if name == "FilterWaitingOnMe" {
			return true
		}
	}
	return false
}

// reportWorkflows prints the workflows that use FilterWaitingOnMe and returns
// the login the first of them would filter with.
func reportWorkflows(cfg config.Config) string {
	fmt.Println("\n== Workflows using FilterWaitingOnMe ==")
	login := ""
	found := 0
	for _, raw := range cfg.RawWorkflows {
		if !usesWaitingOnMe(raw) {
			continue
		}
		found++
		fmt.Printf("  %q -> section %q, username %q, repos %v\n",
			raw.Name, raw.SectionTitle, raw.GithubUsername, workflowRepos(raw, cfg))
		if login == "" {
			login = strings.TrimSpace(raw.GithubUsername)
		}
	}
	if found == 0 {
		fmt.Println("  (none — no workflow in the config lists FilterWaitingOnMe)")
	}

	problems := workflows.ValidateWorkflows(cfg.RawWorkflows, cfg.Repos)
	if len(problems) > 0 {
		fmt.Println("\n== Configuration problems ==")
		for _, p := range problems {
			fmt.Printf("  %s\n", p.Error())
		}
	}
	return login
}

func workflowRepos(raw config.RawWorkflow, cfg config.Config) []string {
	if len(raw.Repos) > 0 {
		return raw.Repos
	}
	return cfg.Repos
}

func resolveRepos(cfg config.Config) []string {
	seen := map[string]bool{}
	repos := []string{}
	for _, raw := range cfg.RawWorkflows {
		if !usesWaitingOnMe(raw) {
			continue
		}
		for _, r := range workflowRepos(raw, cfg) {
			if !seen[r] {
				seen[r] = true
				repos = append(repos, r)
			}
		}
	}
	if len(repos) == 0 {
		return cfg.Repos
	}
	return repos
}

// searchCandidates is the candidate set WaitingOnMeWorkflow works from when no
// repositories are configured: open review requests (mine and my teams') plus
// every open PR I have reviewed or commented on.
func searchCandidates(login string) ([]*github.PullRequest, error) {
	requested, err := git_tools.ReviewRequestedRefs(login)
	if err != nil {
		return nil, err
	}
	interacted, err := git_tools.MyInteractionRefs(login)
	if err != nil {
		return nil, err
	}
	refs := git_tools.DedupeRefs(append(requested, interacted...))
	fmt.Printf("Search found %d candidate PRs (%d review requests, %d with my reviews or comments).\n",
		len(refs), len(requested), len(interacted))
	return git_tools.GetPRsForRefs(git_tools.GetGithubClient(), refs)
}

func joinReviewers(pr *github.PullRequest) string {
	names := []string{}
	for _, r := range pr.RequestedReviewers {
		if r != nil && r.Login != nil {
			names = append(names, *r.Login)
		}
	}
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}

func joinTeams(pr *github.PullRequest) string {
	names := []string{}
	for _, t := range pr.RequestedTeams {
		if t != nil && t.Slug != nil {
			names = append(names, *t.Slug)
		}
	}
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}
