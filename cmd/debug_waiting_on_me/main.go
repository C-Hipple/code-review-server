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
	if err := config.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize configuration: %v\n", err)
		os.Exit(1)
	}
	defer config.C().DB.Close()

	cfg := config.C()

	fmt.Println("== Configuration ==")
	fmt.Printf("GithubUsername (root): %q\n", cfg.GithubUsername)
	if cfg.GithubUsername == "" {
		fmt.Println("  !! Not set. Every identity filter (FilterWaitingOnMe, FilterMyPRs,")
		fmt.Println("     FilterNotMyPRs, FilterMyReviewRequested) compares PRs against an empty")
		fmt.Println("     login, so they match nothing and their sections stay empty.")
	}
	hasToken := os.Getenv("CRS_GITHUB_TOKEN") != ""
	if !hasToken {
		fmt.Println("  !! CRS_GITHUB_TOKEN is not set in this shell.")
	}

	login := reportWorkflows(cfg)

	repos := os.Args[1:]
	if len(repos) == 0 {
		repos = resolveRepos(cfg)
	}
	if len(repos) == 0 {
		fmt.Println("\nNo repositories configured. Set a root-level Repos list or pass owner/repo arguments.")
		return
	}
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

		fmt.Printf("\n%s: %d open PRs\n", entry, len(prs))
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  MATCH\t#\tAUTHOR\tREQUESTED REVIEWERS\tTEAMS\tREASON")
		matched := 0
		for _, pr := range prs {
			decision, reason := explain(pr, login, interacted)
			if decision {
				matched++
			}
			mark := "-"
			if decision {
				mark = "YES"
			}
			fmt.Fprintf(w, "  %s\t%d\t%s\t%s\t%s\t%s\n",
				mark, pr.GetNumber(), pr.GetUser().GetLogin(),
				joinReviewers(pr), joinTeams(pr), reason)
		}
		w.Flush()
		fmt.Printf("  %d of %d matched\n", matched, len(prs))
		totalPRs += len(prs)
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

// reportWorkflows prints the workflows that use FilterWaitingOnMe and returns
// the login the first of them would filter with.
func reportWorkflows(cfg config.Config) string {
	fmt.Println("\n== Workflows using FilterWaitingOnMe ==")
	login := ""
	found := 0
	for _, raw := range cfg.RawWorkflows {
		uses := false
		for _, entry := range raw.Filters {
			name, _ := workflows.ParseFilterString(strings.TrimSpace(entry))
			if name == "FilterWaitingOnMe" {
				uses = true
				break
			}
		}
		if !uses {
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
		for _, entry := range raw.Filters {
			name, _ := workflows.ParseFilterString(strings.TrimSpace(entry))
			if name != "FilterWaitingOnMe" {
				continue
			}
			for _, r := range workflowRepos(raw, cfg) {
				if !seen[r] {
					seen[r] = true
					repos = append(repos, r)
				}
			}
		}
	}
	if len(repos) == 0 {
		return cfg.Repos
	}
	return repos
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
