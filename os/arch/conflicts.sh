#!/usr/bin/env bash
# Removes installed packages that pkg/arch.txt entries replace; --noconfirm
# cannot resolve conflicts, so installs fail while these are present.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# shellcheck source=lib/common.sh
source "$root/lib/common.sh"

# replaced by: pipewire-jack, go-yq, noctalia, dux-shell, dux-greeter-bin
replaced=(jack yq noctalia-shell noctalia-qs dms-shell greetd-dms-greeter-bin)

installed=$(pacman -Qq)
found=()
for pkg in "${replaced[@]}"; do
  grep -qx "$pkg" <<<"$installed" && found+=("$pkg")
done
((${#found[@]})) || exit 0

confirm "packages: remove ${found[*]} (replaced by pkg/arch.txt entries)?" ||
  die "conflicting packages left installed — the install would fail"
sudo pacman -Rdd --noconfirm "${found[@]}"
