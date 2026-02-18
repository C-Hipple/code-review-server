# Configuration

code-review-server works from a toml config expected at the path `~/.config/codereviewserver.toml`. Storage files like the database and lock files are kept in `~/.crs/`. A valid github api token is also expected. If you are using fine-grained tokens, ensure you have access to pull requests, discussions, and commit status, and actions data.

```bash
export CRS_GITHUB_TOKEN="Github Token"
```

## General Fields

The basic format is root level config for general fields:

```toml
Repos: list[str] # List of "owner/repo" strings.
SleepDuration: int (in minutes, optional, default=1 minute)
GithubUsername: str [optional]
RepoLocation: str [optional, default="~/"]
SectionPriority: map[string]int [optional]
```

- **SectionPriority**: Allows you to define the order of sections in your client. Lower numbers come first. This map keys the section title to an integer.
- **Repos**: A list of repositories in the format "owner/repo". Workflows can also define their own `Repos` list which overrides this global list.
- **GithubUsername**: Used for determining when using the NotMyPRs or FilterMyPRs filters, as well as for smart filters like FilterWaitingOnMe and FilterWaitingOnAuthor to correctly determine your review status.
- **RepoLocation**: The directory where you keep your git repositories. It defaults to "~/" if not defined. This is used for LSP integration or other lookup tools which need to read the code of the repo you're reviewing.

## Workflows

A list of tables called `[[Workflows]]` configures each workflow.

Each workflow entry can take the fields:
```toml
WorkflowType: str
Name: str
Owner: str
Filters: list[str]
SectionTitle: str
ReleaseCommandCheck: str
Prune: string
IncludeDiff: bool
Teams: list[str]
```

The `GithubUsername` can be set at the top level of the config file. If a workflow does not have a `GithubUsername` set, it will inherit the top-level setting. This is useful for setting a default user for all workflows.

### Workflow Types

The WorkflowType is one of the following strings:
- `SyncReviewRequestsWorkflow`
- `SingleRepoSyncReviewRequestsWorkflow`
- `ListMyPRsWorkflow`
- `ProjectListWorkflow`

### Pruning

`Prune` tells the workflow runner whether or not to remove PRs from the section if they're no longer relevant. The default behavior is to do nothing, and the options are:
- `Delete`: Removes the item from the section.
- `Keep`: Leave existing items in the section untouched.

### IncludeDiff

`IncludeDiff` will add a subsection which includes the entire diff for the pull request.
> [!WARNING]
> This will make the file get very long very quickly. I recommend only using this for specific workflows which target your non-main reviews org file.

### Workflow Specific Configurations

#### SingleRepoSyncReviewRequestsWorkflow

Takes an additional parameter, `Repo`.

```toml
Repo: str # "owner/repo" format
```

#### ListMyPRsWorkflow

Takes the additional parameter `PRState`, which is passed through to the github API when filtering for PRs.

```toml
PRState: str [open/closed/nil]
```

#### ProjectListWorkflow (JIRA Integration)

The `ProjectListWorkflow` pulls information from Jira to build a realtime list of all PRs which are linked to children cards of the Jira epic given in the config.

Each workflow is tied to a single github repository, if you want multiple repos per project, create two workflows and have them use the same SectionTitle.

```bash
export JIRA_API_TOKEN="Jira API Token"
export JIRA_API_EMAIL="your email with your jira account"
```

```toml
JiraDomain="https://your-company.atlassain.net"

[[Workflows]]
WorkflowType = "ProjectListWorkflow"
Name = "Project - Example"
Owner = "C-Hipple"
Repo = "diff-lsp"
SectionTitle = "Diff LSP Upgrade Project"
JiraEpic = "BOARD-123" # the epic key
```

## Release Checking

Often for work-workflows, it's very important to know when your particular PR is not just merged, but released to production, or in a release client.

You can configure a release check command which is run when PRs are added to the org file or updated. CodeReviewServer will call-out to that program and expected a single string in response.

Example. If we have a program on our PATH variable named release-check, you should call it like this:

```
$ release-check C-Hipple code-review-server abcdef
released

$ release-check C-Hipple code-review-server hijklm
release-client

$ release-check C-Hipple code-review-server nopqrs
merged
```

That string will then be put into the title line of the PR via the org-serializer.

## Example Config

```toml
Repos = [
    "C-Hipple/gtdbot",
    "C-Hipple/diff-lsp",
    "C-Hipple/diff-lsp.el",
]
SleepDuration = 5

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "List Open PRs"
Owner = "C-Hipple"
Filters = ["FilterNotDraft"]
SectionTitle = "Open PRs"
Prune = "Delete"

[[Workflows]]
WorkflowType = "ListMyPRsWorkflow"
Name = "List Closed PRs"
Owner = "C-Hipple"
SectionTitle = "Closed PRs"
Prune = "Delete"
```
