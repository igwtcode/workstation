#!/usr/bin/env bash
# Minimal server package install: detects apt/dnf/pacman, maps the few
# package names that differ per distro, and installs pkg/server.txt one
# package at a time so a package missing from one distro's repos warns
# and is skipped rather than failing the run. Machine-mutating (sudo,
# package installs) — run via `mise run bootstrap`; never as part of the
# dev loop.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=lib/common.sh
source "$root/lib/common.sh"

profile=$(resolve_profile "${1:-}")
[[ $profile == server ]] || die "server install called with profile '$profile'"

confirm "bootstrap (server): install packages?"
sudo -v

# pkg_manager — echoes apt-get|dnf|pacman for the first one found on PATH
pkg_manager() {
  local pm
  for pm in apt-get dnf pacman; do
    ws_has "$pm" && echo "$pm" && return 0
  done
  die "no supported package manager found (expected apt-get, dnf, or pacman)"
}

pm=$(pkg_manager)
log "package manager: $pm"

# map_name <canonical> — distro-specific package name for the packages
# that differ from pkg/server.txt's canonical (upstream) spelling; e.g.
# fd ships as fd-find on apt/dnf (name collision with an existing package).
map_name() {
  local name=$1
  case "$pm:$name" in
    apt-get:fd | dnf:fd) echo fd-find ;;
    *) echo "$name" ;;
  esac
}

# install_one <distro-name> — true/false, one package per call so a
# single missing package can't fail the whole run
install_one() {
  case $pm in
    apt-get) DEBIAN_FRONTEND=noninteractive sudo -E apt-get install -y "$1" ;;
    dnf) sudo dnf install -y "$1" ;;
    pacman) sudo pacman -S --needed --noconfirm "$1" ;;
  esac
}

case $pm in
  apt-get) sudo apt-get update -qq ;;
  dnf) : ;;                               # dnf refreshes its own metadata cache as needed
  pacman) sudo pacman -Syu --noconfirm ;; # full sync+upgrade, never a bare -Sy (partial-upgrade footgun)
esac

mapfile -t pkgs < <(grep -vE '^[[:space:]]*(#|$)' "$root/pkg/server.txt")
log "packages: installing ${#pkgs[@]} from pkg/server.txt via $pm"

installed=0 skipped=0
for name in "${pkgs[@]}"; do
  mapped=$(map_name "$name")
  if install_one "$mapped"; then
    installed=$((installed + 1))
  else
    warn "not available via $pm, skipping: $name${mapped:+ (tried $mapped)}"
    skipped=$((skipped + 1))
  fi
done

log "packages: $installed installed, $skipped skipped"
