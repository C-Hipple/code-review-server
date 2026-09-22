# End-to-end tests

Playwright drives the built web UI through the real Bun bridge (`server.ts`).
Every process the bridge spawns is replaced with a fake, so the suite needs
no GitHub token, network, or SQLite database:

| Bridge spawns                              | E2E stand-in          | What it does                                                                                                                       |
| ------------------------------------------ | --------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `crs --server` (Go backend)                | `harness/fake_crs.ts` | Newline-delimited JSON-RPC over stdio, serving the PRs in `fixtures/prs.ts`, with in-memory comments, reviews and feedback.        |
| `diff-lsp`                                 | `harness/fake_lsp.ts` | Reads the bridge's tempfile like the real diff-lsp and resolves positions the same way (1-indexed tempfile line, raw diff column). |
| `typescript-language-server` (code viewer) | `harness/fake_lsp.ts` | Plain 0-indexed positions into the document from `didOpen`.                                                                        |

Both LSP roles answer hover / definition / references / typeDefinition from the
declarations in `fixtures/repo`, the checkout PR `acme/widgets#42` points at.

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

Set `CRS_E2E_PORT` to move them. Each test resets the fake backend
(`E2E.Reset`), and specs assert on what the UI sent through `backend.calls()`
and `lsp.received()` (see `harness/test.ts`).

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

Two tests are marked `test.fail()` because they describe behavior that is
currently broken. Remove the marker when the bug is fixed:

- **LSP popover close button** (`lsp.e2e.ts`): `.hover-line:hover { filter }`
  in `App.css` turns the hovered row into a stacking context, so the popover
  slides under the next diff row and its × receives no clicks.
- **PR load errors** (`review.e2e.ts`): `Review` sets its content to
  "Error loading PR." but never renders it, so a PR that fails to load shows an
  empty page.
