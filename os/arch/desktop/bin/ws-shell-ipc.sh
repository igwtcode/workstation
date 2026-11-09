#!/usr/bin/env bash
# Installed to /usr/local/bin/ws-shell-ipc by os/arch/desktop/setup.sh.
# Maps neutral shell-IPC verbs to the running desktop shell's own IPC
# (noctalia or DMS), so the compositor keybindings stay identical across
# shells. The shell comes from WS_SHELL (set by the login session entry);
# fallback is a process probe, default noctalia.
set -euo pipefail

verb=${1:-}

usage() {
  cat >&2 <<'EOF'
usage: ws-shell-ipc <verb>
verbs: launcher control-center settings lock
       volume-up volume-down volume-mute mic-mute
       media-next media-prev media-play-pause
       brightness-up brightness-down
EOF
  exit 2
}

[[ -n $verb ]] || usage

shell=${WS_SHELL:-}
if [[ -z $shell ]]; then
  if pgrep -x noctalia >/dev/null 2>&1; then
    shell=noctalia
  elif pgrep -f 'dms(-shell| run)' >/dev/null 2>&1; then
    shell=dms
  else
    shell=noctalia
  fi
fi

noc() { exec noctalia msg "$@"; }
dank() { exec dms ipc call "$@"; }

if [[ $shell == dms ]]; then
  case $verb in
    launcher) dank spotlight toggle ;;
    control-center) dank control-center toggle ;;
    settings) dank settings toggle ;;
    lock) dank lock lock ;;
    volume-up) dank audio increment 5 ;;
    volume-down) dank audio decrement 5 ;;
    volume-mute) dank audio mute ;;
    mic-mute) dank mic mute ;;
    media-next) dank mpris next ;;
    media-prev) dank mpris previous ;;
    media-play-pause) dank mpris playPause ;;
    brightness-up) dank brightness increment 5 ;;
    brightness-down) dank brightness decrement 5 ;;
    *) usage ;;
  esac
else
  case $verb in
    launcher) noc panel-toggle launcher ;;
    control-center) noc panel-toggle control-center ;;
    settings) noc settings-toggle ;;
    lock) noc session lock ;;
    volume-up) noc volume-up ;;
    volume-down) noc volume-down ;;
    volume-mute) noc volume-mute ;;
    mic-mute) noc mic-mute ;;
    media-next) noc media next ;;
    media-prev) noc media previous ;;
    media-play-pause) noc media toggle ;;
    brightness-up) noc brightness-up ;;
    brightness-down) noc brightness-down ;;
    *) usage ;;
  esac
fi
