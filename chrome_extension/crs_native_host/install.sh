#!/usr/bin/env bash
# Registers crs_native_host with Chrome-family browsers, so the Code Review
# Server extension can start codereviewserver. Rerun it after pulling to
# rebuild the host. See --help.
#
# Written for bash 3.2 too (macOS's /bin/bash): no mapfile, no ${x,,}, and
# empty arrays are expanded with the ${a[@]+"${a[@]}"} guard under set -u.
set -euo pipefail

readonly HOST_NAME="com.c_hipple.crs"
readonly DEFAULT_EXTENSION_ID="acmghogknbbihjoejbkejhhikiapmiib"
# What --capture-env records besides PATH: everything the server and its
# plugins read from the environment, and the host's own CRS_SERVER_PATH.
captured_vars=(CRS_GITHUB_TOKEN CRS_HOME GEMINI_API_KEY OPENROUTER_API_KEY CRS_STYLE_GUIDE_DIR
    CRS_LLM_PROVIDER CRS_LLM_MODEL CRS_SERVER_PATH)

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../.." && pwd)"
default_host_path="$HOME/.crs/bin/crs_native_host"
env_file="$HOME/.crs/native_host.env"
os="$(uname -s)"

usage() {
    cat <<EOF
Usage: $(basename "$0") [options]

Builds crs_native_host and registers it as the native messaging host
"$HOST_NAME" for the Code Review Server extension.

By default the host is built with \`go build\` into $default_host_path and
its manifest is written for every installed browser found among Chrome,
Chrome Beta, Chromium, Brave and Edge.

Options:
  --host-path PATH     register an existing crs_native_host binary instead of
                       building one
  --browser NAME       register with this browser only: chrome, chromium,
                       brave or edge (repeatable)
  --manifest-dir DIR   write the host manifest into DIR (repeatable), for a
                       browser or profile not covered by --browser
  --extension-id ID    allow this extension ID instead of the pinned
                       $DEFAULT_EXTENSION_ID
  --capture-env        write $env_file (mode 600) from this shell:
                       PATH plus whichever of these are set:
                       ${captured_vars[*]}
  --uninstall          remove the host manifests (leaves the binary and the
                       env file)
  -h, --help           show this help
EOF
}

die() {
    echo "$(basename "$0"): $*" >&2
    exit 1
}

host_path=""
browsers=()
manifest_dirs=()
extension_id="$DEFAULT_EXTENSION_ID"
capture_env=0
uninstall=0

need_value() {
    [[ $2 -ge 2 ]] || die "$1 needs a value (see --help)"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --host-path) need_value "$1" $#; host_path="$2"; shift 2 ;;
        --host-path=*) host_path="${1#*=}"; shift ;;
        --browser) need_value "$1" $#; browsers+=("$2"); shift 2 ;;
        --browser=*) browsers+=("${1#*=}"); shift ;;
        --manifest-dir) need_value "$1" $#; manifest_dirs+=("$2"); shift 2 ;;
        --manifest-dir=*) manifest_dirs+=("${1#*=}"); shift ;;
        --extension-id) need_value "$1" $#; extension_id="$2"; shift 2 ;;
        --extension-id=*) extension_id="${1#*=}"; shift ;;
        --capture-env) capture_env=1; shift ;;
        --uninstall) uninstall=1; shift ;;
        -h | --help) usage; exit 0 ;;
        *) die "unknown option: $1 (see --help)" ;;
    esac
done

# Chrome derives extension IDs from the letters a-p.
[[ "$extension_id" =~ ^[a-p]{32}$ ]] || die "--extension-id must be 32 letters a-p, got: $extension_id"
for b in ${browsers[@]+"${browsers[@]}"}; do
    case "$b" in
        chrome | chromium | brave | edge) ;;
        *) die "unknown browser: $b (use chrome, chromium, brave or edge)" ;;
    esac
done

# browser_dirs NAME prints the browser's user data directories, one per line,
# the main channel first. Each one's NativeMessagingHosts subdirectory is
# where Chrome looks for user-level hosts.
browser_dirs() {
    local base
    case "$os" in
        Darwin)
            base="$HOME/Library/Application Support"
            case "$1" in
                chrome) printf '%s\n' "$base/Google/Chrome" "$base/Google/Chrome Beta" ;;
                chromium) printf '%s\n' "$base/Chromium" ;;
                brave) printf '%s\n' "$base/BraveSoftware/Brave-Browser" ;;
                edge) printf '%s\n' "$base/Microsoft Edge" ;;
            esac
            ;;
        Linux)
            # Chrome puts its user data under XDG_CONFIG_HOME when it is set.
            base="${XDG_CONFIG_HOME:-$HOME/.config}"
            case "$1" in
                chrome) printf '%s\n' "$base/google-chrome" "$base/google-chrome-beta" ;;
                chromium) printf '%s\n' "$base/chromium" ;;
                brave) printf '%s\n' "$base/BraveSoftware/Brave-Browser" ;;
                edge) printf '%s\n' "$base/microsoft-edge" ;;
            esac
            ;;
    esac
}

# Where the manifest goes: --manifest-dir entries as given; each named
# browser's existing data directories (its main one when none exists yet, so
# naming a browser before first running it still works); with neither, every
# browser found. Uninstalling considers every directory, existing or not.
targets=()
for d in ${manifest_dirs[@]+"${manifest_dirs[@]}"}; do
    targets+=("$d")
done
if [[ ${#browsers[@]} -gt 0 || ${#manifest_dirs[@]} -eq 0 ]]; then
    if [[ "$os" != Darwin && "$os" != Linux ]]; then
        die "browser directories are only known on Linux and macOS; use --manifest-dir"
    fi
    if [[ ${#browsers[@]} -gt 0 ]]; then
        explicit_browsers=1
        candidates=("${browsers[@]}")
    else
        explicit_browsers=0
        candidates=(chrome chromium brave edge)
    fi
    for b in "${candidates[@]}"; do
        first=""
        found=0
        while IFS= read -r d; do
            [[ -n "$first" ]] || first="$d"
            if [[ -d "$d" || $uninstall -eq 1 ]]; then
                targets+=("$d/NativeMessagingHosts")
                found=1
            fi
        done < <(browser_dirs "$b")
        if [[ $found -eq 0 && $explicit_browsers -eq 1 ]]; then
            targets+=("$first/NativeMessagingHosts")
        fi
    done
fi

if [[ $uninstall -eq 1 ]]; then
    removed=0
    for d in ${targets[@]+"${targets[@]}"}; do
        if [[ -f "$d/$HOST_NAME.json" ]]; then
            rm -f "$d/$HOST_NAME.json"
            echo "Removed $d/$HOST_NAME.json"
            removed=1
        fi
    done
    [[ $removed -eq 1 ]] || echo "No $HOST_NAME.json manifests found to remove."
    echo "Left in place: the host binary (default $default_host_path) and $env_file."
    exit 0
fi

[[ ${#targets[@]} -gt 0 ]] ||
    die "no supported browser found; name one with --browser, or give --manifest-dir"

if [[ -z "$host_path" ]]; then
    command -v go >/dev/null 2>&1 ||
        die "go is not on PATH; install Go, or pass --host-path to an existing crs_native_host"
    host_path="$default_host_path"
    mkdir -p "$(dirname "$host_path")"
    echo "Building crs_native_host into $host_path"
    (cd "$repo_root" && go build -o "$host_path" ./chrome_extension/crs_native_host)
fi
[[ -f "$host_path" && -x "$host_path" ]] || die "not an executable file: $host_path"
# The manifest needs an absolute path.
host_path="$(cd "$(dirname "$host_path")" && pwd)/$(basename "$host_path")"

json_escape() {
    local s="$1"
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    printf '%s' "$s"
}

echo "Registering $HOST_NAME for chrome-extension://$extension_id/"
for d in "${targets[@]}"; do
    mkdir -p "$d"
    cat >"$d/$HOST_NAME.json" <<EOF
{
  "name": "$HOST_NAME",
  "description": "Code Review Server bridge",
  "path": "$(json_escape "$host_path")",
  "type": "stdio",
  "allowed_origins": ["chrome-extension://$extension_id/"]
}
EOF
    echo "  wrote $d/$HOST_NAME.json"
done

if [[ $capture_env -eq 1 ]]; then
    mkdir -p "$(dirname "$env_file")"
    if [[ -f "$env_file" ]]; then
        cp -p "$env_file" "$env_file.bak"
        chmod 600 "$env_file.bak"
        echo "Saved the previous env file as $env_file.bak"
    fi
    # mktemp creates the file 0600, so the secrets are never readable by
    # others, even before the chmod.
    tmp="$(mktemp "$env_file.XXXXXX")"
    captured=""
    {
        echo "# Environment for crs_native_host, captured by install.sh --capture-env"
        echo "# on $(date)."
        echo "# A variable here overrides the one the browser starts the host with."
        echo "# KEY=\"VALUE\" lines: the outer quotes are removed and nothing inside"
        echo "# them is expanded. Edit freely, or rerun --capture-env."
        for name in PATH "${captured_vars[@]}"; do
            value="${!name:-}"
            [[ -n "$value" ]] || continue
            if [[ "$value" == *$'\n'* ]]; then
                echo "Skipping $name: its value spans lines" >&2
                continue
            fi
            printf '%s="%s"\n' "$name" "$value"
            captured="$captured $name"
        done
    } >"$tmp"
    chmod 600 "$tmp"
    mv "$tmp" "$env_file"
    echo "Wrote $env_file (mode 600) with:$captured"
else
    echo
    echo "Note: browsers start the host with the desktop session's environment, not your"
    echo "shell's, so PATH additions, CRS_GITHUB_TOKEN and API keys from your shell rc files"
    echo "don't reach it. The host reads KEY=VALUE lines from $env_file,"
    echo "and a variable there overrides the inherited one."
    if [[ -f "$env_file" ]]; then
        echo "That file exists and will be used."
    else
        echo "Rerun with --capture-env to write it from this shell (PATH plus whichever of"
        echo "${captured_vars[*]} are set), or write it by hand."
    fi
fi

# Point out early if the host will have nothing to start. These are the
# places the host searches that don't depend on its environment.
server=""
for d in "$(dirname "$host_path")" "${GOBIN:-}" "${GOPATH:+${GOPATH%%:*}/bin}" "$HOME/go/bin"; do
    if [[ -n "$d" && -x "$d/codereviewserver" ]]; then
        server="$d/codereviewserver"
        break
    fi
done
echo
if [[ -n "$server" ]]; then
    echo "Server: $server"
elif [[ -x "${CRS_SERVER_PATH:-}" ]] || command -v codereviewserver >/dev/null 2>&1; then
    echo "Server: found through this shell's CRS_SERVER_PATH or PATH, which the browser may"
    echo "not share. The host sees them through $env_file (--capture-env writes it)."
else
    echo "codereviewserver was not found. Install it with: (cd $repo_root && go install ./...)"
fi

cat <<EOF

Next steps:
  1. Build the extension:  (cd $repo_root/chrome_extension && bun install && bun run build)
  2. Open chrome://extensions (or your browser's equivalent), turn on Developer mode,
     and "Load unpacked": $repo_root/chrome_extension/dist
     The extension ID should read $extension_id.
  3. On github.com, click the Code Review Server toolbar button (Alt+Shift+R).

The host logs to \$CRS_HOME/native_host.log (default ~/.crs/native_host.log).
EOF
