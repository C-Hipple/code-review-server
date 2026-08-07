# Web Client Feature Reference (`bun_client`)

This document inventories everything the shipped web client (`bun_client/`) does, so
another engineer can build a web-based client of their own — a VS Code webview, a
Next.js app, a different SPA — without reverse-engineering the existing one.

It is a **feature list**, not an API spec. Every server call named here is defined in
the [Protocol](protocol.md); read that document alongside this one. The general rules
for spawning and talking to the server live in [Building Clients](building_clients.md).

One thing worth knowing before you start: the Go server speaks **JSON-RPC 1.0 over
stdio only**. There is no HTTP server in the Go binary. Any web client needs a
process that owns the child process and bridges it to the browser — that is what
`bun_client/server.ts` is.

---

## 1. Architecture

```
browser (React SPA)
    │  HTTP POST /api/*        (JSON)
    │  WebSocket /api/lsp*     (LSP framing)
    ▼
bun_client/server.ts           ← the bridge; owns the child process
    │  stdin/stdout, newline-delimited JSON-RPC 1.0
    ▼
codereviewserver --server      (spawned as `crs --server`)
```

**`server.ts`** — a Bun HTTP + WebSocket server (default port `5172`, override with
`CRS_PORT`). Responsibilities:

1. Spawn the backend once at startup (`Bun.which('crs')`, falling back to `crs`),
   with `stdin: 'pipe'`, `stdout: 'pipe'`, `stderr: 'inherit'`, inheriting the
   parent environment (so `CRS_GITHUB_TOKEN` flows through).
2. Multiplex concurrent RPC calls over the single stdio pipe. Each request gets a
   unique id; replies are matched back to a pending-promise map.
3. Frame the response stream. `rpc_framing.ts` (`JsonRpcLineParser`) splits stdout on
   newlines — Go's `net/rpc/jsonrpc` writes exactly one JSON object per line and
   escapes newlines inside strings, so line splitting frames the stream exactly. It
   buffers partial chunks, so a large diff split across reads reassembles correctly.
4. Serve the frontend: dev-mode proxy to Vite (`localhost:5173`), then embedded
   assets, then `frontend/dist` on disk, then an SPA fallback to `index.html`.
5. Provide the non-RPC endpoints the browser can't do itself: local file reads,
   repository file listing, and LSP WebSocket proxying.

**`frontend/`** — React 19 + Vite SPA. Dependencies are deliberately thin:
`react-markdown` and `react-syntax-highlighter` (Prism) are the only runtime ones.
The UI is built on a small in-repo design system (`frontend/src/design/`) rather than
a component library.

**Packaging** — `bun run build` builds the frontend, generates
`embedded_assets.ts` (`scripts/build.ts` walks `frontend/dist` and emits
`import … with { type: 'file' }` entries), then compiles a single `crs-gui` binary
with `bun build --compile`. A client that wants single-binary distribution can copy
this pattern; if you don't, `server.ts` still serves from `frontend/dist`.

### If you are building your own client

You need the same three responsibilities, however you split them:

- **Process ownership** — one long-lived `codereviewserver --server` child. Spawning
  one per request is wasteful (the server caches in SQLite and holds GitHub rate
  limit state) and racy.
- **Request multiplexing** — the stdio pipe is shared; correlate by JSON-RPC `id`.
- **Line framing** — do not assume one read = one message, in either direction.

---

## 2. HTTP surface exposed to the browser

Everything below is served by `server.ts`. CORS is wide open (`*`), which is what
lets the Vite dev server on `:5173` talk to the bridge on `:5172`.

### RPC pass-through endpoints

Each is a thin `POST` wrapper: the JSON body is forwarded verbatim as the single RPC
parameter, and the reply is returned as `{ "result": … }` (or `{ "error": … }` with
status 500).

| Endpoint | RPC method | Notes |
|---|---|---|
| `/api/reviews` | `RPCHandler.GetAllReviews` | No body; sends `{}` |
| `/api/get-pr` | `RPCHandler.GetPR` | |
| `/api/get-adjacent-pr` | `RPCHandler.GetAdjacentPR` | |
| `/api/sync-pr` | `RPCHandler.SyncPR` | |
| `/api/add-comment` | `RPCHandler.AddComment` | |
| `/api/edit-comment` | `RPCHandler.EditComment` | |
| `/api/delete-comment` | `RPCHandler.DeleteComment` | |
| `/api/remove-pr-comments` | `RPCHandler.RemovePRComments` | Bridged but unused by the current UI |
| `/api/set-feedback` | `RPCHandler.SetFeedback` | |
| `/api/submit-review` | `RPCHandler.SubmitReview` | |
| `/api/list-plugins` | `RPCHandler.ListPlugins` | `GET` or `POST`; bridged but unused by the current UI |
| `/api/get-plugin-output` | `RPCHandler.GetPluginOutput` | `GET` (query params `owner`/`repo`/`number`) or `POST` |
| `/api/rerun-plugins` | `RPCHandler.RerunPlugins` | |
| `/api/get-hunk-context` | `RPCHandler.GetHunkContext` | |
| `/api/get-config` | `RPCHandler.GetConfig` | `GET` or `POST` |
| `/api/update-config` | `RPCHandler.UpdateConfig` | |
| `/api/rpc` | *any* | Generic escape hatch: `{ method, params, id }` |

The frontend's `api.ts` routes known methods to their specialized path and falls back
to `/api/rpc` for anything else, so **any** protocol method is reachable from the
browser without touching `server.ts`. If you build a bridge, `/api/rpc` alone is a
viable minimum; the specialized routes exist mostly for readability.

### Non-RPC endpoints (bridge-local capabilities)

These have no server-side RPC equivalent — they are the bridge doing local work the
browser cannot. Reproduce them if you want the corresponding features.

| Endpoint | Purpose |
|---|---|
| `POST /api/read-file` | Read a file from disk. Accepts an absolute `filePath`, or `repoPath` + relative `filePath` (rejects paths that escape `repoPath`). Powers the code viewer. |
| `POST /api/list-files` | Recursive file listing under `repoPath`, skipping `node_modules`, `.git`, `.next`, `dist`. Powers the code viewer's file tree. |
| `GET /api/check-lsp` | Whether the `diff-lsp` binary is on `PATH`. |
| `GET /api/check-lsp-file?lang=` | Whether a per-language server is on `PATH`. |
| `POST /api/prepare-diff-lsp` | Writes the diff plus a `Project/Root/Worktree/Buffer/Type` header to a temp file and returns its path; `diff-lsp` consumes that file. |
| `WS /api/lsp` | Proxies a `diff-lsp` process. |
| `WS /api/lsp-file?lang=` | Proxies a plain language server (`gopls`, `typescript-language-server`, `rust-analyzer`, `pyright-langserver`), resolved at startup. |

The WebSocket handlers translate between browser JSON messages and LSP's
`Content-Length` framing in both directions, and kill the child process on close.

---

## 3. Feature inventory

### 3.1 Review list (`PRList.tsx`)

The landing view. Backed by a single `GetAllReviews` call; the client uses the
structured `items` array (not the org-mode `content` string).

Laid out as a fixed sidebar of filters beside a dense, full-width list of rows.

- **State sidebar** — Open / Draft / Merged / Closed / Everything, each with a live
  count. The state is derived client-side from `status` + `tags`, mirroring the
  server's own bucketing (`prStatusOrder`): `TODO` is open, `WAITING` is a draft,
  `DONE` is merged when tagged `merged` and closed otherwise; anything else falls
  back to the tags and then to open, so no item is ever dropped. The list opens on
  **Open** and remembers the last choice in `localStorage`.
- **Sectioned list** — items grouped by `item.section`, ungrouped items land in
  "Other", in the order the server sent them. Each section is a collapsible header
  with its own count, plus a global collapse/expand toggle in the toolbar.
- **Per-item row** — lifecycle pill (open / draft / merged / closed, in a fixed-width
  column so titles align), title, `review_ease` pill when the LLM rating is enabled,
  and a meta line of repo, `#number`, author login, and a relative timestamp from
  `created_at` (full timestamp on hover).
- **Non-PR items** — items with `number <= 0` render as "Non-PR item" and are not
  clickable.
- **Actions per row** — revealed on hover or keyboard focus: Plugins (open the plugin
  view) plus whichever of Review / GitHub the row click doesn't already go to, which
  depends on the Preferred Review Location preference.
- **Text filter** — matches title, repo, owner, author, and PR number (with or
  without a leading `#`, by prefix). A "N of M" counter sits beside it.
- **GitHub URL paste** — pasting `https://github.com/{owner}/{repo}/pull/{n}` into the
  filter box opens that review directly instead of filtering.
- **Repo / author narrowing** — a checkable list per field, each value shown with its
  open-PR count and ordered by it (busiest first, alphabetical within a tie); ten rows
  are visible before the list scrolls. Counts are row counts over all items, like the
  state counts, and are computed independently of the current filters so checking a
  box never reorders the list under the pointer. Several boxes OR together within a
  field and AND across fields. The All repos / All authors dropdowns above them are
  single-value shortcuts into the same selection — picking one replaces the selection,
  and they read "N repos selected" when the list holds more than one — so the two
  controls can't disagree. Narrowing (and the text filter) is applied before the state
  counts, so the sidebar describes what the current search holds. On phones both lists
  fold into disclosures so they don't bury the reviews.
- **Manual open form** — a collapsed disclosure in the sidebar taking owner / repo /
  number, for PRs not in any section.
- **Refresh** — re-issues `GetAllReviews`; a failed load offers a retry.
- **Plugin warm-up** — after loading, the list fires a `GetPluginOutput` call for
  every visible PR and ignores the results. This is deliberate: it starts deferred
  plugin execution server-side so output is ready by the time a PR is opened.

### 3.2 Routing and navigation (`App.tsx`)

- **URL-driven state** — `?owner=&repo=&number=` opens the review view;
  `&view=plugins` opens the plugin view. Set via `history.pushState`, and a
  `popstate` listener makes browser back/forward work.
- **Deep links** — any review is a shareable URL; the PR header has a Copy URL button.
- **Modifier-aware links** — list rows and the home link are real `<a>` elements;
  ctrl/cmd/shift/alt clicks fall through to normal browser behavior, plain clicks are
  intercepted for SPA navigation.
- **Prev / Next PR** — header buttons calling `GetAdjacentPR` with `Previous`
  true/false. Navigation state is taken from `adjacent_owner` / `adjacent_repo` /
  `adjacent_number` in the reply (never parsed out of `metadata.url`), and wraps at
  both ends.
- **Dynamic document title** — `Code Review` on the list, `{title} (#{number})` in a
  review, `Plugins {owner}/{repo}::{number}` in the plugin view.
- **Top bar** — wordmark, the active view's tab, the prev / next / back controls, and
  preferences. Pinned on the list (whose sidebar and toolbar stick beneath it, offset
  by `--topbar-height`); static in the review view, whose own sticky toolbar measures
  itself against the viewport top.

### 3.3 PR header (`PRHeader.tsx`)

Rendered from `GetPR`'s `metadata`:

- State pill (open / closed / merged, or a Draft badge) and PR number.
- Info grid: base ← head branch, author, CI status (color-coded by
  success / pending / failure), requested reviewers and requested teams,
  approved-by, changes-requested-by, commented-by.
- Labels, assignees, milestone row.
- Collapsible markdown description, with HTML comments stripped (PR templates are
  full of them). Expanded by default.
- CI failure list when `ci_failures` is non-empty.
- Links out: GitHub button and Copy URL button.

### 3.4 Review discussion (`ReviewDiscussion.tsx`)

Chronological list of submitted reviews from `reviews[]` that have a body, rendered
as markdown with the reviewer's state. Collapsed by default so the diff stays near
the top.

### 3.5 Diff rendering (`DiffView.tsx`, `diff_utils.ts`)

This is the most intricate part of the client, and the part most worth copying
carefully.

**Parsing** — `parseDiff()` turns the raw unified diff into `ParsedLine[]`, tracking
for every line: text, file, GitHub comment `position`, clickability, line type
(`file-header` / `hunk` / `addition` / `deletion` / `code` / `skip`), file status
(modified / new / deleted / renamed, with the original name for renames), the index
into the raw diff, and both old- and new-side line numbers.

Position accounting mirrors the backend exactly, and getting it wrong silently
misplaces comments:

- Position resets at each file. The **first** hunk header of a file does not consume
  a position (matching the server's reset to 0); every subsequent hunk header does,
  but hunk headers are never valid comment targets.
- A file header carries `pos: 0`, which is how a whole-file comment is expressed.
- Unrecognized lines default to `skip` and are dropped, so `git`'s metadata lines,
  `\ No newline at end of file`, and the trailing empty string from `split('\n')`
  never become phantom rows.
- Renames, quoted paths, `/dev/null` on either side, and files with no `+++` header
  all have explicit handling.

**Rendering**:

- Per-line syntax highlighting via Prism, with the language inferred from the file
  extension (a ~60-entry map plus special cases for `Dockerfile`, `Makefile`,
  `*.d.ts`). Files with more than `MAX_HIGHLIGHT_LINES_PER_FILE` (2000) changed lines
  fall back to plain text so large diffs stay responsive.
- Old/new line-number gutters, a `+`/`−` gutter, and per-file `+n/−m` counts.
- Test files sort last (`sortParsedLinesTestsLast`) — `*.test.*`, `*.spec.*`,
  `*_test.go`, `__tests__/`.
- Per-file collapse plus Collapse All / Expand All.
- Sticky file headers and hunk headers. `Review.tsx` measures the toolbar and one
  file row with a `ResizeObserver` and publishes `--review-sticky-top` and
  `--review-hunk-sticky-top`, so headers pin correctly at any font size. Each file's
  rows are wrapped in a section element, which scopes the sticky containing block so
  a header scrolls away with its own file.
- Line wrap toggle (wrap on by default on mobile, off on desktop).
- **Hunk expansion** — ↑ / ↓ buttons on each hunk header fetch more context via
  `GetHunkContext` and splice it into the diff text. Expanded lines are tracked in
  `expandedLineIndices` and passed back into `parseDiff`, where they render and
  advance the line-number gutters but **do not consume a comment position** — they
  aren't in the canonical diff, so counting them would shift every position below.
- **File index** — a pill bar above the diff listing every changed file with its
  status icon and `+/−` counts; clicking scrolls to that file, expanding it first,
  with a flash highlight on arrival.
- **Mobile layout** — the whole diff becomes a horizontally scrollable
  `width: max-content` block so long lines can be read; inline cards are pinned to
  the left edge with `position: sticky` so they stay readable while scrolled.

### 3.6 Comments

Local (pending) comments are stored server-side until the review is submitted; the
client treats `author === 'local'` as "mine, still editable".

- **Inline comment on any line** — clicking the `+`/`−` gutter opens an inline form
  anchored to that row, pre-filled with the file and position.
- **File-level comment** — clicking a file header comments on the file (position 0).
- **Free-form comment modal** — the toolbar's `+ Comment` opens a modal where file
  and position are typed manually.
- **Threading** — comments are grouped by root (`in_reply_to`). Clicking a thread
  replies to its last comment; clicking a local thread edits it instead.
- **Collapsed by default** — threads render as a small indicator badge next to the
  line, in a fixed-width gutter left of the line numbers so the diff never shifts.
  Hovering previews the thread; clicking expands every thread on that row.
- **Edit / delete** local comments; newly added comments auto-expand their thread.
- **Outdated comments** — surfaced per file via a warning button on the file header,
  which opens a right-hand drawer listing each outdated comment with its stored
  `diff_hunk` as syntax-highlighted context.
- **Review feedback draft** — a collapsible PR-level body persisted with
  `SetFeedback`, which pre-fills the Submit Review modal.

Calls used: `AddComment`, `EditComment`, `DeleteComment`, `SetFeedback`. Each returns
a full refreshed PR payload, which the client applies wholesale rather than patching
local state.

### 3.7 Submitting a review

- Modal with Comment / Approve / Request Changes and an optional body, pre-filled
  from the saved feedback draft.
- On mobile, a sticky bottom bar with thumb-sized Approve / Request / Comment buttons
  that open the same modal pre-set to the chosen event.
- After `SubmitReview` succeeds, the client immediately runs a `SyncPR` to pick up the
  server-side state (local comments are gone, the review is on GitHub, section
  membership may have changed).

### 3.8 Sync

The toolbar's Sync button calls `SyncPR` and reports the result through a transient
toast, distinguishing "new commits or comments pulled in" from "already up to date"
using the reply's `updated` flag. Plugin outputs are reloaded at the same time.

### 3.9 Plugins

Two surfaces, both reading `GetPluginOutput` (a `{pluginName: PluginOutput}` map):

- **Plugins drawer** in the review view — right-hand drawer with each plugin's status
  badge, rendered body, annotation list, and a Re-run / Execute button.
- **Full-page plugin view** — reachable from the list's Plugins button or
  `?view=plugins`, same content with more room.

Behavior worth reproducing:

- **Body contract** (`plugin_utils.ts`) — a plugin's `body` may be `markdown` or
  `html`. Trust the server when it sends a recognized `body_type`, *including* an
  empty body (annotations-only plugins are legitimate); fall back to rendering raw
  `result` as markdown only when the body is missing or its type is unrecognized.
- **Status handling** — `pending`, `success`, `error`, `deferred`. A `deferred` plugin
  has never run and gets an **Execute** button; a `pending` one is in flight and gets
  neither; everything else gets **Re-run**. Unknown future statuses are treated as
  re-runnable rather than hidden.
- **Re-run** uses `RerunPlugins` with a `Plugins: [name]` list, then re-polls
  `GetPluginOutput`.

### 3.10 Plugin annotations in the diff (`annotation_utils.ts`)

Plugins can emit line-level annotations, and the client anchors them to diff rows:

- Annotations are collected from the **loaded plugin outputs** (successful runs only),
  not from the PR payload's identical `annotations` field, so re-running a plugin
  refreshes the diff without a full PR reload. The payload field exists for clients
  that don't load plugin output separately.
- Matching is on the **head-side (new) line number**, since plugins read the PR head.
  A leading `./` in a plugin's path is normalized away; anything else must match the
  diff's path exactly.
- Annotations pointing at a line the diff doesn't render (an unchanged region, or a
  base-only line) fall back to a per-file bucket on the file header rather than
  vanishing. Annotations for files not in the diff are dropped from the diff view and
  remain visible in the plugins drawer.
- Collapsed by default behind a badge pinned to the right edge of the row; the badge
  takes the most severe variant in its bucket. Severity is free-form — `error` /
  `critical` / `high` → danger, `warning` / `warn` / `medium` → warning, `info` /
  `note` / `low` → info, anything else neutral.
- A toolbar button expands or collapses every annotation at once, with a total count.

### 3.11 LSP integration (`useLsp.ts`, `lsp.ts`, `LspPopover.tsx`)

Optional, and degrades cleanly when the binaries or the local clone aren't there.

- **Diff mode** — if `diff-lsp` is on `PATH` and `metadata.repo_path` exists, the
  client posts the diff to `/api/prepare-diff-lsp`, opens the returned file over the
  `/api/lsp` WebSocket, and clicking a code line issues hover, references,
  definition, and typeDefinition in parallel. Coordinates are offset for the
  five-line context header and the diff's `+`/`-` prefix column; the clicked column
  is computed from the click's pixel position.
- **File mode** — inside the code viewer, connects to a real language server for that
  file's language via `/api/lsp-file?lang=`.
- **Popover** — inline (inside the comment form) or floating, listing hover text,
  definitions, type definitions and references. Clicking a reference opens a code
  viewer at that file and line.
- **Degradation** — the diff header shows "Repo not found locally. LSP disabled." or
  "LSP not active" instead of failing.

### 3.12 Code viewer and docking (`CodeViewerModal.tsx`, `dock_utils.ts`)

- **Floating viewer** — draggable, resizable window showing a full source file with
  syntax highlighting, virtualized rendering (`VirtualizedCodeBlock`) for large files,
  jump-to-line, a browsable/expandable file tree from `/api/list-files`, and its own
  LSP session. Full-screen and non-draggable on phones.
- **Docking** — drag a floating viewer onto the dock zone, or click ⊞ on a file header
  to open that file's diff as a tab. Tabs live beside a permanent "Review" tab.
- **Split view** — two side-by-side panels; tabs can be dragged between them
  (`application/x-tab` drag payload). Turning split off merges the right panel into
  the left. Split is force-disabled on phones.
- The docking state machine is pure and separately unit-tested (`dock_utils.test.ts`)
  — worth reading if you implement something similar.

### 3.13 Server configuration editor (`ConfigManager.tsx`, `config_utils.ts`)

A full editor for `~/.config/codereviewserver.toml`, in the Preferences → Server
Configuration tab. Built on `GetConfig` / `UpdateConfig`.

- Global settings: repos, sync interval, GitHub username, repo location, Jira domain,
  auto-worktree, desktop notifications.
- Workflow list: collapsible cards, add / delete (with confirmation) / reorder.
- **Pickers are built from the reply's `workflow_types` and `filters` registries**, not
  hard-coded — so the editor keeps working when the server gains a type or filter.
  Fields not used by the selected workflow type are hidden, and filters that require
  an argument get a second input.
- Validation runs client-side for immediate feedback and again on the server; both
  produce the same `{workflow, field, message}` shape, and messages are attached to
  the offending field. Messages stay hidden until the first save attempt.
- Server-side rejections (`okay: false` with `errors`) are rendered as field errors,
  not as an opaque failure. Stale server errors clear as soon as the draft changes.
- Dirty tracking, so Save is meaningful.

### 3.14 Preferences and theming

Client-side only, persisted in `localStorage`:

- **Theme** — 21 options (One Dark/Light, GitHub, Gruvbox, Solarized, Monokai,
  Dracula, Nord, Night Owl, Tokyo Night, the four Catppuccins, Everforest, Rosé Pine,
  SynthWave '84), applied as `data-theme` on `<html>` with CSS custom
  properties; initial value follows `prefers-color-scheme`. Legacy stored values are
  migrated through `resolveTheme`. The Prism diff theme is derived from the active
  theme.
- **Preferred review location** — clicking a PR title opens it in-app or on GitHub.
- **Diff font size** — five steps published as `--diff-font-size`; line numbers,
  gutters and hunk headers size in `em` off it so the whole diff scales together. The
  preference panel shows a live preview line.

### 3.15 Responsive / mobile behavior

`useMediaQuery` / `useIsMobile` drive real behavioral changes, not just CSS: line
wrapping defaults on, the diff becomes horizontally scrollable, line-number gutters
are hidden, split view is disabled, the code viewer goes full-screen, inline cards
stick to the left edge, and the bottom review action bar appears. The review list
restacks in CSS alone: the sidebar becomes a header, its state buckets a horizontally
scrolling strip, and row actions stay visible instead of waiting for a hover.

### 3.16 Miscellaneous

- **Escape** closes the plugins drawer, comment modal, submit modal, inline comment
  form, LSP popover, and outdated-comments drawer.
- Transient toasts for sync results; `alert()` for errors (a gap worth improving in a
  new client).
- Loading states on every async button.

---

## 4. Protocol methods used — and not used

Called by the current client:

`GetAllReviews`, `GetPR`, `GetAdjacentPR`, `SyncPR`, `AddComment`, `EditComment`,
`DeleteComment`, `SetFeedback`, `SubmitReview`, `GetPluginOutput`, `RerunPlugins`,
`GetHunkContext`, `GetConfig`, `UpdateConfig`.

Bridged in `server.ts` but not called by the UI: `RemovePRComments`, `ListPlugins`.

**Available in the protocol but unimplemented in this client** — the obvious feature
gaps if you're building a more complete one:

| Method | What you'd get |
|---|---|
| `MergePR` | Merge from the review view. Note the protocol's warning: check `merged` and `message`, don't infer success from the absence of an error. |
| `GetRateLimitStatus` | A rate-limit indicator; the server throttles below 100 remaining and blocks below 10. |
| `CheckRepoExists` | A cleaner LSP-availability check than relying on `metadata.repo_path`. |
| `Hello` | Health check / connection probe. |
| `RemovePRComments` | "Discard all pending comments" button. |

### Two calls beyond the core review loop

Both are documented in the protocol, but are easy to miss when scanning it for the
basics:

- [`GetHunkContext`](protocol.md#rpchandlergethunkcontext) — extra context lines
  around a hunk boundary, so a diff can be expanded without fetching the whole file.
  Powers the ↑/↓ buttons on hunk headers.
- [`RerunPlugins`](protocol.md#rpchandlerrerunplugins) — clears cached plugin results
  and re-executes. The reply means "accepted", not "finished": poll
  `GetPluginOutput` for results.

### Reply fields worth knowing about

- [`metadata.repo_path`](protocol.md#prmetadata-object) — absolute path to the local
  clone, distinct from `worktree_path`. The client uses it to gate LSP and the code
  viewer.
- [`feedback`](protocol.md#the-pr-payload) — the saved review feedback draft,
  returned with every PR payload so the client can restore the in-progress draft.
- [`annotations`](protocol.md#the-pr-payload) — plugin annotations aggregated
  server-side. The client derives its own from the plugin outputs instead (see
  [3.10](#310-plugin-annotations-in-the-diff-annotation_utilsts)), but this field is
  there for clients that don't poll plugins separately.

---

## 5. Reimplementation gotchas

Ordered roughly by how much time they'll cost you if missed.

1. **Comment positions are diff positions, not line numbers.** Follow the counting
   rules in [3.5](#35-diff-rendering-diffviewtsx-diff_utilsts) precisely, and
   exclude on-demand expanded context from the count.
2. **Cache keys use the short repo name.** Pass `Repo` as `code-review-server`, not
   `C-Hipple/code-review-server` — see the Cache Key Convention in the project's
   `CLAUDE.md`. Full names miss the DB caches and hit GitHub every time.
3. **Nothing on the read path blocks on plugins or the LLM.** `GetPR` returns
   immediately with whatever is cached; plugin output arrives via separate polling.
   Design your UI for results that show up late, and consider the list-view warm-up
   trick in [3.1](#31-review-list-prlisttsx).
4. **Mutations return the whole PR.** `AddComment`, `EditComment`, `DeleteComment`,
   `SetFeedback`, `SubmitReview` all reply with a full payload. Applying it wholesale
   is simpler and less error-prone than patching local state.
5. **`GetAdjacentPR` tells you where you landed.** Use `adjacent_owner` /
   `adjacent_repo` / `adjacent_number`, not `metadata.url`.
6. **`UpdateConfig` rejections are not RPC errors.** A refused config comes back
   `okay: false` with `errors`. Treat it as field-level validation feedback.
7. **`UpdateConfig` is partial, except `Workflows`.** Omitted fields keep their
   on-disk value; sending `Workflows` replaces the entire list.
8. **Build config pickers from the registries.** `workflow_types` and `filters` come
   with every `GetConfig` reply; hard-coded lists rot.
9. **Framing.** One JSON object per line on stdout; buffer partial reads. Monitor
   `stderr` — the server logs there.
10. **Plugin bodies can legitimately be empty.** Only fall back to raw `result` when
    `body_type` is missing or unrecognized.
11. **Annotations anchor to head-side line numbers**, and need a fallback bucket for
    lines the diff doesn't render.

---

## 6. Development and testing

```bash
cd bun_client
bun install

bun run dev          # bridge on :5172 + Vite on :5173 (bridge proxies to Vite)
bun start            # bridge only, serving frontend/dist

bun run build        # frontend → embedded assets → single `crs-gui` binary

bun run test         # frontend tests + bridge tests (bun:test)
bun run lint         # ESLint (frontend) + Biome (bridge)
bun run format:check # Prettier
bun run type-check   # tsc --noEmit
```

CI runs lint, format, type-check, and test for changes under `bun_client/`.

Testing approach worth borrowing: the hard logic is factored into pure modules with
unit tests, so it can be verified without rendering anything —
`diff_utils` (diff parsing and positions), `annotation_utils` (anchoring),
`plugin_utils` (response contract), `dock_utils` (tab/split state), `config_utils`
(config validation), `theme`, and `rpc_framing` (stdio framing). Several tests are
named `repro_*` and pin down specific past bugs — comment positions, statuses,
PR-specific parsing.

---

## 7. Suggested build order for a new client

1. **Bridge + list.** Spawn the server, frame stdio, call `GetAllReviews`, render the
   sections. This alone is a useful client.
2. **Read-only review.** `GetPR` → header, diff parsing, syntax highlighting, existing
   comments. Get positions right here; everything else depends on it.
3. **Write path.** `AddComment` / `EditComment` / `DeleteComment` → `SubmitReview`,
   plus `SyncPR` afterward.
4. **Navigation.** `GetAdjacentPR`, URL state, deep links.
5. **Plugins.** `GetPluginOutput` polling, the body contract, `RerunPlugins`.
6. **Extras.** Annotations in the diff, hunk expansion, the config editor, LSP, the
   code viewer.

Steps 1–4 give you a client comparable to the existing ones. Steps 5–6 are what make
the web client distinct.
