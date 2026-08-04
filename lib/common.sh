#!/usr/bin/env bash
# Shared helpers for every workstation script: logging, prompts, pickers,
# OS/profile detection. Source it — entry points stay thin:
#   source "$root/lib/common.sh"
#
# Interactive helpers honor WS_YES=1 (set by --yes flags) and fail with a
# clear message instead of hanging when no TTY is available.

set -euo pipefail

[[ -n ${_WS_COMMON_SOURCED:-} ]] && return 0
_WS_COMMON_SOURCED=1

# ws_has <cmd> — true if the command exists
ws_has() { command -v "$1" >/dev/null 2>&1; }

# styled <color> <text> — one line in the given ANSI color (plain without gum)
styled() {
  local color=$1
  shift
  if ws_has gum; then
    gum style --foreground "$color" "$*"
  else
    echo "$*"
  fi
}

# log/warn/die — leveled messages; die exits 1
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

# require_cmd <cmd>... — die if any command is missing
require_cmd() {
  local cmd
  for cmd in "$@"; do
    ws_has "$cmd" || die "required command not found: $cmd"
  done
}

# confirm <prompt> — yes/no. WS_YES=1 auto-accepts; without a TTY it dies
# instead of hanging (pass --yes / set WS_YES=1 in non-interactive runs).
confirm() {
  local prompt=${1:-Continue?}
  if [[ ${WS_YES:-0} == 1 || ${WS_YES:-} == true ]]; then
    return 0
  fi
  if [[ ! -t 0 || ! -t 2 ]]; then
    die "non-interactive session: '$prompt' needs --yes (or WS_YES=1)"
  fi
  if ws_has gum; then
    gum confirm "$prompt"
  else
    local reply
    read -r -p "$prompt [y/N] " reply
    [[ $reply == [yY]* ]]
  fi
}

# pick <prompt> — fzf picker over stdin lines; prints the selection.
# Callers must offer a non-interactive path (positional arg) around it.
pick() {
  local prompt=${1:->}
  require_cmd fzf
  if [[ ! -t 2 ]]; then
    die "non-interactive session: cannot show a picker — pass the value directly"
  fi
  # --no-multi on purpose: callers consume exactly one value, and the
  # interactive shell turns --multi on globally via FZF_DEFAULT_OPTS
  fzf --height=40% --reverse --no-multi --prompt="$prompt "
}

# os_detect — mac | arch | linux (generic: debian/ubuntu/fedora/…).
# Arch derivatives report themselves through ID_LIKE and are treated as
# arch; there is no per-derivative profile.
os_detect() {
  if [[ $(uname -s) == Darwin ]]; then
    echo mac
    return
  fi
  local id="" id_like=""
  if [[ -r /etc/os-release ]]; then
    # shellcheck disable=SC1091
    id=$(. /etc/os-release && echo "${ID:-}")
    # shellcheck disable=SC1091
    id_like=$(. /etc/os-release && echo "${ID_LIKE:-}")
  fi
  if [[ $id == arch || " $id_like " == *" arch "* ]]; then
    echo arch
  else
    echo linux
  fi
}

# resolve_profile [explicit] — flag > $WS_PROFILE > detected OS.
# Prints one of: mac | arch | server
resolve_profile() {
  local profile=${1:-${WS_PROFILE:-}}
  if [[ -z $profile ]]; then
    case $(os_detect) in
      mac) profile=mac ;;
      arch) profile=arch ;;
      *) profile=server ;;
    esac
  fi
  case $profile in
    mac | arch | server) echo "$profile" ;;
    *) die "unknown profile '$profile' (expected mac|arch|server)" ;;
  esac
}
