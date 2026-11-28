#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
here=$root/os/arch/desktop
# shellcheck source=lib/common.sh
source "$root/lib/common.sh"

if ! confirm "desktop: install session entries + greeter config and enable greetd?"; then
  declined_exit desktop
fi

log "desktop: installing shell dispatchers to /usr/local/bin"
sudo install -Dm755 "$here/bin/ws-session-shell.sh" /usr/local/bin/ws-session-shell
sudo install -Dm755 "$here/bin/ws-shell-ipc.sh" /usr/local/bin/ws-shell-ipc

log "desktop: installing wayland session entries (compositor × shell matrix)"
for f in "$here"/sessions/*.desktop; do
  sudo install -Dm644 "$f" "/usr/share/wayland-sessions/$(basename "$f")"
done

if [[ -f /etc/greetd/config.toml ]] && ! cmp -s "$here/greetd-config.toml" /etc/greetd/config.toml; then
  warn "desktop: replacing /etc/greetd/config.toml (backup: config.toml.pre-ws)"
  sudo cp /etc/greetd/config.toml /etc/greetd/config.toml.pre-ws
fi
sudo install -Dm644 "$here/greetd-config.toml" /etc/greetd/config.toml

current_dm=$(basename "$(readlink -f /etc/systemd/system/display-manager.service 2>/dev/null)" 2>/dev/null || true)
if [[ -n $current_dm && $current_dm != greetd.service && $current_dm != *"not-found"* ]]; then
  if confirm "desktop: disable current display manager ($current_dm)?"; then
    sudo systemctl disable "$current_dm"
  fi
fi
sudo systemctl enable greetd.service

log "desktop: done — greetd active on next boot"
log "desktop: theme the greeter after logging into a DMS session: dms greeter sync"
