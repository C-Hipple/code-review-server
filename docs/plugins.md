# Plugins

Plugins are external projects which are expected to be discoverable on your `$PATH`, and are called per PR.
You can install external plugins to process PR data asynchronously. Plugins receive data via CLI flags and their output is stored in the database.

For full plugin development, check the plugin example_plugin contained in this repo to understand the interface of building your own plugin. You can do it in any language you'd like.

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
```

## Included Plugins

- **Summarize Diff**: Uses Gemini 2.5 Flash to provide a terse bulleted summary of the changes in a PR.
- **Security Check**: Uses Gemini 2.5 Flash to analyze the diff for potential security risks, specifically looking for unprotected sensitive endpoints, hardcoded secrets, or missing security decorators (like `@authenticated`).

Plugins are expected to accept flags like `--owner`, `--repo`, `--number`, and any of the optional content flags enabled above.
