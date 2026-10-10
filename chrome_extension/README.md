# Code Review Server Chrome extension

A client for code-review-server that lives on github.com. It knows which pull
request the tab is on and shows that PR's [AI features](../docs/ai_features.md)
and [plugins](../docs/plugins.md): their reports and output, with Run and Re-run.
Away from a PR it shows the server's review list, and a click takes the tab to a
PR.

It is not a review UI. Reading the diff, commenting and submitting stay on
GitHub (or in the web and Emacs clients); the extension brings the server's
analysis to the page you are already reading. That includes PRs that are in no
review list: opening one makes the server fetch and cache it from GitHub, the
same as opening it in any other client.

Like every client, it starts its own server. An extension can't start
processes, so it does that through a small native messaging host,
`crs_native_host`, which Chrome launches and which runs
`codereviewserver --server` (see [Architecture](#architecture)).

## Prerequisites

- **The server.** `go install ./...` from the repository root installs
  `codereviewserver` (and `crs_native_host`, though `install.sh` builds its own
  copy) to `$GOPATH/bin`.
- **`CRS_GITHUB_TOKEN`**, as for every client, plus `GEMINI_API_KEY` or
  `OPENROUTER_API_KEY` for AI features and plugins on those providers. The
  browser does not start the host with your shell's environment, so these reach
  the server through an env file; `install.sh --capture-env` writes it (see
  [The env file](#the-env-file)).
- **Something to show.** AI features and plugins are off until the server
  config (`~/.config/codereviewserver.toml`) turns them on with `[[AIFeatures]]`
  and `[[Plugins]]`; without any, the PR view lists none.
- **Go** to build the host, **Bun** to build the extension.
- **Chrome 116 or later**, or Chromium, Brave or Edge, on Linux or macOS.
  `install.sh` knows those browsers' directories on those two systems only.

## Installing

1. Build the extension into `chrome_extension/dist`:

    ```bash
    cd chrome_extension
    bun install && bun run build
    ```

2. Register the native host, from the same directory, in a shell where
   `CRS_GITHUB_TOKEN` and your API keys are set:

    ```bash
    ./crs_native_host/install.sh --capture-env
    ```

    This builds the host into `~/.crs/bin/crs_native_host`, writes its manifest,
    `com.c_hipple.crs.json`, into the `NativeMessagingHosts` directory of every
    installed browser it finds (Chrome, Chrome Beta, Chromium, Brave, Edge), and
    with `--capture-env` writes `~/.crs/native_host.env`. It finishes by saying
    whether it can see a `codereviewserver` for the host to start.

3. Open `chrome://extensions`, turn on **Developer mode**, click **Load
   unpacked** and pick `chrome_extension/dist`. The extension's ID must read
   `acmghogknbbihjoejbkejhhikiapmiib` (see
   [Why the ID is pinned](#why-the-id-is-pinned)).

4. Pin the **Code Review Server** button from the toolbar's extensions menu, or
   use its shortcut, `Alt+Shift+R`.

After pulling changes, rerun `bun run build` and `install.sh` (which rebuilds
the host), then click the extension's reload button on `chrome://extensions`.
Reloading also restarts the host and the server, so it is how a newly installed
`codereviewserver` or an edited env file takes effect.

### install.sh options

| Option               | Effect                                                                                          |
| -------------------- | ----------------------------------------------------------------------------------------------- |
| `--capture-env`      | Write `~/.crs/native_host.env` (mode 600) from the current shell (see below)                    |
| `--browser NAME`     | Register with `chrome` (Chrome and Chrome Beta), `chromium`, `brave` or `edge` only. Repeatable |
| `--manifest-dir DIR` | Write the manifest into `DIR`, for a browser or profile `--browser` doesn't cover. Repeatable   |
| `--host-path PATH`   | Register an existing `crs_native_host` binary instead of building one                           |
| `--extension-id ID`  | Allow this extension ID instead of the pinned one                                               |
| `--uninstall`        | Remove the manifests; the host binary and the env file stay                                     |

`--capture-env` records `PATH` plus whichever of `CRS_GITHUB_TOKEN`, `CRS_HOME`,
`GEMINI_API_KEY`, `OPENROUTER_API_KEY`, `CRS_STYLE_GUIDE_DIR`,
`CRS_LLM_PROVIDER`, `CRS_LLM_MODEL` and `CRS_SERVER_PATH` are set — everything
the server and its plugins read from the environment, and the host's own
`CRS_SERVER_PATH` — and keeps an existing file as `native_host.env.bak`.

Without `--capture-env` the script writes nothing secret and prints how the env
file works instead. `--help` lists the options with the paths they default to.

### The env file

Chrome starts a native host with the desktop session's environment, not a login
shell's, so `PATH` additions, `CRS_GITHUB_TOKEN` and API keys exported from your
shell rc files never reach it. The host therefore merges an env file into its
environment before doing anything else, and the server inherits the result:
`~/.crs/native_host.env`, or the file `CRS_NATIVE_HOST_ENV` names.

The format is `KEY=VALUE` lines. Blank lines and lines starting with `#` are
skipped, a leading `export ` is allowed (so a shell can source the file too),
matching single or double quotes around a value are removed with nothing inside
them expanded, and a value starting with `~/` gets your home directory. A
variable in the file **overrides** the same variable in the inherited
environment: the file is your explicit configuration for the host, the inherited
environment whatever the browser happened to start with. A missing
`~/.crs/native_host.env` is fine; a missing file that `CRS_NATIVE_HOST_ENV`
names is logged as an error.

`install.sh --capture-env` writes the file from the shell you run it in; rerun
it, or edit the file, when a token changes. The host reads the file when it
starts, so reload the extension afterwards.

### Why the ID is pinned

A native host's manifest admits extensions by exact ID (`allowed_origins`; no
wildcards), and an unpacked extension's ID is normally derived from the
directory it is loaded from, so it would differ between checkouts and machines.
`manifest.json` carries a `key` (a public key; the private half isn't needed to
load unpacked), and Chrome derives the ID from it instead, so every checkout
loads as `acmghogknbbihjoejbkejhhikiapmiib` and `install.sh` can write the host
manifest before the extension is ever loaded. If you load the extension under
another ID, rerun `install.sh --extension-id <its ID>`.

## Using it

Press the toolbar button or `Alt+Shift+R` (change it at
`chrome://extensions/shortcuts`):

- **On a pull request** (`github.com/:owner/:repo/pull/:number`, any of its
  tabs), a modal opens over the page on the **PR view** for that PR. The
  button's tooltip names the PR (`Code Review Server — owner/repo#42`).
- **Anywhere else on github.com**, the modal opens on the **review list**.
- **Off GitHub**, the panel opens in a popup window of its own, on the review
  list; pressing the button again focuses that window rather than opening
  another.

The modal closes with its ✕, `Esc`, a click outside it, or the button again.
While it is open, GitHub's in-page navigation to another PR (or off PRs) reloads
the panel for the new page.

The panel follows the system's light or dark setting, not GitHub's theme. The
dot in its header says whether the server is connected; hovering it shows the
server binary the host started, or the last connection error.

### The review list

The server's review list (`GetAllReviews`), grouped into its sections in the
server's order, with a filter on title, `owner/repo`, author and number, and a
**Refresh** button. Each row shows the PR's state, its tags — **Draft**,
**Merged**, **Conflict** when the branch conflicts with its base, and the
[review-ease](../docs/ai_features.md#review-ease) rating — and its comment count;
the PR the tab is on is marked **This tab**.

Clicking a row takes the GitHub tab to the PR (from the popup window, it opens
in a new tab). The **Tools** button at the row's end opens the PR view in the
panel instead, without leaving the page; **Back** in the header returns to the
list where it was scrolled to, filter intact.

### The PR view

Opening a PR calls `GetPR` first, as opening it in any client does: on a cache
miss the server fetches the PR from GitHub, and opening it starts the
post-update hooks — the plugins that aren't on demand, the AI features
configured `Automatic`, and the enabled applied features. The top of the view
shows the PR's state, title, author and branches, its diffstat, the review-ease
pill and **Conflict** when they apply, **Sync** (`SyncPR`, which refetches the
PR from GitHub), and a link to the PR.

**AI features.** A card for each enabled feature that has a report
(comments-addressed, feature-flags, change-diagram): its status (not run,
running, success, failed, insufficient input), **Stale** when the PR has changed
since the result was computed, **Truncated** when the input was cut to fit the
prompt, and when it last ran. Collapsed, a card shows a one-line preview of its
result; expanded, the whole report and its annotations.

- **Run** appears when there is nothing current to show — never run, stale or
  failed — and asks without forcing, so a stored result that still covers the
  PR answers without a model call. **Re-run** appears beside a current result
  and forces a fresh run. While a run is in flight the button is disabled and
  the previous result, if any, stays on screen.
- **change-diagram** is drawn with the mermaid library, fitted to the card's
  width (**100%** shows it at full size), with a legend for the change classes
  it uses (added, changed, removed), a **Source** toggle and **Copy source**,
  in the system's light or dark theme. When mermaid can't parse the diagram,
  its error shows beside the source.
- **file-ordering** is an applied feature: the server orders the diff it serves
  by it, and the extension doesn't reorder GitHub's Files changed tab yet (see
  [Roadmap](#roadmap)). Its card says so and lists the stored order, each file
  linking to its diff. It has no Run button; the server computes it when a PR is
  opened.
- **review-ease** has no card: it is the pill at the top of the view.

Features the server has but doesn't enable are named on one line under the
cards.

**Plugins.** A card for each configured plugin, with its status: not run yet,
on demand (an `OnlyOnDemand` plugin nobody has asked for), running, success or
failed. **Run** or **Re-run** reruns that plugin alone; **Re-run all** reruns
every plugin that runs automatically, naming them so an on-demand plugin keeps
the result someone asked for (`RerunPlugins` with no names would clear it).

Bodies render by their declared type. Markdown is rendered with raw HTML
dropped and the rest sanitized. HTML goes in a sandboxed frame that runs no
script and is styled to the panel's theme. Annotations are listed by file and
line, each linking to its line in GitHub's Files changed tab
(`…/pull/N/files#diff-<sha256 of the path>R<line>`).

While anything reads as running, and for ten seconds after the PR is opened or
a run is requested (a run is dispatched in the background, so the first read
can come before it is marked), the view polls `GetAIOutput` and
`GetPluginOutput` every three seconds. Whether each card is expanded is
remembered.

## Architecture

```
github.com tab
  content.js             the modal: a closed shadow root holding an <iframe>
    panel.html           the React panel, on the extension's own origin
                         (or panel.html?standalone=1 in a popup window)
        │  chrome.runtime.sendMessage({type: 'crs-rpc', method, params})
        ▼
background.js            the service worker: sender checks, RPC_METHODS, call ids,
        │                timeouts, chunk reassembly
        │  one native messaging port: a 4-byte length, then JSON, per message
        ▼
crs_native_host          bridges the two framings, chunks large responses
        │  stdin/stdout: newline-delimited JSON-RPC 1.0
        ▼
codereviewserver --server
```

**One server per browser session.** The service worker owns a single native
port to the host `com.c_hipple.crs`, opened on the first RPC and reused after.
Chrome starts one host process per port, and the host starts one server. An open
native port keeps an MV3 service worker alive (Chrome 105 and later), so the
server lives until the browser exits or the extension is reloaded or disabled.
Then Chrome closes the host's stdin, the host closes the server's stdin — which
is how every client stops the server — and kills it if it is still running five
seconds later. If the server dies, the host reports it and exits; pending calls
fail, and the next call starts a new host and server. Like any other client's,
this server runs the workflows on their schedule. When several servers are
running — this one, the web client's, a background daemon — they share the
database, and only the first to start syncs: it holds a lock file in `~/.crs`,
and the others skip their sync.

**Framing and chunking.** Chrome speaks to native hosts in frames — a 4-byte
length in native byte order, then UTF-8 JSON — and the server speaks
newline-delimited JSON-RPC. The host forwards each request from Chrome as one
compacted line (compacting removes any raw newline, which makes the newline a
safe delimiter) and refuses anything that isn't a JSON object. The other way,
Chrome accepts at most 1 MB per message from a host and drops the connection on
a bigger one, while a `GetPR` reply carries the whole diff. A response line of
up to 900,000 bytes is forwarded as is; a longer one is cut on UTF-8 boundaries
into pieces of at most 256 KiB and sent as a stream of messages:

```json
{ "crs_chunk": { "stream": 7, "seq": 0, "total": 3, "data": "{\"id\":12,\"result\":{…" } }
```

Re-encoding a piece as a JSON string at most doubles it, so each message stays
near half the limit. A single writer goroutine sends everything, so a stream's
chunks arrive back to back; the service worker joins their `data` and parses the
result as the response. The host also reports on itself in `crs_host` messages:
`ready` (with the server's path, the log's path and the host's version) once the
server has started, `error` when it can't be found or started, and
`server_exited`, with the server's last lines of output, when it dies.

**What the panel may call.** The service worker forwards only the methods in
`RPC_METHODS` (`src/types.ts`): `Hello`, `GetAllReviews`, `GetPR`, `SyncPR`,
`ListPlugins`, `GetPluginOutput`, `RerunPlugins`, `ListAIFeatures`,
`RunAIFeature` and `GetAIOutput`. Anything else is refused, so nothing in the
extension can submit a review, merge, comment or rewrite the config; using
another method means adding it there and a function for it in `src/rpc.ts`.

**Content script and panel.** The content script runs on every GitHub page, so
it stays small: no React and no server access. It draws the modal in a closed
shadow root, which keeps GitHub's CSS out, and puts the panel in an iframe.
The panel is an extension page: GitHub's page can't script into it, and it talks
to the service worker directly, never through the page. `panel.html` and its
files are web-accessible to `https://github.com/*` only. The trust checks:

- The service worker answers only the extension's own pages: the sender must
  carry the extension's ID and a `chrome-extension://<id>/` URL. A content
  script shares the ID but reports the GitHub page's URL, so nothing a page can
  influence reaches the server.
- The only message from the panel to the page is `close`, posted to
  `https://github.com`. The content script acts on it only when it comes from
  the extension's origin and from its own iframe's window.
- Bodies and diagrams are model or plugin output derived from the PR, so none
  of it is trusted markup: markdown is sanitized, HTML is sandboxed without
  `allow-scripts`, and mermaid runs at its `strict` security level.

The host logs each request's method and ID, never its parameters, which can
hold comment text.

## Troubleshooting

The panel shows a connection failure in place of its content, with the steps to
fix it; other failures show in the section that hit them, with **Retry**.

- **"Connect the extension to code-review-server"**: Chrome found no manifest
  for `com.c_hipple.crs`. Run `chrome_extension/crs_native_host/install.sh`,
  reload the extension, and press Retry. A browser whose user data lives
  somewhere else (a custom `--user-data-dir`, say) needs `--manifest-dir`
  pointing at its `NativeMessagingHosts` directory.
- **"The native host doesn't allow this extension"**: the manifest lists another
  extension ID. Rerun `install.sh --extension-id <ID>` with the ID the error card
  shows.
- **"The server isn't running"**: the host couldn't find or start
  `codereviewserver`, or the server exited; the card carries the host's message,
  which lists every place it looked or the server's last lines of output. The
  host looks for the server, in order, at `CRS_SERVER_PATH`, on `PATH`, beside
  the host binary, then in `$GOBIN`, each `$GOPATH`'s `bin` and `~/go/bin` — all
  after merging the env file. A server whose last lines say `No Github Token!`
  didn't get `CRS_GITHUB_TOKEN`: rerun `install.sh --capture-env`, or set it in
  the env file, and reload the extension.
- **"The server didn't answer in time"**: a call took longer than 120 seconds,
  the allowance for a `GetPR` that has to go to GitHub.
- **"Lost the connection to the server"**: the native port closed for another
  reason, such as the host crashing. The next call reconnects.

The host's log is `~/.crs/native_host.log` (`$CRS_HOME/native_host.log` when
`CRS_HOME` is set, including by the env file). It records the host's start,
which env-file variables it set (names only), the server it resolved, each
request's method and ID and the size of chunked responses, and receives the
server's own log. It is emptied when a host starts and finds it over 5 MB. The
service worker logs to its own console: on `chrome://extensions`, click
**service worker** under the extension.

## Development

```bash
cd chrome_extension
bun install
bun run build                 # dist/, minified
bun scripts/build.ts --dev    # dist/, unminified with inline source maps
bun run test                  # unit tests
bun run test:e2e              # end-to-end suite, see e2e/README.md
bun run lint
bun run format:check          # or `bun run format` to fix
bun run type-check            # tsconfig.json (what ships) and tsconfig.test.json
go test ./crs_native_host/    # the host's tests, which run against a fake server
```

The build bundles `background.js` as an ES module (the manifest declares the
service worker `type: module`), `content.js` as an IIFE (content scripts are
classic scripts), and the panel with code splitting, which puts the lazily
imported mermaid in a chunk of its own so the panel opens without it. It then
checks that every file `manifest.json` and `panel.html` name exists in `dist/`
and is web-accessible where it needs to be. CI runs lint, format, type-check,
the unit tests and the build; the Go job covers the host.

To try a change, rebuild and click the extension's reload button on
`chrome://extensions`. GitHub tabs don't need reloading: a tab whose content
script predates the reload gets a fresh copy injected when the button is
pressed. To inspect the panel, right-click inside the modal and choose Inspect.

| Path                         | What it is                                                                                |
| ---------------------------- | ----------------------------------------------------------------------------------------- |
| `manifest.json`              | The MV3 manifest, copied into `dist/`                                                     |
| `src/background.ts`          | The service worker: RPC for extension pages, the toolbar button, the per-tab button title |
| `src/native_client.ts`       | The native port: request IDs, timeouts, reconnecting                                      |
| `src/native_protocol.ts`     | The host's messages, chunk reassembly and the error kinds (pure, unit tested)             |
| `src/content.ts`             | The modal on github.com                                                                   |
| `src/github_url.ts`          | Recognising PR URLs and building diff links                                               |
| `src/rpc.ts`, `src/types.ts` | The panel's typed RPC client, the wire shapes and `RPC_METHODS`                           |
| `src/panel/`                 | The React panel: `App.tsx` routes between `PRList.tsx` and `PRTools.tsx`                  |
| `crs_native_host/`           | The native host (Go, standard library only) and `install.sh`                              |

The host imports nothing from the module, so it builds without cgo or the
server's dependencies. It is still part of the module — `go build ./...` builds
it and `go install ./...` installs it — which is why `go.mod` has
`ignore ./chrome_extension/node_modules`: some npm packages ship Go files that
would otherwise be built as part of the module.

## Roadmap

- **Apply file-ordering to the Files changed tab.** The server already orders
  the diff it serves by the stored file-ordering result; the extension only
  lists that order. The follow-up reorders GitHub's own Files changed tab to
  match. `TODO(file-ordering)` in `src/content.ts` marks where it goes and
  sketches it: on a PR's `/files` view, ask the service worker for that PR's
  stored file-ordering report (a new, read-only message it answers for the
  sender tab's own PR, since content scripts get no general RPC access), move
  GitHub's per-file diff containers into the report's order with the files it
  doesn't name left at the end in GitHub's order, reapply after GitHub's in-page
  navigation and as it lazily renders more files, and leave the order alone once
  the user has picked a file from GitHub's file tree.
