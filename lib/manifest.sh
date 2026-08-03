#!/usr/bin/env bash
# Symlink-manifest engine for links/*.links — requires lib/common.sh.
#
# Manifest format (one link per line):
#   <repo-path> <target-path>
# repo-path is relative to the repo root and must not contain spaces;
# target-path starts with ~/ (or is absolute) and runs to the end of the
# line, so it may contain spaces (mac: ~/Library/Application Support/…).
# `#` comments and blank lines are ignored; no inline comments.

set -euo pipefail

[[ -n ${_WS_MANIFEST_SOURCED:-} ]] && return 0
_WS_MANIFEST_SOURCED=1

# manifest_files <repo-root> <profile> — print the manifest paths to apply
# (common layer + the profile's OS layer), one per line.
manifest_files() {
  local repo_root=$1 profile=$2 layer
  case $profile in
    mac) layer=mac ;;
    arch) layer=linux ;;
    server) layer=server ;;
    *) die "unknown profile: $profile" ;;
  esac
  printf '%s\n' "$repo_root/links/common.links" "$repo_root/links/$layer.links"
}

# manifest_apply <repo-root> <manifest>... — apply links idempotently and
# report created/relinked/replaced/ok/skipped per link plus a summary.
# WS_DRY_RUN=1 reports without touching anything. Replacing an existing
# regular file/dir prompts (WS_YES=1 skips) and backs it up to <name>.pre-ws.
manifest_apply() {
  local repo_root=$1
  shift
  local created=0 changed=0 ok=0 skipped=0 tag=""
  [[ ${WS_DRY_RUN:-0} == 1 ]] && tag="[dry-run] "
  local file src target
  for file in "$@"; do
    [[ -f $file ]] || die "manifest not found: $file"
    # target soaks up the rest of the line — mac targets may contain spaces
    while read -r -u 3 src target || [[ -n ${src:-} ]]; do
      [[ -z $src || $src == \#* ]] && continue
      [[ -n $target ]] || die "malformed line in $file: '$src'"
      _manifest_link "$repo_root/$src" "$target"
    done 3<"$file"
  done
  log "${tag}links: $created created, $changed changed, $ok ok, $skipped skipped"
}

# _run <cmd>... — execute unless WS_DRY_RUN=1
_run() {
  if [[ ${WS_DRY_RUN:-0} == 1 ]]; then
    return 0
  fi
  "$@"
}

# _manifest_link <src-abs> <raw-target> — create/refresh one link; counts
# into the caller's created/changed/ok/skipped (dynamic scope).
_manifest_link() {
  local src=$1 raw=$2 target disp
  [[ $raw == \~/* || $raw == /* ]] || die "manifest target must start with ~/ or /: $raw"
  target=${raw/#\~/$HOME}
  disp=${target/#$HOME/"~"}
  [[ -e $src ]] || die "manifest source missing: $src"

  if [[ -L $target ]]; then
    if [[ $(readlink "$target") == "$src" ]]; then
      styled 8 "$(printf '%-9s %s' ok "$disp")"
      ok=$((ok + 1))
    else
      _run ln -sfn "$src" "$target"
      styled 3 "${tag}$(printf '%-9s %s -> %s' relinked "$disp" "$src")"
      changed=$((changed + 1))
    fi
  elif [[ -e $target ]]; then
    local kind=file backup=$target.pre-ws
    [[ -d $target ]] && kind=directory
    if [[ ${WS_DRY_RUN:-0} == 1 ]]; then
      styled 3 "${tag}$(printf '%-9s %s (existing %s; would confirm, back up, link)' replace "$disp" "$kind")"
      changed=$((changed + 1))
    elif confirm "Replace existing $kind $disp (backup: $disp.pre-ws)?"; then
      [[ -e $backup || -L $backup ]] && die "backup target already exists: $backup — move it away first"
      mv "$target" "$backup"
      ln -sfn "$src" "$target"
      styled 3 "$(printf '%-9s %s -> %s (backup: %s)' replaced "$disp" "$src" "$disp.pre-ws")"
      changed=$((changed + 1))
    else
      styled 1 "$(printf '%-9s %s (kept existing %s)' skipped "$disp" "$kind")"
      skipped=$((skipped + 1))
    fi
  else
    _run mkdir -p "$(dirname "$target")"
    _run ln -sfn "$src" "$target"
    styled 2 "${tag}$(printf '%-9s %s -> %s' created "$disp" "$src")"
    created=$((created + 1))
  fi
}
