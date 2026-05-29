# Claude Review Plugin

A plugin for code-review-server that launches the Claude CLI to review PRs.
It feeds the agent the diff, headers, existing review comments, and any
prior automated review for the same PR; enables tool use so the agent can
investigate beyond the diff (git history, callers, tests, linters); and
asks for a strict structured output.

## Building and Installation

```bash
go install crs/cmd/claude_review@latest
# or from the repo root:
go install ./cmd/claude_review
```

The binary is installed to `$GOPATH/bin/claude_review` (or `$(go env GOPATH)/bin`).

## Configuration

In `~/.config/codereviewserver.toml`:

```toml
[[Plugins]]
Name = "claude_review"
Command = "claude_review"
IncludeDiff = true
IncludeHeaders = true
IncludeComments = true
```

### CLI flags

```
claude_review --owner <owner> --repo <repo> --number <number>
              [--diff <unified-diff>]
              [--headers <pr-metadata-json>]
              [--comments <comments-json>]
              [--post-review]
```

- `--diff`, `--headers`, `--comments` — when the server is configured with
  the corresponding `Include*` options, the values are injected into the
  prompt so the agent doesn't have to re-fetch them.
- `--post-review` — after producing the review, post it to GitHub as a
  comment-style PR review via `gh pr review`. Requires `gh` on `$PATH`
  with `repo` scope.

### Environment variables

- `CRS_CLAUDE_REVIEW_MODEL` — model passed to `claude --model`. Default: `sonnet`.
  Set to `opus` for deeper reviews.
- `CRS_CLAUDE_REVIEW_TIMEOUT_SEC` — soft timeout in seconds before the
  plugin kills the `claude` subprocess and returns whatever has been
  captured so far. Default: `270` (just under the server's 5-minute
  plugin timeout).
- `CRS_HOME` / `HOME` — used to locate the per-PR cache directory.

### Caching

After each run the plugin writes its output to
`${CRS_HOME:-$HOME/.cache/crs}/claude_review/<owner>__<repo>__<number>.md`.
On the next run for the same PR, that file is included in the prompt so
the agent can skip already-reported findings that have been fixed.

### Trivial-diff fast path

If the diff only touches Markdown, lockfiles, `*.pb.go`, `*.gen.go`,
`*_generated.go`, `CHANGELOG`, or `LICENSE`, the plugin returns a
canned "no review needed" response without calling the model.

### Output format

The agent is instructed to produce, in order:

```
## Summary
…

## Blockers
…

## Suggestions
…

## Nits
…

## Findings JSON
```json
[{"file":"path","line":42,"severity":"blocker|major|nit","message":"…"}]
```
```

The plugin streams this to stdout (so the server captures it as the
plugin result) and mirrors errors and tool-use telemetry to stderr (so
they end up in `slog` warnings rather than the stored review).
