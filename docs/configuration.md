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
DesktopNotifications: bool [optional, default=false]
SectionPriority: map[string]int [optional]
SectionSorting: map[string]string [optional]
```

- **SectionPriority**: Allows you to define the order of sections in your client. Lower numbers come first. This map keys the section title to an integer.
- **SectionSorting**: Controls how PRs are sorted within a section. Maps the section title to a sorting method. Supported values are `"newest_first"` and `"oldest_first"`, which sort by PR creation time. If not set for a section, items will roughly appear in the order they were added to the section.
- **Repos**: A list of repositories in the format "owner/repo". Workflows can also define their own `Repos` list which overrides this global list.
- **GithubUsername**: Used for determining when using the NotMyPRs or FilterMyPRs filters, as well as for smart filters like FilterWaitingOnMe and FilterWaitingOnAuthor to correctly determine your review status.
- **RepoLocation**: The directory where you keep your git repositories. It defaults to "~/" if not defined. This is used for LSP integration or other lookup tools which need to read the code of the repo you're reviewing.
- **DesktopNotifications**: When `true`, sends a desktop notification each time a PR is newly added to a section. Defaults to `false`. Currently only macOS is supported (via `osascript`); enabling this on other operating systems will log an error.

## Editing the Config from a Client

The config file is the source of truth, but you don't have to edit it by hand. Clients can read and write it over the RPC API with `RPCHandler.GetConfig` and `RPCHandler.UpdateConfig` (see [Protocol](protocol.md)). The web client exposes this as the **Server Configuration** tab behind the gear icon (see [Clients](clients.md)).

Things worth knowing before editing the config from a client:

- **Updates are partial.** A client sends only the settings it means to change; everything else in the file — including keys the server doesn't know about — is left as it is. Sending `Workflows` replaces the whole workflow list, which is how entries are added, removed, or reordered.
- **Nothing is written unless it validates.** The server checks the configuration the update would produce and refuses to write one it can't run, reporting each problem against the field that caused it. The rules are listed below.
- **The previous file is kept as `codereviewserver.toml.bak`.** Comments and the original key ordering are **not** preserved when the server rewrites the file, so keep your own copy if a hand-written config has comments you care about.
- **Changes apply on the next sync.** The background workflow manager reloads the config at the start of each cycle (`SleepDuration` minutes apart), so a saved workflow starts collecting PRs on the following sync rather than instantly.
- **Removed workflows leave their PRs behind until the next sync**, at which point items no longer claimed by any workflow are pruned. The section itself stays in the database even when nothing feeds it.

### Validation Rules

A configuration is rejected when any of the following is true:

Root level:

- `SleepDuration` is not a positive number of minutes, or is greater than 1440 (24 hours).
- A `Repos` entry is not in `owner/repo` form.
- A `SectionSorting` value is not `newest_first` or `oldest_first`.

Per workflow:

- `Name`, `WorkflowType`, or `SectionTitle` is missing.
- Two workflows share a `Name` — names identify which workflow owns an item, so they must be unique.
- `WorkflowType` is not one of the types listed below.
- A filter is not one this server knows, an argument-taking filter (`FilterByLabel`, `FilterByAuthor`, `FilterExcludeAuthor`) is missing its argument, or an argument is given to a filter that takes none.
- A `Repos` entry is not in `owner/repo` form, or a `Teams` entry is empty.
- `PRState` is set to something other than `open`, `closed`, or `all`.
- The workflow has no repositories to work with: neither its own `Repos` nor a root-level `Repos` list.
- `ProjectListWorkflow` is missing `JiraEpic`, or `SingleRepoSyncReviewRequestsWorkflow` is missing a valid `Repo`.

## Workflows

A list of tables called `[[Workflows]]` configures each workflow.

Each workflow entry can take the fields:
```toml
WorkflowType: str
Name: str
Owner: str
Filters: list[str]
SectionTitle: str
IncludeDiff: bool
Teams: list[str]
DesktopNotifications: bool [optional]
```

The `GithubUsername` can be set at the top level of the config file. If a workflow does not have a `GithubUsername` set, it will inherit the top-level setting. This is useful for setting a default user for all workflows.

`DesktopNotifications` on a workflow overrides the top-level `DesktopNotifications` setting for that workflow only. Omit it to inherit the global value; set it to `true` or `false` to force notifications on or off for this workflow. This lets you keep notifications globally off but opt in on a specific workflow (e.g. review requests) — or vice versa.

### Workflow Types

The WorkflowType is one of the following strings:
- `SyncReviewRequestsWorkflow`
- `SingleRepoSyncReviewRequestsWorkflow` (deprecated, use `SyncReviewRequestsWorkflow` with single-element `Repos`)
- `ListMyPRsWorkflow` (deprecated, use `SyncReviewRequestsWorkflow` with `FilterMyPRs` in filters)
- `ProjectListWorkflow`

### IncludeDiff

`IncludeDiff` will add a subsection which includes the entire diff for the pull request.
> [!WARNING]
> This will make the file get very long very quickly. I recommend only using this for specific workflows which target your non-main reviews org file.

### Workflow Specific Configurations

#### SingleRepoSyncReviewRequestsWorkflow (Deprecated)

> [!WARNING]
> This workflow type is deprecated. Use `SyncReviewRequestsWorkflow` with a single-element `Repos` list instead.

Takes an additional parameter, `Repo`.

```toml
Repo: str # "owner/repo" format
```

**Migration:** Convert to `SyncReviewRequestsWorkflow`:
```toml
WorkflowType = "SyncReviewRequestsWorkflow"
Repos = ["owner/repo"]  # Instead of Repo = "owner/repo"
```

#### ListMyPRsWorkflow (Deprecated)

> [!WARNING]
> This workflow type is deprecated. Use `SyncReviewRequestsWorkflow` with `FilterMyPRs` in the filters list instead.

Takes the additional parameter `PRState`, which is passed through to the github API when filtering for PRs.

```toml
PRState: str [open/closed/nil]
```

**Migration:** Convert to `SyncReviewRequestsWorkflow`:
```toml
WorkflowType = "SyncReviewRequestsWorkflow"
PRState = "closed"  # Keep this as-is
Filters = ["FilterMyPRs"]  # Explicitly add this filter
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

You can configure a release check command per repository using the `[RepoConfigs]` section. The command is run for all closed PRs each time they are updated during a sync cycle. The result is stored in the database alongside PR metadata and included in `GetPR` and `GetAllReviews` responses as the `release_status` field.

```toml
[RepoConfigs."owner/repo"]
ReleaseCheckCommand = "release-check"
```

The command is invoked as: `<command> <owner> <repo> <merge-commit-sha>`

Example. If we have a program on our PATH variable named release-check, you should call it like this:

```
$ release-check C-Hipple code-review-server abcdef
released

$ release-check C-Hipple code-review-server hijklm
release-client

$ release-check C-Hipple code-review-server nopqrs
merged
```

The returned string is stored and exposed as the `release_status` field in PR metadata.

## Experimental LLM Features

These root-level flags enable LLM-powered helpers. Both are off by default and require a Gemini API key:

```bash
export GEMINI_API_KEY="Gemini API Key"
```

```toml
ExperimentalLLMFileOrdering = true
ExperimentalLLMReviewEase = true
```

- **ExperimentalLLMFileOrdering**: Orders the files in a PR diff via an LLM so the PR reads top-to-bottom (integration points first, then implementation, then styling, then tests) instead of the default test-files-last sort.
- **ExperimentalLLMReviewEase**: Rates how easy each PR is to review — `easy`, `medium`, or `hard` — in the same LLM call used for the file ordering. The rating is exposed as the `review_ease` field in PR metadata (`GetPR`) and in review list items (`GetAllReviews`), and is appended as a tag (e.g. `:easy:`) on PR headlines in the rendered org content.

Results are cached in the database per PR head SHA, so the LLM is queried at most once per PR revision.

Every LLM call is appended to `~/.crs/llm_calls.log` (respects `CRS_HOME`). Each report records which PR and code path triggered the call, which provider/model was used, input sizes, duration, and the outcome — `SUCCESS`, `PARTIAL` (e.g. the response's review-ease line couldn't be parsed), or `FAILURE` with the stage that failed (`client-init`, `request`, `http-status`, `decode`, `empty-response`, or `parse`) — including a snippet of the raw response when parsing had problems. Check this log when a PR is missing its review-ease tag.

## Example Config

```toml
Repos = [
    "C-Hipple/gtdbot",
    "C-Hipple/diff-lsp",
    "C-Hipple/diff-lsp.el",
]
SleepDuration = 5
DesktopNotifications = true

[SectionSorting]
"Open PRs" = "newest_first"

[RepoConfigs."C-Hipple/diff-lsp"]
ReleaseCheckCommand = "release-check"

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "List Open PRs"
Filters = ["FilterNotDraft"]
SectionTitle = "Open PRs"

[[Workflows]]
WorkflowType = "ListMyPRsWorkflow"
Name = "List Closed PRs"
SectionTitle = "Closed PRs"
```
