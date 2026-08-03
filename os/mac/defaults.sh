#!/usr/bin/env bash
# macOS system settings via `defaults write` — a curated minimal set, one
# comment per line saying what it does. Idempotent; affected apps restart
# at the end so changes take effect. Re-run any time (`os/mac/defaults.sh`).
#
# Keys verified against macos-defaults.com (tested through current macOS);
# `defaults write` needs no sudo — everything here is per-user.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=lib/common.sh
source "$root/lib/common.sh"

[[ $(uname -s) == Darwin ]] || die "defaults.sh must run on macOS"

log "defaults: keyboard"
# fastest key repeat rate (lower = faster; UI minimum is 2)
defaults write NSGlobalDomain KeyRepeat -int 2
# shortest delay until repeat kicks in (UI minimum is 15)
defaults write NSGlobalDomain InitialKeyRepeat -int 15
# ApplePressAndHoldEnabled is deliberately left at its default (true): the
# press-and-hold accent popup is how non-English characters get typed, and
# the fast repeat rate above already covers the reason people disable it.
# no auto-capitalization, smart quotes/dashes, period on double-space, or
# spell-correct — they fight code and terminals
defaults write NSGlobalDomain NSAutomaticCapitalizationEnabled -bool false
defaults write NSGlobalDomain NSAutomaticQuoteSubstitutionEnabled -bool false
defaults write NSGlobalDomain NSAutomaticDashSubstitutionEnabled -bool false
defaults write NSGlobalDomain NSAutomaticPeriodSubstitutionEnabled -bool false
defaults write NSGlobalDomain NSAutomaticSpellingCorrectionEnabled -bool false

log "defaults: trackpad"
# tap to click (builtin + bluetooth trackpads, plus the per-host flag)
defaults write com.apple.AppleMultitouchTrackpad Clicking -bool true
defaults write com.apple.driver.AppleBluetoothMultitouch.trackpad Clicking -bool true
defaults -currentHost write NSGlobalDomain com.apple.mouse.tapBehavior -int 1

log "defaults: dock"
# hide the dock unless hovered, with no reveal delay
defaults write com.apple.dock autohide -bool true
defaults write com.apple.dock autohide-delay -float 0
# icon size in pixels
defaults write com.apple.dock tilesize -int 48
# no recent-apps section
defaults write com.apple.dock show-recents -bool false

log "defaults: finder"
# file extensions are left at the Finder default (hidden for known types);
# the terminal is where filenames get inspected anyway
# path bar + status bar at the bottom of every window
defaults write com.apple.finder ShowPathbar -bool true
defaults write com.apple.finder ShowStatusBar -bool true
# list view by default
defaults write com.apple.finder FXPreferredViewStyle -string "Nlsv"
# search the current folder, not the whole mac
defaults write com.apple.finder FXDefaultSearchScope -string "SCcf"
# folders sort above files
defaults write com.apple.finder _FXSortFoldersFirst -bool true

log "defaults: screenshots"
# png screenshots into ~/Pictures/screenshots, without the window shadow
mkdir -p "$HOME/Pictures/screenshots"
defaults write com.apple.screencapture location -string "$HOME/Pictures/screenshots"
defaults write com.apple.screencapture type -string "png"
defaults write com.apple.screencapture disable-shadow -bool true

# restart the apps that own these domains so settings apply now;
# NSGlobalDomain changes need a re-login to reach every app
for app in Dock Finder SystemUIServer; do
  killall "$app" >/dev/null 2>&1 || true # not running is fine
done

log "defaults: done (log out/in for keyboard settings to apply everywhere)"
