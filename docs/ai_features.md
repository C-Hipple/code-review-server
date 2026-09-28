# AI Features

AI features are units of AI work the server runs on a pull request and serves
to every client. The first, **comments-addressed**, answers *"are all the
review comments addressed, and what is still outstanding?"*

They sit beside the two older AI paths rather than on top of them.
[Plugins](plugins.md) remain separate binaries with their own config, table and
RPCs — the two share only the code that runs a subprocess — and the
[experimental LLM diff analysis](configuration.md#experimental-llm-features)
keeps its own flags.

**Everything is off by default.** Nothing runs, and nothing changes for
existing users, until a config file enables a feature.

## Enabling a feature

```toml
[AI]
# Defaults every feature inherits. See Providers below for the two choices
# and the order in which these settings pick one.
DefaultProvider = "command"   # run a program on this machine ("gemini": call the Gemini API)
DefaultCommand = "claude -p"  # the program the command provider runs

[[AIFeatures]]
ID = "comments-addressed"
Enabled = true
Automatic = false   # also run after a PR is fetched or updated
Mode = "oneshot"    # or "agent"
# Provider = "gemini"          # this feature only; beats both [AI] settings
# Command = "llm -m my-model"  # this feature only; picks the command provider
```

| Field       | Default                                  | Meaning                                                                                  |
|-------------|------------------------------------------|------------------------------------------------------------------------------------------|
| `ID`        | required                                 | The feature to configure. Registered today: `comments-addressed`                        |
| `Enabled`   | `false`                                  | Switches the feature on. Without it the feature is listed but can't run                 |
| `Automatic` | `false`                                  | Also run it after a PR is fetched or updated, the way plugins run. Needs `Enabled`       |
| `Mode`      | the feature's default                    | `oneshot` (one model call) or `agent` (a multi-turn tool loop); see [Modes](#modes)      |
| `Provider`  | [picked by order](#which-provider-runs-a-feature) | `gemini` or `command`; see [Providers](#providers)                             |
| `Command`   | `AI.DefaultCommand`                      | The command line the `command` provider runs. Setting it also picks that provider unless `Provider` says otherwise |

Two entries with the same `ID` stop the config from loading, as duplicate
plugin names do. Everything else — an unknown feature, provider or mode, a
mode the feature doesn't support, an enabled feature that would use the
command provider with no command, a command line that can't be split — is
logged at startup and rejected by `UpdateConfig`.

## Providers

A feature reaches a model through a provider. There are two, and neither is
privileged:

- **`gemini` — a hosted API.** The server itself calls Google's Gemini API
  (`gemini-flash-latest`) over HTTPS with the key in `GEMINI_API_KEY`, through
  the same client the experimental LLM features use.
- **`command` — a program on this machine.** For each run the server starts
  the command line you name — `claude -p`, `llm -m <model>`, a wrapper
  script — and treats it as the model. The contract is the simplest one any CLI
  agent can meet: **the prompt arrives on stdin, the answer is whatever the
  command prints on stdout**, and a non-zero exit is a failure. Where the model
  itself runs is up to that program: `claude -p` calls Claude with its own
  credentials, and another CLI may run a model locally. The server needs no
  `GEMINI_API_KEY` for it; any credentials are the program's own. The command
  line is split into words the way a shell would (quotes
  work; pipes, globs and variables don't, since nothing runs it through a
  shell) and runs with the server's environment and working directory. No
  default command is pinned.

### Which provider runs a feature

Each feature works its provider out on its own. The first of these that is
set wins:

| Rule | Setting                                   | Picks                      |
|------|-------------------------------------------|----------------------------|
| 1    | the feature's `Provider`                  | what it names              |
| 2    | the feature's `Command`                   | `command`                  |
| 3    | `[AI]` `DefaultProvider`                  | what it names              |
| 4    | `[AI]` `DefaultCommand`                   | `command`                  |
| 5    | none of the above                         | `gemini`                   |

In other words, a feature's own `[[AIFeatures]]` settings beat the `[AI]`
defaults, and at each level a named `Provider` beats the one a command
implies. The `command` provider runs the feature's `Command`, else
`AI.DefaultCommand`; an enabled feature that lands on `command` with neither is
a config error.

**`GEMINI_API_KEY` never picks the provider.** The server reads it only once a
feature has landed on `gemini`. Exporting it doesn't move a feature off a
command, and a feature that lands on `gemini` without it runs without a model
(see below). The usual setups:

```toml
# 1. Everything on the Gemini API: export GEMINI_API_KEY and set no command.
#    Rule 5 then picks gemini; DefaultProvider = "gemini" says so explicitly,
#    and keeps gemini even if a DefaultCommand is added later (rule 3 beats 4).
[AI]
DefaultProvider = "gemini"
```

```toml
# 2. Everything on a program on this machine: a DefaultCommand is enough
#    (rule 4). GEMINI_API_KEY is ignored, set or not.
[AI]
DefaultCommand = "claude -p"
```

```toml
# 3. A program by default, with one feature on the Gemini API.
[AI]
DefaultCommand = "claude -p"

[[AIFeatures]]
ID = "comments-addressed"
Enabled = true
Provider = "gemini"   # rule 1 beats the DefaultCommand at rule 4
```

To check what a config resolves to, the
[`ListAIFeatures`](protocol.md#rpchandlerlistaifeatures) reply carries each
feature's `provider`.

> **Treat the prompt as untrusted input.** It carries text other people wrote —
> the PR title, review and conversation comments, the diff — and anyone who can
> comment on a PR can try to steer the model. With the `command` provider, run
> the CLI without write, shell or network tools enabled, and check how it treats
> tool permissions when nobody is there to approve them. The server's own
> `read_file` tool, in agent mode, only reads repository files as of the PR head
> and rejects paths outside the repository.

A provider that can't be built — no `GEMINI_API_KEY`, say — doesn't fail a
feature that can manage without one: comments-addressed still produces its
deterministic report, leaves the items it needed the model for unclear, and
says why.

## Modes

- **`oneshot`** answers from a single model call.
- **`agent`** runs a multi-turn loop in which the model may call tools the
  feature offers, see their results and call more before answering. It works
  on either provider: tool calls go through a plain-text protocol (the model
  replies with a `TOOL_CALL {"name": ..., "arguments": {...}}` line), so even
  a CLI that knows nothing of the server's tools can use them. A run gets at
  most six turns. comments-addressed offers one tool, `read_file`, which reads
  a file as of the PR head from the local clone (or GitHub).

## Running

- **On demand.** A client asks with `RunAIFeature`; the run happens in the
  background and the client polls `GetAIOutput` while it reads `pending`.
  Opening a report in either client asks for a run when the feature has never
  run for the PR or its report is [stale](#caching-and-staleness).
- **Automatically**, for features with `Automatic = true`: after a workflow
  cycle adds a PR or sees a new push, and when a client opens a PR — the same
  post-update hook that runs plugins.

At most two automatic runs execute at once, across every feature; a run a
client asked for never queues behind them. Each run has five minutes. Only one
run per PR and feature is ever in flight, and a run that is still queued reads
as `pending`. An automatic run doesn't retry a failure for inputs it already
failed on; asking for the feature does.

## Caching and staleness

Each result is stored in the `AIResults` table, keyed by two things:

- the PR's **head SHA** — the code — and
- the **inputs digest** — a hash of every comment's ID and body, every
  review's state, and each review thread's resolved and outdated flags, read
  from the `PRComments`, `PRReviews` and `PRReviewThreads` caches.

A stored result answers a run request only when both match. `GetAIOutput`
reports the key a result covers (`covers_sha`, `covers_digest`) beside the
PR's current one (`current_sha`, `current_digest`) and sets `stale` when they
differ — so resolving a thread, a new or edited comment, a new review or
dismissal, and a push each make a report stale. Local (unsubmitted) comments
and reactions are not inputs.

## comments-addressed

### How it decides

Deterministically first: GitHub's own thread state decides whatever it can,
and the model only fills the gaps it leaves.

| Situation                                                                 | Status          | Decided by |
|---------------------------------------------------------------------------|-----------------|------------|
| A review thread resolved on GitHub                                        | addressed       | GitHub     |
| An unresolved thread someone replied to after the latest commit           | outstanding     | GitHub     |
| An unresolved thread nobody replied to since the latest commit (did those commits fix it?), or whose commits aren't known | the model's verdict | model |
| A thread GitHub reported no state for                                     | unclear         | GitHub     |
| A conversation comment — not the PR author's, not a bot's — which GitHub tracks no resolution for | the model's verdict | model |

The model sees the unclear items, the whole human conversation, and the diff.
It answers `addressed`, `outstanding` or `unclear` per item, with a one-line
rationale.

The design goal is never to report a confidently wrong "all addressed":

- **A resolved thread always stays addressed.** The model is never asked about
  one, and if it volunteers a verdict anyway, that is kept as a note only.
- **Missing thread state stays unclear.** The model isn't asked to guess at
  state only GitHub has.
- **A verdict on cut input is discarded.** If the thread's file, a comment's
  text, or (for a conversation comment) any part of the diff or conversation
  didn't fit the prompt, the item goes back to unclear. In agent mode, a file
  the model read whole with `read_file` counts as seen.
- **Missing inputs make the run `insufficient-input`**, not a clean report: no
  comments cached for the PR, or review threads whose resolution state is
  unavailable. The model isn't consulted for a run that can't reach a verdict.
- **An active change request keeps the verdict at outstanding.** A reviewer
  whose latest approve / request-changes / dismiss review still requests
  changes is listed, even when every thread is resolved.

"The latest commit" is the newest commit's author date. A rebased or amended
commit can carry an older author date than its push; that errs toward calling a
thread outstanding, never toward calling it addressed.

The prompt is bounded: at most 40 items go to the model (the rest stay unclear
and the report is marked truncated), each comment is clipped at 2,000
characters, the conversation at 40 KB, and the diff at 150 KB, filled with the
files the unclear threads are on first. A file that doesn't fit whole doesn't
count as seen.

### The report

The result's `body` is a complete markdown report any client can render. Its
`report` is the typed version:

| Field             | Meaning                                                                                        |
|-------------------|------------------------------------------------------------------------------------------------|
| `verdict`         | `all-addressed`, `outstanding`, `unclear`, `no-comments` or `insufficient-input`               |
| `summary`         | The verdict in one sentence                                                                    |
| `counts`          | `total`, `addressed`, `outstanding`, `unclear`, and `by_model` (items the model decided)       |
| `items`           | One per thread or judged conversation comment; see below                                       |
| `change_requests` | Reviewers whose latest review still requests changes: `reviewer`, `review_id`, `submitted_at`, `excerpt`, `html_url` |
| `model`           | Whether and how the model was consulted: `consulted`, `provider`, `model`, `mode`, `asked`, agent `turns` / `tool_calls`, and a `note` when it was needed but not usefully consulted |
| `truncated`       | Something was cut to fit the prompt                                                            |
| `missing`         | What was unavailable, when the verdict is `insufficient-input`                                 |

Each item carries `root_comment_id` (the comment that opened the thread, or
the conversation comment; it matches `Comment.id` in the PR payload), a
nullable `thread_id`, `kind` (`thread` or `conversation`), `status`
(`addressed`, `outstanding`, `unclear`), `source` (`github` or `model`),
`rationale`, an optional `model_note`, and display fields: `author`,
`excerpt`, `path`, `line`, `outdated`, `resolved`, `resolved_by`, `replies`,
`last_author`, `last_activity`, `html_url`.

`outstanding` lists every item still needing attention — outstanding first,
then unclear — whether or not it can be anchored to a line, and `annotations`
marks the open threads that can (a warning for outstanding, info for unclear).

## Clients

- **Web.** The review toolbar shows a button per enabled feature, with the
  number of items needing attention once a report exists. It opens the report:
  verdict, what needs attention with who decided each item, change requests,
  and the addressed items collapsed. An item's location jumps to its thread in
  the diff (or the outdated-comments panel); **↻ Re-run** forces a fresh run.
  The review list offers the same without opening the review: each PR's **✦ AI**
  button, beside its Plugins button, opens a page with every enabled feature's
  report for that PR.
- **Emacs.** `C` in a review buffer (`crs-get-ai-output`) opens the report of
  an enabled feature in its own buffer, which polls while a run is pending. In
  that buffer, `r` refreshes, `R` re-runs and `q` quits.

Both run the feature on open when it never ran or went stale.

## Monitoring

Every run appends an `=== AI Feature Run ===` entry to `~/.crs/llm_calls.log`
(respects `CRS_HOME`), whether or not it needed the model: the PR, trigger,
feature and mode, the provider and model (or why none could be built), what
the feature worked from, how many model calls it made, and the outcome —
`SUCCESS`, `PARTIAL` (the model was unavailable or its answer unreadable;
the raw response is included), `INSUFFICIENT-INPUT` with what was missing, or
`FAILURE` at a stage: `input` (the PR's inputs couldn't be assembled),
`feature`, `client-init`, `request`, `http-status`, `decode`,
`empty-response`, `parse`, `exit-status` (the command exited non-zero) or
`timeout`.

## Rollout and rollback

The AI layer ships switched off: `[AI]` and `[[AIFeatures]]` are absent from
the built-in defaults, each feature needs `Enabled = true`, and automatic runs
also need `Automatic = true`.

The `AIResults` table is created with `CREATE TABLE IF NOT EXISTS` and touches
no existing table, so an older binary simply ignores it. Rolling back is
removing the `[[AIFeatures]]` entries, or their `Enabled` flags.

## Adding a feature

A feature is a Go type registered in `ai/registry.go` implementing `ai.Feature`:

```go
type Feature interface {
    ID() string
    Name() string
    Run(ctx context.Context, req ai.Request) (ai.Result, error)
}
```

`Describer`, `ModeSupporter` (to run in `agent` mode too) and
`TimeoutProvider` are optional. The `Request` carries the PR's diff, raw
comments JSON, metadata, review threads, the server's partition of the
discussion, and the two seams: `req.Model.Generate` for one-shot calls and, in
agent mode, `req.Agent.Run` with the tools the feature offers. The `Result` is
the plugin response contract (a body and annotations) plus an optional typed
`Report`, an `Outstanding` list and a `Log` line for the call log. Caching,
the in-flight guard, the automatic-run cap, storage and the RPCs come with the
registration; a client renders any feature's markdown body without changes.

## Decisions on the design's open questions

- **Annotation source.** `PRAnnotation.source` sits beside `plugin` rather
  than replacing it. The PR payload's `annotations` stay plugin-only, so a
  client that predates `source` never shows AI output as a plugin's; AI
  annotations arrive through `GetAIOutput` with `source: "ai"` and `feature`.
- **CLI contract.** Prompt on stdin, answer on stdout, non-zero exit is a
  failure. No default command is pinned.
- **Digest composition.** Comment IDs and bodies, review IDs and states, and
  thread resolved/outdated flags. Review states are in scope for the first
  feature: an active change request is part of its verdict.
- **`GetPR` payload.** It doesn't carry AI reports; clients call
  `GetAIOutput`, which also serves them the staleness information.
- **Automatic-run cap.** Two concurrent automatic runs, shared by every
  feature. It isn't configurable yet.
