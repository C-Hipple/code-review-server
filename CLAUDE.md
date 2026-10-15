# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

**Go backend:**
- `go build -v ./...` — build all packages (binary: `codereviewserver`)
- `go install ./...` — install server + plugin binaries to `$GOPATH/bin`
- `go test -v ./...` — run all tests
- `go test -v ./server/` — run tests for a single package
- `go test -v -run TestName ./package/` — run a single test

**Bun web client (`bun_client/`):**
- `bun install` — install dependencies
- `bun run build` — build frontend
- `bun run test` — run tests
- `bun run test:e2e` — Playwright e2e suite (builds the frontend first); drives the real `server.ts` against a fake `crs` and fake language servers, see `bun_client/e2e/README.md`
- `bun run lint` — ESLint
- `bun run format:check` — Prettier check
- `bun run type-check` — TypeScript type checking

**CI runs** `go build && go test` for Go changes, and lint/format/type-check/test plus the e2e suite for `bun_client/` changes.

## Workflow

After building a feature, always open a pull request for it (once the change is committed and pushed to the working branch and the relevant build/tests pass).

## Architecture

JSON-RPC server (over stdio) that aggregates GitHub PRs into a review dashboard. Two main subsystems:

### Workflow Layer (`workflows/`)
Periodic background jobs fetch PRs from GitHub, apply filters, and persist results to SQLite.
- `manager.go` — `ManagerService` runs workflows on a schedule (configurable, default 10 min)
- `logic.go` — `PRToOrgBridge` converts `github.PullRequest` → `FileChanges`, applies filters, upserts into DB sections
- `builders.go` — builds `FileChanges` structs from PR data
- `auxdata.go` — in-memory auxiliary data (reviews, commits, CI status, review threads) used during workflow display

### Server Layer (`server/`)
RPC handlers serve data to clients (web UI, Emacs).
- `server.go` — RPC method dispatch (`GetAllReviews`, `GetPR`, `AddComment`, `SubmitReview`, the AI RPCs `ListAIFeatures`/`RunAIFeature`/`GetAIOutput`, etc.)
- `renderer.go` — `GetPRDetails()` fetches PR metadata/diff/comments/reviews, checking DB caches first; `aiDiscussion` partitions its comments into threads/conversation for the AI features
- `plugins.go` — async plugin execution
- `hooks.go` — the post-update hooks (plugins, automatic AI features), the request for the applied AI features when a client opens a PR (`requestAppliedAIFeatures`), and AI request assembly
- `applied.go` — reads the applied AI features' stored results into what the server serves: `orderDiffFiles` (file-ordering → the diff's file order) and `reviewEase` (review-ease → `review_ease`), falling back to the pre-AI `DiffFileOrderingCache`, which nothing writes any more

### Supporting Packages
- `database/` — SQLite with WAL mode. Sections, items, PR caches, local comments, plugin results, workflow action log (`workflow_action_log.go`)
- `config/` — TOML config from `~/.config/codereviewserver.toml`, accessed via `config.C()` (global singleton with RWMutex)
- `git_tools/` — GitHub API wrapper using go-github. REST first: every REST GET is revalidated with the ETag of the reply cached for it (`http_cache.go`, in memory), and GitHub doesn't charge a 304 against the rate limit, so re-asking about unchanged data is free; GraphQL can't be revalidated. A question both APIs can answer is a `Lookup` (`routing.go`) with an implementation on each — the review-request history (`team_reviews.go`: issue events or the timeline), who reacted to each comment (`reactions.go`: a request per reacted-to comment, or one query that also sees reactions on review bodies, which REST can't) and whether each PR merges cleanly (`mergeability.go`: one PR per request, or fifty per query) — and `RouteFor` picks the API from the `[GitHubAPI]` config table, REST by default; it is the seam for routing by remaining budget. Review-thread resolution (`review_threads.go`, `isResolved`) has no REST equivalent and always goes to GraphQL (`graphql.go`). The rate limiter tracks each budget GitHub meters separately — core, graphql, search — by `X-RateLimit-Resource` (`GetRateLimitStatusFor`)
- `images/` — downloads and caches the images embedded in PR bodies, reviews and comments to `$CRS_HOME/images`, content-addressed by URL. Only `github.com` / `*.githubusercontent.com` are fetched, since bodies are attacker-supplied text. Clients render from the cached file rather than from GitHub: a private repo's attachments are served only to a request carrying the server's token
- `llm/` — the text-generation `Client` interface built by `NewClient`, the stages a call fails at (`CallError`), and the call log: the AI runner appends every run to `~/.crs/llm_calls.log` via `AppendCallReport`. Two HTTP backends: Gemini (model pinned) and OpenRouter (`NewOpenRouterClient(model)`, a `ContextClient` so a deadline cancels the request)
- `openrouter/` — the OpenRouter chat-completions client, shared by `llm` and the bundled plugins (`cmd/internal/pluginkit`). It imports nothing from the module, so plugin binaries don't link the server's config/database (cgo sqlite)
- `ai/` — the AI feature layer (docs/ai_features.md): a `Registry` of `Feature`s (`comments-addressed`; `feature-flags`, which judges whether each hunk's logic change is behind a feature flag, a hunk that only adds definitions being judged where it's called; `change-diagram`, whose report is the raw Mermaid source of a flowchart of the PR's changes, which clients render themselves; and the applied features `file-ordering` and `review-ease`, an `AppliedFeature` being one whose result the server applies to what it serves — the diff's file order, the list's ease rating — rather than a report a client opens), the `Provider` (one-shot: Gemini or OpenRouter via `llm.Client`, or a command-backed CLI agent; `config.AIProviderFor` resolves the provider plus the command/model it reads) and `Agent` (tool loop on any provider) seams, and the `Runner` — cache check keyed by head SHA + `InputsDigest`, in-memory in-flight tracking (so "pending" never outlives a restart), a cap of two concurrent automatic runs, a per-feature timeout, and one call-log entry per run. A `CodeOnlyFeature` is keyed by head SHA alone (`KeyDigest`), so comments don't make it stale. Results go to the `AIResults` table. Configured by `[AI]`/`[[AIFeatures]]`, all off by default; the legacy root-level `ExperimentalLLM*` keys still work — `config.AIFeatureSettings` reads a legacy flag as an automatic entry for file-ordering or review-ease (`legacyLLMKeys`), so ask it, not `AIFeatures`, whether a feature is on
- `subprocess/` — runs a command with a timeout, capturing stdout/stderr; shared by plugins and the command provider
- `org/` — org-mode serialization for the Emacs client
- `utils/` — diff parsing utilities
- `cmd/` — plugin binaries (summarize_diff, security_check, etc.). The bundled LLM plugins call `pluginkit.ModelFromEnv()`, which picks Gemini or OpenRouter from `CRS_LLM_PROVIDER`/`CRS_LLM_MODEL` — set by the server from a `[[Plugins]]` entry's `Provider`/`Model` (`config.Plugin.Env`). `style_guidelines` is agentic instead: it drives its own two-phase tool loop (review, then validate the findings) over `pluginkit.Chat`, which speaks Gemini function calling and OpenRouter tool calls natively, and reads its guide from a file or a directory (`CRS_STYLE_GUIDE_DIR`)

### Clients
- `bun_client/` — Bun + React web UI. `server.ts` bridges HTTP/WebSocket to the Go backend's stdio, and `lsp_pool.ts` keeps language servers (`diff-lsp`, `gopls`, ...) running across LSP WebSockets so reopening a review doesn't re-index the workspace. The change diagram is drawn with the `mermaid` library (`MermaidDiagram.tsx`), imported lazily in `mermaid_utils.ts` so it stays out of the main bundle; its Copy image / Download image buttons rasterize the drawn SVG to a PNG on a canvas (`mermaid_image.ts`), made in the background once the diagram is drawn and kept for both, within a pixel budget and time limits
- `client.el/` — Emacs client, split into modules with `crs-client.el` as the entry point (`crs-vars`, `crs-html`, `crs-rpc`, `crs-render`, `crs-list-mode`, `crs-review`, `crs-comments`, `crs-review-actions`, `crs-plugins`, `crs-ai`, `crs-diagram`). `crs-diagram` shows the change diagram in a `mermaid-mode` buffer (an optional dependency) through `crs-ai`'s fetch/poll machinery (`crs--ai-insert-function`). A new module must also be added to the byte-compile list in `.github/workflows/all-checks.yml`

## Key Data Flow

1. **Workflow fetch**: GitHub API → filter PRs → `ProcessPRsDB` upserts into `sections`/`items` tables, first recording each open PR's mergeability in `PRMergeability` (`recordMergeability`, mergeability.go) — read from a single-PR REST response when the PR came from one, otherwise looked up through `git_tools.GetMergeability` (a single-PR request each over REST, batches of fifty over GraphQL) — which the list serves as `merge_conflicts` and a `:conflict:` headline tag
2. **Aux data fetch**: `fetchAuxDataForPR` (manager.go) fetches reviews/commits/CI/review-threads and persists to DB caches, recording each write in `WorkflowActionLog` (workflow name, head SHA, fields written). What gets fetched is the union of what the workflows' filters asked for and what `applyCacheWarmRequirements` adds for a PR that is new or has a new head SHA — the warm covers every field `GetPRDetails` reads, so opening a review is a pure cache hit
3. **Post-update hooks**: for each PR the cycle added or re-fetched after a push, `notifyPRsUpdated` (manager.go) calls the hook registered via `workflows.SetPRUpdatedHook` — `server.WarmPRAnalysis` in server mode — which runs the plugins and any AI features configured `Automatic` in the background so they're cached before anyone opens the review. A client opening a PR (`ensurePostUpdateHooks`) runs the same hooks and also requests the enabled applied AI features, which no client asks for itself
4. **Image prefetch**: `ensurePostUpdateHooks` also hands every image URL found in the description, reviews and comments to `images.Store.Prefetch`, ahead of the head-SHA debounce — a new comment carrying a screenshot doesn't change the SHA. `PRPayload.images` then reports where each one landed, and `GetImage` fetches on demand anything the prefetch hasn't reached
5. **Client request**: RPC `GetPR` → `GetPRDetails` (renderer.go) → checks DB caches → falls back to GitHub API
6. **Rendering**: `OrgRenderer` reads sections/items from DB, sorts by priority, returns org-mode or JSON

Nothing on the read path waits for a plugin, an LLM call or an AI feature: `ensurePostUpdateHooks` dispatches them in goroutines, `orderDiffFiles` applies the stored file-ordering result or falls back to `sortFilesTestsLast`, and AI results are only ever read — by `GetAIOutput`, `orderDiffFiles` and `reviewEase` — none of which starts a run. The workflow layer cannot import `server` (`server` imports `workflows`), which is why step 3 goes through a registered callback wired up in `main.go`.

## Cache Key Convention

DB cache tables use the **short repo name** (e.g., `code-review-server`), NOT the full name (`C-hipple/code-review-server`). `GetName()` returns the short name; `GetFullName()` returns `owner/repo`. All cache lookups from `GetPRDetails` use the short name from RPC args.

## Environment Variables

- `CRS_GITHUB_TOKEN` — required GitHub API token
- `CRS_HOME` — override `~/.crs` directory
- `GEMINI_API_KEY` — for the summarization plugin and AI features using the `gemini` provider (`ai/`, through `llm/`)
- `OPENROUTER_API_KEY` — for AI features and plugins using the `openrouter` provider
- `CRS_STYLE_GUIDE_DIR` — a directory of Markdown files for the `style_guidelines` plugin, in place of `~/.config/style_guidelines.md`
- `CRS_LLM_PROVIDER` / `CRS_LLM_MODEL` — which backend the bundled LLM plugins call; the server sets them per plugin, and a value exported to the server reaches every plugin that doesn't set its own
