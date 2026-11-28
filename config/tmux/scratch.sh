#!/usr/bin/env bash
set -euo pipefail

dir=$1 owner=$2
name="_popup-scratch-$owner"
name=${name//[.:]/_}
sock=${TMUX%%,*}

TMUX='' exec tmux ${sock:+-S "$sock"} new-session -A -s "$name" -c "$dir" \
  \; set-option -t "$name" status off \
  \; set-option -t "$name" detach-on-destroy on
