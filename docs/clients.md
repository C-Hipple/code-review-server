# Clients

Code Review Server ships with a web client, an Emacs client, a TUI client, and a Chrome extension, but you can build your own using the [Protocol](protocol.md).

Each client runs its own server by default. The web and Emacs clients can instead connect to a server a team shares — see [Connecting to a shared server](#connecting-to-a-shared-server).

## Web Client (bun)

The web client is packaged with bun, and has a bun backend with a react frontend. If you build and run the bun backend, you'll get a working webserver on `localhost:3000` which lists all of your PRs.

For a full inventory of what this client does — architecture, HTTP surface, every feature, and the protocol details a reimplementation needs to get right — see [Web Client Features](web_client_features.md).

### Installation

1. `cd` to `bun_client`
2. `bun install && bun run build`
3. `bun start`

### Preferences

The gear icon next to the header opens Preferences, which has two tabs:

- **Appearance** — theme, preferred review location, and diff font size. These are client-side settings stored in the browser's local storage.
- **Server Configuration** — an editor for the server's TOML config (`~/.config/codereviewserver.toml`), covering the global settings, the workflow list, the plugins and the AI features.

In the Server Configuration tab, each workflow is a collapsible card you can expand to edit, reorder with the arrow buttons, or delete. **+ Add workflow** appends a new one. The workflow type and filter dropdowns are populated from the registries the server sends with the config, so they always offer what the running server supports; filters that need an argument (`FilterByLabel` and friends) get an extra input for it, and fields that don't apply to the selected workflow type are hidden.

Plugins are cards in the same style: the command to run, which PR data it is passed (`--diff`, `--headers`, `--comments`, `--branch`), whether it runs only on demand, and the LLM provider and model it is handed through `CRS_LLM_PROVIDER` / `CRS_LLM_MODEL`.

Under **AI Features**, the defaults every feature inherits (`[AI]`: provider, command, model) sit above one card per feature the server has registered. The checkbox on a card switches the feature on; expanding it sets whether it runs automatically, its mode (for features with more than one), and a provider, command or model of its own. Each card says which provider the feature would run on, worked out from the settings as they stand, the same way the server does. A feature that a legacy key such as `ExperimentalLLMReviewEase` still switches on shows as on, with a note; changing it writes an `[[AIFeatures]]` entry, which takes precedence over the key.

The editor validates as you go and blocks a save while anything is wrong — a missing section title, a duplicate workflow name, a repository that isn't in `owner/repo` form, an OpenRouter plugin or feature with no model, an enabled feature that would run a command when none is set. The server validates again before writing and reports anything the client missed against the field that caused it, so a config that can't run never reaches disk. See [Configuration](configuration.md#editing-the-config-from-a-client) for what saving does to the file and when the changes take effect.

## TUI Client

The TUI client (`tui_client/`) is a fast, keyboard-driven terminal interface built in Rust. It's well-suited for quick access to your review queue from any terminal.

### Installation

**Prerequisites:** The `crs` binary must be installed and in your `$PATH` (see [Quickstart](index.md)).

```bash
cd tui_client
cargo build --release
# Optionally install to PATH:
cargo install --path tui_client
```

### Running

```bash
crs-tui
```

The client automatically spawns `crs --server` as a child process, so no separate server process is needed.

### Keybindings

The TUI uses vim-style keybindings throughout.

#### PR List (Sections View)

| Key(s) | Action |
|--------|--------|
| `j` / `↓` | Move cursor down |
| `k` / `↑` | Move cursor up |
| `d` | Jump down 10 items |
| `u` | Jump up 10 items |
| `g` | Jump to top |
| `G` | Jump to bottom |
| `Enter` / `l` | Open PR detail view |
| `o` | Open PR in browser |
| `r` | Refresh from server |
| `q` | Quit |

#### PR Detail View

| Key(s) | Action |
|--------|--------|
| `j` / `↓` | Scroll down 1 line |
| `k` / `↑` | Scroll up 1 line |
| `d` | Page down 20 lines |
| `u` | Page up 20 lines |
| `g` | Jump to top |
| `G` | Jump to bottom |
| `o` | Open PR in browser |
| `r` | Sync PR from GitHub |
| `q` / `Esc` / `h` / `Backspace` | Back to PR list |

### Running a Background Sync Daemon

The TUI client spawns a fresh server process each time it starts, which means it won't have any locally cached PR data until a sync completes. The first load can be slow if the cache is cold.

More importantly, GitHub API rate limits mean that frequent short-lived syncs are wasteful. The server uses lock files to prevent concurrent workflow runs from over-querying the API, so having a single long-running sync process in the background is the recommended approach.

Run a persistent sync daemon alongside your normal workflow:

```bash
# Run as a background daemon (workflows repeat on schedule, default every 10 min)
crs &

# Or use a more persistent approach via systemd, tmux, etc.
```

With a daemon running, the TUI client will find an already-warm cache on startup and load reviews nearly instantly. The daemon's lock files also ensure the TUI's own `r` (refresh) calls don't race with background syncs.

See [Configuration](configuration.md) for `WorkflowInterval` and other scheduling options.

## Emacs Client

The emacs client lives in the `client.el/` directory. It is split into
several modules; `crs-client.el` is the entry point that loads them all
(`crs-vars`, `crs-html`, `crs-rpc`, `crs-render`, `crs-list-mode`,
`crs-review`, `crs-comments`, `crs-review-actions`, `crs-plugins`, `crs-ai`).

### Installation

#### Spacemacs

```elisp
   ;; in dotspacemacs-additional-packages
   (code-review-server :location (recipe
                      :fetcher github
                      :repo "C-Hipple/gtdbot"
                      :files ("*.el")))
```

### Usage

1. Open `client.el/crs-client.el` && evaluate the buffer (it `require`s the other modules)
2. Run commands:

```elisp
(crs-start-server) ;; to start the processing
(crs-get-reviews)  ;; Load your required reviews into an ephermeral org-mode buffer

;; To start a review
(crs-start-review-at-point)  ;; when your cursor is on a github URL


(crs-get-review "C-Hipple" "code-review-server" 1)  ;; Start it directly.
```

In the reviews buffer, each PR's headline carries its tags: the repository,
`draft` or `merged`, `conflict` when GitHub reports that the PR's branch conflicts
with its base (drawn in `crs-list-conflict-face`), and the
[review-ease](ai_features.md#review-ease) rating when that feature is on.

Starting a review will then load a new code-review buffer which you can read the review, make comments, and submit your review.

Submitting (`C-c C-c`, `crs-submit-review`) runs in the background: the command
returns as soon as the request is sent, so you can read on or move to the next
PR while GitHub takes the review and the server refetches the PR. When the
server replies, a message says what went out — `Approved acme/widgets #42`,
naming any replies GitHub refused — and the review buffer is redrawn from the
reply. A PR takes one submit at a time, and a failed submit leaves your review
feedback in place to submit again.

When the server has an [AI feature](ai_features.md) enabled, `C` in a review
buffer (`crs-get-ai-output`) opens its report for the PR — for example, which
review comments are still outstanding — running it first when it never ran or its
report is stale. The report buffer updates itself while a run is pending; `r`
refreshes it, `R` forces a re-run and `q` quits.

The [change diagram](ai_features.md#change-diagram) has a command of its own:
`M` in a review buffer, or on a PR's line in the reviews buffer, runs
`crs-show-change-diagram`, which shows the diagram's raw Mermaid source in a
buffer of its own — in [`mermaid-mode`](https://github.com/abrochard/mermaid-mode)
when it is installed, so `C-c C-b` renders it with `mmdc` and `C-c C-o` opens it
in the Mermaid live editor. Called with `M-x` anywhere else, it asks for the PR as
a URL or `owner/repo#number`. The same `r`, `R` and `q` apply, and the header line
says whether the diagram still describes the PR.

### Embedded screenshots

Descriptions, reviews and comments are rendered with `shr`, and screenshots in
them display inline in graphical Emacs. The client does not fetch those images
itself: an attachment on a private repository is served only to a request
carrying a GitHub token, which `shr` has no way to send. The server downloads
them instead and the reply's `images` field says where each one landed, so the
client points the `<img>` tag at that local file. An image the server did not
cache — anything hosted off GitHub, or one that has not been downloaded yet — is
left for `shr` to fetch as before.

### Configuration

The following customizable variables control client behavior. Set them in your Emacs init file or via `M-x customize-group RET crs RET`.

#### `crs-include-comments-tree`

**Default:** `nil`

When set to a non-nil value, a `*** Comments` sub-tree is included under each PR heading in the reviews buffer. Each comment is rendered as a fourth-level org heading showing the author and, for inline comments, the file path.

```elisp
(setq crs-include-comments-tree t)
```

This is disabled by default because rendering comment threads for every PR in the list can noticeably slow down the reviews buffer. The server always returns comment data from its local DB cache — no extra GitHub API calls are involved — but the additional org structure adds rendering overhead proportional to the total number of comments across all listed PRs. Setting this to `nil` strips the comments before handing the content to org-mode.

Enable it if you want a quick overview of comment activity without opening each PR individually.

## Chrome Extension

The Chrome extension (`chrome_extension/`) brings the server's [AI features](ai_features.md) and [plugins](plugins.md) to the GitHub pull request you are reading. It is not a review UI: it shows the reports and plugin output for the PR the tab is on, with Run and Re-run, and the review list for getting to a PR. It works for any PR, including ones in no review list — opening one makes the server fetch and cache it, as in any client.

An extension can't start processes, so the extension starts the server through a native messaging host, `crs_native_host`: Chrome launches the host, and the host runs `crs --server` and bridges Chrome's message framing to the server's stdio. The full guide — the env file, troubleshooting, the architecture and the bridge's protocol — is the extension's [README](https://github.com/C-Hipple/code-review-server/blob/main/chrome_extension/README.md).

### Installation

**Prerequisites:** `crs` installed (`go install ./...`), Go and Bun, and Chrome 116 or later (or Chromium, Brave or Edge) on Linux or macOS.

1. `cd chrome_extension && bun install && bun run build`
2. `./crs_native_host/install.sh --capture-env`, in a shell where `CRS_GITHUB_TOKEN` and any API keys are set. It builds the host into `~/.crs/bin/`, registers it with every supported browser it finds, and writes `~/.crs/native_host.env`.
3. On `chrome://extensions`, turn on Developer mode, **Load unpacked**, and pick `chrome_extension/dist`. The extension's ID is pinned to `acmghogknbbihjoejbkejhhikiapmiib` by the `key` in its manifest, which is the ID the host's manifest allows.

`--capture-env` matters because browsers start native hosts with the desktop session's environment rather than your shell's, so `CRS_GITHUB_TOKEN`, API keys and `PATH` additions from your shell rc files never reach the server otherwise. The host merges `~/.crs/native_host.env` into its environment before starting the server, and a variable there overrides the inherited one. It reads the file when it starts, so reload the extension after changing it. The host logs to `~/.crs/native_host.log`.

### Usage

Press the toolbar button or `Alt+Shift+R`:

- **On a pull request**, a modal opens over the page on that PR's tools: a summary with the review-ease rating and **Sync**, a card per enabled AI feature with a report (the [change diagram](ai_features.md#change-diagram) drawn inline), a card for [file-ordering](ai_features.md#file-ordering) listing the suggested order, and a card per plugin with **Re-run all**. Results are polled while anything runs. Annotations link to their line in GitHub's Files changed tab.
- **Anywhere else on github.com**, the modal opens on the review list, grouped by section, with a filter. Clicking a PR takes the tab to it on GitHub; its **Tools** button opens the PR's tools in the modal instead, and **Back** returns to the list.
- **Off GitHub**, the same panel opens in a popup window, where PR links open in a new tab.

The panel follows the system's light or dark setting. It can call only a fixed list of read-and-run methods (`GetAllReviews`, `GetPR`, `SyncPR`, the plugin and AI feature RPCs, `Hello`), so it can't submit reviews, merge, comment or change the config.

On a pull request's Files changed tab the content script lays the stored [file-ordering](ai_features.md#file-ordering) result over GitHub's own page, reordering the per-file diffs and the file tree to match it; files the report doesn't name stay where GitHub put them.

## Connecting to a shared server

By default a client runs its own `crs` (`crs --server`, over stdio) with your GitHub token. A team can instead run one server, `crs --listen host:port` (see [Hosted Server](hosted_server.md)), that the web and Emacs clients connect to over TCP. Each user gets a dashboard of their own, built from the workflows in their own `codereviewserver.toml`; the GitHub token, the caches and the plugin and AI runs are the server's.

### How a client connects

A connection starts unauthenticated, so the client opens each one with the connect flow (the methods are in the [Protocol](protocol.md)):

1. `RPCHandler.Authenticate {Token}` with your stored token, if there is one.
2. If the server doesn't know the token — you never registered there, or it removed you after a stretch of inactivity — or there is none, `RPCHandler.RegisterUser` with the server's registration key, your GitHub login, and the `Workflows`, `Repos`, `SectionPriority` and `SectionSorting` of your local `codereviewserver.toml`. Your stored token goes along, so a returning user gets the same one back. The token in the reply is saved, readable by you only.

Requests made meanwhile wait for it. One the server refuses as `not authenticated:` runs the flow again and is retried once, and a dropped connection fails the requests in flight before the client reconnects. The server still carries out a request whose connection has gone, so a review submit lost that way may have been posted: both clients say its outcome is unknown and reload the PR rather than offer to submit it again. A submit names the pending comments the client showed (`ExpectedDraftIDs`), so one sent again after the first went through — or after the server deleted your drafts with your account — posts nothing and is refused with `drafts changed:`.

Your workflows are sent when the server registers you — the first time, and again whenever it has forgotten you. In between, the server runs the copy it has: editing your local file doesn't change it (`RPCHandler.UpdateMyWorkflows` does). The server builds your dashboard from them on its next workflow cycle, so right after registering it is empty for up to the server's `SleepDuration`; the client passes on the server's word on it (the bridge logs it under `[Remote]`; Emacs says it when it connects).

The token is a password for the server that issued it, so it is kept per server: in `$CRS_HOME/remote_tokens/<host>_<port>` (`~/.crs/remote_tokens/crs.internal.example.com_443`), named after the address you connect to — lower-cased, with any character other than a letter, digit, `.` or `-` written `_` — and only ever sent there. Pointing a client at another server registers you there afresh rather than handing it this one's token. Both clients keep it there, so they connect to a server as the same user. To use a token from another machine, copy that server's file across (or set `CRS_REMOTE_TOKEN`): registering a login the server already knows needs the token it was registered with. The same server reached under two names (`localhost` and `127.0.0.1`, say) has two files.

### What is different on a shared server

- **Admin-only actions.** Only the server's administrators can view or change its configuration or rerun plugins. The web client's Server Configuration tab says so instead of showing the editor, and a refused rerun says so in a toast. An administrator connects with the server's admin token as their token and sees the server's own dashboard.
- **Writes go out as you, once you link your GitHub account.** Everything the server reads from GitHub — the dashboard, diffs, comments, CI — goes through its own token. A review you submit, with its replies (and a merge, from a client that merges), goes out as your own GitHub account once you have [linked it](#linking-your-github-account). Until then it goes out as the server's account if its administrator allows that (`AllowServerIdentityActions`; merging that way also takes an administrator), and is otherwise refused: with `refused: link-github:` where the server can link accounts, which the client answers by offering to link, or with a plain `refused:` where it can't, which is final — submit the review on GitHub instead. Either way your draft and pending comments are kept.
- **Paths on the server's host.** Anything that reads a file by its path works only where the same path exists on your machine: the language-server features of the web client (diff-lsp, and go-to-definition in the code viewer), the code viewer's file list, and opening a file from an Emacs review. Hunk expansion and everything else served over RPC work as usual.
- **Screenshots.** The web client asks the server for the bytes of each image it cached (`GetImage` with `IncludeData`), so screenshots render as they do locally, private repositories included. The Emacs client still opens the server's cached file by its path, so on another machine it leaves the image to `shr`, which can fetch a public repository's attachments but not a private one's.
- **Team chips.** Which required-review teams the list marks as yours is worked out for the account whose token the server holds, not for you.

### Linking your GitHub account

Where the server's administrator has set linking up ([Hosted Server](hosted_server.md#linking-your-github-account)), you link your account once, through GitHub's device flow: the client shows a one-time code such as `WDJB-MJHT`, you enter it at <https://github.com/login/device> signed in to GitHub as the login you registered with, and authorize the app. Linking is optional — you can browse and draft without it — and the server keeps the link, so every client connected with your token posts as you. A link ends in one of:

| Outcome | Meaning |
|---------|---------|
| linked | Your reviews now go to GitHub as you. |
| denied | You declined the authorization on GitHub; nothing was linked. Try again. |
| expired | The code (good for about fifteen minutes) wasn't entered in time. Try again for a new one. |
| mismatch | GitHub authorized a different account than the login you registered with, so nothing was stored. Sign in to GitHub as that login and try again. |
| error | Something else went wrong — GitHub refused the app, or the server could not store the link; the client shows the server's reason. Try again. |
| unavailable | This server can't link accounts (no OAuth app or secret key configured), or you are connected with the admin token, which is the server's own identity. Ask its administrator. |

Unlinking deletes the stored token (and revokes it on GitHub, where the server is set up to); your reviews are refused again until you link, or go out as the server's account where it allows that. If GitHub stops accepting your token — you revoked it, or it expired — the next submit is refused with `refused: link-github:` and you link again.

### Web client

Set these before `bun start` (or the compiled `crs-gui`):

| Variable | Meaning |
|----------|---------|
| `CRS_REMOTE` | `host:port` of the shared server. Unset, the bridge spawns `crs --server` as always. |
| `CRS_REMOTE_TLS` | `1` to speak TLS, to the proxy terminating it in front of the server. A private CA can be trusted with `NODE_EXTRA_CA_CERTS`. |
| `CRS_REGISTRATION_KEY` | The server's registration key, from its administrator. Needed only to register. |
| `CRS_LOGIN` | Your GitHub login. Defaults to the root `GithubUsername` of your `codereviewserver.toml`. |
| `CRS_REMOTE_TOKEN` | Your token, used instead of the token file. |
| `CRS_HOME` | Where the token files live (`$CRS_HOME/remote_tokens/<host>_<port>`, default `~/.crs`). |
| `XDG_CONFIG_HOME` | Where `codereviewserver.toml` is read from (default `~/.config`). |

```bash
export CRS_REMOTE=crs.internal.example.com:443 CRS_REMOTE_TLS=1
export CRS_REGISTRATION_KEY=...   # the first time only
cd bun_client && bun start
```

The bridge logs the connect flow under `[Remote]`, including what to set when something it needs is missing. While the server is unreachable it retries with a growing delay (up to 30 seconds), and requests made between attempts fail at once with the reason rather than hang.

Connected to a shared server, the header shows who you are signed in as, with a dot for your GitHub link — green linked, amber not linked yet, grey when the server can't link accounts. Until the bridge has signed in — the server is unreachable, or refused to register you — it says "not connected" in red, with the reason, and it catches up on its own once the bridge connects. It opens the **Account** tab of Preferences, which says which server and login you are on (and whether you are an administrator), and where your GitHub link stands, with **Link GitHub account** or **Unlink** (which asks first). Linking opens a dialog with GitHub's code, a Copy button and a link to the page to enter it on; it waits for GitHub and says how the link ended. Closing it stops the waiting but not the link, and opening it again shows the same code.

A submit refused with `refused: link-github:` opens the same dialog, saying why the review wasn't submitted, with your draft kept; once linked, **Submit again** sends it. A plain `refused:` says so in a toast and leaves the draft in place to take to GitHub. A submit whose connection dropped before the server answered says it may or may not have gone out and reloads the PR, without reopening the submit dialog: check the PR before submitting again. The submit dialog's preview names the login you are signed in as, since the server's configuration, where a local client reads `GithubUsername`, is for its administrators.

### Emacs client

```elisp
(setq crs-remote-address "crs.internal.example.com:443"
      crs-remote-tls t
      crs-remote-registration-key "...")  ; the first time only
```

| Variable | Default | Meaning |
|----------|---------|---------|
| `crs-remote-address` | `nil` | `"host:port"` of the shared server; `nil` runs `crs -server` locally as always. |
| `crs-remote-tls` | `nil` | Speak TLS (`open-network-stream` with `:type 'tls`). |
| `crs-remote-token-directory` | `$CRS_HOME/remote_tokens` | Where the tokens are kept, one file per server (`<host>_<port>`). `CRS_REMOTE_TOKEN` wins when set. |
| `crs-remote-login` | `nil` | Your GitHub login; `nil` means `CRS_LOGIN`, else the root `GithubUsername` of your `codereviewserver.toml`. |
| `crs-remote-registration-key` | `nil` | The registration key; `nil` means `CRS_REGISTRATION_KEY`. |
| `crs-remote-user-config-command` | `("crs" "-print-user-config")` | Prints what registering sends, read from your `codereviewserver.toml`, as JSON. Point it at any command printing the same object if `crs` isn't installed here. |

`crs-start-server` connects instead of spawning, and the commands that start the server do the same. When the connection drops, the requests it owed get an error and the next command reconnects; one already sent gets an error beginning `connection lost:`, since the server may still have carried it out. A review submit lost that way says it may or may not have gone out and reloads the PR's buffer, rather than reading as not submitted. A submit names the pending comments the review buffer shows (`ExpectedDraftIDs`), and one refused because some are gone (`drafts changed:`) says so and reloads the PR too; either way your feedback is kept. A connect flow that fails says what to set in the `*Warnings*` buffer.

Connecting says who you are signed in as, and reminds you to link your GitHub account while it isn't. Three commands handle the link:

| Command | What it does |
|---------|--------------|
| `crs-link-github` | Asks the server for a code and shows it in a `*crs GitHub link*` buffer, copies it to the kill ring, and opens GitHub's code page with `browse-url` (unless `crs-link-github-browse` is `nil`). It then polls until GitHub answers and reports the outcome in the echo area. Run again while a code is waiting, it shows the same code. |
| `crs-unlink-github` | Unlinks your account, after asking. |
| `crs-github-link-status` | Says whether you are linked, and as which GitHub account. |

A submit refused with `refused: link-github:` says to run `crs-link-github`; your feedback and pending comments are kept, so submit again once linked.
