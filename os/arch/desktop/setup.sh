#!/usr/bin/env bash
# Desktop login layer (arch): greetd + DMS greeter, plus a wayland
# session entry per compositor×shell combo (niri/hyprland × noctalia/DMS)
# so any combination is picked at login. Machine-mutating (sudo, /etc,
# systemd) — run via os/arch/bootstrap.sh or standalone to refresh the
# session entries; never as part of the dev loop.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
here=$root/os/arch/desktop
# shellcheck source=lib/common.sh
source "$root/lib/common.sh"

confirm "desktop: install session entries + greeter config and enable greetd?"

# Shell dispatchers, spawned by both compositor configs. They live in
# /usr/local/bin because sessions launched by greetd don't source the
# user's shell rc — a $HOME-linked bin dir wouldn't be on PATH yet.
log "desktop: installing shell dispatchers to /usr/local/bin"
sudo install -Dm755 "$here/bin/ws-session-shell.sh" /usr/local/bin/ws-session-shell
sudo install -Dm755 "$here/bin/ws-shell-ipc.sh" /usr/local/bin/ws-shell-ipc

# Session entries. dms-greeter reads /usr/share/wayland-sessions only —
# not /usr/local/share. The stock niri/Hyprland entries stay alongside
# (they behave like the noctalia combos, since noctalia is the default).
log "desktop: installing wayland session entries (compositor × shell matrix)"
for f in "$here"/sessions/*.desktop; do
  sudo install -Dm644 "$f" "/usr/share/wayland-sessions/$(basename "$f")"
done

# greetd + DMS greeter
if [[ -f /etc/greetd/config.toml ]] && ! cmp -s "$here/greetd-config.toml" /etc/greetd/config.toml; then
  warn "desktop: replacing /etc/greetd/config.toml (backup: config.toml.pre-ws)"
  sudo cp /etc/greetd/config.toml /etc/greetd/config.toml.pre-ws
fi
sudo install -Dm644 "$here/greetd-config.toml" /etc/greetd/config.toml

# Hand over from any other enabled display manager, then enable greetd.
# Not `--now`: switching the DM under a live session would kill it —
# greetd takes over on the next boot.
current_dm=$(basename "$(readlink -f /etc/systemd/system/display-manager.service 2>/dev/null)" 2>/dev/null || true)
if [[ -n $current_dm && $current_dm != greetd.service && $current_dm != *"not-found"* ]]; then
  if confirm "desktop: disable current display manager ($current_dm)?"; then
    sudo systemctl disable "$current_dm"
  fi
fi
sudo systemctl enable greetd.service

log "desktop: done — greetd active on next boot"
log "desktop: theme the greeter after logging into a DMS session: dms greeter sync"
