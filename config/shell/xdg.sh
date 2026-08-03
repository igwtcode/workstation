# shellcheck shell=sh
# XDG base directories, sourced by ~/.zshenv and ~/.bashrc. Declared rather
# than left to the platform so macOS uses the linux layout. Only reaches
# terminal-launched tools; a GUI app that needs a path gets a manifest entry.
# POSIX sh on purpose. Conditional, so an upstream value still wins.

export XDG_CONFIG_HOME="${XDG_CONFIG_HOME:-$HOME/.config}"
export XDG_DATA_HOME="${XDG_DATA_HOME:-$HOME/.local/share}"
export XDG_STATE_HOME="${XDG_STATE_HOME:-$HOME/.local/state}"
export XDG_CACHE_HOME="${XDG_CACHE_HOME:-$HOME/.cache}"
