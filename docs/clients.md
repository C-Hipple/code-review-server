# Clients

Code Review Server ships with a web client, an Emacs client, and a TUI client, but you can build your own using the [Protocol](protocol.md).

## Web Client (bun)

The web client is packaged with bun, and has a bun backend with a react frontend. If you build and run the bun backend, you'll get a working webserver on `localhost:3000` which lists all of your PRs.

### Installation

1. `cd` to `bun_client`
2. `bun install && bun run build`
3. `bun start`

### Preferences

The gear icon next to the header opens Preferences, which has two tabs:

- **Appearance** — theme, preferred review location, and diff font size. These are client-side settings stored in the browser's local storage.
- **Server Configuration** — an editor for the server's TOML config (`~/.config/codereviewserver.toml`), covering the global settings and the workflow list.

In the Server Configuration tab, each workflow is a collapsible card you can expand to edit, reorder with the arrow buttons, or delete. **+ Add workflow** appends a new one. The workflow type and filter dropdowns are populated from the registries the server sends with the config, so they always offer what the running server supports; filters that need an argument (`FilterByLabel` and friends) get an extra input for it, and fields that don't apply to the selected workflow type are hidden.

The editor validates as you go and blocks a save while anything is wrong — a missing section title, a duplicate workflow name, a repository that isn't in `owner/repo` form. The server validates again before writing and reports anything the client missed against the field that caused it, so a config that can't run never reaches disk. See [Configuration](configuration.md#editing-the-config-from-a-client) for what saving does to the file and when the changes take effect.

## TUI Client

The TUI client (`tui_client/`) is a fast, keyboard-driven terminal interface built in Rust. It's well-suited for quick access to your review queue from any terminal.

### Installation

**Prerequisites:** The `codereviewserver` binary must be installed and in your `$PATH` (see [Quickstart](index.md)).

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

The client automatically spawns `codereviewserver --server` as a child process, so no separate server process is needed.

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
codereviewserver &

# Or use a more persistent approach via systemd, tmux, etc.
```

With a daemon running, the TUI client will find an already-warm cache on startup and load reviews nearly instantly. The daemon's lock files also ensure the TUI's own `r` (refresh) calls don't race with background syncs.

See [Configuration](configuration.md) for `WorkflowInterval` and other scheduling options.

## Emacs Client

The emacs client lives in the `client.el/` directory. It is split into
several modules; `crs-client.el` is the entry point that loads them all
(`crs-vars`, `crs-html`, `crs-rpc`, `crs-render`, `crs-list-mode`,
`crs-review`, `crs-comments`, `crs-review-actions`, `crs-plugins`).

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

Starting a review will then load a new code-review buffer which you can read the review, make comments, and submit your review.

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
