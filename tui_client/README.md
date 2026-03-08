# CRS TUI — Terminal UI Client for Code Review Server

A fast, keyboard-driven terminal interface for browsing and reviewing pull requests managed by the Code Review Server.

## Features

- **Organized by sections**: PRs are grouped into categories (e.g., "Needs Review", "In Progress") with color-coded status
- **Vim keybindings**: Native support for `j`/`k` movement, `g`/`G` navigation, `d`/`u` paging
- **PR detail view**: Full metadata, syntax-highlighted diffs, comments, and reviews
- **Open in browser**: Press `o` to open any PR in your default browser
- **Real-time sync**: Press `r` to fetch fresh data from the server or GitHub
- **Lightweight**: Built in Rust with no heavy dependencies

## Installation

### Prerequisites

- The `codereviewserver` (aka `crs`) binary must be installed and in your `$PATH`
- Rust 1.70+ (for building from source)

### Build from Source

```bash
cd tui_client
cargo build --release
./target/release/crs-tui
```

Or install directly:
```bash
cargo install --path tui_client
crs-tui
```

## Usage

### Starting the Client

```bash
crs-tui
```

The client will automatically spawn the `codereviewserver --server` process and begin fetching reviews.

### Navigation

#### Sections View (PR List)

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
| `Ctrl+C` | Quit (always works) |

#### PR Detail View

| Key(s) | Action |
|--------|--------|
| `j` / `↓` | Scroll down 1 line |
| `k` / `↑` | Scroll up 1 line |
| `d` | Page down 20 lines |
| `u` | Page up 20 lines |
| `g` | Jump to top of content |
| `G` | Jump to bottom of content |
| `o` | Open PR in browser |
| `r` | Sync PR from GitHub |
| `q` / `Esc` / `h` / `Backspace` | Back to sections list |
| `Ctrl+C` | Quit |

## Architecture

### Core Modules

- **`rpc.rs`**: Spawns the `codereviewserver` child process and manages JSON-RPC 1.0 communication over stdio
- **`types.rs`**: Serde-serializable types matching the server's protocol (ReviewItem, PRMetadata, etc.)
- **`app.rs`**: Application state machine with two views (Sections and PRDetail), keyboard handling, and business logic
- **`ui.rs`**: Ratatui-based UI rendering with syntax highlighting for diffs

### Communication

The TUI communicates with the Code Review Server using **JSON-RPC 1.0** over **stdio**:

1. Client spawns: `crs --server` (or `codereviewserver --server`)
2. Client sends JSON-RPC requests to server's stdin
3. Server responds with JSON-RPC responses on stdout
4. Messages are newline-delimited

Key RPC methods used:
- `GetAllReviews` — Fetch all sections and PR items
- `GetPR` — Fetch full PR details (metadata, diff, comments, reviews)
- `SyncPR` — Force-refresh from GitHub

See [`docs/protocol.md`](../docs/protocol.md) for the full protocol specification.

## Building and Testing

### Build

```bash
cargo build
```

### Run Tests

```bash
cargo test
```

### Release Build

```bash
cargo build --release
```

Binary location: `target/release/crs-tui`

## Troubleshooting

### "Could not find 'crs' or 'codereviewserver' on PATH"

Ensure the Go server binary is installed:
```bash
cd /path/to/code-review-server
go install ./...
```

Then ensure `$GOPATH/bin` is in your `$PATH`:
```bash
export PATH="$PATH:$(go env GOPATH)/bin"
```

### Server Hangs or Doesn't Respond

- Check that `CRS_GITHUB_TOKEN` is set in your environment
- Verify the server is properly installed: `which crs`
- Look at stderr output (server logs may appear in your terminal)

### Terminal Rendering Issues

The TUI uses `crossterm` for terminal control. If you see rendering glitches:
- Try a different terminal emulator (xterm, iTerm2, Windows Terminal, etc.)
- Ensure your terminal supports 256 colors

## Configuration

The TUI inherits configuration from the Code Review Server. Configuration is typically in:
```
~/.config/codereviewserver.toml
```

See the server's [`docs/configuration.md`](../docs/configuration.md) for details.

## Development

### Adding New Keybindings

Edit `app.rs`:
- `handle_sections_key()` for sections view bindings
- `handle_pr_detail_key()` for detail view bindings

Example (add `n` to jump to next section):
```rust
KeyCode::Char('n') => {
    // Jump to next section header in list
    self.list_cursor = next_section_cursor(self.list_cursor, &self.sections);
}
```

### Adding New RPC Methods

1. Add the type definitions to `types.rs` (reply struct with `#[derive(Deserialize)]`)
2. Call `self.rpc.call()` in `app.rs`
3. Handle the response and update state

Example:
```rust
let result = self.rpc.call("SubmitReview", serde_json::json!({
    "Owner": owner,
    "Repo": repo,
    "Number": number,
    "Event": "APPROVE",
}))?;
```

## Performance

- Startup: ~200ms (server spawn + initial fetch)
- Navigation: <1ms (local state)
- PR detail load: ~500ms–2s (depends on diff size and GitHub API latency)
- Refresh: ~1–5s (full GitHub API refresh)

Diffs are rendered without line-wrapping by default for performance. Edit `ui.rs` to enable wrapping if needed.

## License

Same as Code Review Server (see parent repository).

## Contributing

Contributions welcome! Please ensure:
- No new warnings in `cargo build`
- Tests pass: `cargo test`
- Code follows Rust conventions

Create a PR against the main branch.
