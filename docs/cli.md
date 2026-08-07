# Command-Line Interface

The code-review-server binary can be used directly as a CLI tool to query the database without starting the RPC server or running background workflows. This is useful for quick lookups, scripting, and integration with shell tools.

## Basic Usage

All CLI flags exit immediately after printing results. Configuration is loaded from `~/.config/codereviewserver.toml` and the SQLite database from `~/.crs/codereviewserver.db`.

```bash
# List all PRs
codereviewserver -list-prs

# List all sections with item counts
codereviewserver -list-sections

# Get details for a specific PR
codereviewserver -pr owner/repo/123

# Print the built-in default configuration
codereviewserver -print-default-config
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

**Example:**
```bash
$ codereviewserver -list-prs
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

**Example:**
```bash
$ codereviewserver -list-sections
SECTION              PRIORITY  ITEMS
my-section           1         5
another-section      2         3
```

### `-pr <owner/repo/number>`

Fetches and displays cached details for a specific PR. Uses the same code path as the RPC handler: cached data is preferred, but will query GitHub API if the cache is cold.

**Format:** `-pr owner/repo/number`

**Output:** Full PR details including metadata, diff, comments, reviews, and local feedback.

**Example:**
```bash
$ codereviewserver -pr myorg/myrepo/123
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
codereviewserver -print-default-config > ~/.config/codereviewserver.toml
```

Logs go to stderr, so the redirect above captures only TOML.

## Use Cases

### Shell Scripting

Get the count of open PRs:
```bash
codereviewserver -list-prs | wc -l
```

Find PRs in a specific section:
```bash
codereviewserver -list-prs | grep "my-section"
```

Export to CSV:
```bash
codereviewserver -list-prs | tr '\t' ',' > prs.csv
```

### Integration with Other Tools

Get PR details for processing:
```bash
codereviewserver -pr myorg/myrepo/123 | grep "Approved-By" | cut -d: -f2
```

Check section status:
```bash
codereviewserver -list-sections | awk '{sum+=$3} END {print "Total PRs:", sum}'
```

## Server Mode

To start the RPC server normally (for web and Emacs clients), omit the CLI flags:

```bash
codereviewserver                    # Run workflows once
codereviewserver -server            # Run workflows + RPC server
codereviewserver -oneoff            # Run workflows once only
```

See [Configuration](configuration.md) for more details on server modes.
