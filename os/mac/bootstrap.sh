#!/usr/bin/env bash
# macOS bootstrap: Xcode CLT, Homebrew, pkg lists, rustup, system defaults.
# Machine-mutating; run via `mise run bootstrap`. Re-runnable.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=lib/common.sh
source "$root/lib/common.sh"

profile=$(resolve_profile "${1:-}")
[[ $profile == mac ]] || die "mac bootstrap called with profile '$profile'"
[[ $(uname -s) == Darwin ]] || die "mac bootstrap must run on macOS"

confirm "bootstrap (mac): install packages and modify system settings?"
if ! sudo -n true 2>/dev/null; then
  { [[ -t 0 && -t 2 ]] && sudo -v; } || die "need sudo access on macos"
fi

# brew: /opt/homebrew (apple silicon) or /usr/local (intel)
brew_env() {
  local prefix
  for prefix in /opt/homebrew /usr/local; do
    if [[ -x $prefix/bin/brew ]]; then
      eval "$("$prefix/bin/brew" shellenv)"
      return 0
    fi
  done
  return 1
}

ensure_clt() {
  if xcode-select -p >/dev/null 2>&1; then
    log "xcode clt: present"
    return 0
  fi
  # poll until the GUI dialog is done
  if [[ ! -t 0 || ! -t 2 ]]; then
    die "xcode clt missing — run 'xcode-select --install' first (needs GUI)"
  fi
  log "xcode clt: launching installer — complete the dialog to continue"
  xcode-select --install
  until xcode-select -p >/dev/null 2>&1; do
    sleep 5
  done
  log "xcode clt: installed"
}

ensure_brew() {
  if brew_env; then
    log "brew: present ($HOMEBREW_PREFIX)"
    return 0
  fi
  log "brew: running the official installer"
  NONINTERACTIVE=1 /bin/bash -c \
    "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
  brew_env || die "brew: install finished but brew not found on PATH"
}

# Homebrew 6 asks for confirmation by default; one prompt (confirm above) is
# the budget.
export HOMEBREW_NO_ASK=1

# trust_qualified <file> — tap + trust owner/tap/name entries; Homebrew 6
# will not evaluate an untrusted third-party formula. Re-running is safe.
trust_qualified() {
  local file=$1 pkg tap
  while IFS= read -r pkg; do
    tap=${pkg%/*}
    log "tap trust: $pkg"
    brew tap "$tap"
    brew trust --formula "$pkg" ||
      warn "brew trust failed for '$pkg' — 'brew install' will refuse it until trusted"
  done < <(grep -E '^[^#[:space:]]+/[^/]+/[^/]+$' "$file" || true)
}

# install_list <file> [--cask] — `brew install` is idempotent: current is a
# no-op, outdated upgrades. `while read`, not mapfile: this runs under
# Apple's bash 3.2, before brew's bash is installed.
install_list() {
  local file=$1 kind=formulae
  shift
  local -a pkgs=()
  local pkg
  while IFS= read -r pkg; do
    pkgs+=("$pkg")
  done < <(grep -vE '^[[:space:]]*(#|$)' "$file")
  [[ $* == *--cask* ]] && kind=casks
  log "packages: installing ${#pkgs[@]} $kind from ${file##*/}"
  brew install "$@" "${pkgs[@]}"
}

# brew's rustup is keg-only — call it by keg path
setup_rustup() {
  local rustup=$HOMEBREW_PREFIX/opt/rustup/bin/rustup
  if "$rustup" show active-toolchain >/dev/null 2>&1; then
    log "rustup: toolchain already configured"
  else
    "$rustup" default stable
  fi
}

ensure_clt
ensure_brew

brew update
trust_qualified "$root/pkg/mac-brew.txt"
install_list "$root/pkg/mac-brew.txt"
install_list "$root/pkg/mac-cask.txt" --cask

setup_rustup

"$root/os/mac/defaults.sh"

log "bootstrap (mac): OS setup done"
