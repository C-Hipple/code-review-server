# code-review-server

code-review-server is a service which runs highly configurable workflows to load code reviews which your are interested into easily managed customizable interfaces.

As the name implies, this repo is for a backend server service, which you'll need a client to attach to.

It ships with a web client and an emacs client, but you can easily build your own using the docs from review_protocol.md

The web client can be found in `bun_client`
the emacs client is in client.el

## Quickstart

1. Clone the repository

2. Configure your toml config per guidelines below && setup your environment variables
```bash
export GTDBOT_GITHUB_TOKEN="Github Token"  # Required.
export GEMINI_API_KEY="Gemini Token"  # Only necessary for plugin use.
```

3. compile the go server with `go install ./...`

You need to do `go install` so that the server is installed in system PATH and that clients can find it.  Clients are responsibile for starting the server process (mirroring the implementation of LSPs)

Doing `./...` ensures that the server binary and the included plugins are installed.

### For the web client (using bun)

The web client is packaged with bun, and has a bun backend with a react frontend.  If you build and run the bun backend, you'll get a working webserver on localhost:3000 which lists all of your PRs.  From there you can 

4. `cd` to `bun_client`
5. `bun install && bun run build`
6. `./start-server`

### For emacs client

4. open `client.el`  && evaluate the buffer
5. run commands

```elisp
(crs-start-server) ;; to start the processing
(crs-get-reviews)  ;; Load your required reviews into an ephermeral org-mode buffer

;; To start a review
(crs-start-review-at-point)  ;; when your cursor is on a github URL 


(crs-get-review "C-Hipple" "code-review-server" 1)  ;; Start it directly.
```

Starting a review will then load a new code-review buffer which you can read the review, make comments, and submit your review.

## Installation

```bash
git clone git@github.com/C-Hipple/gtdbot
cd code-review-server
go install
```


## Configuration

code-review-server works from a toml config expected at the path `~/config/gtdbot.toml`.  A valid github api token is also expected.  If you are using fine-grained tokens, ensure you have access to pull requests, discussions, and commit status, and actions data.


```bash
export GTDBOT_GITHUB_TOKEN="Github Token"
```

the basic format is root level config for general fields

and then a list of tables called [[Workflows]] configuring each workflow.

The general fields are:
-
```
Repos: list[str]
SleepDuration: int (in minutes, optional, default=1 minute)
OrgFileDir: str
GithubUsername: str [optional]
RepoLocation: str [optional, default="~/"]
```

OrgFileDir will default to "~/" if it's not defined.  Github username is used for determining when using the NotMyPRs or MyPRs filters
RepoLocation is the directory where you keep your git repositories. It defaults to "~/" if not defined.  This is used for LSP integration or other lookup tools which need to read the code of the repo you're reviewing.


Each workflow entry can take the fields:
```
WorkflowType: str
Name: str
Owner: str
Filters: list[str]
OrgFileName: str
SectionTitle: str
ReleaseCommandCheck: str
Prune: string
IncludeDiff: bool
Teams: list[str]
```

The `GithubUsername` can be set at the top level of the config file. If a workflow does not have a `GithubUsername` set, it will inherit the top-level setting. This is useful for setting a default user for all workflows.

The WorkflowType is one of the following strings:
SyncReviewRequestsWorkflow
SingleRepoSyncReviewRequestsWorkflow
ListMyPRsWorkflow
ProjectListWorkflow

Prune tells the workflow runner whether or not to remove PRs from the section if they're no longer relevant.  The default behavior is to do nothing, and the options are:
Delete: Removes the item from the section.
Archive: Tags the items with :ARCHIVE: so that org functions can clean them up
Keep: Leave existing items in the section untouched.

IncludeDiff will add a subsection which includes the entire diff for the pull request.  Warning: This will make the file get very long very quickly.  I recommend only using this for specific workflows which target your non-main reviews org file.

### Workflow specific configurations
Single Repo Sync workflow takes an additional parameter, Repo.
```
Repo: str
```

ListMyPRsWorkflow takes the additional parameter PRState, which is passed through to the github API when filtering for PRs.
```
PRState: str [open/closed/nil]
```



An Example complete config file is below

```toml

Repos = [
    "C-Hipple/gtdbot",
    "C-Hipple/diff-lsp",
    "C-Hipple/diff-lsp.el",
]
SleepDuration = 5
OrgFileDir = "~/gtd/"

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "List Open PRs"
Owner = "C-Hipple"
Filters = ["FilterNotDraft"]
OrgFileName = "reviews.org"
SectionTitle = "Open PRs"
Prune = "Archive"

[[Workflows]]
WorkflowType = "ListMyPRsWorkflow"
Name = "List Closed PRs"
Owner = "C-Hipple"
OrgFileName = "reviews.org"
SectionTitle = "Closed PRs"
Prune = "Delete"
```

## Filters

Each workflow can use the available filters:

*   `FilterMyReviewRequested` - PRs where you are personally requested as a reviewer
*   `FilterNotDraft` - Exclude draft PRs
*   `FilterIsDraft` - Only include draft PRs
*   `FilterNotMyPRs` - Exclude PRs authored by you

### Team-Based Filtering

You can filter PRs by team reviewers by adding a `Teams` field to your workflow configuration. When `Teams` is specified, only PRs where one of those teams is requested as a reviewer will be included. Each workflow can specify its own list of teams, allowing different workflows to target different teams.

```toml
[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "Growth Team Reviews"
Owner = "your-org"
Filters = ["FilterNotDraft"]
Teams = ["growth-pod-review", "growth-and-purchase-pod"]
OrgFileName = "reviews.org"
SectionTitle = "Growth Team Reviews"
Prune = "Archive"

[[Workflows]]
WorkflowType = "SyncReviewRequestsWorkflow"
Name = "Backend Team Reviews"
Owner = "your-org"
Filters = ["FilterNotDraft"]
Teams = ["backend-team", "api-reviewers"]
OrgFileName = "reviews.org"
SectionTitle = "Backend Reviews"
Prune = "Archive"
```

Note: The `Teams` field uses team **slugs** (the URL-safe identifier), not display names. You can find a team's slug in the GitHub URL when viewing the team page.


## JIRA Integration

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
OrgFileName = "reviews.org"
SectionTitle = "Diff LSP Upgrade Project"
JiraEpic = "BOARD-123" # the epic key
```


## Release Checking

Often for work-workflows, it's very important to know when your particular PR is not just merged, but released to production, or in a release client.

You can configure a release check command which is run when PRs are added to the org file or updated.  GTDBOT will call-out to that program and expected a single string in response for

example. If we have a program on our PATH variable named release-check, you should call it like this:

```
$ release-check C-Hipple code-review-server abcdef
released

$ release-check C-Hipple code-review-server hijklm
release-client

$ release-check C-Hipple code-review-server nopqrs
merged
```

That string will then be put into the title line of the PR via the org-serializer.

## Plugins

Plugins are external projects which are expected to be discoverable on your `$PATH`, and are called per PR.
You can install external plugins to process PR data asynchronously. Plugins receive data via CLI flags and their output is stored in the database.

For full plugin development, checkout the the full [docs](https://code-review-server.readthedocs.io/en/latest/)
You can also check the plugin example_plugin contained in this repo to understand the interface of building your own plugin.  You can do it in any language you'd like.

Add plugins to your `codereviewserver.toml` using `[[Plugins]]` tables:


```toml
[[Plugins]]
Name = "Summarize Diff"
Command = "summarize_diff"
IncludeDiff = true     # Passes --diff flag
IncludeHeaders = true  # Passes --headers flag (metadata)
IncludeComments = true # Passes --comments flag

[[Plugins]]
Name = "Security Check"
Command = "security_check"
IncludeDiff = true
IncludeHeaders = true
IncludeComments = false
```

### Included Plugins

- **Summarize Diff**: Uses Gemini 2.5 Flash to provide a terse bulleted summary of the changes in a PR.
- **Security Check**: Uses Gemini 2.5 Flash to analyze the diff for potential security risks, specifically looking for unprotected sensitive endpoints, hardcoded secrets, or missing security decorators (like `@authenticated`).

Plugins are expected to accept flags like `--owner`, `--repo`, `--number`, and any of the optional content flags enabled above.


## Emacs integration

This project ships with `client.el` for running and configuring this in emacs seamlessly.

### Installation

#### Spacemacs
```elisp
   ;; in dotspacemacs-additional-packages
   (code-review-server :location (recipe
                      :fetcher github
                      :repo "C-Hipple/gtdbot"
                      :files ("*.el")))
```

### Keybinds


You'll likely want to bind run-gtdbot-oneoff and/or run-gtdbot-service.

By default this package sets (if you use evil mode) `,r l` and `, r s` for those two commands.

If you don't use evil mode, you'll have to pick your own keybinds.
