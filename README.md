# Screenshots for the Emacs client's comment rendering PRs

Not meant to be merged: this branch only holds the images the PR bodies
show, so they stay out of `main`.

Each image is a real GUI Emacs 29 frame (modus-vivendi / modus-operandi,
JetBrains Mono) showing the same sample PR, its diff washed by git-delta
the way magit-delta does (`--color-only`). `harness/shot.el` renders the
sample through `crs--render-and-update` and exports the frame:

    harness/run.sh <client.el dir> <out prefix> [theme] [scenes]

- `*-expanded.png`: comments and annotations shown
- `*-collapsed.png`: both hidden (`H`), as `<C: …>` / `<A: …>` indicators
- `main-*` is the client as it is on main; `quiet`, `cards`, `compact`,
  `compactcards` the four options.
