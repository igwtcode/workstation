#!/usr/bin/env bash
set -euo pipefail

[[ -n ${_WS_COMMON_SOURCED:-} ]] && return 0
_WS_COMMON_SOURCED=1

# shellcheck disable=SC2034
readonly WS_EXIT_DECLINED=75
readonly WS_EXIT_USER_DECLINED=76

ws_has() { command -v "$1" >/dev/null 2>&1; }

log() {
  if ws_has gum; then
    gum log --level info "$*"
  else
    printf 'info: %s\n' "$*"
  fi
}

warn() {
  if ws_has gum; then
    gum log --level warn "$*"
  else
    printf 'warn: %s\n' "$*" >&2
  fi
}

die() {
  if ws_has gum; then
    gum log --level error "$*"
  else
    printf 'error: %s\n' "$*" >&2
  fi
  exit 1
}

require_cmd() {
  local cmd
  for cmd in "$@"; do
    ws_has "$cmd" || die "required command not found: $cmd"
  done
}

declined_exit() {
  warn "${1:-hook}: declined"
  if [[ ${WS_NON_INTERACTIVE:-0} == 1 || ! -t 0 || ! -t 2 ]]; then
    exit "$WS_EXIT_DECLINED"
  fi
  exit "$WS_EXIT_USER_DECLINED"
}

confirm() {
  local prompt=${1:-Continue?}
  if [[ ${WS_YES:-0} == 1 || ${WS_YES:-} == true ]]; then
    return 0
  fi
  if [[ ${WS_NON_INTERACTIVE:-0} == 1 ]]; then
    warn "non-interactive: declined '$prompt' (rerun with --yes to accept)"
    return 1
  fi
  if [[ ! -t 0 || ! -t 2 ]]; then
    warn "non-interactive session: declined '$prompt' (rerun with --yes to accept)"
    return 1
  fi
  if ws_has gum; then
    gum confirm "$prompt"
  else
    local reply
    read -r -p "$prompt [y/N] " reply
    [[ $reply == [yY]* ]]
  fi
}
