#!/usr/bin/env bash
# Theme renderer for themes/*/{dark,light}.toml — requires lib/common.sh.
#
# Plain {{token}} substitution and nothing else (no filters, no logic).
# Tokens come from the palette schema (themes/README.md); every #rrggbb
# value also gets a derived <token>_strip form (hex without '#').
# Templates and outputs are declared in themes/registry.

set -euo pipefail

[[ -n ${_WS_THEME_SOURCED:-} ]] && return 0
_WS_THEME_SOURCED=1

# The palette contract — themes/README.md documents it; adding a token to a
# template means extending this list and every palette in the same change.
WS_THEME_TOKENS=(
  name mode
  bg bg_alt fg fg_dim accent selection_bg selection_fg cursor border
  color0 color1 color2 color3 color4 color5 color6 color7
  color8 color9 color10 color11 color12 color13 color14 color15
)

# Associative arrays need bash 4+, and a mac runs Apple's 3.2 until brew
# installs bash from pkg/mac-brew.txt. Sourcing has to stay harmless there —
# bootstrap reads theme_current before that install; rendering refuses below.
if ((${BASH_VERSINFO[0]:-0} >= 4)); then
  declare -gA WS_THEME=()
fi

# theme_list <repo-root> — print available theme ids (<name>-<mode>)
theme_list() {
  local root=$1 dir mode
  for dir in "$root"/themes/*/; do
    for mode in dark light; do
      if [[ -f $dir$mode.toml ]]; then
        echo "$(basename "$dir")-$mode"
      fi
    done
  done
  return 0
}

# theme_palette_file <repo-root> <theme-id> — resolve id to its palette path
theme_palette_file() {
  local root=$1 id=$2 name mode file
  name=${id%-*}
  mode=${id##*-}
  file=$root/themes/$name/$mode.toml
  if [[ $id != *-* || ! -f $file ]]; then
    die "unknown theme '$id' — available: $(theme_list "$root" | paste -sd' ' -)"
  fi
  echo "$file"
}

# theme_load <palette-file> — parse key = "value" lines into WS_THEME,
# derive *_strip forms, and enforce the full token contract.
theme_load() {
  local file=$1 line key val
  ((${BASH_VERSINFO[0]:-0} >= 4)) ||
    die "theme rendering needs bash 4+ (running $BASH_VERSION) — run 'brew install bash' and re-run"
  WS_THEME=()
  while IFS= read -r line || [[ -n $line ]]; do
    [[ $line =~ ^[[:space:]]*([a-z0-9_]+)[[:space:]]*=[[:space:]]*\"([^\"]*)\" ]] || continue
    key=${BASH_REMATCH[1]}
    val=${BASH_REMATCH[2]}
    WS_THEME[$key]=$val
    if [[ $val =~ ^#[0-9a-f]{6}$ ]]; then
      WS_THEME[${key}_strip]=${val#\#}
    fi
  done <"$file"
  local token
  for token in "${WS_THEME_TOKENS[@]}"; do
    [[ -n ${WS_THEME[$token]:-} ]] || die "palette $file missing token '$token'"
  done
}

# theme_render <template> <output> — substitute {{token}}s from WS_THEME;
# unknown or unterminated tokens are fatal (the /audit check relies on this).
theme_render() {
  local template=$1 output=$2 rest token out=""
  [[ -f $template ]] || die "template not found: $template"
  rest=$(<"$template")
  while [[ $rest == *'{{'* ]]; do
    out+=${rest%%\{\{*}
    rest=${rest#*\{\{}
    [[ $rest == *'}}'* ]] || die "unterminated {{ in $template"
    token=${rest%%\}\}*}
    rest=${rest#*\}\}}
    [[ -n ${WS_THEME[$token]:-} ]] || die "unknown token '{{$token}}' in $template"
    out+=${WS_THEME[$token]}
  done
  out+=$rest
  mkdir -p "$(dirname "$output")"
  printf '%s\n' "$out" >"$output"
}

# theme_apply <repo-root> <theme-id> — render every registry entry, run
# post-hooks, record the active theme in the state file.
theme_apply() {
  local root=$1 id=$2 palette registry line template output hook
  palette=$(theme_palette_file "$root" "$id")
  theme_load "$palette"
  registry=$root/themes/registry
  [[ -f $registry ]] || die "registry not found: $registry"

  local -a hooks=()
  while read -r template output hook || [[ -n ${template:-} ]]; do
    [[ -z $template || $template == \#* ]] && continue
    [[ -n $output ]] || die "malformed registry line: '$template'"
    theme_render "$root/$template" "$root/$output"
    log "rendered $output"
    [[ -n $hook && $hook != - ]] && hooks+=("$hook")
  done < <(tr -s '[:blank:]' ' ' <"$registry")

  for hook in "${hooks[@]:-}"; do
    [[ -n $hook ]] || continue
    # post-hook failure is non-fatal by design: reload targets (tmux, nvim,
    # …) may simply not be running
    if (cd "$root" && bash -c "$hook"); then
      log "post-hook ok: $hook"
    else
      warn "post-hook failed: $hook"
    fi
  done

  local state_dir=${XDG_STATE_HOME:-$HOME/.local/state}/ws
  mkdir -p "$state_dir"
  printf '%s\n' "$id" >"$state_dir/theme"
  log "theme: $id"
}

# theme_current — print the recorded active theme id, if any
theme_current() {
  local state=${XDG_STATE_HOME:-$HOME/.local/state}/ws/theme
  [[ -f $state ]] && cat "$state" || true
}
