# shellcheck shell=bash
# zsh colors for the active theme — sourced by core.zsh before the plugins.
# Rendered by `mise run theme` into colors.sh; edit this template only.
# zsh-autosuggestions only defaults its style when unset, and its default
# fg=8 reads under 2.5:1 against bg in most dark palettes — invisible.
# shellcheck disable=SC2034 # read by zsh-autosuggestions once it loads
ZSH_AUTOSUGGEST_HIGHLIGHT_STYLE="fg={{fg_dim}}"
