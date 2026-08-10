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

## Architecture

- **Backend (`server.ts`)**: Spawns the `crs` binary and bridges JSON-RPC communication over stdio. Exposes HTTP POST `/api/rpc`.
- **Frontend (`frontend/`)**: React application interacting with the Bun backend.

## Features

- **List PRs**: View list of reviews (from `GetAllReviews`).
- **Review PR**: View PR details, diffs, and comments (from `GetPR`).
- **Embedded images**: Screenshots in descriptions, reviews and comments render
  inline. Attachments on a private repo are fetched with `CRS_GITHUB_TOKEN` through
  `GET /api/github-image`, since the browser can't authenticate to GitHub itself.
- **Add Comments**: Add inline comments by specifying filename and position.
- **Submit Review**: Approve, Comment, or Request Changes.
