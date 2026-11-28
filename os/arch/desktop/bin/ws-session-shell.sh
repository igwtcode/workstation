#!/usr/bin/env bash
set -euo pipefail

case ${WS_SHELL:-noctalia} in
  dms) exec dms run ;;
  *) exec noctalia ;;
esac
