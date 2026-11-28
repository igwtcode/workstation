#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=lib/common.sh
source "$root/lib/common.sh"

[[ $(uname -s) == Darwin ]] || die "postinstall.sh must run on macOS"

steps=(xcode rustup tailscale)

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

step_xcode() {
  local dev=/Applications/Xcode.app/Contents/Developer
  if [[ ! -d $dev ]]; then
    warn "xcode: Xcode.app not installed — no xcodebuild/simctl (install from the App Store)"
    return 0
  fi
  if [[ $(xcode-select -p 2>/dev/null || true) == "$dev" ]]; then
    log "xcode: active developer dir is Xcode.app"
    return 0
  fi
  log "xcode: switching active developer dir to Xcode.app"
  sudo xcode-select -s "$dev"
  sudo xcodebuild -license accept
  sudo xcodebuild -runFirstLaunch
}

step_rustup() {
  local rustup=$HOMEBREW_PREFIX/opt/rustup/bin/rustup
  if [[ ! -x $rustup ]]; then
    warn "rustup: $rustup not found — skipping"
    return 0
  fi
  if "$rustup" show active-toolchain >/dev/null 2>&1; then
    log "rustup: toolchain already configured"
  else
    "$rustup" default stable
  fi
}

step_tailscale() {
  if [[ -d /Applications/Tailscale.app ]]; then
    warn "tailscale: Tailscale.app is installed — remove it (and reboot), it conflicts with tailscaled"
  fi
  sudo "$HOMEBREW_PREFIX/bin/brew" services start tailscale
  if tailscale status >/dev/null 2>&1; then
    log "tailscale: already up"
    return 0
  fi
  if [[ -t 0 && -t 2 && ${WS_NON_INTERACTIVE:-0} != 1 && ${WS_YES:-0} != 1 && ${WS_YES:-} != true ]] &&
    confirm "tailscale: run 'tailscale up --ssh' now (opens a login URL)?"; then
    tailscale up --ssh
  else
    warn "tailscale: not logged in — run 'tailscale up --ssh' when ready"
  fi
}

if [[ ${1:-} == --list ]]; then
  printf '%s\n' "${steps[@]}"
  exit 0
fi

if (($#)); then
  run=("$@")
else
  run=("${steps[@]}")
fi

need_brew=0
for step in "${run[@]}"; do
  [[ " ${steps[*]} " == *" $step "* ]] || die "unknown step '$step' (see --list)"
  if [[ $step == rustup || $step == tailscale ]]; then
    need_brew=1
  fi
done

if ((need_brew)); then
  brew_env || die "brew not found on PATH"
fi

for step in "${run[@]}"; do
  log "postinstall: $step"
  "step_$step"
done
