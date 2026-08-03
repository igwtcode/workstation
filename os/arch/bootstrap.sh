#!/usr/bin/env bash
# Arch bootstrap: paru, full package list, post-install tweaks,
# desktop login layer, snapshot stack (raw arch only). Machine-mutating
# (sudo, package installs, systemd, /etc) — run via `mise run bootstrap`
# on the target machine; never as part of the dev loop.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=lib/common.sh
source "$root/lib/common.sh"

profile=$(resolve_profile "${1:-}")
case $profile in
  arch) ;;
  *) die "arch bootstrap called with profile '$profile'" ;;
esac

confirm "bootstrap ($profile): install packages and modify system configuration?"
sudo -v

ensure_paru() {
  if ws_has paru; then
    log "paru: present"
    return 0
  fi
  # raw arch: build paru-bin from the AUR (base-devel + git first)
  log "paru: building paru-bin from the AUR"
  sudo pacman -S --needed --noconfirm base-devel git
  local dir
  dir=$(mktemp -d)
  git clone --depth=1 https://aur.archlinux.org/paru-bin.git "$dir/paru-bin"
  (cd "$dir/paru-bin" && makepkg -si --noconfirm)
  rm -rf "$dir"
}

install_packages() {
  local pkgs
  mapfile -t pkgs < <(grep -vE '^[[:space:]]*(#|$)' "$root/pkg/arch.txt")
  log "packages: installing ${#pkgs[@]} from pkg/arch.txt"
  paru -Syu --needed --noconfirm "${pkgs[@]}"
}

install_flatpak_themes() {
  flatpak remote-add --if-not-exists flathub https://flathub.org/repo/flathub.flatpakrepo
  flatpak install -y flathub org.gtk.Gtk3theme.adw-gtk3 org.gtk.Gtk3theme.adw-gtk3-dark
}

ensure_paru

# `jack` conflicts with `pipewire-jack` (see CLAUDE.md § Traps)
paru -Rdd --noconfirm jack >/dev/null 2>&1 || true # absent is fine

install_packages
install_flatpak_themes

"$root/os/arch/postinstall.sh"
"$root/os/arch/desktop/setup.sh"
"/os/arch/snapshots.sh" # self-skips when / is not btrfs or snapper is set up

log "bootstrap ($profile): OS setup done"
