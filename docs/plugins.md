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
```

## Included Plugins

- **Summarize Diff**: Uses Gemini 2.5 Flash to provide a terse bulleted summary of the changes in a PR.
- **Security Check**: Uses Gemini 2.5 Flash to analyze the diff for potential security risks, specifically looking for unprotected sensitive endpoints, hardcoded secrets, or missing security decorators (like `@authenticated`).
- **Claude Review**: Runs `claude -p "review PR #<number> on repo <owner>/<repo>" --model sonnet` via the Claude CLI. Written in Zig. Build with `zig build` inside `cmd/claude_review/` and place the resulting binary on your `$PATH`.

Plugins are expected to accept flags like `--owner`, `--repo`, `--number`, and any of the optional content flags enabled above.

## Writing a Plugin

You can write a plugin in any language you like. The only requirement is that the binary must be discoverable on your `$PATH`.

The `example_plugin` included in this repository demonstrates the interface and potential options.

When your plugin runs, its standard output (stdout) is captured and stored in the database. Clients can then retrieve and display this output when you are reviewing a PR. For example, in the web client, plugin outputs appear in a dedicated "Plugins" section for each PR.

## Rerunning Plugins

By default, plugins only run once per PR commit (SHA). To force plugins to rerun for a PR, use the `RerunPlugins` RPC method.

### RerunPlugins RPC Method

**Arguments:**
- `Owner` (string): GitHub repository owner
- `Repo` (string): GitHub repository name
- `Number` (int): Pull request number
- `Plugins` (array of strings, optional): Specific plugin names to rerun. If empty or omitted, all plugins rerun.

**Returns:**
- `Okay` (bool): Success status
- `Message` (string): Description of what was rerun
- `Output` (object): Empty object (plugins run asynchronously)

**Example:**
```json
{
  "Owner": "myorg",
  "Repo": "myrepo",
  "Number": 123,
  "Plugins": ["Summarize Diff", "Security Check"]
}
```

To rerun all plugins for a PR, omit the `Plugins` array:
```json
{
  "Owner": "myorg",
  "Repo": "myrepo",
  "Number": 123
}
```

The rerun bypasses the SHA cache check, allowing you to reprocess the same PR commit with potentially updated plugin logic or external dependencies.
