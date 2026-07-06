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
- `bun run lint` — ESLint
- `bun run format:check` — Prettier check
- `bun run type-check` — TypeScript type checking

**CI runs** `go build && go test` for Go changes, and lint/format/type-check/test for `bun_client/` changes.

## Architecture

JSON-RPC server (over stdio) that aggregates GitHub PRs into a review dashboard. Two main subsystems:

### Workflow Layer (`workflows/`)
Periodic background jobs fetch PRs from GitHub, apply filters, and persist results to SQLite.
- `manager.go` — `ManagerService` runs workflows on a schedule (configurable, default 10 min)
- `logic.go` — `PRToOrgBridge` converts `github.PullRequest` → `FileChanges`, applies filters, upserts into DB sections
- `builders.go` — builds `FileChanges` structs from PR data
- `auxdata.go` — in-memory auxiliary data (reviews, commits, CI status) used during workflow display

### Server Layer (`server/`)
RPC handlers serve data to clients (web UI, Emacs).
- `server.go` — RPC method dispatch (`GetAllReviews`, `GetPR`, `AddComment`, `SubmitReview`, etc.)
- `renderer.go` — `GetPRDetails()` fetches PR metadata/diff/comments/reviews, checking DB caches first
- `plugins.go` — async plugin execution

### Supporting Packages
- `database/` — SQLite with WAL mode. Sections, items, PR caches, local comments, plugin results
- `config/` — TOML config from `~/.config/codereviewserver.toml`, accessed via `config.C()` (global singleton with RWMutex)
- `git_tools/` — GitHub API wrapper using go-github v48
- `llm/` — non-plugin LLM calls (experimental diff file ordering + review-ease rating) behind a `Client` interface; Gemini is the only backend today. Appends every call to `~/.crs/llm_calls.log`
- `org/` — org-mode serialization for the Emacs client
- `utils/` — diff parsing utilities
- `cmd/` — plugin binaries (summarize_diff, security_check, etc.)

### Clients
- `bun_client/` — Bun + React web UI. `server.ts` bridges HTTP/WebSocket to the Go backend's stdio
- `client.el/` — Emacs client, split into modules with `crs-client.el` as the entry point (`crs-vars`, `crs-html`, `crs-rpc`, `crs-render`, `crs-list-mode`, `crs-review`, `crs-comments`, `crs-review-actions`, `crs-plugins`)

## Key Data Flow

1. **Workflow fetch**: GitHub API → filter PRs → `ProcessPRsDB` upserts into `sections`/`items` tables
2. **Aux data fetch**: `fetchAuxDataForPR` (manager.go) fetches reviews/commits/CI and persists to DB caches
3. **Client request**: RPC `GetPR` → `GetPRDetails` (renderer.go) → checks DB caches → falls back to GitHub API
4. **Rendering**: `OrgRenderer` reads sections/items from DB, sorts by priority, returns org-mode or JSON

## Cache Key Convention

DB cache tables use the **short repo name** (e.g., `code-review-server`), NOT the full name (`C-hipple/code-review-server`). `GetName()` returns the short name; `GetFullName()` returns `owner/repo`. All cache lookups from `GetPRDetails` use the short name from RPC args.

## Environment Variables

- `CRS_GITHUB_TOKEN` — required GitHub API token
- `CRS_HOME` — override `~/.crs` directory
- `GEMINI_API_KEY` — for the summarization plugin and the experimental LLM diff analysis (`llm/`)
