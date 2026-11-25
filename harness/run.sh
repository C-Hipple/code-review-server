#!/bin/bash
# run.sh CRS_DIR OUT_PREFIX [THEME] [SCENES]
here=$(cd "$(dirname "$0")" && pwd)
CRS_DIR="$1" SHOT_OUT="$2" SHOT_THEME="${3:-modus-vivendi}" SHOT_SCENES="${4:-expanded,collapsed}" \
  xvfb-run -a -s "-screen 0 1800x1400x24" emacs -Q -l "$here/shot.el" 2>"$2.stderr"
ls -la "$2"* 2>/dev/null | grep -v stderr
[ -f "$2-error.txt" ] && cat "$2-error.txt"
true
