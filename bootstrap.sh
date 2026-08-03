#!/usr/bin/env bash
# Fresh-machine entry point. Installs git + mise (mac: also Xcode CLT and
# Homebrew), clones or pulls the repo, then hands off to `mise run
# bootstrap`. Plain bash + curl only — no repo tooling exists yet. Re-runnable.
set -euo pipefail

WS_REPO=${WS_REPO:-https://github.com/igwtcode/workstation.git}
WS_DIR=${WS_DIR:-$HOME/personal/code/igwtcode/workstation}

# --- plain-bash output helpers (gum arrives later, via the pkg lists) ----

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

# --- args -----------------------------------------------------------------

usage() {
  cat <<EOF
usage: bootstrap.sh [--profile mac|arch|server] [--yes]

  -p, --profile   profile override (default: detected from the OS)
  -y, --yes       skip confirmations (here and in the handed-off tasks)
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

# --- OS + profile (minimal mirror of lib/common.sh, which isn't here yet) -

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

# --- confirm before touching the machine ----------------------------------

log "profile: $profile"
log "repo:    $WS_REPO -> $WS_DIR"
if [[ $yes != 1 ]]; then
  if [[ ! -t 0 || ! -t 2 ]]; then
    die "non-interactive session: pass --yes"
  fi
  read -r -p "bootstrap this machine? [y/N] " reply
  [[ $reply == [yY]* ]] || die "aborted"
fi

# --- prerequisites: git + mise only; everything else is os/<profile>/ ------

prereqs_mac() {
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

# unknown distros: best-effort git, mise via the standalone installer
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

# --- clone or update --------------------------------------------------------

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

# --- hand off to mise -------------------------------------------------------

mise_bin=$(command -v mise || true)
if [[ -z $mise_bin && -x $HOME/.local/bin/mise ]]; then
  mise_bin=$HOME/.local/bin/mise
fi
[[ -n $mise_bin ]] || die "mise not found after prerequisite install"

cd "$WS_DIR"
"$mise_bin" trust # config in a fresh clone is untrusted; needed pre-run

args=(run bootstrap --profile "$profile")
if [[ $yes == 1 ]]; then
  args+=(--yes)
fi
log "handing off: mise ${args[*]}"
exec "$mise_bin" "${args[@]}"
