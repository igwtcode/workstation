#!/usr/bin/env bash
set -euo pipefail

WS_REPO=${WS_REPO:-https://github.com/igwtcode/workstation.git}
WS_DIR=${WS_DIR:-$HOME/personal/code/igwtcode/workstation}

if [[ -t 2 ]]; then
  _c_info=$'\033[1;34m' _c_err=$'\033[1;31m' _c_off=$'\033[0m'
else
  _c_info='' _c_err='' _c_off=''
fi

log() { printf '%s==>%s %s\n' "$_c_info" "$_c_off" "$*" >&2; }
die() {
  printf '%serror:%s %s\n' "$_c_err" "$_c_off" "$*" >&2
  exit 1
}
has() { command -v "$1" >/dev/null 2>&1; }

usage() {
  cat <<EOF
usage: bootstrap.sh [--profile mac|arch|server] [--yes]

Install git + mise (plus Xcode CLT and Homebrew on macOS), clone or pull
the repo, install the pinned Go toolchain, build ws, and exec ws bootstrap.

  -p, --profile   profile override (default: detected from the OS)
  -y, --yes       skip confirmations (here and in ws bootstrap)
  -h, --help      this text

env: WS_REPO  clone URL   (default: https://github.com/igwtcode/workstation.git)
     WS_DIR   checkout dir (default: ~/personal/code/igwtcode/workstation)
EOF
}

profile='' yes=0
while [[ $# -gt 0 ]]; do
  case $1 in
    -p | --profile)
      [[ $# -ge 2 ]] || die "$1 needs a value"
      profile=$2
      shift 2
      ;;
    -y | --yes)
      yes=1
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

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
  if [[ $id == arch || $id == cachyos || " $id_like " == *" arch "* ]]; then
    echo arch
  else
    echo linux
  fi
}

os=$(os_detect)

if [[ -z $profile ]]; then
  case $os in
    mac | arch) profile=$os ;;
    *) profile=server ;;
  esac
fi
case $profile in
  mac | arch | server) ;;
  *) die "unknown profile '$profile' (expected mac|arch|server)" ;;
esac

log "profile: $profile"
log "repo:    $WS_REPO -> $WS_DIR"
if [[ $yes != 1 ]]; then
  if [[ ! -t 0 || ! -t 2 ]]; then
    die "non-interactive session: pass --yes"
  fi
  read -r -p "bootstrap this machine? [y/N] " reply
  [[ $reply == [yY]* ]] || die "aborted"
fi

if [[ $os == mac ]] && ! sudo -n true 2>/dev/null; then
  { [[ -t 0 && -t 2 ]] && sudo -v; } || die "need sudo access on macos"
fi

prereqs_mac() {
  export HOMEBREW_NO_ASK=1
  if xcode-select -p >/dev/null 2>&1; then
    log "xcode clt: present"
  else
    if [[ ! -t 0 || ! -t 2 ]]; then
      die "xcode clt missing — run 'xcode-select --install' first (needs GUI)"
    fi
    log "xcode clt: launching installer — complete the dialog to continue"
    xcode-select --install
    until xcode-select -p >/dev/null 2>&1; do
      sleep 5
    done
    log "xcode clt: installed"
  fi

  local prefix
  for prefix in /opt/homebrew /usr/local; do
    if [[ -x $prefix/bin/brew ]]; then
      eval "$("$prefix/bin/brew" shellenv)"
      break
    fi
  done
  if has brew; then
    log "brew: present"
  else
    log "brew: running the official installer"
    NONINTERACTIVE=1 /bin/bash -c \
      "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
    for prefix in /opt/homebrew /usr/local; do
      [[ -x $prefix/bin/brew ]] && eval "$("$prefix/bin/brew" shellenv)" && break
    done
    has brew || die "brew: install finished but brew not found"
  fi

  has mise || brew install mise
}

prereqs_arch() {
  if has git && has mise; then
    log "git + mise: present"
    return 0
  fi
  sudo pacman -S --needed --noconfirm git mise
}

prereqs_linux() {
  if ! has git; then
    if has apt-get; then
      sudo apt-get update -qq && sudo apt-get install -y git
    elif has dnf; then
      sudo dnf install -y git
    else
      die "git missing and no known package manager — install git, then re-run"
    fi
  fi
  if ! has mise && [[ ! -x $HOME/.local/bin/mise ]]; then
    log "mise: running the official installer (-> ~/.local/bin)"
    curl -fsSL https://mise.run | sh
  fi
}

case $os in
  mac) prereqs_mac ;;
  arch) prereqs_arch ;;
  *) prereqs_linux ;;
esac

if [[ -e $WS_DIR/.git ]]; then
  log "repo: existing clone — pulling (ff-only)"
  git -C "$WS_DIR" pull --ff-only
elif [[ -e $WS_DIR ]]; then
  die "$WS_DIR exists but is not a git clone — move it aside and re-run"
else
  log "repo: cloning"
  mkdir -p "$(dirname "$WS_DIR")"
  git clone "$WS_REPO" "$WS_DIR"
fi

mise_bin=$(command -v mise || true)
if [[ -z $mise_bin && -x $HOME/.local/bin/mise ]]; then
  mise_bin=$HOME/.local/bin/mise
fi
[[ -n $mise_bin ]] || die "mise not found after prerequisite install"

mise_dir=$(dirname "$mise_bin")
case :$PATH: in
  *":$mise_dir:"*) ;;
  *) export PATH="$mise_dir:$PATH" ;;
esac

cd "$WS_DIR"
repo=$(pwd)
"$mise_bin" trust
log "toolchain: installing repo tools (go)"
"$mise_bin" install
log "ws: building binary"
"$mise_bin" run ws:install

ws_bin=$HOME/.local/bin/ws
[[ -x $ws_bin ]] || die "ws not found at $ws_bin after build"

args=(bootstrap --repo "$repo" --profile "$profile")
if [[ $yes == 1 ]]; then
  args+=(--yes)
fi
log "handing off: $ws_bin ${args[*]}"
exec "$ws_bin" "${args[@]}"
