#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=lib/common.sh
source "$root/lib/common.sh"

steps=(shell rustup docker libvirt kvm dns tailscale gsettings)

step_shell() {
  local zsh_path current
  zsh_path=$(command -v zsh) || die "shell: zsh not installed"
  current=$(getent passwd "$USER" | cut -d: -f7)
  if [[ $current == "$zsh_path" ]]; then
    log "shell: default is already $zsh_path"
  else
    sudo chsh -s "$zsh_path" "$USER"
    log "shell: default set to $zsh_path"
  fi
}

step_rustup() {
  if rustup show active-toolchain >/dev/null 2>&1; then
    log "rustup: toolchain already configured"
  else
    rustup default stable
  fi
}

step_docker() {
  sudo systemctl enable docker.service
  if id -nG "$USER" | tr ' ' '\n' | grep -qx docker; then
    log "docker: $USER already in docker group"
  else
    sudo usermod -aG docker "$USER"
    log "docker: added $USER to docker group (takes effect on re-login)"
  fi
}

step_libvirt() {
  if ! sudo grep -qs '^firewall_backend = "iptables"' /etc/libvirt/network.conf; then
    echo 'firewall_backend = "iptables"' | sudo tee -a /etc/libvirt/network.conf >/dev/null
  fi
  sudo systemctl enable --now libvirtd.service
  if ! id -nG "$USER" | tr ' ' '\n' | grep -qx libvirt; then
    sudo usermod -aG libvirt "$USER"
    log "libvirt: added $USER to libvirt group (takes effect on re-login)"
  fi
  sudo virsh net-autostart default >/dev/null 2>&1 || true
  sudo virsh net-start default >/dev/null 2>&1 || true
  if ws_has ufw; then
    sudo ufw route allow from 192.168.122.0/24
  fi
}

step_kvm() {
  if id -nG "$USER" | tr ' ' '\n' | grep -qx kvm; then
    log "kvm: $USER already in kvm group"
  else
    sudo usermod -aG kvm "$USER"
    log "kvm: added $USER to kvm group (takes effect on re-login)"
  fi
}

step_dns() {
  sudo systemctl enable --now systemd-resolved.service
  if [[ $(readlink -f /etc/resolv.conf) != /run/systemd/resolve/stub-resolv.conf ]]; then
    sudo ln -sfn /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf
    sudo systemctl restart NetworkManager
  fi
  log "dns: systemd-resolved active, stub resolver linked"
}

step_tailscale() {
  sudo systemctl enable --now tailscaled.service
  if tailscale status >/dev/null 2>&1; then
    log "tailscale: already up"
    return 0
  fi
  if [[ -t 0 && -t 2 && ${WS_NON_INTERACTIVE:-0} != 1 && ${WS_YES:-0} != 1 && ${WS_YES:-} != true ]] &&
    confirm "tailscale: run 'tailscale up' now (opens a login URL)?"; then
    sudo tailscale up
    sudo tailscale set --operator="$USER"
  else
    warn "tailscale: not logged in — run 'sudo tailscale up' when ready"
  fi
}

step_gsettings() {
  ws_has gsettings || {
    warn "gsettings: not available — skipped"
    return 0
  }
  if ! gsettings set org.gnome.desktop.interface gtk-theme 'adw-gtk3' 2>/dev/null; then
    warn "gsettings: no session bus — run 'os/arch/postinstall.sh gsettings' from a desktop session"
    return 0
  fi
  gsettings set org.gnome.desktop.interface color-scheme 'prefer-dark'
  log "gsettings: gtk theme + color scheme set"
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

for step in "${run[@]}"; do
  [[ " ${steps[*]} " == *" $step "* ]] || die "unknown step '$step' (see --list)"
  log "postinstall: $step"
  "step_$step"
done
