#!/usr/bin/env zsh
# Mac layer — sourced by zshrc before core.zsh. Brew env and GNU tool
# parity: linux-authored scripts expect GNU coreutils/sed/grep/make, so the
# brew gnubin dirs go in front of PATH (CLAUDE.md § Traps). gawk needs no
# gnubin — its formula ships an unprefixed awk.

eval "$(/opt/homebrew/bin/brew shellenv)"

export PATH=$HOMEBREW_PREFIX/opt/coreutils/libexec/gnubin:$HOMEBREW_PREFIX/opt/gnu-sed/libexec/gnubin:$HOMEBREW_PREFIX/opt/grep/libexec/gnubin:$HOMEBREW_PREFIX/opt/make/libexec/gnubin:$PATH

export HOMEBREW_NO_ANALYTICS=1
export HOMEBREW_NO_AUTO_UPDATE=1

# brew's rustup is keg-only — the rustup command itself isn't linked into
# brew's bin ($HOME/.cargo/bin proxies come from core.zsh)
export PATH=$PATH:$HOMEBREW_PREFIX/opt/rustup/bin

# clipboard helper used by core aliases
clipcopy() { pbcopy "$@" }
