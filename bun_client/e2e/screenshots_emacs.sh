#!/bin/sh
# Takes the Emacs client screenshots in docs/img (see screenshots.el): a
# graphical Emacs under Xvfb, talking to the fake backend over the demo
# fixtures. Needs emacs, markdown-mode, xvfb-run, ImageMagick's `import`,
# the DejaVu fonts and bun.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT

# The client runs `crs -server`; this one is the e2e fake.
cat > "$bin/crs" <<EOF
#!/bin/sh
CRS_E2E_FIXTURES=demo exec '$(command -v bun)' '$here/harness/fake_crs.ts' "\$@"
EOF
chmod +x "$bin/crs"

PATH="$bin:$PATH" \
CRS_SHOT_DIR="$root/docs/img" \
CRS_SHOT_CLIENT="$root/client.el" \
    xvfb-run -a -s "-screen 0 1600x1000x24" emacs -Q -l "$here/screenshots.el"
