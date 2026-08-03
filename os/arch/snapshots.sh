#!/usr/bin/env bash
# Btrfs snapshot stack: snapper root config + snap-pac for pre/post pacman
# snapshots, made boot-selectable via grub-btrfs or limine-snapper-sync
# depending on the detected bootloader. Packages: pkg/arch-snapshots.txt
# (common + matching bootloader section). Self-skips when / is not btrfs or
# when snapper is already configured, so an arch derivative that ships its
# own snapshot stack is a no-op rather than a conflict. Machine-mutating —
# run via os/arch/bootstrap.sh or explicitly; never as part of the dev loop.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=lib/common.sh
source "$root/lib/common.sh"

if [[ $(findmnt -no FSTYPE /) != btrfs ]]; then
  log "snapshots: / is not btrfs — skipping"
  exit 0
fi

if ws_has snapper && sudo snapper list-configs 2>/dev/null | grep -qE '^root\b'; then
  log "snapshots: a snapper 'root' config already exists — leaving it alone"
  exit 0
fi
require_cmd paru

detect_bootloader() {
  if [[ -d /boot/grub ]] && ws_has grub-mkconfig; then
    echo grub
  elif [[ -e /boot/limine.conf || -e /boot/EFI/limine/limine.conf ]] ||
    pacman -Qq limine >/dev/null 2>&1; then
    echo limine
  else
    echo unknown
  fi
}

bootloader=$(detect_bootloader)
log "snapshots: bootloader detected: $bootloader"

confirm "snapshots: set up snapper + snap-pac ($bootloader boot entries)?"

pkgs=(snapper snap-pac)
case $bootloader in
  grub) pkgs+=(grub-btrfs inotify-tools) ;;
  limine) pkgs+=(limine-snapper-sync) ;;
  unknown) warn "snapshots: no grub/limine found — snapshots won't be boot-selectable" ;;
esac
paru -S --needed --noconfirm "${pkgs[@]}"

# snapper root config. When /.snapshots is already a mounted subvolume
# (fstab-managed layout), create-config refuses — unmount it, let snapper
# create its config, drop the subvolume snapper made, then remount the
# original (the standard dance from the snapper docs).
if sudo snapper -c root get-config >/dev/null 2>&1; then
  log "snapshots: snapper root config already exists"
else
  premounted=0
  if findmnt -n /.snapshots >/dev/null 2>&1; then
    premounted=1
    sudo umount /.snapshots
    sudo rmdir /.snapshots
  fi
  sudo snapper -c root create-config /
  if ((premounted)); then
    sudo btrfs subvolume delete /.snapshots
    sudo mkdir /.snapshots
    sudo mount /.snapshots
  fi
  sudo chmod 750 /.snapshots
  log "snapshots: snapper root config created"
fi

# pre/post pacman snapshots (snap-pac) are the point here — no timeline
sudo snapper -c root set-config 'TIMELINE_CREATE=no' 'ALLOW_GROUPS=wheel' 'SYNC_ACL=yes'
sudo systemctl enable --now snapper-cleanup.timer

case $bootloader in
  grub)
    sudo systemctl enable --now grub-btrfsd.service
    sudo grub-mkconfig -o /boot/grub/grub.cfg
    ;;
  limine)
    sudo systemctl enable --now limine-snapper-sync.service
    ;;
esac

log "snapshots: done — pacman ops now create boot-selectable pre/post snapshots"
