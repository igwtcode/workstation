#!/usr/bin/env bash
#
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=lib/common.sh
source "$root/lib/common.sh"

[[ $(uname -s) == Darwin ]] || die "defaults.sh must run on macOS"

log "defaults: keyboard"
defaults write NSGlobalDomain KeyRepeat -int 2
defaults write NSGlobalDomain InitialKeyRepeat -int 15
defaults write com.microsoft.VSCode ApplePressAndHoldEnabled -bool false
defaults write NSGlobalDomain NSAutomaticCapitalizationEnabled -bool false
defaults write NSGlobalDomain NSAutomaticQuoteSubstitutionEnabled -bool false
defaults write NSGlobalDomain NSAutomaticDashSubstitutionEnabled -bool false
defaults write NSGlobalDomain NSAutomaticPeriodSubstitutionEnabled -bool false
defaults write NSGlobalDomain NSAutomaticSpellingCorrectionEnabled -bool false

log "defaults: trackpad"
defaults write com.apple.AppleMultitouchTrackpad Clicking -bool true
defaults write com.apple.driver.AppleBluetoothMultitouch.trackpad Clicking -bool true
defaults -currentHost write NSGlobalDomain com.apple.mouse.tapBehavior -int 1

log "defaults: dock"
defaults write com.apple.dock autohide -bool true
defaults write com.apple.dock autohide-delay -float 0
defaults write com.apple.dock tilesize -int 48
defaults write com.apple.dock show-recents -bool false

log "defaults: finder"
defaults write com.apple.finder ShowPathbar -bool true
defaults write com.apple.finder ShowStatusBar -bool true
defaults write com.apple.finder FXPreferredViewStyle -string "Nlsv"
defaults write com.apple.finder FXDefaultSearchScope -string "SCcf"
defaults write com.apple.finder _FXSortFoldersFirst -bool true

log "defaults: screenshots"
mkdir -p "$HOME/Pictures/screenshots"
defaults write com.apple.screencapture location -string "$HOME/Pictures/screenshots"
defaults write com.apple.screencapture type -string "png"
defaults write com.apple.screencapture disable-shadow -bool true

for app in Dock Finder SystemUIServer; do
  killall "$app" >/dev/null 2>&1 || true
done

log "defaults: done (log out/in for keyboard settings to apply everywhere)"
