#!/usr/bin/env zsh

_ws_cached /opt/homebrew/bin/brew shellenv zsh

export PATH=$HOMEBREW_PREFIX/opt/coreutils/libexec/gnubin:$HOMEBREW_PREFIX/opt/gnu-sed/libexec/gnubin:$HOMEBREW_PREFIX/opt/grep/libexec/gnubin:$HOMEBREW_PREFIX/opt/make/libexec/gnubin:$PATH

export HOMEBREW_NO_AUTO_UPDATE=1

export PATH=$PATH:$HOMEBREW_PREFIX/opt/rustup/bin

export PATH=$PATH:$HOMEBREW_PREFIX/opt/util-linux/bin:$HOMEBREW_PREFIX/opt/util-linux/sbin

export ANDROID_HOME=$HOME/Library/Android/sdk
export PATH=$PATH:$ANDROID_HOME/emulator:$ANDROID_HOME/platform-tools:$ANDROID_HOME/cmdline-tools/latest/bin

clipcopy() { pbcopy "$@" }

claude() { caffeinate -i command claude "$@" }
codex() { caffeinate -i command codex "$@" }
opencode() { caffeinate -i command opencode "$@" }
