# End-to-end tests

Playwright drives the built web UI through the real Bun bridge (`server.ts`).
Every process the bridge spawns is replaced with a fake, so the suite needs
no GitHub token, network, or SQLite database:

| Bridge spawns                              | E2E stand-in          | What it does                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| ------------------------------------------ | --------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `crs --server` (Go backend)                | `harness/fake_crs.ts` | Newline-delimited JSON-RPC over stdio, serving the PRs in `fixtures/prs.ts`, with in-memory comments, reviews and feedback. Enables the comments-addressed AI feature, whose runs read `pending` for a moment; change-diagram is off unless a spec calls `backend.setDiagram`. Review-ease is on, rating three of the four PRs, unless a spec calls `backend.setReviewEase(false)`. Offers one chat backend (a stand-in for Claude Code) unless a spec calls `backend.setChatAvailable(false)`. |
| `diff-lsp`                                 | `harness/fake_lsp.ts` | Reads the bridge's tempfile like the real diff-lsp and resolves positions the same way (1-indexed tempfile line, raw diff column).                                                                                                                                                                                                                                                                                                                                                              |
| `typescript-language-server` (code viewer) | `harness/fake_lsp.ts` | Plain 0-indexed positions into the document from `didOpen`.                                                                                                                                                                                                                                                                                                                                                                                                                                     |

The fake `crs` also answers `Subscribe`: it sends a `Server.Heartbeat` every
second (the launcher sets `CRS_HEARTBEAT_SECONDS=1`) until a spec calls
`backend.setHeartbeat(false)`, which `E2E.Reset` undoes. `backend.updatePR(pr, ...)`
plays a webhook delivery: it changes the PR's fixture (a title, a conversation
comment, text swapped in the diff) and sends the `Server.PRUpdated` the server would
(`live_updates.e2e.ts`).

It serves filtered diffs too (`ListDiffFilters`, `GetFilteredDiff`): on
`acme/widgets#42`, "Since my last review" is the `src/main.ts` part of its diff;
every other filter and PR has no baseline.

It runs plugin re-runs and AI runs the way the server does: in the
background, on a timer, announcing each step with a `Server.PluginOutputChanged`
or `Server.AIOutputChanged` notification (a re-run is cleared, then marked
pending, then lands; an AI run is pending, then lands). The pages read the
output again on each announcement and never poll for it. `backend.holdRuns(true)`
keeps runs short of landing until `backend.holdRuns(false)`, so a spec can look
at a pending run, or check that nothing is asked for while one is in flight;
`backend.runPlugin` and `backend.runAIFeature` start a run as the server would
on its own, with nothing on the page asking for it. A review chat's turn runs
the same way (`chat.e2e.ts`): the reply is drafted, the agent reads `pr.diff`,
then the reply lands, each step announced with a `Server.ChatChanged`; held
runs stop it short of the reply.

Both LSP roles answer hover / definition / references / typeDefinition from the
declarations in `fixtures/repo`, the checkout PR `acme/widgets#42` points at.

Started with `--listen host:port`, `fake_crs.ts` stands in for a hosted
`crs --listen` instead: the same methods over TCP, each connection a session
that must `Authenticate` or `RegisterUser` first, answered with the Go
server's own error strings and reply fields — its method policy
(`not authenticated:` before a session, `permission denied:` for the
administrator's methods and for `SkipCache`), and its write resolver for
`SubmitReview` / `MergePR`: a user who linked their GitHub account writes as
themselves, the open identity gate lends the server's account, and otherwise
the write is refused with `refused: link-github:` where the server can link
accounts and `refused:` where it cannot. `StartGitHubLink`, `GetGitHubLink`
and `UnlinkGitHub` keep a device flow per user, which a spec ends the way
GitHub would.

Its `GetImage` serves one screenshot (`SCREENSHOT_URL` in `harness/test.ts`;
embed it with `backend.setPRBody`): from a real file to a local bridge, and
from a path that only exists on "the server's host" to a remote one, which
has to ask for the bytes.

## Running

```bash
cd bun_client
bun run test:e2e                     # builds the frontend, then runs everything
bunx playwright test -c e2e/playwright.config.ts lsp   # one spec, frontend already built
bun run type-check:e2e
```

Playwright's Chromium must be installed (`bunx playwright install chromium`).

`harness/start_server.ts` boots each bridge with a `PATH` containing only shims
for the fakes, so nothing installed on the machine leaks in. Two bridges run:

- `:5190` — fake `crs`, fake `diff-lsp`, fake TypeScript server
- `:5191` — fake `crs` and no language servers (the "LSP not active" paths)
- `:5193` — remote mode (`remote.e2e.ts`, `remote_link.e2e.ts`): no `crs` on PATH at all; the
  bridge connects with `CRS_REMOTE` to a fake `crs --listen` on `:5194` and
  registers with `CRS_REGISTRATION_KEY`. Its `HOME`, `XDG_CONFIG_HOME` and
  `CRS_HOME` are under its state directory, holding the TOML from
  `fixtures/remote_user.ts` and the token it is issued, so nothing of the
  machine's leaks in (the launcher also strips any `CRS_REMOTE*` /
  `CRS_LOGIN` / `CRS_REGISTRATION_KEY` from every bridge's environment).

The bridge pools language servers per workspace (`lsp_pool.ts`), so a fake
server started by one test can still be answering in the next: the fake
`diff-lsp` re-reads the tempfile named by each request's document, and tests
that need a fresh server pass a unique `worktree` when preparing the tempfile.

Set `CRS_E2E_PORT` to move them. Each test resets the fake backend
(`E2E.Reset`), and specs assert on what the UI sent through `backend.calls()`
and `lsp.received()` (see `harness/test.ts`).

The remote fake keeps its registered users and its log of `Authenticate` /
`RegisterUser` calls across `E2E.Reset` — the bridge registers once, as it
starts — but not their GitHub links: every test starts unlinked, on a server
that cannot link until the spec says otherwise. Specs read that log with
`backend.authLog()`, and a call the server refused — by its policy or in the
handler — carries the error in `refused`.

Drafts and feedback are each login's own, as on the server, and so is the
dashboard: a user's is empty after registering — and after every reset —
until the spec runs a workflow cycle with `backend.runCycle()`, which builds
every user's from the fixtures. The server's own (stdio, and the admin token)
always has them. `E2E.ForgetUser` drops a user with their drafts, feedback and
dashboard, the way the server's retention sweep does (with `Disconnect: true`
it also hangs up, as a restart would), and `E2E.SetIdentityActions` opens the
identity gate. `backend.dropNext(method)` carries out the next call to
`method` and then hangs up instead of answering, as a connection lost mid-call
would. Like the server, `SubmitReview` posts nothing and answers
`drafts changed:` when a pending comment named in its `ExpectedDraftIDs` is
gone.

For GitHub linking (`remote_link.e2e.ts`), `backend.setGitHubLinkAvailable`
lets the server link accounts (its codes poll every second, not GitHub's
five), `backend.gitHubLink` reads a user's flow — with the code the dialog
should show — and stored link, `backend.endGitHubLink` ends a pending flow as
GitHub would (`linked`, `denied`, `expired`, `mismatch` or `error`),
`backend.linkGitHub` links without a flow, and `backend.breakGitHubLink`
revokes a link on "GitHub" (the next write is refused with
`refused: link-github:` and clears it) or makes it unreadable to the server.

## Real language servers (opt-in)

`lsp_real.e2e.ts` runs the diff and code-viewer LSP flows against the real
`diff-lsp` and `typescript-language-server` on a third bridge (`:5192`), which
checks the fake's position contract against the real one:

```bash
cargo install --git https://github.com/C-Hipple/diff-lsp
npm install -g typescript-language-server typescript@5   # 7.x ships no tsserver.js
CRS_E2E_REAL_LSP=1 bun run test:e2e
```

## Known bugs

One test is marked `test.fail()` because it describes behavior that is
currently broken. Remove the marker when the bug is fixed:

- **PR load errors** (`review.e2e.ts`): `Review` sets its content to
  "Error loading PR." but never renders it, so a PR that fails to load shows an
  empty page.
