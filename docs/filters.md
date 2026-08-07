# Filters

Each workflow can use the available filters to customize which PRs are included.

## Available Filters

*   `FilterMyReviewRequested` - PRs where you are personally requested as a reviewer
*   `FilterNotDraft` - Exclude draft PRs
*   `FilterIsDraft` - Only include draft PRs
*   `FilterNotMyPRs` - Exclude PRs authored by you
*   `FilterMyPRs` - Only include PRs authored by you
*   `FilterCIPassing` - Only include PRs with passing CI/Actions
*   `FilterCIFailing` - Only include PRs with failing CI/Actions
*   `FilterStale` - PRs with no activity for more than 3 days
*   `FilterNotStale` - PRs with activity within the last 3 days
*   `FilterWaitingOnMe` - PRs that need action from you (see below)
*   `FilterWaitingOnAuthor` - PRs where you were the last to act and are waiting on the author
*   `FilterByLabel:<label_name>` - Only include PRs with the specified label (e.g., `FilterByLabel:bug`)
*   `FilterByAuthor:<username>` - Only include PRs authored by the specified user
*   `FilterExcludeAuthor:<username>` - Exclude PRs authored by the specified user

## Filters that need your GitHub username

`FilterMyReviewRequested`, `FilterMyPRs`, `FilterNotMyPRs`, `FilterWaitingOnMe` and `FilterWaitingOnAuthor` compare each PR against *your* GitHub login. They read `GithubUsername` from the workflow, falling back to the root-level setting, and finally to the user your `CRS_GITHUB_TOKEN` belongs to — looked up from the GitHub API and never written to your config file.

So in the common case you don't have to set `GithubUsername` at all. Set it anyway when the token belongs to a bot or a shared account, since an explicit value wins.

If none of the three yields a login — no `GithubUsername` and no usable token — a workflow using one of these filters is **skipped at startup** with an explanatory log line, and the config editor reports the same problem. (Previously the filter simply matched nothing, leaving the section silently empty.)

To see what the filter decides for each of your open PRs, run:

```
go run ./cmd/debug_waiting_on_me            # repos from your config
go run ./cmd/debug_waiting_on_me owner/repo # or an explicit list
```

It prints your resolved username, any configuration problems, per-repo fetch errors, and a row per PR showing the requested reviewers and why the PR did or didn't match.

## FilterWaitingOnMe

A PR is included when any of the following is true:

1.  **You have an open review request.** GitHub clears a review request as soon as you submit a review, and re-requesting a review puts you back in the PR's requested reviewers — so being listed there means the request is outstanding, no matter how recently you reviewed or commented. This is what makes re-review requests show up.
2.  **Your most recent review was dismissed.** Stale-review dismissal and CODEOWNERS re-requests throw away your approval *without* adding you back to the requested reviewers, so this is the only signal that you owe a re-review.
3.  **You have unresponded comments.** You participated in a review thread (or the PR conversation) and someone else replied after you.

`FilterWaitingOnAuthor` is the complement: it excludes PRs where you have an open request or a dismissed review, so a PR never lands in both sections.

Only *personal* review requests count. If your review is requested through a team, use the `Teams` field described below to build a section for it.

## Team-Based Filtering

You can filter PRs by team reviewers by adding a `Teams` field to your workflow configuration. When `Teams` is specified, only PRs where one of those teams is requested as a reviewer will be included. Each workflow can specify its own list of teams, allowing different workflows to target different teams.

```toml
[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "Growth Team Reviews"
Owner = "your-org"
Filters = ["FilterNotDraft"]
Teams = ["growth-pod-review", "growth-and-purchase-pod"]
SectionTitle = "Growth Team Reviews"

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "Backend Team Reviews"
Owner = "your-org"
Filters = ["FilterNotDraft"]
Teams = ["backend-team", "api-reviewers"]
SectionTitle = "Backend Reviews"
```

Note: The `Teams` field uses team **slugs** (the URL-safe identifier), not display names. You can find a team's slug in the GitHub URL when viewing the team page.
