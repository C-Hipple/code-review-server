# Plugins

Plugins are external projects which are expected to be discoverable on your `$PATH`, and are called per PR.
You can install external plugins to process PR data asynchronously. Plugins receive data via CLI flags and their output is stored in the database.

![Plugin Web Interface](img/plugin_web.png)

This image shows the plugin output in the bun_client when reviewing a PR in this repository.

Plugins are separate from the server's built-in [AI features](ai_features.md), which
have their own config (`[[AIFeatures]]`), storage and RPCs. The two share only the
code that runs a subprocess and the OpenRouter client; an annotation's `source` field (`plugin` or `ai`) says
which one produced it.

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

[[Plugins]]
Name = "Summarize (Claude)"
Command = "summarize_diff"
IncludeDiff = true
IncludeHeaders = true
Provider = "openrouter"                  # Call OpenRouter instead of Gemini
Model = "anthropic/claude-sonnet-4.5"    # The OpenRouter model to ask for
```

### Plugin Configuration Options

- `Name` (string, required): Display name for the plugin; unique across plugins
- `Command` (string, required): Executable name on your `$PATH`
- `IncludeDiff` (bool, optional): Pass the PR diff via `--diff` flag
- `IncludeHeaders` (bool, optional): Pass PR metadata via `--headers` flag (includes `head_ref` among other fields)
- `IncludeComments` (bool, optional): Pass PR comments via `--comments` flag
- `IncludeBranch` (bool, optional): Pass the PR's head branch name via `--branch` flag
- `OnlyOnDemand` (bool, optional, default: false): If true, plugin only runs when explicitly requested via RerunPlugins
- `Provider` (string, optional): The LLM backend the plugin should call — `gemini` or `openrouter`. See [Choosing a Model](#choosing-a-model)
- `Model` (string, required with `Provider = "openrouter"`): The model to ask OpenRouter for, as OpenRouter names it (e.g. `anthropic/claude-sonnet-4.5`)

> **Note:** The branch name is also available in the `--headers` JSON as the `head_ref` field. Use `IncludeBranch` when you want the branch as a simple standalone argument without parsing the full metadata JSON.

## Choosing a Model

The bundled LLM plugins — Summarize Diff, Security Check and Style Guidelines —
call one of two backends:

- **`gemini`** (the default): the latest Gemini Flash model
  (`gemini-flash-latest`), with the key in `GEMINI_API_KEY`.
- **`openrouter`**: any model [OpenRouter](https://openrouter.ai) serves —
  Anthropic's, OpenAI's, Google's and many more — with the key in
  `OPENROUTER_API_KEY`. OpenRouter has no default model and neither does the
  plugin, so the entry names one in `Model`.

A plugin's `Provider` and `Model` reach it as the environment variables
`CRS_LLM_PROVIDER` and `CRS_LLM_MODEL`, alongside the rest of the server's
environment (API keys included). The server sets each only when the entry
does, so exporting them to the server itself switches every plugin that
doesn't say otherwise:

```bash
export OPENROUTER_API_KEY="..."
export CRS_LLM_PROVIDER=openrouter
export CRS_LLM_MODEL=google/gemini-2.5-flash
```

The variables are passed rather than flags so that a plugin which calls no
model — or picks one its own way — can ignore them: unlike an unknown flag, an
unknown environment variable breaks nothing. Your own plugins are free to
honor them too. A config naming any other `Provider`, or `openrouter` without a
`Model`, is logged at startup and rejected by `UpdateConfig`. Plugins don't
read the AI features' `[AI]` settings: a `DefaultProvider` or `DefaultModel`
there doesn't reach them.

The plugins ask Gemini for structured JSON output where they emit
[annotations](#plugin-response-contract), and OpenRouter for the same schema
through its structured outputs. Not every model behind OpenRouter supports
those, so the prompt also spells out the JSON wanted; a reply that still isn't
JSON becomes the plugin's body verbatim, without annotations.

Style Guidelines is the exception: it runs an agentic tool loop rather than a
single prompt, so the model it calls must support **tool calling** (Gemini's
function calling, or OpenRouter's tool calls — most current models do; check
the model's page on OpenRouter for "tools"). Its answers arrive as tool calls,
so it asks for no structured output. See
[Style Guidelines Plugin](#style-guidelines-plugin).

## Included Plugins

- **Summarize Diff**: Uses the latest Gemini Flash model (`gemini-flash-latest`), or [the model you choose](#choosing-a-model), to explain what a PR is trying to accomplish and to mark its hotspots. Emits the [response contract](#plugin-response-contract): a markdown body stating the PR's goal and the approach it takes, plus up to four annotations on the lines carrying the key implementation or business logic — the ones that deserve the closest review. Mechanical changes (renames, moved code, formatting) are deliberately left unannotated.
- **Security Check**: Uses the latest Gemini Flash model (`gemini-flash-latest`), or [the model you choose](#choosing-a-model), to analyze the diff for potential security risks, specifically looking for unprotected sensitive endpoints, hardcoded secrets, or missing security decorators (like `@authenticated`).
- **Style Guidelines**: Uses the latest Gemini Flash model (`gemini-flash-latest`), or [the model you choose](#choosing-a-model) (it must support tool calling), to check a PR's diff against your style guide — a single Markdown file, or a whole directory of them. It runs two agentic phases: a reviewer that searches and reads the guide to find violations, then a validator that checks each finding against the guide and drops the ones that are wrong or out of scope, such as a frontend rule applied to backend code. Emits the [response contract](#plugin-response-contract): a markdown body holding the report, plus an annotation on each confirmed violation. Requires `GEMINI_API_KEY`, or `OPENROUTER_API_KEY` on OpenRouter. See [Style Guidelines Plugin](#style-guidelines-plugin) below.
- **Claude Review**: Runs `claude -p "review PR #<number> on repo <owner>/<repo>" --model sonnet` via the Claude CLI. Written in Zig. Build with `zig build` inside `cmd/claude_review/` and place the resulting binary on your `$PATH`.

Plugins are expected to accept flags like `--owner`, `--repo`, `--number`, `--call-type`, and any of the optional content flags enabled above (`--diff`, `--headers`, `--comments`, `--branch`).

## Call Types

Every invocation carries a `--call-type` flag naming why the plugin is being run, so a plugin can behave differently on a rerun than it does on the server's own scheduled runs.

| Value       | When it's passed                                                                       |
|-------------|----------------------------------------------------------------------------------------|
| `automatic` | The server triggered the run itself, because a PR was fetched or its head SHA changed. |
| `explicit`  | A deferred (`OnlyOnDemand = true`) plugin was requested by name via `RerunPlugins`. These plugins never run automatically, so any run of one is an explicit request. |
| `rerun`     | A plugin that would otherwise run automatically was rerun via `RerunPlugins`. |

`--call-type` is passed on **every** invocation, alongside `--owner`, `--repo` and `--number`, with no configuration option to turn it off. A plugin that rejects unknown flags — anything using Go's `flag` package, Python's `argparse`, or similar — must therefore declare it, even if it ignores the value. Plugins that parse flags loosely need no change.

Both `explicit` and `rerun` mean a person asked for this run, which is the signal worth acting on: skip a cached result, spend a larger model budget, or re-fetch external state that an `automatic` run would have reused. The distinction between the two says whether the plugin is expensive-by-configuration (`explicit`) or was rerun on top of work it does routinely (`rerun`).

Go plugins in this repository can use `cmd/internal/pluginkit` rather than parsing the value by hand:

```go
callType := pluginkit.RegisterCallTypeFlag() // declares --call-type
flag.Parse()

if callType().Requested() {
    // A person asked for this run — redo the expensive work.
}
```

`ParseCallType` maps an unrecognised or empty value to `automatic`, so a plugin built against a different server version keeps working. Plugins that don't act on the value can still call `pluginkit.RegisterCallTypeFlag()` and discard the result, purely so `flag.Parse` accepts the flag — that's what the bundled `summarize_diff`, `security_check`, `style_guidelines` and `claude_review` plugins do.

## Writing a Plugin

You can write a plugin in any language you like. The only requirement is that the binary must be discoverable on your `$PATH`.

The `example_plugin` included in this repository demonstrates the interface and potential options.

Go plugins living in this repository share `cmd/internal/pluginkit`, which holds the response contract types, the diff line numbering that lets a model anchor annotations, and the model call the bundled LLM plugins make: `pluginkit.ModelFromEnv()` builds the backend `CRS_LLM_PROVIDER` / `CRS_LLM_MODEL` name (see [Choosing a Model](#choosing-a-model)), and its `Generate` takes the prompt and an optional response schema. A plugin that wants a multi-turn tool loop instead starts a `Chat` with `Model.NewChat(system, tools)`: `AddUser` and `AddToolResults` extend the conversation, and `Next` asks for the model's next turn — text, tool calls, or both — optionally requiring a named tool. `Chat` speaks each backend's native tool calling and keeps the transcript in the backend's own shape, so what a model hands back with its calls (Gemini's thought signatures, OpenRouter's reasoning) is resent untouched; the loop itself — which tools to run, when to stop — is the plugin's. `style_guidelines` is the worked example.

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

Annotations are listed beneath each plugin's body on both the plugin output page and the review view's plugin drawer, sorted by file and line.

They also render inline in the diff, collapsed the same way review comments are. An annotated line carries a flag badge at its right-hand end — right-aligned to the line rather than sitting in a gutter, so code and line numbers render exactly as they would in a PR with no annotations — coloured by the most severe annotation on it. Hovering previews the annotations; clicking expands a card beneath the line with each annotation's severity, source plugin, and text. The badge stays pinned to the visible edge while a long line scrolls sideways. The toolbar's **⚑ Annotations** button expands or collapses all of them at once.

An expanded annotation offers **💬 Add as comment**, which adopts it as a local comment at the same position — the same place a comment typed on that line would land, or the file itself for an annotation collapsed onto a file header. The comment body is the annotation's text under an `Automated comment by <plugin>` line, so the plugin behind it is still named once the review is posted to GitHub. It is an ordinary local comment from there on: editable, deletable, and submitted with the review. An annotation already adopted says so instead of offering the button again, and one anchored to a line the diff has no comment position for — context fetched by expanding a hunk — offers no button, since no comment can be attached there at all.

An annotation anchors to the line matching its `line` on the PR's **head** side, so it lands on an added or unchanged line of the diff. Annotations the diff can't show a row for — a line outside any hunk, or one that only exists on the base side — collapse onto that file's header instead, labelled with the line they point at, rather than disappearing. Annotations naming a file that isn't in the diff at all are only listed in the plugins drawer. Paths must match the diff's repo-relative paths (a leading `./` is tolerated).

The diff reads annotations from the plugin results it has loaded, so rerunning a plugin from the drawer updates the diff without reloading the PR. Only successfully executed plugins contribute, matching the `annotations` aggregate on the PR reply.

### Rendering in the Emacs Client

Plugin output buffers show each plugin's parsed body (instead of its raw stdout, which is JSON for contract-emitting plugins), with the plugin's annotations listed beneath the body sorted by file and line.

Annotations also render inline in the review buffer's diff, from the `annotations` aggregate on the `GetPR` reply. They are inserted after the diff has been washed with git-delta — the same way review comments are — so the washer's syntax highlighting is unaffected. Annotations start collapsed: an annotated line carries a right-aligned `<A: plugin>` indicator, and placing the cursor on such a line previews the annotations in the echo area. Toggling comments open with `H` expands annotations along with them; `A` toggles annotations on their own.

As in the web client, an annotation anchors to the line matching its `line` on the PR's head side, landing on an added or unchanged line of the diff. Annotations the diff can't show a row for attach to their file's first hunk header, labelled with the line they point at. Annotations naming a file that isn't in the diff at all appear only in the plugin output buffers.

An annotation can also be promoted to review feedback. With the cursor on an annotated line — on the collapsed `<A: plugin>` indicator, or anywhere inside an expanded annotation block — `a` (`crs-add-annotation-as-comment`) files that annotation as a local comment at the same diff position, without opening a comment buffer. The body is:

```
Automated comment by <plugin name>

<annotation content>
```

A line carrying several annotations prompts for which one to file. Annotations that collapsed onto a file's hunk header are refused: the line they name is not in the diff, so there is no position for a comment to anchor to. The comment is an ordinary local comment from there on — editable with `c`, deletable with `d`, and posted by `crs-submit-review` like any other.

### Rendering in the Chrome Extension

The [Chrome extension](clients.md#chrome-extension) shows a card per configured plugin in its PR view, with the plugin's status — not run yet, on demand (a deferred plugin nobody has asked for), running, success or failed — and a count of its annotations. Expanding a card shows the parsed body: `markdown` is rendered with raw HTML dropped and the rest sanitized, and `html` goes in a sandboxed frame without `allow-scripts`, styled to follow the panel's light or dark theme, whose links open in a new tab. A plugin whose output has no recognised `body_type` has its raw stdout rendered as markdown.

Annotations are listed beneath the body, sorted by file and line. The extension doesn't render the diff, so each one links to its line in GitHub's own Files changed tab instead (`https://github.com/<owner>/<repo>/pull/<n>/files#diff-<sha256 of the path>R<line>`, the anchor GitHub gives a line on the head side).

**Run**, on a plugin that hasn't run or is deferred, and **Re-run**, on one that has, both call `RerunPlugins` naming that plugin alone, so a deferred plugin runs too. **Re-run all** calls it with no names: the plugins that run automatically rerun, and deferred ones are reset to on demand. The view polls `GetPluginOutput` until nothing reads `pending`.

## On-Demand Plugins

By default, all configured plugins automatically run when a PR is fetched or when its commit changes (once per SHA). However, some plugins can be expensive to run (e.g., those making API calls to third-party services like Gemini, OpenRouter or Claude).

To avoid unnecessary costs, you can mark a plugin as `OnlyOnDemand = true` in the configuration. These plugins will:
- **Not run automatically** when a PR is fetched or updated
- Receive a `"deferred"` status in the database
- **Only execute when explicitly requested** via the RerunPlugins RPC method with their name in the plugin list
- Always be invoked with `--call-type explicit`, since a deferred plugin has no automatic runs to distinguish a rerun from (see [Call Types](#call-types))

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

Reruns are also visible to the plugin itself: a plugin invoked through `RerunPlugins` receives `--call-type rerun`, or `--call-type explicit` if it is deferred, instead of the `automatic` it gets from the server's own runs. See [Call Types](#call-types).

## Style Guidelines Plugin

The `style_guidelines` plugin checks PR diffs against your own style rules, written in Markdown. The rules can be a single file or, for a large project, a directory of files — one per language, layer or topic.

### Setup

1. **Install the binary:**
   ```sh
   go install ./cmd/style_guidelines/...
   ```

2. **Write your style guide.** The plugin looks for it in this order, using the first it finds:

   | Where | How |
   |-------|-----|
   | `--style-guide-dir <dir>` | A flag, for running the plugin by hand. The server doesn't pass it. |
   | `CRS_STYLE_GUIDE_DIR` | An environment variable naming a directory. The server passes its own environment to every plugin, so export it where you start the server. `~/` is expanded. |
   | `~/.config/style_guidelines/` | A directory, used if it exists. |
   | `~/.config/style_guidelines.md` | A single file — the original location, still the default. |

   A directory named by the flag or the variable must exist; the plugin fails rather than silently falling back. Every `.md`, `.markdown` and `.mdx` file under it is read, recursively, skipping hidden files and directories (such as `.git`), up to 500 files of at most 1 MiB each. Point it at the guide itself, not at a whole repository.

   A single file, in plain Markdown:
   ```markdown
   # Style Guidelines

   - Functions must have docstrings explaining their purpose.
   - Use snake_case for all variable names.
   - No magic numbers; use named constants instead.
   - Error messages must be lowercase and end without punctuation.
   ```

   Or a directory, laid out however suits the project:
   ```
   style-guide/
   ├── README.md          # rules for everything
   ├── backend/
   │   ├── python.md
   │   └── api-design.md
   ├── frontend/
   │   ├── react.md
   │   └── css.md
   └── testing.md
   ```

   Say what each rule covers. The validation phase rejects a rule applied outside its scope, and it judges scope from the file's path, its headings and the text around the rule: a file named `frontend/react.md`, or a heading like "## Backend (Python)", does that job.

3. **Set your Gemini API key** (or, to run it through OpenRouter, `OPENROUTER_API_KEY` plus `Provider` and `Model` in the entry below — see [Choosing a Model](#choosing-a-model)). The model must support tool calling:
   ```sh
   export GEMINI_API_KEY=your_key_here
   export CRS_STYLE_GUIDE_DIR=~/code/myproject/docs/style-guide   # optional
   ```

4. **Add to `~/.config/codereviewserver.toml`:**
   ```toml
   [[Plugins]]
   Name = "Style Guidelines"
   Command = "style_guidelines"
   IncludeDiff = true
   IncludeHeaders = true
   ```

### How It Works

The plugin runs its own small agentic loop, twice. In each phase the model is given the PR's title and description, its diff with head-side line numbers, and the guide, plus two tools for reading the guide:

- `search_style_guide` — a case-insensitive regular expression search over the guide (or one file of it), returning `file:line: text` for each match.
- `read_style_guide` — a guide file, or a range of its lines, numbered.

A guide of up to about 40 KB goes into the prompt whole; a larger one is given as an index of its files and their headings, and the model reads the sections that bear on the diff with the tools. Each phase ends when the model calls its submit tool; the loop checks the answer and hands any problem back as the tool's result, so the model can correct it and submit again.

1. **Review** (`submit_findings`, up to 12 turns). The model finds the rules that apply to each changed file and submits up to 25 candidate findings, each naming the file and line, a severity, the guide file and the rule it breaks. Only lines the PR adds can be flagged: a finding on a file that isn't in the diff, a line the diff doesn't show, or an unchanged line is sent back to be fixed.
2. **Validation** (`submit_verdicts`, up to 10 turns). A fresh conversation, told to be skeptical, takes each candidate back to the guide and the diff and confirms it only if the rule exists as cited, applies to that file (a frontend rule on backend code, a Python rule on Go, or a test convention on production code is rejected), is actually broken by the line, and isn't a duplicate. It can adjust the severity and rewrite the remark. A candidate it gives no verdict is treated as rejected. A review with no candidates skips this phase.

On its last turn, or when the phase's deadline is under 45 seconds away, the model is made to call the submit tool. The whole run is bounded at 4½ minutes, inside the server's 5-minute plugin timeout, with the review phase getting at most 2½ of them.

### Output

The plugin emits the [response contract](#plugin-response-contract).

Its markdown body is a report with:
- The validator's overall assessment of the diff's compliance
- The confirmed violations, worst first, each with its file and line, severity, remark and the guide rule it breaks
- The findings dismissed on validation, each with the reason — so you can see what was filtered out, and spot a guide rule whose scope needs stating more clearly
- A footer naming the guide it read, the model, and the turns and tool calls each phase took

Alongside the report it returns up to ten annotations — the most severe confirmed violations — so they render inline in the diff as well as in the report. Each annotation says what's wrong and how to fix the line, followed by the guide file it comes from, with a severity of `info` for a nit, `warning` for a clear violation, or `error` for one that breaks a rule the guide states as a hard requirement. Dismissed findings are never annotated.

If the validation phase fails (an API error, or the model never submits), the report lists the candidates as unconfirmed and no annotations are returned, rather than putting unchecked findings in the diff. If the review phase fails, the plugin prints the error and exits non-zero, as before. A diff with no parseable hunks can't be anchored, so it yields no annotations, and the report alone is returned.
