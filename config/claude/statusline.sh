#!/usr/bin/env bash
# Claude Code status line: model, context gauge, cumulative session tokens,
# 5h/7d rate limits, MCP call count.
#
# Colors come from colors.sh next to this file, rendered by `mise run theme`
# from the active palette — so the status line follows the machine's theme.
#
# This file is NOT a Claude config dir. Each identity zone has its own
# CLAUDE_CONFIG_DIR (~/.config/claude-work, ~/.config/claude-personal) holding
# that account's credentials and settings; this is one shared script that both
# of them point at, so the status line is themed once, not per zone:
#   "statusLine": { "type": "command", "command": "~/.config/ws/claude/statusline.sh" }
# Claude Code runs the command through a shell, so ~ expands.
set -uo pipefail

input=$(cat)

# --- palette ----------------------------------------------------------------
# Defaults keep the status line readable before the first `mise run theme`
# (colors.sh is generated, so a fresh checkout has none).
WS_SL_MODEL="#fabd2f" WS_SL_LOW="#b8bb26" WS_SL_MID="#fe8019" WS_SL_HIGH="#fb4934"
WS_SL_SESSION="#8ec07c" WS_SL_MCP="#83a598" WS_SL_LIMIT="#d3869b"
WS_SL_TEXT="#ebdbb2" WS_SL_MUTED="#928374"
_colors=${BASH_SOURCE[0]%/*}/colors.sh
# shellcheck source=/dev/null
[[ -f $_colors ]] && source "$_colors"

# sgr <#rrggbb> — 24-bit foreground escape. The palette stores hex; bash's
# printf reads 0x-prefixed literals as numbers, so no external tool is needed.
sgr() {
  local h=${1#\#}
  printf '\033[38;2;%d;%d;%dm' "0x${h:0:2}" "0x${h:2:2}" "0x${h:4:2}"
}

MODEL=$(sgr "$WS_SL_MODEL")
LOW=$(sgr "$WS_SL_LOW")
MID=$(sgr "$WS_SL_MID")
HIGH=$(sgr "$WS_SL_HIGH")
SESSION=$(sgr "$WS_SL_SESSION")
MCP=$(sgr "$WS_SL_MCP")
LIMIT=$(sgr "$WS_SL_LIMIT")
TEXT=$(sgr "$WS_SL_TEXT")
MUTED=$(sgr "$WS_SL_MUTED")
RST=$'\033[0m'

SEP="  ${MUTED}|${RST}  "

# --- input ------------------------------------------------------------------
model=$(jq -r '.model.display_name // "Unknown"' <<<"$input" | sed 's/^Claude //')
ctx_size=$(jq -r '.context_window.context_window_size // 0' <<<"$input")
total_in=$(jq -r '.context_window.total_input_tokens // 0' <<<"$input")
used_pct_raw=$(jq -r '.context_window.used_percentage // ""' <<<"$input")
transcript=$(jq -r '.transcript_path // ""' <<<"$input")
five_pct_raw=$(jq -r '.rate_limits.five_hour.used_percentage // ""' <<<"$input")
five_reset=$(jq -r '.rate_limits.five_hour.resets_at // ""' <<<"$input")
week_pct_raw=$(jq -r '.rate_limits.seven_day.used_percentage // ""' <<<"$input")

# --- helpers ----------------------------------------------------------------
fmt_num() {
  local n=${1:-0}
  if [[ $n -ge 1000000 ]]; then
    awk "BEGIN{printf \"%.1fm\", $n/1000000}"
  elif [[ $n -ge 1000 ]]; then
    awk "BEGIN{printf \"%.1fk\", $n/1000}"
  else
    printf '%s' "$n"
  fi
}

pct_color() {
  local p=${1:-0}
  if [[ $p -ge 80 ]]; then
    printf '%s' "$HIGH"
  elif [[ $p -ge 50 ]]; then
    printf '%s' "$MID"
  else
    printf '%s' "$LOW"
  fi
}

# Gauge: each filled cell takes the color of its OWN position along the bar
# (low up to 50%, mid 50-80%, high beyond), so a filling bar shifts color from
# left to right. Empty cells are muted.
build_bar() {
  local pct=${1:-0} width=10 filled empty i frac cell bar=""
  filled=$(awk "BEGIN{printf \"%d\", int($pct * $width / 100)}")
  empty=$((width - filled))
  for ((i = 0; i < filled; i++)); do
    frac=$(((i * 100) / width))
    if [[ $frac -ge 80 ]]; then
      cell=$HIGH
    elif [[ $frac -ge 50 ]]; then
      cell=$MID
    else
      cell=$LOW
    fi
    bar="${bar}${cell}█"
  done
  for ((i = 0; i < empty; i++)); do bar="${bar}${MUTED}░"; done
  printf '%s%s' "$bar" "$RST"
}

# --- transcript scan: MCP calls + cumulative session tokens ------------------
# Cumulative total = every API call's tokens across the session (input + cache
# creation + cache read + output). That is the number that counts toward the
# 5h/7d limits, because each call re-sends the whole growing context.
mcp_count=0
cum_tokens=0
if [[ -n $transcript && -f $transcript ]]; then
  mcp_count=$(grep -c '"name": *"mcp__[^"]*"' "$transcript" 2>/dev/null || true)
  cum_tokens=$(jq -r '
    select(.type == "assistant") |
    .message.usage |
    select(. != null) |
    ((.input_tokens // 0)
     + (.cache_creation_input_tokens // 0)
     + (.cache_read_input_tokens // 0)
     + (.output_tokens // 0))
  ' "$transcript" 2>/dev/null | awk '{s+=$1} END{print s+0}')
fi

# --- context gauge ----------------------------------------------------------
# Prefer Claude Code's used_percentage; otherwise derive it, so the bar and
# numbers are never blank. total_input_tokens already includes cache traffic.
if [[ -n $used_pct_raw ]]; then
  pct_int=$(awk "BEGIN{printf \"%d\", int($used_pct_raw)}")
elif [[ $ctx_size -gt 0 ]]; then
  pct_int=$(awk "BEGIN{printf \"%d\", int($total_in * 100 / $ctx_size)}")
else
  pct_int=0
fi
ctx_section="$(build_bar "$pct_int") ${TEXT}$(fmt_num "$total_in")/$(fmt_num "$ctx_size")${RST} ${MID}${pct_int}%${RST}"

# --- cumulative session tokens ----------------------------------------------
cum_section=""
if [[ $cum_tokens -gt 0 ]]; then
  cum_section="${MUTED}sess${RST} ${SESSION}$(fmt_num "$cum_tokens")${RST}"
fi

# --- 5-hour limit: gauge, percentage, reset countdown -----------------------
if [[ -n $five_pct_raw ]]; then
  five_int=$(awk "BEGIN{printf \"%d\", int($five_pct_raw)}")
  rst_str=""
  if [[ -n $five_reset ]]; then
    secs_left=$((five_reset - $(date +%s)))
    if [[ $secs_left -le 0 ]]; then
      rst_str="  ${MUTED}rst:now${RST}"
    elif [[ $((secs_left / 3600)) -gt 0 ]]; then
      rst_str="  ${MUTED}rst:$((secs_left / 3600))h$(((secs_left % 3600) / 60))m${RST}"
    else
      rst_str="  ${MUTED}rst:$((secs_left / 60))m${RST}"
    fi
  fi
  five_section="${LIMIT}5h${RST} $(build_bar "$five_int") $(pct_color "$five_int")${five_int}%${RST}${rst_str}"
else
  five_section="${LIMIT}5h${RST} ${MUTED}░░░░░░░░░░${RST} ${MUTED}--%${RST}"
fi

# --- 7-day limit ------------------------------------------------------------
if [[ -n $week_pct_raw ]]; then
  week_int=$(awk "BEGIN{printf \"%d\", int($week_pct_raw)}")
  week_section="${LIMIT}7d${RST} $(pct_color "$week_int")${week_int}%${RST}"
else
  week_section="${LIMIT}7d${RST} ${MUTED}--%${RST}"
fi

# --- assemble ---------------------------------------------------------------
out="${MODEL}${model}${RST}${SEP}${ctx_section}"
[[ -n $cum_section ]] && out="${out}${SEP}${cum_section}"
out="${out}${SEP}${five_section}${SEP}${week_section}"
[[ $mcp_count -gt 0 ]] && out="${out}${SEP}${MCP}mcp:${mcp_count}${RST}"

printf '%s\n' "$out"
