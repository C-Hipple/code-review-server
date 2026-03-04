# =============================================================================
# Stage 1: Build Go binary + plugin binaries
# =============================================================================
FROM golang:1.25 AS go-builder

WORKDIR /src

# Use the installed toolchain; don't try to auto-download a newer one
ENV GOTOOLCHAIN=local

# Download dependencies first (layer cache)
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .

RUN mkdir -p /out

# CGO is required for go-sqlite3
RUN CGO_ENABLED=1 go build -o /out/crs .

# Build all plugin binaries from cmd/
RUN for d in cmd/*/; do \
        name=$(basename "$d"); \
        CGO_ENABLED=1 go build -o /out/"$name" ./"$d"; \
    done

# =============================================================================
# Stage 2: Build Bun frontend + compile crs-gui
# =============================================================================
FROM oven/bun:latest AS bun-builder

WORKDIR /src/bun_client

# Install dependencies (layer cache)
COPY bun_client/package.json bun_client/bun.lockb* ./
COPY bun_client/frontend/package.json ./frontend/
RUN bun install

# Copy full source and build
COPY bun_client/ ./

# 1. Build React frontend (Vite → frontend/dist/)
# 2. Generate embedded_assets.ts from dist/
# 3. Compile server.ts + assets into self-contained crs-gui binary
RUN bun --filter frontend build && \
    bun scripts/build.ts && \
    bun build server.ts --compile --outfile crs-gui

# =============================================================================
# Stage 3: Runtime image
# =============================================================================
FROM debian:bookworm-slim

# ca-certificates for HTTPS calls to GitHub API / Gemini
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Copy Go binary and plugins
COPY --from=go-builder /out/crs             /usr/local/bin/crs
COPY --from=go-builder /out/security_check  /usr/local/bin/security_check
COPY --from=go-builder /out/summarize_diff  /usr/local/bin/summarize_diff
COPY --from=go-builder /out/debug_diff      /usr/local/bin/debug_diff
COPY --from=go-builder /out/debug_comments  /usr/local/bin/debug_comments
COPY --from=go-builder /out/example_plugin  /usr/local/bin/example_plugin

# Copy compiled Bun server (spawns crs automatically)
COPY --from=bun-builder /src/bun_client/crs-gui /usr/local/bin/crs-gui

# Run as non-root
RUN useradd -m -u 1001 crs
USER crs

# /home/crs/data → SQLite database, persisted via Docker volume
RUN mkdir -p /home/crs/.config /home/crs/data
VOLUME ["/home/crs/data"]

# Tell the app where to find config and data
ENV XDG_CONFIG_HOME=/home/crs/.config
ENV CRS_HOME=/home/crs/data

# Web UI port (override with -e PORT=...)
ENV PORT=5172
EXPOSE 5172

ENTRYPOINT ["/usr/local/bin/crs-gui"]
