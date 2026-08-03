# shellcheck shell=bash
# fzf colors for the active theme — appended to FZF_DEFAULT_OPTS.
# Source once per shell (zsh does), after any base FZF_DEFAULT_OPTS.
# Rendered by `mise run theme` into colors.sh; edit this template only.
export FZF_DEFAULT_OPTS="${FZF_DEFAULT_OPTS:-} --color=fg:{{fg}},bg:{{bg}},hl:{{accent}},fg+:{{fg}},bg+:{{selection_bg}},hl+:{{accent}},gutter:{{bg}},query:{{fg}},disabled:{{fg_dim}},info:{{fg_dim}},border:{{border}},separator:{{border}},scrollbar:{{border}},prompt:{{accent}},pointer:{{accent}},marker:{{color2}},spinner:{{accent}},header:{{fg_dim}},label:{{fg_dim}}"
