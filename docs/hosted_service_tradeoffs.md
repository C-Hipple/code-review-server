# Hosting CRS as a Shared Service: Tradeoff Analysis

**Status:** decision doc — no code changes proposed here.
**Question:** CRS today is LSP-shaped: each client spawns its own `crs --server`
over stdio. To host it once for a whole company — GitHub OAuth login,
server-side syncs that run whether or not a client is open, workflows
configured through the client starting from the defaults — which architecture
gets there without turning the project into a mess?

Two candidate strategies were on the table, plus a third worth naming so it
can be explicitly deferred:

- **Strategy A — de-globalized core** ("abstract the Go code into a shared
  library used by both the local binary and the new service"): one hosted
  process runs N in-process per-user instances of the core.
- **Strategy B — per-user backend processes** ("a new central client that
  spawns instances of the current Go backend as necessary"): an orchestrator
  handles OAuth and routing, and runs one `crs --server` process per user.
- **Strategy C — full multi-tenant rewrite**: `user_id` columns on every
  table, shared fetch pool, one scheduler. Named here only to be rejected
  for now.

**Recommendation (details in §7): ship Strategy B first, then do Strategy A's
de-globalization incrementally behind the RPC seam — and only if a concrete
trigger appears.** B is not a detour: everything it requires except the
process table (OAuth, user registry, session routing, per-user data dirs,
deploy image) is also required by A and C and carries over unchanged.

---

## 1. What the hosted service must do

1. **GitHub OAuth login** — a user authorizes the service; the service holds a
   per-user token. Non-negotiable even beyond auth: `SubmitReview` and
   `MergePR` act *as* the token's owner, so a shared token would post every
   review from one GitHub account.
2. **Server-side syncs, decoupled from clients** — the workflow scheduler runs
   24/7 per user; closing the browser must not stop syncs. This is the
   specific thing the current client-spawns-server model cannot do.
3. **Per-user config** — each user configures workflows through the client,
   starting from `config.DefaultConfigTOML`.
4. **Local mode keeps working** — the Emacs/TUI/local-web flow stays
   first-class.

## 2. What the current design already gives us

More is in place than the "LSP-shaped" framing suggests:

- **A single-user hosted deployment already exists.** The `Dockerfile` builds
  `crs-gui` (the compiled Bun server) which spawns `crs --server` in a
  container, with `CRS_HOME` and `XDG_CONFIG_HOME` relocating *all* state —
  DB, config, logs — under per-instance directories. Multi-user is the only
  missing dimension.
- **The RPC protocol is already the seam.** `bun_client/server.ts` bridges
  HTTP/WebSocket to stdio through a single `RpcBridge` (`server.ts:133`);
  browser tabs already multiplex over one backend via `net/rpc` request IDs.
  Clients cannot tell what sits behind the bridge, so the backend's shape can
  change without touching any client.
- **First-run provisioning is solved.** With no config file, the server runs
  the built-in defaults (#215), and `config.LoginResolver` fills in
  `GithubUsername` from the token. A brand-new user needs an empty directory
  and a token — nothing else.
- **Per-user storage is cheap.** One WAL-mode SQLite file per user keeps the
  entire `database/` package unchanged under every strategy short of C.
- **Per-user tokens shard the rate limit.** GitHub's 5,000 req/hr limit is
  per token, so per-user syncs don't compete for one budget the way today's
  single `CRS_GITHUB_TOKEN` would.

## 3. The actual obstacle: the process is the tenancy boundary

The codebase's invariants are process-scoped. That is not an accident to be
fixed so much as the design assumption to either *keep* (B: multiply
processes) or *move into the code* (A: instances). The inventory of
process-wide state, from a pass over the code:

| Process-wide assumption | Where | If two users shared one process today |
|---|---|---|
| Config + DB singleton, `config.C()` | `config/config.go:96` — ~130 call sites across 22 files | Both users read one config and one DB |
| Token read from env per call; `os.Exit(1)` if missing | `git_tools/git_tools.go:492` | One identity per process; a missing token kills everyone |
| Rate-limit manager, 50-slot API semaphore, TTL data cache | `git_tools/git_tools.go:229,389,718` | Counters and throttling conflate tokens; cached CI status crosses token visibility |
| Aux-data store swapped per sync cycle | `workflows/auxdata.go:73`, `manager.go:525` | Two concurrent cycles overwrite each other's store |
| PR-updated hook | `workflows/manager.go:35` | Shared callback with no way to say *whose* PR updated |
| Sync advisory flock in `$HOME/.crs` (ignores `CRS_HOME`) | `workflows/manager.go:612` | Second process **silently skips all syncs** |
| Global plugin-process cap | `server/plugins.go:25` | Harmless shared, wrong per-user |
| Hook-dispatch debounce map | `server/server.go:262` | One user's open suppresses another's plugin run |
| `~/.crs` log files (`llm_calls.log`, cache-miss log) | `llm/call_log.go`, `server/renderer.go` | Interleaved across users |
| Desktop notifications via `osascript` | `workflows/notifications.go` | Meaningless on a server |
| Local-repo features: worktrees, `git show` context fallback | `manager.go:287`, `server.go:1000` | Already degrade gracefully (skip / fall back to API) |

Everything in this table is trivially correct under one-process-per-user and
individually wrong under naive in-process multi-tenancy. That asymmetry is
the heart of the tradeoff.

## 4. Strategy A — de-globalized core, one service process

**What it actually is.** Not a new repo and not really a "library extraction"
— the packages (`server/`, `workflows/`, `database/`, `config/`, `git_tools/`)
*already are* the shared library; `cmd/` already holds multiple binaries. The
work is making the packages **instantiable**: an `App` (or `Instance`) struct
owning config, DB handle, GitHub client factory, rate limiter, caches,
scheduler, and log paths, threaded through everything that currently reaches
for a global. The hosted binary runs one `App` per user in one process; the
local binary runs exactly one `App` wired to stdio — same code, no fork.

**Work items** (rough, assuming one experienced dev):

| Item | Size |
|---|---|
| Thread `*App` through the ~130 `config.C()` sites + DB handles (mechanical, compiler-driven) | 3–5 days |
| Per-instance GitHub client/`TokenSource`, rate limiter, semaphore, data cache | 2–3 days |
| Per-instance scheduler, aux-data store, hooks, log paths; remove flock | 2–3 days |
| Concurrency audit + `recover()` at instance boundaries (a panic in one user's goroutine currently kills the process) | 1–2 days |
| Hosted binary: HTTP/WS transport reusing the jsonrpc codec, instance registry | 2–3 days |
| OAuth, sessions, user registry, encrypted token store *(shared with B)* | 4–7 days |
| Regression risk on local mode + tests that use `config.SetC` | ongoing tax |

Realistically **3–5 weeks to first hosted login**, most of it before anything
is demonstrable.

**Pros**

- Cleanest end state: one process, one deploy, one upgrade; no supervisor.
- The de-globalization pays off even locally — tests stop fighting
  `config.SetC`, and running two accounts locally becomes possible.
- Only strategy that opens *cross-user* wins later: shared PR/diff caches
  keyed by `(repo, PR, SHA)` (diffs are user-independent), org-wide
  dashboards, one shared fetch for a repo 30 people watch.
- Bounded per-process resources; one pprof to watch.

**Cons / risks**

- Longest time-to-value, and the value is invisible until the very end.
- The table in §3 is a minefield of latent concurrency bugs — the aux-data
  store and rate-limit conflation are *correctness* issues, not style. Each
  must be found and reasoned about; missing one produces cross-user data
  bleed, which is the worst failure mode a multi-user service can have.
- Failure isolation is worse than B until deliberately engineered: one
  user's panic, OOM, or `os.Exit` takes down everyone.
- This is where the feared "large mess" genuinely lives — not in the
  mechanical injection (the compiler drives that), but in the long tail of
  process-wide assumptions nobody has listed yet.

## 5. Strategy B — orchestrator spawns per-user backends

**What it actually is.** Generalize what `bun_client/server.ts` already does
for one user. The orchestrator (grow `server.ts`, or a small new Go binary —
the pattern is identical) owns: GitHub OAuth, session cookies, a user
registry, and a process table `login → RpcBridge`. On login it ensures a
`crs --server` exists for that user, spawned with
`HOME`/`CRS_HOME`/`XDG_CONFIG_HOME` pointing at `/data/users/<login>/` and
`CRS_GITHUB_TOKEN` from the token store, then routes that user's WS/HTTP
traffic to their bridge. Processes stay up after the browser closes — that
alone delivers requirement 2. The Go core keeps believing it is a single-user
process, because it is one.

**Go changes required** (small, and worth doing regardless):

1. **Fix the sync flock to respect `CRS_HOME`** (`workflows/manager.go:612`
   uses `os.UserHomeDir()` directly). As-is, per-user processes sharing one
   real `$HOME` would contend on one lock file and **all but one user's syncs
   would silently stop**. Setting per-process `HOME` also works, but the fix
   is a two-line correctness change. *(hours)*
2. **Gate desktop notifications / local-only features off in hosted mode**
   (env flag or config default). Worktrees and local `git show` already
   degrade gracefully. *(hours)*
3. **Token indirection for refresh**: `GetGithubClient` re-reads the env on
   every call, but a process's env is frozen at spawn. With a classic OAuth
   app (non-expiring tokens) this is moot; if a GitHub App with expiring
   tokens is chosen, support `CRS_GITHUB_TOKEN_FILE` so the orchestrator can
   rotate tokens without restarting backends. *(half a day)*

**Orchestrator work:** OAuth flow + encrypted token store + registry
(4–7 days, same as A), process manager with restart/backoff and log tagging
(2–4 days), routing (1–2 days, mostly exists). Realistically **1.5–2.5 weeks
to first hosted login**, demonstrable from week one.

**Pros**

- Fastest path, by roughly 2×, with near-zero regression risk: the business
  logic is untouched, so local mode and hosted mode cannot drift.
- OS-grade isolation per user: a panic, a wedged sync, a poisoned config, or
  `os.Exit(1)` on a bad token affects exactly one user. Every global in §3
  is automatically correct because each user gets their own copy.
- Per-user rate limiting, plugin caps, and log files fall out for free.
- Upgrades are boring: replace the binary, rolling-restart child processes.
- `GetRateLimitStatus` and friends stay truthful per user with no work.

**Cons / risks**

- **N always-on processes.** Each runs its own 10-minute scheduler and
  SQLite. Expect tens of MB RSS per idle backend (measure before capacity
  planning); at 25 users this is a non-issue on one modest VM, at 100+ it is
  a real but still tractable memory bill.
- **Ops surface**: supervision, crash-restart, log aggregation, deciding
  whether departed users' processes get reaped. This is genuine new
  operational code, just well-trodden (it is a tiny PaaS).
- **Thundering herd**: `prefetchConcurrency = 8` *per process*; N processes
  can put N×8 GitHub calls and many plugin subprocesses in flight at once.
  Mitigate by jittering each user's sync phase (spawn-time offset), which the
  orchestrator can do without touching Go.
- **Redundant fetches**: 30 users watching one repo means 30 fetches of the
  same PR list and diffs. Each spends their *own* rate-limit budget, so it's
  waste, not breakage — but only Strategy A/C can ever deduplicate it.
- **Mess risk lives in the orchestrator**: it will be tempting to put user
  management, config templating, and feature logic there, creating a second
  brain in TypeScript. Needs a stated rule (see §7 guardrails).
- Tokens passed as env vars are visible in `/proc` inside the container —
  acceptable for an internal single-trust-domain deployment, worth noting;
  the `CRS_GITHUB_TOKEN_FILE` change also closes this.

## 6. Strategy C — full multi-tenant rewrite (rejected for now)

`user_id` on every table, one shared DB (likely Postgres), one scheduler
multiplexing all users, shared fetch pool with per-token attribution. This is
the "proper SaaS" shape — and it is the option with the highest certainty of
becoming the feared mess: it rewrites the DB layer *and* the workflow engine
*and* the cache-key convention (short-repo-name keys would collide across
users immediately) in one motion, for benefits (fetch dedup, org features,
1000-user scale) that an internal deployment does not need on day one.
Everything in A is a prefix of C, so deferring C costs nothing.

## 7. Comparison and recommendation

| Axis | A: de-globalized core | B: per-user processes | C: full rewrite |
|---|---|---|---|
| Time to first hosted login | 3–5 wks | **1.5–2.5 wks** | 6–10 wks |
| Go core changes | Large, mechanical + subtle | **~3 small fixes** | Very large |
| Cross-user data-bleed risk | Real until audited | **~None (OS boundary)** | Real |
| Failure isolation | Process-wide until engineered | **Per user** | Process-wide |
| Resources at ~25 users | **One process** | ~25 small processes (fine) | One process |
| Resources at 100+ users | **Scales in-process** | Memory bill, still viable | **Best** |
| Ops surface | One service | Supervisor + N processes | One service + Postgres |
| Redundant GitHub fetches | Fixable later | Inherent | **Deduplicated** |
| Local mode risk | Regression tax during refactor | **Zero drift** | Forked or rewritten |
| Unlocks org dashboards / shared caches | Eventually | Never | Yes |
| Where the mess accumulates | Long tail of globals | Orchestrator scope creep | Everywhere at once |

**Recommendation: B now, A incrementally, C never (until proven needed).**

The decisive observations:

1. **The expensive parts of B are needed under every strategy.** OAuth, token
   store, user registry, per-user data dirs, session routing, the deploy
   image — all of it carries over to A unchanged. The only B-specific
   artifact is the process table, a few hundred lines. Choosing B defers
   almost nothing and discards almost nothing.
2. **B preserves the code's invariants; A rewrites them.** The system was
   built process-scoped ("modeled around LSPs"). B leans into that and gets
   §3's entire table correct by construction. A must fix every row and find
   the rows not yet on the list.
3. **The RPC seam makes B → A a refactor, not a migration.** Clients talk
   JSON-RPC either way; users' data stays in per-user SQLite files either
   way. If A later collapses N processes into N in-process instances, nothing
   above the seam notices.

**Phased plan**

- **Phase 0 — hardening (days):** flock fix, hosted-mode gate for
  notifications, `CRS_GITHUB_TOKEN_FILE`, image tweak to run without a
  mounted config.
- **Phase 1 — orchestrator MVP (1–2 wks):** OAuth app + login flow, encrypted
  token store, user registry, process table with spawn/restart/route,
  per-user data dirs, sync-phase jitter.
- **Phase 2 — ops (days):** log tagging per user, health/restart policy,
  reap-or-keep decision for inactive users, backup of `/data/users/*`.
- **Phase 3 — opportunistic de-globalization (background, no deadline):**
  new code takes dependencies explicitly instead of calling `config.C()`;
  existing globals get folded into an `App` struct package by package as
  they're touched. No big bang; local binary constructs one `App`.
- **Phase 4 — only on trigger:** collapse processes into in-process
  instances. Triggers: memory pressure at ~100+ users, a real need for
  shared caches or org-wide views, or the process supervisor demonstrably
  costing more upkeep than the refactor.

**Guardrails so B doesn't become the mess**

- The orchestrator does **auth, spawn, route — nothing else**. Any feature
  that knows what a workflow or PR *is* belongs in the Go core behind RPC.
- One deploy artifact: the existing image grows the orchestrator; per-user
  backends are the same `crs` binary local users run.
- Write down the env contract (`HOME`, `CRS_HOME`, `XDG_CONFIG_HOME`,
  `CRS_GITHUB_TOKEN[_FILE]`, `GEMINI_API_KEY`) as the interface between
  orchestrator and backend, and treat it as versioned API.

## 8. Gotchas that apply regardless of strategy

- **Attribution**: per-user tokens are required for reviews/merges to be
  posted by the right GitHub account — this, not politeness, is why a shared
  service can't run on one PAT.
- **Token lifetime**: pick classic OAuth app (non-expiring, simpler) vs
  GitHub App (expiring + refresh, org-installable, finer permissions) early;
  it decides whether token rotation machinery is needed at all.
- **Gemini quota**: `GEMINI_API_KEY` stays a single shared server key;
  plugins and LLM analysis across all users draw one budget. Consider
  per-user opt-in defaults for the experimental LLM flags.
- **Sizing**: measure idle RSS and steady-state CPU of one `crs --server`
  under a realistic config before promising a user count.
- **Backups**: per-user SQLite is all derived cache except local pending
  comments and feedback — small, but users will notice losing half-written
  reviews; back up `/data/users/*` accordingly.
- **Departed users**: a reaped process stops syncs (that's the point), but
  the registry needs an explicit disable so an orphaned token isn't polling
  GitHub forever.
