# Configuration

code-review-server works from a toml config expected at the path `~/.config/codereviewserver.toml`. Storage files like the database and lock files are kept in `~/.crs/`. A valid github api token is also expected. If you are using fine-grained tokens, ensure you have access to pull requests, discussions, and commit status, and actions data.

```bash
export CRS_GITHUB_TOKEN="Github Token"
```

## Running Without a Config File

The config file is optional. With a token set and no `~/.config/codereviewserver.toml`, the server logs a warning naming the path it looked at and runs a built-in default configuration:

| Section | What it holds |
| --- | --- |
| **Waiting On Me** | PRs in your court right now: an open review request (yours or one of your teams'), an approval that CODEOWNERS dismissed, or a comment thread waiting on your reply. |
| **Review Requested** | Everything you or your teams have been asked to review, whether or not it is your turn — the same list as github.com's "Review requests". |

Neither default names a repository or a user. They find their PRs through GitHub search rather than by listing configured repos, and the login they search for comes from the API token (see [Identity](#identity)), so a token is the only setup step.

Print the defaults to start your own config from them:

```bash
codereviewserver -print-default-config > ~/.config/codereviewserver.toml
```

Editing the config from a client while running on defaults writes the same thing: the first save materializes the default workflows alongside whatever you changed, so nothing disappears out from under you.

Note that a config file which *exists* but can't be parsed is still an error — the defaults are only a fallback for "no file at all", never a silent replacement for a config you meant to work.

## Identity

Several filters, and both default workflows, compare pull requests against *you*. That identity is resolved in this order:

1. The workflow's own `GithubUsername`.
2. The root-level `GithubUsername`.
3. The user the `CRS_GITHUB_TOKEN` belongs to, looked up from the GitHub API at startup and cached.

The looked-up login is never written to your config file. Setting `GithubUsername` explicitly is still worth doing if the token belongs to a bot or a shared account, since it takes precedence.

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
- **GithubUsername**: Your GitHub login. Required by every filter that compares PRs against you (`FilterMyPRs`, `FilterNotMyPRs`, `FilterMyReviewRequested`, `FilterWaitingOnMe`, `FilterWaitingOnAuthor`) and by `ListMyPRsWorkflow`. A workflow may set its own `GithubUsername` to override this; if neither is set, workflows using those filters are skipped at startup with an error in the log rather than running with no identity and matching nothing.
- **RepoLocation**: The directory where you keep your git repositories. It defaults to "~/" if not defined. This is used for LSP integration or other lookup tools which need to read the code of the repo you're reviewing.
- **DesktopNotifications**: When `true`, sends a desktop notification each time a PR is newly added to a section. Defaults to `false`. Currently only macOS is supported (via `osascript`); enabling this on other operating systems will log an error.

## Editing the Config from a Client

The config file is the source of truth, but you don't have to edit it by hand. Clients can read and write it over the RPC API with `RPCHandler.GetConfig` and `RPCHandler.UpdateConfig` (see [Protocol](protocol.md)). The web client exposes this as the **Server Configuration** tab behind the gear icon (see [Clients](clients.md)).

Things worth knowing before editing the config from a client:

- **Updates are partial.** A client sends only the settings it means to change; everything else in the file — including keys the server doesn't know about — is left as it is. Sending `Workflows` replaces the whole workflow list, which is how entries are added, removed, or reordered.
- **Nothing is written unless it validates.** The server checks the configuration the update would produce and refuses to write one it can't run, reporting each problem against the field that caused it. The rules are listed below.
- **The previous file is kept as `codereviewserver.toml.bak`.** Comments and the original key ordering are **not** preserved when the server rewrites the file, so keep your own copy if a hand-written config has comments you care about.
- **Changes apply on the next sync.** The background workflow manager reloads the config at the start of each cycle (`SleepDuration` minutes apart), so a saved workflow starts collecting PRs on the following sync rather than instantly.
- **Removed workflows leave their PRs behind until the next sync.** At the end of each cycle the server releases every claim held by a workflow the config no longer defines, then deletes the items nobody claims any more and any section left empty that no workflow writes into. Re-pointing a workflow's `SectionTitle` clears out the old section the same way. A workflow that is still in the file but fails to validate keeps its items — only a workflow actually removed from `Workflows` loses them.

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
- The workflow has no repositories to work with: neither its own `Repos` nor a root-level `Repos` list. (The search-driven types — `WaitingOnMeWorkflow` and `MyReviewRequestsWorkflow` — need no repositories and are exempt.)
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
- `WaitingOnMeWorkflow`
- `MyReviewRequestsWorkflow`
- `SingleRepoSyncReviewRequestsWorkflow` (deprecated, use `SyncReviewRequestsWorkflow` with single-element `Repos`)
- `ListMyPRsWorkflow` (deprecated, use `SyncReviewRequestsWorkflow` with `FilterMyPRs` in filters)
- `ProjectListWorkflow`

### IncludeDiff

`IncludeDiff` will add a subsection which includes the entire diff for the pull request.
> [!WARNING]
> This will make the file get very long very quickly. I recommend only using this for specific workflows which target your non-main reviews org file.

### Workflow Specific Configurations

#### WaitingOnMeWorkflow and MyReviewRequestsWorkflow (search-driven)

These are the two workflows the [default configuration](#running-without-a-config-file) runs, and they are ordinary workflow types you can put in your own config:

```toml
[[Workflows]]
WorkflowType = "MyReviewRequestsWorkflow"
Name = "review-requested"
SectionTitle = "Review Requested"
Filters = ["FilterNotDraft"]   # optional, further narrows the section
```

They take no `Repos`, `Teams`, or `PRState`. Instead of listing the open PRs of configured repositories, they ask GitHub search:

- `MyReviewRequestsWorkflow` runs `review-requested:<you>` plus `team-review-requested:<org>/<team>` for every team you belong to, since the direct qualifier does not expand to team membership. Archived repos are excluded, matching github.com's own list. Teams past the first ten are skipped, to stay inside the search API's 30-requests-per-minute budget.
- `WaitingOnMeWorkflow` takes those same review requests plus `reviewed-by:<you>` and `commenter:<you>` as its candidates, then applies `FilterWaitingOnMe` (the same filter you can use in any other workflow) to keep only the PRs actually in your court.

Search results carry no reviewer or branch data, so each candidate PR is then fetched individually — one API call per candidate, capped at 250 per cycle. That is more per-PR traffic than listing a repo, and the reason to prefer an explicit `Repos` list with `SyncReviewRequestsWorkflow` once you know exactly which repositories you care about.

If a search fails (rate limit, outage), the workflow logs the failure and leaves its section exactly as it was rather than treating "no results" as "everything went away".

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

A project often spans more than one repository, so `Repos` takes a list, just like the other workflow types. Every repo in the list is checked against the epic's linked PRs, each repo's PRs are evaluated independently, and they all land in the same section — one workflow covers the whole project. If `Repos` is omitted the workflow falls back to the root-level `Repos` list.

```bash
export JIRA_API_TOKEN="Jira API Token"
export JIRA_API_EMAIL="your email with your jira account"
```

```toml
JiraDomain="https://your-company.atlassain.net"

[[Workflows]]
WorkflowType = "ProjectListWorkflow"
Name = "Project - Example"
Repos = ["C-Hipple/diff-lsp", "C-Hipple/code-review-server"]
SectionTitle = "Diff LSP Upgrade Project"
JiraEpic = "BOARD-123" # the epic key
```

Repos are matched on the full `owner/repo` pair, so a project can span repos that share a short name under different owners. PRs linked to the epic from a repo outside the list are ignored.

The singular `Owner`/`Repo` pair is still accepted for a single-repo project, but `Repos` is preferred:

```toml
[[Workflows]]
WorkflowType = "ProjectListWorkflow"
Name = "Project - Single Repo"
Owner = "C-Hipple"
Repo = "diff-lsp"
SectionTitle = "Diff LSP Upgrade Project"
JiraEpic = "BOARD-123"
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

## AI Features

AI features — the **comments-addressed** report of which review comments are still
outstanding, the **feature-flags** report of which changes would run with every
feature flag off, **file-ordering**, which orders a PR's files so it reads top to
bottom, and **review-ease**, which rates how easy each PR is to review — are off by
default and switched on one by one:

```toml
[[AIFeatures]]
ID = "comments-addressed"
Enabled = true
Automatic = false              # true also runs it after a PR updates
```

Each feature runs on one of two providers:

- **`gemini`** — the server calls Google's hosted Gemini API itself. It needs
  `GEMINI_API_KEY` exported.
- **`command`** — the server runs a program on this machine, such as
  `claude -p`, writing the prompt to its stdin and reading the answer from its
  stdout. It needs that command line, and no `GEMINI_API_KEY`.

The first of these that is set picks a feature's provider:

1. the feature's own `Provider`
2. the feature's own `Command`, which picks `command`
3. `[AI]` `DefaultProvider`
4. `[AI]` `DefaultCommand`, which picks `command`
5. none of them: `gemini`

`GEMINI_API_KEY` never picks the provider; the server reads it only once a
feature has landed on `gemini`. So for every feature on a program on this
machine:

```toml
[AI]
DefaultCommand = "claude -p"   # rule 4
```

and for every feature on the Gemini API, export `GEMINI_API_KEY` and either
leave `[AI]` out (rule 5) or say so with `DefaultProvider = "gemini"` (rule 3,
which also beats any `DefaultCommand`). See
[AI Features](ai_features.md#which-provider-runs-a-feature) for more examples,
every setting, and how the reports decide.

### File ordering and review ease

These two have no report to open: the server applies what they produce to what it
already shows. **file-ordering** orders the files in a PR diff so the PR reads
top-to-bottom — integration points first, then implementation, then styling, then
tests — instead of the default test-files-last sort. **review-ease** rates how easy
each PR is to review — `easy`, `medium`, or `hard` — and the rating is exposed as the
`review_ease` field in PR metadata (`GetPR`) and in review list items
(`GetAllReviews`), and is appended as a tag (e.g. `:easy:`) on PR headlines in the
rendered org content.

```toml
[[AIFeatures]]
ID = "file-ordering"
Enabled = true
Automatic = true   # order each PR as it arrives, before anyone opens it

[[AIFeatures]]
ID = "review-ease"
Enabled = true
Automatic = true   # rate each PR as it arrives, so the list shows it
```

Opening a PR in a client asks for both whenever what is stored doesn't cover the
PR's head commit; `Automatic = true` also computes them when a workflow fetches or
updates a PR. Results are stored per PR head SHA, so the model is asked at most once
per PR revision, and every run is logged to `~/.crs/llm_calls.log` (respects
`CRS_HOME`) — check it when a PR is missing its review-ease tag or its file order.

Configs written before these were AI features may set the root-level flags
`ExperimentalLLMFileOrdering = true` and `ExperimentalLLMReviewEase = true`. Both
still work: each stands for the matching entry above, on the `gemini` provider, as
the flag always ran. An `[[AIFeatures]]` entry for the same feature wins over its
flag.

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
