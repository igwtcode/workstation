# shellcheck shell=bash
# gum colors for the active theme — GUM_<COMMAND>_<PROPERTY> env vars
# (verified against gum 0.17 --help). Sourced once per shell from zsh so
# every gum prompt, including this repo's own scripts, follows the theme.
# Rendered by `mise run theme` into colors.sh; edit this template only.

export GUM_CHOOSE_CURSOR_FOREGROUND="{{accent}}"
export GUM_CHOOSE_HEADER_FOREGROUND="{{fg_dim}}"
export GUM_CHOOSE_ITEM_FOREGROUND="{{fg}}"
export GUM_CHOOSE_SELECTED_FOREGROUND="{{accent}}"

export GUM_CONFIRM_PROMPT_FOREGROUND="{{fg}}"
export GUM_CONFIRM_SELECTED_BACKGROUND="{{accent}}"
export GUM_CONFIRM_SELECTED_FOREGROUND="{{bg}}"
export GUM_CONFIRM_UNSELECTED_BACKGROUND="{{bg_alt}}"
export GUM_CONFIRM_UNSELECTED_FOREGROUND="{{fg_dim}}"

export GUM_FILTER_CURSOR_TEXT_FOREGROUND="{{fg}}"
export GUM_FILTER_HEADER_FOREGROUND="{{fg_dim}}"
export GUM_FILTER_INDICATOR_FOREGROUND="{{accent}}"
export GUM_FILTER_MATCH_FOREGROUND="{{accent}}"
export GUM_FILTER_PLACEHOLDER_FOREGROUND="{{fg_dim}}"
export GUM_FILTER_PROMPT_FOREGROUND="{{accent}}"
export GUM_FILTER_SELECTED_PREFIX_FOREGROUND="{{color2}}"
export GUM_FILTER_TEXT_FOREGROUND="{{fg}}"
export GUM_FILTER_UNSELECTED_PREFIX_FOREGROUND="{{fg_dim}}"

export GUM_INPUT_CURSOR_FOREGROUND="{{accent}}"
export GUM_INPUT_HEADER_FOREGROUND="{{fg_dim}}"
export GUM_INPUT_PLACEHOLDER_FOREGROUND="{{fg_dim}}"
export GUM_INPUT_PROMPT_FOREGROUND="{{accent}}"

export GUM_SPIN_SPINNER_FOREGROUND="{{accent}}"
export GUM_SPIN_TITLE_FOREGROUND="{{fg}}"
