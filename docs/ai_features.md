# AI Features

AI features are units of AI work the server runs on a pull request and serves
to every client. Five are registered. Three produce a report a client opens:

- **comments-addressed** answers *"are all the review comments addressed, and
  what is still outstanding?"*
- **feature-flags** answers *"is every change in this PR behind a feature
  flag?"* — how safe the PR is to approve, if its flags keep what it changes
  switched off.
- **[change-diagram](#change-diagram)** draws what the PR changes as a Mermaid
  diagram, served as raw Mermaid for each client to render.

The other two are *applied*: the server applies what they produce to what it
already shows, so there is no report to open.

- **[file-ordering](#file-ordering)** orders the files of a PR's diff so the
  PR reads top to bottom.
- **[review-ease](#review-ease)** rates how easy the PR is to review: `easy`,
  `medium` or `hard`.

They sit beside [plugins](plugins.md) rather than on top of them: plugins
remain separate binaries with their own config, table and RPCs, and the two
share only the code that runs a subprocess and the OpenRouter client.

**Everything is off by default.** Nothing runs, and nothing changes for
existing users, until a config file enables a feature.

## Enabling a feature

```toml
[AI]
# Defaults every feature inherits. See Providers below for the three choices
# and the order in which these settings pick one.
DefaultProvider = "command"   # run a program on this machine ("gemini" / "openrouter": call an API)
DefaultCommand = "claude -p"  # the program the command provider runs
# DefaultModel = "anthropic/claude-sonnet-4.5"  # the model the openrouter provider asks for

[[AIFeatures]]
ID = "comments-addressed"
Enabled = true
Automatic = false   # also run after a PR is fetched or updated
Mode = "oneshot"    # or "agent"
# Provider = "gemini"          # this feature only; beats both [AI] settings
# Command = "llm -m my-model"  # this feature only; picks the command provider
# Model = "openai/gpt-5"       # this feature only; the openrouter provider's model
```

| Field       | Default                                  | Meaning                                                                                  |
|-------------|------------------------------------------|------------------------------------------------------------------------------------------|
| `ID`        | required                                 | The feature to configure. Registered today: `comments-addressed`, `feature-flags`, `change-diagram`, `file-ordering`, `review-ease` |
| `Enabled`   | `false`                                  | Switches the feature on. Without it the feature is listed but can't run                 |
| `Automatic` | `false`                                  | Also run it after a PR is fetched or updated, the way plugins run. Needs `Enabled`       |
| `Mode`      | the feature's default                    | `oneshot` (one model call) or `agent` (a multi-turn tool loop); see [Modes](#modes)      |
| `Provider`  | [picked by order](#which-provider-runs-a-feature) | `gemini`, `openrouter` or `command`; see [Providers](#providers)               |
| `Command`   | `AI.DefaultCommand`                      | The command line the `command` provider runs. Setting it also picks that provider unless `Provider` says otherwise |
| `Model`     | `AI.DefaultModel`                        | The model the `openrouter` provider asks for, as OpenRouter names it (`anthropic/claude-sonnet-4.5`). It picks no provider, and the others ignore it |

Two entries with the same `ID` stop the config from loading, as duplicate
plugin names do. Everything else — an unknown feature, provider or mode, a
mode the feature doesn't support, an enabled feature that would use the
command provider with no command, a command line that can't be split — is
logged at startup and rejected by `UpdateConfig`; so is an enabled feature
that would use the openrouter provider with no model.

## Providers

A feature reaches a model through a provider. There are three, and none is
privileged:

- **`gemini` — a hosted API.** The server itself calls Google's Gemini API
  (`gemini-flash-latest`) over HTTPS with the key in `GEMINI_API_KEY`.
- **`openrouter` — any model OpenRouter serves.** The server itself calls
  [OpenRouter](https://openrouter.ai)'s chat completions API over HTTPS with
  the key in `OPENROUTER_API_KEY`, asking for the model the feature's `Model`
  (else `AI.DefaultModel`) names — Anthropic's, OpenAI's, Google's, Meta's and
  many more behind one key and one bill, named the way OpenRouter's model list
  names them (`anthropic/claude-sonnet-4.5`, `openai/gpt-5`,
  `google/gemini-2.5-flash`). No default model is pinned, since which model to
  pay for is yours to choose; an enabled feature that lands on `openrouter`
  without one is a config error. The run's deadline cancels the request
  itself, and requests carry the project's name as OpenRouter's app
  attribution (`X-Title: code-review-server`), never anything about you. As
  with `gemini`, the prompt — the PR's code and discussion — leaves this
  machine: it goes to OpenRouter and on to the vendor serving the model, and
  your OpenRouter account's privacy settings decide which vendors it may go
  to.
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
a config error. Likewise the `openrouter` provider asks for the feature's
`Model`, else `AI.DefaultModel`, and needs one of them. A `Model` never picks
a provider, so `openrouter` is always named — and only it reads `Model`: a
feature on `gemini` stays on `gemini-flash-latest` whatever `AI.DefaultModel`
says.

**An API key never picks the provider.** The server reads `GEMINI_API_KEY`
only once a feature has landed on `gemini`, and `OPENROUTER_API_KEY` only once
one has landed on `openrouter`. Exporting either doesn't move a feature off a
command, and a feature that lands on a provider without its key runs without a
model (see below). The usual setups:

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

```toml
# 4. Everything through OpenRouter: export OPENROUTER_API_KEY. One feature
#    asks for a cheaper model than the rest.
[AI]
DefaultProvider = "openrouter"                 # rule 3
DefaultModel = "anthropic/claude-sonnet-4.5"

[[AIFeatures]]
ID = "comments-addressed"
Enabled = true

[[AIFeatures]]
ID = "feature-flags"
Enabled = true
Automatic = true
Model = "google/gemini-2.5-flash"              # beats DefaultModel
```

To check what a config resolves to, the
[`ListAIFeatures`](protocol.md#rpchandlerlistaifeatures) reply carries each
feature's `provider`; a report's `model` says which provider and model
actually answered.

> **Treat the prompt as untrusted input.** It carries text other people wrote —
> the PR title, review and conversation comments, the diff — and anyone who can
> comment on a PR, or push to it, can try to steer the model. With the `command`
> provider, run the CLI without write, shell or network tools enabled, and check
> how it treats tool permissions when nobody is there to approve them. The
> server's own tools, in agent mode, only read: `read_file` reads repository
> files as of the PR head and rejects paths outside the repository, and
> `search_code` runs a fixed-string `git grep` over the local clone at the PR
> head, with the query passed as a pattern, never as an option.

A provider that can't be built — no `GEMINI_API_KEY` or `OPENROUTER_API_KEY`,
say — doesn't fail a
feature that can manage without one: comments-addressed still produces its
deterministic report, and feature-flags still settles the files its path rules
decide; each leaves what it needed the model for unclear, and says why.
change-diagram, file-ordering and review-ease need the model: without one their
run fails at `client-init`, so there is no diagram, the diff keeps its default
order and the list gets no new rating.

## Modes

- **`oneshot`** answers from a single model call.
- **`agent`** runs a multi-turn loop in which the model may call tools the
  feature offers, see their results and call more before answering. It works
  on every provider: tool calls go through a plain-text protocol (the model
  replies with a `TOOL_CALL {"name": ..., "arguments": {...}}` line), so even
  a CLI that knows nothing of the server's tools can use them. A run gets at
  most six turns. comments-addressed offers one tool, `read_file`, which reads
  a file as of the PR head from the local clone (or GitHub); feature-flags adds
  [`search_code`](#agent-mode-and-search_code).

change-diagram, file-ordering and review-ease run `oneshot` only.

## Running

- **On demand.** A client asks with `RunAIFeature`; the run happens in the
  background and the client polls `GetAIOutput` while it reads `pending`.
  Opening a report in either client asks for a run when the feature has never
  run for the PR or its report is [stale](#caching-and-staleness).
- **Automatically**, for features with `Automatic = true`: after a workflow
  cycle adds a PR or sees a new push, and when a client opens a PR — the same
  post-update hook that runs plugins.
- **When a client opens a PR**, for the applied features. No client asks for
  one, so the server does on its behalf: opening a PR (`GetPR`, and the other
  RPCs that serve one) asks for each enabled applied feature whose stored
  result doesn't cover the PR's head, the way opening a report does. Nothing
  waits for it — the review shows the default file order until the next time
  it is rendered.

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

A feature that reads only the code — feature-flags, change-diagram,
file-ordering and review-ease — is keyed by the head SHA alone: both of its digests read
`code-only`, so only a push makes its report stale, and a new comment neither
makes it stale nor costs a model call.

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
`last_author`, `last_activity`, `html_url`, and `code_context`.

`excerpt` is the comment's first 200 characters as one line of plain text:
HTML comments and inline SVGs are dropped, an image becomes its alt text in
brackets (the `P1` badge a review bot puts in front of its comments reads
`[P1]`), other HTML tags are removed and entities decoded. Markdown, and
anything inside backticks, is left as written. The model's prompt gets comment
bodies cleaned the same way (with their line breaks), so a bot's badge markup
doesn't use up a comment's 2,000 characters.

`code_context` is set on threads that are not addressed: the last 8 lines of
GitHub's `diff_hunk` for the thread's first comment, which ends at the
commented line. The `@@` header is kept only when the whole hunk fits. The
markdown body shows it as a `diff` block under the item.

`outstanding` lists every item still needing attention — outstanding first,
then unclear — whether or not it can be anchored to a line, and `annotations`
marks the open threads that can (a warning for outstanding, info for unclear).

## feature-flags

Answers *"is every change in this PR behind a feature flag?"* A flag here is
anything that decides at run time whether code runs: a feature flag; a
django-waffle flag, switch or sample; a LaunchDarkly, Unleash, Flipper,
OpenFeature, GrowthBook or Statsig check; a settings or environment toggle; or
an in-house helper of the same shape.

```toml
[[AIFeatures]]
ID = "feature-flags"
Enabled = true
Automatic = true    # judge each PR as it arrives, so the answer is waiting
Mode = "agent"      # optional: lets the model look up a changed function's callers
```

### How it decides

The unit is a **change**: one hunk of the diff, or a whole file a path rule
decides. Path rules come first; the model judges the rest.

| Change                                                                    | Status              | Decided by |
|---------------------------------------------------------------------------|---------------------|------------|
| A test file: `*_test.go`, `test_*.py`, `*_test.py`, `conftest.py`, `*.test.ts`, `*.spec.js` and the like, `*_spec.rb`, or anything under a `test`, `tests`, `__tests__`, `testdata` or `e2e` directory | no-effect | path rule |
| Documentation: a `.md`, `.markdown`, `.rst` or `.adoc` file at the repository root, under a `docs` or `doc` directory, or named `README`, `CHANGELOG`, `CONTRIBUTING` and the like | no-effect | path rule |
| A dependency lockfile: `package-lock.json`, `yarn.lock`, `go.sum`, `poetry.lock`, `Cargo.lock` and the like — it changes what every build installs | ungated | path rule |
| Every other hunk, and a file changed with no text diff (a binary file, a rename or a mode change) | the model's verdict | model |

The path rules are deliberately narrow: a file they miss only costs a model
judgment, while a runtime file taken for a test would be reported as having no
effect. Markdown anywhere else goes to the model, since it may be a template or
a prompt read at run time, and a name like `ABTest.java` is not taken for a
test.

The model answers one question per change: *if this change were wrong, could
it affect anyone while its flags are off, at their defaults?*

- **`gated`** — no: it only runs while a flag is on. Code reachable only from
  gated code, such as a new function whose only callers are behind the flag,
  counts.
- **`ungated`** — yes. That includes refactors however safe they look, changes
  to what runs while the flag is off, removing a flag check, new routes, jobs,
  migrations and schema changes, dependency and configuration changes, and a
  flag the diff turns on by default.
- **`no-effect`** — it can't change behaviour: comments, formatting, dead code.
- **`unclear`** — it can't tell; for example, whether a flag check encloses the
  change is outside the diff shown.

The design goal is never to report a confidently wrong "all gated":

- **A gated verdict must name its flag, and the flag must be in the code the
  model saw** — the diff in its prompt, the tests shown as context, or what its
  tools returned in agent mode. Case and separators don't matter
  (`NEW_CHECKOUT` matches `"new-checkout"`). A verdict that names no flag, or a
  flag the code never mentions, goes back to unclear. This catches an invented
  flag; it can't prove the flag it found really encloses the change.
- **A cut prompt keeps only ungated verdicts.** If any change the model had to
  judge didn't fit its prompt, its gated and no-effect verdicts are discarded:
  whether code is reachable only from behind a flag can hinge on the part it
  didn't see. Ungated verdicts stand, since they only ever ask the reviewer to
  look.
- **No verdict, no status.** A change the model gave no verdict for — and every
  change, when the model can't be reached — stays unclear.
- **No diff makes the run `insufficient-input`**, not a clean report.

The verdict is `ungated` when any change runs without a flag, `unclear` when
none is known to but some are unclear, `all-gated` when every change that
affects behaviour is behind a flag, and `no-runtime-changes` when none affects
behaviour (a tests-and-docs PR, say).

The prompt is bounded: at most 100 changes go to the model (the rest stay
unclear, and the report is marked truncated), and the changes to judge fill up
to 150 KB of diff in diff order, whole — one that doesn't fit is left out.
Test files follow as context when there is room, since they often show which
flag a change is behind; docs and lockfiles are listed by name only. The
prompt carries the PR title but none of the discussion.

### Agent mode and `search_code`

The question a diff most often leaves open is whether a changed function has
callers outside the flag. In `agent` mode the model has two tools to find out:
`read_file`, as comments-addressed has, and **`search_code`**, which returns the
lines of the repository at the PR head containing a fixed string — typically a
function's name, to see its callers. It runs `git grep` over the local clone
(under `RepoLocation`) at the head SHA, so it is offered only when that clone
exists, and a search the clone can't answer (it hasn't fetched the head yet,
say) comes back to the model as an error rather than as no matches. Queries
need at least three characters; at most 60 matches are returned.

### The feature-flags report

The result's `body` is a complete markdown report: the verdict, then the
changes that run without a flag, the unclear ones, the gated ones with their
flags, and those with no runtime effect. Its `report` is the typed version:

| Field       | Meaning                                                                                        |
|-------------|------------------------------------------------------------------------------------------------|
| `verdict`   | `all-gated`, `ungated`, `unclear`, `no-runtime-changes` or `insufficient-input`                |
| `summary`   | The verdict in one sentence                                                                    |
| `counts`    | `total`, `gated`, `ungated`, `no_effect`, `unclear`, and `by_model` (changes the model decided) |
| `flags`     | The flags that gate changes, in order of first appearance: `name` and how many `changes`       |
| `changes`   | One per change, in diff order; see below                                                       |
| `model`     | As for comments-addressed: `consulted`, `provider`, `model`, `mode`, `asked`, agent `turns` / `tool_calls`, and a `note` |
| `truncated` | A change was cut from the model's prompt                                                       |
| `missing`   | What was unavailable, when the verdict is `insufficient-input`                                 |

Each change carries `id` (its number in the diff, as the prompt numbers it),
`path`, `line` and `end_line` (its span in the head version; absent for a
whole file and for a deleted file), `context` (what the hunk header names,
usually the enclosing function), `added`, `removed`, `new_file`,
`deleted_file`, `status` (`gated`, `ungated`, `no-effect`, `unclear`), `source`
(`model` when the model's verdict decided it, otherwise `rule`), `category`
(`test`, `docs` or `lockfile`, for a file a path rule decided), `flag` (for a
gated change, as the code spells it) and `rationale`.

`outstanding` lists the changes that run without a flag, then the unclear
ones, and `annotations` marks those with a head line (a warning for ungated,
info for unclear); a whole-file change, such as a lockfile, anchors to none.

## change-diagram

Draws what a PR changes as a [Mermaid](https://mermaid.js.org) flowchart, so a
reviewer can see how the pieces fit together before reading the diff: the
packages, files, functions, types, endpoints or components the PR adds, changes
or removes, and how they connect — calls, data flow, reads and writes.

```toml
[[AIFeatures]]
ID = "change-diagram"
Enabled = true
Automatic = true    # optional: draw each PR's diagram as it arrives
```

The server serves the **raw Mermaid source**; each client renders it. The web
client draws it with the mermaid library in a modal that takes most of the
screen, and Emacs shows the source in a
[`mermaid-mode`](https://github.com/abrochard/mermaid-mode) buffer.

The model is sent the PR title, the list of changed files and the diff, cut at
200 KB as for file-ordering, and asked for a flowchart of about 30 nodes at
most, each node marked `:::added`, `:::changed` or `:::removed`. Tests, docs and
lockfiles are left out unless they are what the PR is about.

What the model answers is checked and cleaned before it is stored:

- **The diagram is kept alone.** A mermaid code fence wins over any other, and
  whatever precedes the diagram's first line — chatter, comments, front matter —
  is dropped. The first line must declare a diagram that can describe code:
  `flowchart` or `graph` (with an optional direction), or `sequenceDiagram`,
  `classDiagram`, `stateDiagram`, `erDiagram`, a C4 diagram, `block-beta`,
  `architecture-beta` or `mindmap`. An answer without one fails at stage
  `parse`, its raw text in the call log.
- **Interactions and directives are removed.** The diagram is model output
  steered by a PR's diff, so `click` statements (and `link` / `callback` in a
  class diagram), which make a node a link or run a function, and `%%{init}%%`
  directives, which reconfigure the renderer, are dropped. The web client also
  renders at mermaid's `strict` security level, which encodes HTML in labels
  and disables clicks whatever the source says.
- **A flowchart gets the change classes.** The server defines `added` (green),
  `changed` (amber) and `removed` (red, dashed) at the end, replacing any
  definition the model gave, so every diagram colors them alike and the web
  client's legend holds.
- **It must be renderable.** A diagram over 50,000 bytes — mermaid's own limit —
  fails at stage `parse`. Whether it parses is left to the renderer: the web
  client shows mermaid's error beside the source when it doesn't.

The result's `report` is:

| Field          | Meaning                                                              |
|----------------|----------------------------------------------------------------------|
| `mermaid`      | The diagram's raw Mermaid source, ready to render                    |
| `diagram_type` | The keyword the diagram declares itself with, e.g. `flowchart`       |

Its `body` is the same source in a ```` ```mermaid ```` fence, for a client that
only renders markdown bodies. The diagram reads only the code, so only a push
makes it stale.

## file-ordering

Orders the files of a PR's diff so a reviewer can read the PR from top to
bottom: the entry points where the change is integrated (call sites, public
APIs, top-level wiring) first, then helper functions and implementation
details, then styling changes such as CSS, then tests.

```toml
[[AIFeatures]]
ID = "file-ordering"
Enabled = true
Automatic = true    # order each PR as it arrives, before anyone opens it
```

It is an applied feature: `GetPR` serves the diff in the stored order when it
was computed for the PR's head SHA, and in the default order — test files last
— otherwise. Rendering never waits for a run, so the first open of a new
revision shows the default order.

The model is sent the list of changed files and the diff, cut at 200 KB (the
list always goes in whole), and answers with the paths in reading order. Each
path is matched to a file of the diff exactly, or failing that by its base
name; a file the answer leaves out keeps its place after the ones it orders. A
diff with a single file needs no model call, and an answer with no paths fails
at stage `parse`, its raw text in the call log.

The `report` is `{"files": [...]}`: the paths in reading order, as the model
wrote them.

## review-ease

Rates how easy a PR is to review: `easy` for small, mechanical, or repetitive
changes; `medium` for typical changes that need a careful read; `hard` for
large, subtle, or high-risk changes (tricky logic, concurrency, security, many
interacting files).

```toml
[[AIFeatures]]
ID = "review-ease"
Enabled = true
Automatic = true    # rate each PR as it arrives, so the list shows it
```

It is an applied feature: the rating is the `review_ease` field of review list
items (`GetAllReviews`) and of PR metadata (`GetPR`), a pill beside the PR in
the web client's list (whose sidebar can narrow the list to one or more
levels), and a tag such as `:easy:` on the PR's headline in the org content. After a push, a PR keeps its latest rating until the new head's
is stored.

The model is sent the same input as for file-ordering and answers with a
`REVIEW_EASE: <rating>` line. An answer without a usable rating fails at stage
`parse`, its raw text in the call log.

The `report` is `{"rating": "easy" | "medium" | "hard"}`.

## Clients

- **Web.** The review toolbar shows a button per enabled feature, with the
  number of items needing attention once a report exists — for feature-flags,
  the changes that run without a flag or are unclear — or ✓ when none do. It
  opens the report (feature-flags renders its markdown body):
  verdict, what needs attention with who decided each item, change requests,
  and the addressed items collapsed. An item's location jumps to its thread in
  the diff (or the outdated-comments panel); **↻ Re-run** forces a fresh run.
  The review list offers the same without opening the review: each PR's **✦ AI**
  button, beside its Plugins button, opens a page with every enabled feature's
  report for that PR. The applied features get no button: their results are
  the diff's order and the list's review-ease pill and filter. The change diagram's button
  opens a modal that takes most of the screen, drawing the diagram with the
  mermaid library (loaded the first time a diagram is drawn): it opens fitted
  to the window, zooms with **−** / **+** / **1:1**, pans by dragging, and
  shows the raw source on **Source**; the AI page draws it inline.
- **Emacs.** `C` in a review buffer (`crs-get-ai-output`) opens the report of
  an enabled feature in its own buffer, which polls while a run is pending. In
  that buffer, `r` refreshes, `R` re-runs and `q` quits. The applied features
  aren't offered; review-ease shows as the headline tag in the reviews buffer.
  The change diagram has a dedicated command, `crs-show-change-diagram` (`M` in
  a review buffer or on a PR in the reviews buffer; with `M-x` elsewhere it asks
  for a PR URL or `owner/repo#number`). It shows the raw Mermaid source alone in
  a buffer of its own, in `mermaid-mode` when that is installed — so its
  `C-c C-b` renders the diagram with `mmdc` — with the status in the header
  line. The same `r`, `R` and `q` apply.

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

The server's own log carries each run too, as plugin runs' do: an `AI feature
run triggered` line when it starts — before an automatic run waits for its
slot — naming the feature, PR, trigger, mode, provider and head SHA, then `AI
feature run finished` with the status, model calls and duration, or `AI
feature run failed` at `ERROR` with the stage and the error.

## Rollout and rollback

The AI layer ships switched off: `[AI]` and `[[AIFeatures]]` are absent from
the built-in defaults, each feature needs `Enabled = true`, and automatic runs
also need `Automatic = true`.

file-ordering and review-ease were once switched on by root-level flags, and a
config that still sets them keeps working: `ExperimentalLLMFileOrdering = true`
stands for an entry that enables file-ordering automatically on the `gemini`
provider, and `ExperimentalLLMReviewEase = true` the same for review-ease.
`ExperimentalLLMProvider = "openrouter"` moves both onto `openrouter`, asking
for `ExperimentalLLMModel`; the `[AI]` defaults don't reach them. An
`[[AIFeatures]]` entry for the same feature wins over its flag. Orders and
ratings computed before they were AI features stay in the
`DiffFileOrderingCache` table, which the server now only reads: a PR shows them
until a run of the feature stores its own.

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

`Describer`, `ModeSupporter` (to run in `agent` mode too), `TimeoutProvider`,
`CodeOnlyFeature` (to key results by the head SHA alone) and `AppliedFeature`
(for a result the server applies itself, rather than a report) are optional. The
`Request` carries the PR's diff, raw comments JSON, metadata, review threads,
the server's partition of the discussion, and the two seams:
`req.Model.Generate` for one-shot calls and, in agent mode, `req.Agent.Run`
with the tools the feature offers — built from `req.ReadFile` and
`req.SearchCode` where the server can provide them. The `Result` is
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
- **OpenRouter model.** No default model is pinned either, for the same
  reason: which model runs — and what it costs — is the user's choice. `Model`
  is read only by the `openrouter` provider, so a default model meant for it
  can't reach a feature on `gemini` as a model Gemini doesn't serve.
- **Digest composition.** Comment IDs and bodies, review IDs and states, and
  thread resolved/outdated flags. Review states are in scope for the first
  feature: an active change request is part of its verdict.
- **`GetPR` payload.** It doesn't carry AI reports; clients call
  `GetAIOutput`, which also serves them the staleness information.
- **Automatic-run cap.** Two concurrent automatic runs, shared by every
  feature. It isn't configurable yet.
- **Applied features.** A feature whose result the server applies to what it
  already serves declares it (`AppliedFeature`). `ListAIFeatures` marks it
  `applied`, and clients offer no report for it. Since no client asks for one,
  the server does whenever a client opens the PR; a workflow warming a PR runs
  it only when it is `Automatic`, so it queues behind the automatic-run cap like
  any other fan-out work.
- **Code-only keys.** A feature that reads only the code declares it
  (`CodeOnlyFeature`) rather than the digest growing per-feature inputs: the
  runner and `GetAIOutput` swap in `code-only` for its digest, so the table and
  the RPCs stay unchanged.
