#!/usr/bin/env zsh
# Linux layer — sourced by zshrc before core.zsh.

export LIBVIRT_DEFAULT_URI="qemu:///system"

# clipboard helper used by core aliases (wayland)
clipcopy() { wl-copy "$@" }

# clipboard history picker (cliphist + wayland)
alias ch="cliphist list | fzf --no-sort -d $'\t' --with-nth 2 | cliphist decode | wl-copy"

# --- paru pickers (arch only) ----------------------------------------------

if command -v paru &>/dev/null; then
  pinstall() {
    paru -Slq | fzf \
      --preview "paru -Si {}" \
      --preview-window=right:67%:wrap \
      --height=36% \
      --prompt="Install: " |
      xargs -r paru -S --needed --noconfirm
  }

  premove() {
    paru -Qq | fzf \
      --preview "paru -Qi {}" \
      --preview-window=right:78%:wrap \
      --height=33% \
      --prompt="Remove: " |
      xargs -r paru -Rns
  }

  pinfo() {
    paru -Qq | fzf \
      --preview "paru -Qi {}" \
      --preview-window=right:78%:wrap \
      --height=33% \
      --prompt="Info: " | xargs -r wl-copy
  }
fi
