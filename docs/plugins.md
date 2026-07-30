# Plugins

Plugins are external projects which are expected to be discoverable on your `$PATH`, and are called per PR.
You can install external plugins to process PR data asynchronously. Plugins receive data via CLI flags and their output is stored in the database.

![Plugin Web Interface](img/plugin_web.png)

This image shows the plugin output in the bun_client when reviewing a PR in this repository.

## Configuration

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

[[Plugins]]
Name = "Claude Review"
Command = "claude_review"
IncludeDiff = false
IncludeHeaders = false
IncludeComments = false
IncludeBranch = true   # Passes --branch flag (PR head branch name)

[[Plugins]]
Name = "Expensive Analysis"
Command = "expensive_analysis"
IncludeDiff = true
OnlyOnDemand = true    # This plugin only runs when explicitly requested
```

### Plugin Configuration Options

- `Name` (string, required): Display name for the plugin
- `Command` (string, required): Executable name on your `$PATH`
- `IncludeDiff` (bool, optional): Pass the PR diff via `--diff` flag
- `IncludeHeaders` (bool, optional): Pass PR metadata via `--headers` flag (includes `head_ref` among other fields)
- `IncludeComments` (bool, optional): Pass PR comments via `--comments` flag
- `IncludeBranch` (bool, optional): Pass the PR's head branch name via `--branch` flag
- `OnlyOnDemand` (bool, optional, default: false): If true, plugin only runs when explicitly requested via RerunPlugins

> **Note:** The branch name is also available in the `--headers` JSON as the `head_ref` field. Use `IncludeBranch` when you want the branch as a simple standalone argument without parsing the full metadata JSON.

## Included Plugins

- **Summarize Diff**: Uses Gemini 2.5 Flash to provide a terse bulleted summary of the changes in a PR.
- **Security Check**: Uses Gemini 2.5 Flash to analyze the diff for potential security risks, specifically looking for unprotected sensitive endpoints, hardcoded secrets, or missing security decorators (like `@authenticated`).
- **Style Guidelines**: Uses Gemini 2.5 Flash to evaluate a PR's diff against your personal style guide. Reads rules from `~/.config/style_guidelines.md` and reports violations, compliance highlights, and an overall assessment. Requires `GEMINI_API_KEY`. See [Style Guidelines Plugin](#style-guidelines-plugin) below.
- **Claude Review**: Runs `claude -p "review PR #<number> on repo <owner>/<repo>" --model sonnet` via the Claude CLI. Written in Zig. Build with `zig build` inside `cmd/claude_review/` and place the resulting binary on your `$PATH`.

Plugins are expected to accept flags like `--owner`, `--repo`, `--number`, and any of the optional content flags enabled above (`--diff`, `--headers`, `--comments`, `--branch`).

## Writing a Plugin

You can write a plugin in any language you like. The only requirement is that the binary must be discoverable on your `$PATH`.

The `example_plugin` included in this repository demonstrates the interface and potential options.

When your plugin runs, its standard output (stdout) is captured and stored in the database. Clients can then retrieve and display this output when you are reviewing a PR. For example, in the web client, plugin outputs appear in a dedicated "Plugins" section for each PR.

## Plugin Response Contract

A plugin's stdout can be plain text, but a plugin may instead emit a JSON document matching the response contract. This lets it declare how its body should be rendered and attach line-level annotations to the PR diff:

```json
{
  "body": {
    "body_type": "markdown",
    "body_content": "This is the response."
  },
  "annotations": [
    {"filename": "test.py", "line": 75, "severity": "warning", "content": "this line looks wrong"}
  ]
}
```

### `body` Object

| Field          | Type   | Description                                  |
|----------------|--------|----------------------------------------------|
| `body_type`    | string | Either `markdown` or `html` (case-insensitive) |
| `body_content` | string | The renderable output of the plugin          |

### `annotations` List (optional)

Each annotation anchors a remark to a line of a file in the PR:

| Field      | Type   | Description                                                  |
|------------|--------|--------------------------------------------------------------|
| `filename` | string | Path of the file within the repo (required)                  |
| `line`     | int    | 1-based line number the annotation applies to (required)     |
| `severity` | string | Free-form severity, e.g. `info`, `warning`, `error`          |
| `content`  | string | The annotation text                                          |

Annotations without a `filename` or a positive `line` are dropped, since they can't be anchored to the diff.

### Backwards Compatibility

Output that doesn't match the contract — plain text, invalid JSON, JSON without a `body` object, or an unknown `body_type` — is treated as legacy output and wrapped as:

```json
{"body": {"body_type": "markdown", "body_content": "<the raw output, verbatim>"}, "annotations": []}
```

so existing plugins keep working unchanged. The raw stdout is always stored and returned as-is in the `result` field of `GetPluginOutput`; parsing happens when results are served to clients.

Annotations from every successfully executed plugin for a PR are also aggregated into the `annotations` field of the `GetPR` response (each tagged with the plugin's name), so clients can render them into the diff.

### Rendering in the Web Client

The web client renders a `markdown` body as markdown and an `html` body inside a sandboxed frame that inherits the current theme. The frame withholds `allow-scripts`, so scripts and inline event handlers in a plugin's HTML never execute — plugin bodies are often LLM-generated text derived from a PR's diff and description, which is not trusted markup.

Annotations are listed beneath each plugin's body on both the plugin output page and the review view's plugin drawer, sorted by file and line. Rendering them inline in the diff is separate work.

## On-Demand Plugins

By default, all configured plugins automatically run when a PR is fetched or when its commit changes (once per SHA). However, some plugins can be expensive to run (e.g., those making API calls to third-party services like Gemini or Claude).

To avoid unnecessary costs, you can mark a plugin as `OnlyOnDemand = true` in the configuration. These plugins will:
- **Not run automatically** when a PR is fetched or updated
- Receive a `"deferred"` status in the database
- **Only execute when explicitly requested** via the RerunPlugins RPC method with their name in the plugin list

This allows cost control while keeping expensive plugins available for on-demand use.

## Rerunning Plugins

By default, plugins only run once per PR commit (SHA). To force plugins to rerun for a PR, use the `RerunPlugins` RPC method.

### RerunPlugins RPC Method

**Arguments:**
- `Owner` (string): GitHub repository owner
- `Repo` (string): GitHub repository name
- `Number` (int): Pull request number
- `Plugins` (array of strings, optional): Specific plugin names to rerun. If empty, omitted, or null, behavior depends on on-demand configuration (see below).

**Returns:**
- `Okay` (bool): Success status
- `Message` (string): Description of what was rerun
- `Output` (object): Empty object (plugins run asynchronously)

**Plugin Behavior:**
- **With specific plugin names**: Only those plugins are rerun, regardless of their `OnlyOnDemand` setting. This allows you to explicitly trigger expensive plugins.
- **With empty/omitted array**: Reruns all normal plugins (those with `OnlyOnDemand = false`). On-demand plugins are skipped unless explicitly named.

**Example: Rerun specific plugins (including an on-demand one):**
```json
{
  "Owner": "myorg",
  "Repo": "myrepo",
  "Number": 123,
  "Plugins": ["Summarize Diff", "Expensive Analysis"]
}
```

**Example: Rerun all normal plugins (skips on-demand plugins):**
```json
{
  "Owner": "myorg",
  "Repo": "myrepo",
  "Number": 123
}
```

The rerun bypasses the SHA cache check, allowing you to reprocess the same PR commit with potentially updated plugin logic or external dependencies.

## Style Guidelines Plugin

The `style_guidelines` plugin evaluates PR diffs against a Markdown file of your own style rules.

### Setup

1. **Install the binary:**
   ```sh
   go install ./cmd/style_guidelines/...
   ```

2. **Create your style guide** at `~/.config/style_guidelines.md`. Write your rules in plain Markdown — the entire file is used as the system prompt for Gemini. For example:
   ```markdown
   # Style Guidelines

   - Functions must have docstrings explaining their purpose.
   - Use snake_case for all variable names.
   - No magic numbers; use named constants instead.
   - Error messages must be lowercase and end without punctuation.
   ```

3. **Set your Gemini API key:**
   ```sh
   export GEMINI_API_KEY=your_key_here
   ```

4. **Add to `~/.config/codereviewserver.toml`:**
   ```toml
   [[Plugins]]
   Name = "Style Guidelines"
   Command = "style_guidelines"
   IncludeDiff = true
   IncludeHeaders = true
   ```

### Output

The plugin produces a brief report with:
- Specific violations (with file/line references where available)
- Areas of the diff that comply well with the guidelines
- An overall style compliance assessment
