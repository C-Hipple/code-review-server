# Code Review Server Client (Bun + React)

## Setup

1. Install all dependencies (Backend + Frontend) from the root:
    ```bash
    cd bun_client
    bun install
    ```

## Running

1. Start the Bun Backend (API Server):

    ```bash
    # Make sure CRS_GITHUB_TOKEN is set
    cd bun_client
    ./start_server.sh
    ```

    This runs on `http://localhost:5172`.

2. Start the Frontend (Development Mode):
    ```bash
    cd bun_client
    bun run dev
    ```
    This runs on `http://localhost:5173`.

## Remote mode

By default the bridge spawns its own `crs --server`. To use a server your team
shares (`crs --listen`), point the bridge at it instead:

```bash
export CRS_REMOTE=crs.internal.example.com:443  # host:port of the shared server
export CRS_REMOTE_TLS=1                         # when a TLS proxy fronts it
export CRS_REGISTRATION_KEY=...                 # from its administrator; first run only
bun start
```

On each connection the bridge authenticates with the token in
`$CRS_HOME/remote_token` (default `~/.crs/remote_token`; `CRS_REMOTE_TOKEN`
wins when set). With no token, or one the server no longer knows, it registers
you with `CRS_REGISTRATION_KEY`, your GitHub login (`CRS_LOGIN`, else the root
`GithubUsername` of `~/.config/codereviewserver.toml`) and the workflows and
section settings of that file, then saves the token it is given with mode
`0600`. It reconnects on its own when the connection drops, and logs each step
under `[Remote]`, including what to set when something is missing.

On a shared server the configuration editor and plugin reruns are for its
administrators, submitting a review is refused unless the server allows acting
under its GitHub account, and the features that read files by path (the
language servers, the code viewer's file list, cached screenshots) work only
where the same paths exist on your machine. See
[Connecting to a shared server](../docs/clients.md#connecting-to-a-shared-server).

## Tests

```bash
bun run test        # unit tests (frontend + bridge)
bun run test:e2e    # Playwright end-to-end suite, see e2e/README.md
```

The e2e suite drives the built UI through the real bridge against a fake Go
backend and fake language servers, so it needs no GitHub token.

## Architecture

- **Backend (`server.ts`)**: Spawns the `crs` binary and bridges JSON-RPC communication over stdio — or, in remote mode, connects to a shared `crs --listen` over TCP and authenticates (`remote_rpc.ts`). Exposes HTTP POST `/api/rpc`.
- **Frontend (`frontend/`)**: React application interacting with the Bun backend.

## Features

- **List PRs**: View list of reviews (from `GetAllReviews`).
- **Review PR**: View PR details, diffs, and comments (from `GetPR`).
- **Embedded images**: Screenshots in descriptions, reviews and comments render
  inline. The browser can't authenticate to GitHub, so `GET /api/github-image` asks
  the Go server (which holds `CRS_GITHUB_TOKEN`) for the copy it downloaded.
- **Add Comments**: Add inline comments by specifying filename and position.
- **Submit Review**: Approve, Comment, or Request Changes.
