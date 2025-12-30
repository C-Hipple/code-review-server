# Code Review Server Client (Bun + React)

## Setup

1. Install dependencies for backend:
   ```bash
   cd bun_client
   bun install
   ```

2. Install dependencies for frontend:
   ```bash
   cd frontend
   bun install
   ```

## Running

1. Start the Bun Backend (API Server):
   ```bash
   # Make sure GTDBOT_GITHUB_TOKEN is set
   cd bun_client
   ./start_server.sh
   ```
   This runs on `http://localhost:3000`.

2. Start the Frontend (Development Mode):
   ```bash
   cd bun_client/frontend
   bun run dev
   ```
   This runs on `http://localhost:5173`.

## Architecture

- **Backend (`server.ts`)**: Spawns the `crs` binary and bridges JSON-RPC communication over stdio. Exposes HTTP POST `/api/rpc`.
- **Frontend (`frontend/`)**: React application interacting with the Bun backend.

## Features

- **List PRs**: View list of reviews (from `GetAllReviews`).
- **Review PR**: View PR details, diffs, and comments (from `GetPR`).
- **Add Comments**: Add inline comments by specifying filename and position.
- **Submit Review**: Approve, Comment, or Request Changes.
