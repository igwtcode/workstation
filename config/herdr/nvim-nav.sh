#!/usr/bin/env bash
# Bound to bare ctrl+h/j/k/l in config-template.toml. Forwards the keystroke
# into the focused pane when it's running vim/nvim (so nvim's own splits
# handle it), otherwise moves focus between herdr panes directly.
set -euo pipefail

direction=$1
case "$direction" in
  left) key=ctrl+h ;;
  down) key=ctrl+j ;;
  up) key=ctrl+k ;;
  right) key=ctrl+l ;;
  *)
    echo "usage: nvim-nav.sh <left|down|up|right>" >&2
    exit 1
    ;;
esac

info=$(herdr pane process-info --current)
pane_id=$(jq -r '.result.process_info.pane_id' <<<"$info")

if jq -e '.result.process_info.foreground_processes[] | select(.name == "nvim" or .name == "vim")' <<<"$info" >/dev/null; then
  herdr pane send-keys "$pane_id" "$key"
else
  herdr pane focus --current --direction "$direction"
fi
