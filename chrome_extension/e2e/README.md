# End-to-end tests

Playwright loads the built extension (`dist/`) into Chromium and drives it
through the real chain: the content script's modal on a github.com page, the
panel in its iframe, the service worker, and the real native host
(`crs_native_host`, built fresh by `global_setup.ts`). Only the two ends are
fakes, so the suite needs no GitHub token, network or SQLite database:

| Real thing                  | E2E stand-in             | What it does                                                                                                                                                                                                                                                 |
| --------------------------- | ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `codereviewserver --server` | `harness/fake_crs.ts`    | Newline-delimited JSON-RPC 1.0 on stdio, as Go's `net/rpc/jsonrpc` speaks it, serving `fixtures/data.ts`: the review list, PR `acme/widgets#42` with every AI feature and plugin output, and `acme/monorepo#77` with a ~3 MB multi-byte diff. Run under Bun. |
| github.com                  | `context.route` handlers | A minimal page for any URL; nothing reaches the network.                                                                                                                                                                                                     |

## Running

```bash
cd chrome_extension
bun run test:e2e                                          # builds dist/, then runs everything
bunx playwright test -c e2e/playwright.config.ts modal    # one spec, dist/ already built
bun run type-check:e2e
```

Global setup needs Go (to build the host) and Bun (to run the fake). The
browser is Playwright's Chromium (`bunx playwright install chromium`) with
`channel: 'chromium'`: the headless shell can't load extensions.

Set `CRS_E2E_SCREENSHOTS=<dir>` to have `theme.e2e.ts` save the modal on a PR
and on the list, in light and dark.

## How a test is set up

Every test (`harness/test.ts`) launches its own browser profile under
`$TMPDIR/crs-ext-e2e/test-*`, so its own service worker, host and fake server;
tests share nothing and run in parallel.

- **Host registration**: the profile's `NativeMessagingHosts/com.c_hipple.crs.json`
  points at the built host. `test.use({ hostInstall: 'missing' })` leaves it
  out; `'forbidden'` registers it for another extension id.
- **Host environment**: the browser is launched with `CRS_NATIVE_HOST_ENV`
  naming an env file, which the host merges as it does
  `~/.crs/native_host.env`: `CRS_SERVER_PATH` (the fake's launcher), `CRS_HOME`
  (where the host writes `native_host.log`) and `CRS_E2E_STATE_DIR`.
  `test.use({ hostEnv })` adds lines; `browserEnv` adds to the browser's own
  environment.
- **Steering the fake** (`harness/scenario.ts`): specs write `scenario.json`
  in the state directory (`crs.update(...)`) and the fake re-reads it on every
  request — e.g. `finishRuns` lets a pending AI or plugin run land,
  `exitOn` makes the server die on a method. The fake appends every request to
  `calls.jsonl`, which `crs.calls()` / `crs.waitForCall()` read.
- **The toolbar button**: Playwright can't click browser UI, and headless
  Chromium doesn't deliver the `Alt+Shift+R` command, so `github.clickToolbarButton`
  calls `chrome.action.onClicked.dispatch(tab)` in the service worker. That
  runs the listener `background.ts` registered with the real tab, exactly as a
  click does. Before toggling, `github.open` waits until the content script
  is running (asked through `chrome.scripting.executeScript` in its world).
- **The panel's frame** is in a closed shadow root, out of reach of locators,
  so `panelFrame` finds it among the page's frames by URL.
- **No viewport emulation**: Chromium hit-tests the modal's out-of-process
  iframe against the real window, so with an emulated viewport taller than
  the window, clicks low in the panel are lost. The window size sets the
  viewport instead (about 1280×900).

On failure, the host's log (which also holds the fake's stderr) and the
fake's call log are attached to the report.
