#!/usr/bin/env bash
# Installed to /usr/local/bin/ws-session-shell by os/arch/desktop/setup.sh.
# Both compositor configs spawn this at startup; it starts the desktop
# shell picked at login — the wayland-session entry sets WS_SHELL (see
# os/arch/desktop/sessions/), default is noctalia.
set -euo pipefail

case ${WS_SHELL:-noctalia} in
  dms) exec dms run ;;
  *) exec qs -c noctalia-shell ;;
esac
