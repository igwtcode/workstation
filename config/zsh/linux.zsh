#!/usr/bin/env zsh

export LIBVIRT_DEFAULT_URI="qemu:///system"

export ANDROID_HOME=$HOME/Android/Sdk
export PATH=$PATH:$ANDROID_HOME/emulator:$ANDROID_HOME/platform-tools:$ANDROID_HOME/cmdline-tools/latest/bin
[[ -d /usr/lib/jvm/java-17-openjdk ]] && export JAVA_HOME=/usr/lib/jvm/java-17-openjdk

(( $+commands[devpod-cli] )) && alias devpod=devpod-cli

clipcopy() { wl-copy "$@" }

alias ch="cliphist list | fzf --no-sort -d $'\t' --with-nth 2 | cliphist decode | wl-copy"

if (( $+commands[ws] )); then
  pinstall() { command ws pkg search --tui "$@" }
  premove() { command ws pkg remove "$@" }
  pinfo() { command ws pkg info "$@" }
elif command -v paru &>/dev/null; then
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
