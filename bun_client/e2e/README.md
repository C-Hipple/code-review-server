# End-to-end tests

Playwright drives the built web UI through the real Bun bridge (`server.ts`).
Every process the bridge spawns is replaced with a fake, so the suite needs
no GitHub token, network, or SQLite database:

| Bridge spawns                              | E2E stand-in          | What it does                                                                                                                                                                                                                                                                                                                                                                        |
| ------------------------------------------ | --------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `crs --server` (Go backend)                | `harness/fake_crs.ts` | Newline-delimited JSON-RPC over stdio, serving the PRs in `fixtures/prs.ts`, with in-memory comments, reviews and feedback. Enables the comments-addressed AI feature, whose runs read `pending` for one poll; change-diagram is off unless a spec calls `backend.setDiagram`. Review-ease is on, rating three of the four PRs, unless a spec calls `backend.setReviewEase(false)`. |
| `diff-lsp`                                 | `harness/fake_lsp.ts` | Reads the bridge's tempfile like the real diff-lsp and resolves positions the same way (1-indexed tempfile line, raw diff column).                                                                                                                                                                                                                                                  |
| `typescript-language-server` (code viewer) | `harness/fake_lsp.ts` | Plain 0-indexed positions into the document from `didOpen`.                                                                                                                                                                                                                                                                                                                         |

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
handler — carries the error in `refused`. `E2E.ForgetUser` drops a user the
way the server's retention sweep does (with `Disconnect: true` it also hangs
up, as a restart would), and `E2E.SetIdentityActions` opens the identity gate.

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
