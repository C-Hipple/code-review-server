# Command-Line Interface

The `crs` binary can be used directly as a CLI tool to query the database without starting the RPC server or running background workflows. This is useful for quick lookups, scripting, and integration with shell tools.

## Basic Usage

All CLI flags exit immediately after printing results. Configuration is loaded from `~/.config/codereviewserver.toml` and the SQLite database from `~/.crs/codereviewserver.db`.

```bash
# List all PRs
crs -list-prs

# List all sections with item counts
crs -list-sections

# Get details for a specific PR
crs -pr owner/repo/123

# Print the built-in default configuration
crs -print-default-config

# Print what registering with a hosted server sends from the config file
crs -print-user-config
```

## Flags

### `-list-prs`

Lists all PRs across all sections in a tab-aligned table format.

**Output columns:**
- `SECTION` — The section name the PR is in
- `STATUS` — The PR status (e.g., "open", "draft")
- `REPO` — Owner and repository name
- `#` — Pull request number
- `AUTHOR` — PR author
- `TITLE` — PR title

On a server that keeps dashboards for registered users, every user's PRs are listed after the server's own, and a leading `USER` column says whose dashboard each row is on (empty for the server's). Without users the column is left out.

**Example:**
```bash
$ crs -list-prs
SECTION              STATUS  REPO                #    AUTHOR           TITLE
my-section           open    myorg/myrepo       123  @alice            Fix the critical bug
another-section      draft   myorg/other-repo   456  @bob              WIP: Refactor module
```

### `-list-sections`

Lists all configured sections with their priority and item count.

**Output columns:**
- `SECTION` — The section name
- `PRIORITY` — Section ordering priority (lower numbers first)
- `ITEMS` — Number of PRs in the section

As with `-list-prs`, a leading `USER` column appears once users have sections of their own.

**Example:**
```bash
$ crs -list-sections
SECTION              PRIORITY  ITEMS
my-section           1         5
another-section      2         3
```

### `-pr <owner/repo/number>`

Fetches and displays cached details for a specific PR. Uses the same code path as the RPC handler: cached data is preferred, but will query GitHub API if the cache is cold.

**Format:** `-pr owner/repo/number`

**Output:** Full PR details including metadata, diff, comments, reviews, and local feedback. The drafts shown are the server's own, the ones a local client writes.

**Example:**
```bash
$ crs -pr myorg/myrepo/123
#123: Fix the critical bug
Author: @alice
Title: Fix the critical bug
Refs: main ... fix-critical-bug
URL: https://github.com/myorg/myrepo/pull/123
State: open
...
```

### `-print-default-config`

Prints the configuration the server runs when `~/.config/codereviewserver.toml` does not exist (see [Configuration](configuration.md#running-without-a-config-file)) and exits. Unlike the other flags this one touches neither the config file nor the database, so it works on a machine that has neither — which is the point:

```bash
crs -print-default-config > ~/.config/codereviewserver.toml
```

Logs go to stderr, so the redirect above captures only TOML.

### `-print-user-config`

Prints, as one line of JSON, what a client sends when it registers with a [hosted server](hosted_server.md): the `[[Workflows]]`, `Repos`, `SectionPriority` and `SectionSorting` from `~/.config/codereviewserver.toml`, and nothing else.

```bash
$ crs -print-user-config
{"Workflows":[{"WorkflowType":"SyncReviewRequestsWorkflow","Name":"review",...}],"Repos":["myorg/myrepo"],"SectionPriority":{},"SectionSorting":{}}
```

The values are as written in the file: a workflow's empty `GithubUsername` stays empty (the server fills in the registered login), and `DesktopNotifications` is dropped, as the server would drop it. Keys the file leaves out print as `[]` or `{}`, never `null`. Like `-print-default-config` it touches neither the database nor GitHub, so it works on a machine that only runs a client; the Emacs client runs it to register. A missing or unparsable file exits non-zero with a message on stderr.

## Use Cases

### Shell Scripting

Get the count of open PRs:
```bash
crs -list-prs | wc -l
```

Find PRs in a specific section:
```bash
crs -list-prs | grep "my-section"
```

Export to CSV:
```bash
crs -list-prs | tr '\t' ',' > prs.csv
```

### Integration with Other Tools

Get PR details for processing:
```bash
crs -pr myorg/myrepo/123 | grep "Approved-By" | cut -d: -f2
```

Check section status:
```bash
crs -list-sections | awk '{sum+=$3} END {print "Total PRs:", sum}'
```

## Server Mode

To start the RPC server normally (for web and Emacs clients), omit the CLI flags:

```bash
crs                      # Run workflows once
crs -server              # Run workflows + RPC server
crs -oneoff              # Run workflows once only
crs -listen host:port    # Run workflows + RPC server for remote clients
```

`-listen host:port` (or `--listen`) serves the same RPCs over TCP to any number of remote clients, each registered as a user with a dashboard of their own, instead of to one client over stdio. It runs the workflows and their hooks like `-server`, never reads stdin, and cannot be combined with `-server` or `-oneoff`. See [Hosted Server](hosted_server.md).

See [Configuration](configuration.md) for more details on server modes.

## Logging

The server logs to stderr, which each client sends somewhere different: the bun web client to the terminal running it, Emacs to the `*crs-client-stderr*` buffer, the Chrome extension to `~/.crs/native_host.log`. Three environment variables give the log a form and a place of its own, for a script or an agent to read:

| Variable | Effect |
| --- | --- |
| `CRS_LOG_FILE` | Also append the log to this file (a leading `~/` is your home directory). A file over 64 MB is moved aside to `<file>.1` when the server next starts. Several servers — one per client — can share the file. |
| `CRS_LOG_FORMAT` | `text` (the default: `DATE::TIME - LEVEL - message key=value ...`) or `json`, one object per line with `time`, `level`, `msg` and the line's keys. |
| `CRS_LOG_LEVEL` | `debug`, `info` (the default), `warn` or `error`. |

```bash
CRS_LOG_FILE=~/.crs/crs.log CRS_LOG_FORMAT=json crs -server
```

Every request the server sends GitHub is logged at `info` as `GitHub API Call`, and every cycle ends with `GitHub API calls this cycle` and `GitHub rate limit post-cycle`. See [GitHub Webhooks](webhooks.md#measuring-what-it-saves) for what those report and how to compare polling with webhooks.
