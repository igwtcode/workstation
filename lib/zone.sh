#!/usr/bin/env bash
# Identity-zone helpers — requires lib/common.sh. A zone is a tree under
# $HOME whose mise.toml [env] applies to every subdirectory. Zone paths are
# read back out of mise, never hardcoded. Identity values come from WS_ZONES
# in mise.local.toml: name|git_name|git_email[|signingkey[|aws_profile]]

set -euo pipefail

[[ -n ${_WS_ZONE_SOURCED:-} ]] && return 0
_WS_ZONE_SOURCED=1

# zone_run <cmd>... — execute unless WS_DRY_RUN=1
zone_run() {
  [[ ${WS_DRY_RUN:-0} == 1 ]] && return 0
  "$@"
}

# zone_root <name> — the zone's tree root
zone_root() { printf '%s\n' "$HOME/$1"; }

# zone_config <name> — the zone's mise config (a real file, seeded by identity)
zone_config() { printf '%s\n' "$HOME/$1/mise.toml"; }

# zone_gitconfig <name> — the generated per-zone git identity file
zone_gitconfig() { printf '%s\n' "$HOME/$1/.gitconfig"; }

# zone_lines — every WS_ZONES line, validated
zone_lines() {
  [[ -n ${WS_ZONES:-} ]] ||
    die "WS_ZONES is not set — define zone lines in mise.local.toml [env] (name|git_name|git_email[|signingkey[|aws_profile]], see README)"
  local line name gname gemail found=0
  while IFS= read -r line; do
    line=${line%$'\r'}
    [[ -z ${line//[[:space:]]/} || $line == \#* ]] && continue
    IFS='|' read -r name gname gemail _ <<<"$line"
    [[ -n $name && -n $gname && -n $gemail ]] ||
      die "malformed WS_ZONES line '$line' (expected name|git_name|git_email[|signingkey[|aws_profile]])"
    [[ $name == */* || $name == .* ]] &&
      die "invalid zone name '$name' — a zone is a single directory under \$HOME"
    printf '%s\n' "$line"
    found=1
  done <<<"$WS_ZONES"
  [[ $found -eq 1 ]] || die "WS_ZONES contains no zone lines"
}

# zone_names — configured zone names, one per line
zone_names() { zone_lines | cut -d'|' -f1; }

# zone_line <name> — the WS_ZONES line of one zone
zone_line() {
  local want=$1 line
  while IFS= read -r line; do
    [[ ${line%%|*} == "$want" ]] && {
      printf '%s\n' "$line"
      return 0
    }
  done < <(zone_lines)
  die "no zone '$want' in WS_ZONES (configured: $(zone_names | tr '\n' ' '))"
}

# zone_env <name> <VAR> — the value the zone's mise config exports for VAR;
# empty when the zone root, the config or the var is missing. Requires jq.
zone_env() {
  local name=$1 var=$2 root
  root=$(zone_root "$name")
  [[ -d $root ]] || return 0
  mise env --json -C "$root" 2>/dev/null |
    jq -r --arg v "$var" '.[$v] // empty' 2>/dev/null || true
}

# zone_export <name> — export a zone's identity env into this process.
# Repo tasks always resolve from the repo's own zone, so anything acting on
# behalf of another zone (cloning work repos, say) has to ask for it.
zone_export() {
  local name=$1 root json line
  root=$(zone_root "$name")
  [[ -d $root ]] || die "zone root missing: $(zone_tilde "$root") — run 'mise run identity'"
  json=$(mise env --json -C "$root" 2>/dev/null) ||
    die "cannot read the env of zone '$name' — untrusted config? run 'mise run identity'"
  while IFS= read -r line; do
    [[ -n $line ]] || continue
    # shellcheck disable=SC2163 # exporting NAME=value strings by design
    export "${line?}"
  done < <(jq -r 'to_entries[]
      | select(.key | test("^(WS_ZONE$|WS_CODE_ROOT$|KUBECONFIG$|GH_|CLAUDE_|AWS_)"))
      | "\(.key)=\(.value)"' <<<"$json")
}

# zone_current — zone of the current directory (env first, then path);
# returns 1 when outside every zone
zone_current() {
  if [[ -n ${WS_ZONE:-} ]]; then
    printf '%s\n' "$WS_ZONE"
    return 0
  fi
  [[ -n ${WS_ZONES:-} ]] || return 1
  local name root cwd=${MISE_ORIGINAL_CWD:-$PWD}
  while IFS= read -r name; do
    root=$(zone_root "$name")
    [[ $cwd == "$root" || $cwd == "$root"/* ]] && {
      printf '%s\n' "$name"
      return 0
    }
  done < <(zone_names)
  return 1
}

# zone_tilde <path> — path with $HOME collapsed to ~ (for output)
zone_tilde() { printf '%s\n' "${1/#$HOME/"~"}"; }

# zone_trusted <root> — true if mise trusts the zone's own config.
# `mise trust --show` reports one "<config-dir>: trusted|untrusted" line per
# config in scope (paths under $HOME collapsed to ~), so match the zone's
# own line exactly — a trusted *parent* config must not count.
zone_trusted() {
  local root=$1
  mise trust --show -C "$root" 2>/dev/null |
    grep -qFx -e "$root: trusted" -e "$(zone_tilde "$root"): trusted"
}
