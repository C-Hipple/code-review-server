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
*   `FilterWaitingOnMe` - PRs where you are a requested reviewer and need to act
*   `FilterWaitingOnAuthor` - PRs where you were the last to act and are waiting on the author
*   `FilterByLabel:<label_name>` - Only include PRs with the specified label (e.g., `FilterByLabel:bug`)
*   `FilterByAuthor:<username>` - Only include PRs authored by the specified user
*   `FilterExcludeAuthor:<username>` - Exclude PRs authored by the specified user

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
Prune = "Delete"

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "Backend Team Reviews"
Owner = "your-org"
Filters = ["FilterNotDraft"]
Teams = ["backend-team", "api-reviewers"]
SectionTitle = "Backend Reviews"
Prune = "Delete"
```

Note: The `Teams` field uses team **slugs** (the URL-safe identifier), not display names. You can find a team's slug in the GitHub URL when viewing the team page.
