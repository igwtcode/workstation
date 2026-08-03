# shellcheck shell=bash
# Claude Code statusline colors for the active theme, as #rrggbb.
# Sourced by statusline.sh, which converts them to 24-bit SGR escapes —
# the palette has no decimal-RGB form and the renderer has no filters.
# Rendered by `mise run theme` into colors.sh; edit this template only.

# shellcheck disable=SC2034 # every var here is read by the sourcing script

WS_SL_MODEL="{{color11}}"   # model name
WS_SL_LOW="{{color10}}"     # gauge, under 50%
WS_SL_MID="{{accent}}"      # gauge 50-80%, and percentages
WS_SL_HIGH="{{color9}}"     # gauge over 80%
WS_SL_SESSION="{{color14}}" # cumulative session tokens
WS_SL_MCP="{{color12}}"     # mcp call count
WS_SL_LIMIT="{{color13}}"   # rate-limit labels (5h / 7d)
WS_SL_TEXT="{{fg}}"         # token counts and labels
WS_SL_MUTED="{{fg_dim}}"    # separators, empty gauge cells, reset countdown
