# Claude Review Plugin

A plugin for code-review-server that launches the Claude CLI to review PRs. It accepts PR information (owner, repo, number) and invokes `claude` with a review prompt.

## Building and Installation

Build the Zig binary:
```bash
cd cmd/claude_review
zig build-exe -O ReleaseSafe -femit-bin=$HOME/.local/bin/claude_review main.zig
```

The binary will be installed to `~/.local/bin/claude_review`.

## Configuration

Add the plugin to your `~/.config/codereviewserver.toml` file under the `[[Plugins]]` section:

```toml
[[Plugins]]
Name = "claude_review"
Command = "claude_review"
IncludeDiff = true
IncludeHeaders = true
IncludeComments = true
```

### Configuration Options

- **Name**: Identifier for the plugin (must be unique)
- **Command**: Path to the `claude_review` binary (or just the binary name if it's on `$PATH`)
- **IncludeDiff**: Whether to pass the PR diff to the plugin
- **IncludeHeaders**: Whether to pass PR metadata/headers to the plugin
- **IncludeComments**: Whether to pass PR comments to the plugin

The plugin will be executed with command-line arguments:
```
claude_review --owner <owner> --repo <repo> --number <number> [--diff] [--headers] [--comments]
```
