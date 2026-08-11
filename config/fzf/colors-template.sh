# shellcheck shell=bash
# fzf colors for the active theme — appended to FZF_DEFAULT_OPTS.
# Source once per shell (zsh does), after any base FZF_DEFAULT_OPTS.
# Rendered by `mise run theme` into colors.sh; edit this template only.
# The current row must use selection_bg with its own selection_fg — pairing
# it with plain fg is what made the text unreadable.
# bg/gutter use -1 (fzf's "default terminal color") so the terminal's own
# background shows through instead of a hardcoded, opaque palette color.
export FZF_DEFAULT_OPTS="${FZF_DEFAULT_OPTS:-} --color=fg:{{fg}},bg:-1,hl:{{accent}},fg+:{{selection_fg}}:bold,bg+:{{selection_bg}},hl+:{{accent}}:bold,gutter:-1,query:{{fg}},disabled:{{fg_dim}},info:{{fg_dim}},border:{{border}},separator:{{border}},scrollbar:{{border}},prompt:{{accent}},pointer:{{accent}},marker:{{color2}},spinner:{{accent}},header:{{fg_dim}},label:{{fg_dim}}"
