#!/bin/bash
# Ensure we use the native bun installation if available to avoid Snap confinement issues
export PATH="$HOME/.bun/bin:$PATH"

if ! command -v bun &> /dev/null; then
    echo "Bun is not installed or not in PATH."
    exit 1
fi

echo "Using Bun from: $(which bun)"
bun run server.ts
