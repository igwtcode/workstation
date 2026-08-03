#!/usr/bin/env bash
# 1Password CLI (op v2) helpers: session handling + document CRUD. Source
# after lib/common.sh. WS_OP_VAULT scopes lookups to one vault. Secret
# values are never printed; content only moves between 1Password and files.

set -euo pipefail

[[ -n ${_WS_OP_SOURCED:-} ]] && return 0
_WS_OP_SOURCED=1

_op_vault=()
[[ -n ${WS_OP_VAULT:-} ]] && _op_vault=(--vault "$WS_OP_VAULT")

# op_ensure — authenticated op session or die; non-interactive fails clearly
op_ensure() {
  ws_has op || die "1Password CLI (op) not found — install 1password-cli"
  op whoami >/dev/null 2>&1 && return 0
  if [[ -t 0 && -t 2 ]]; then
    eval "$(op signin)" # respects --account / OP_ACCOUNT / last-used order
    op whoami >/dev/null 2>&1 || die "1Password signin failed"
  else
    die "not signed in to 1Password — run 'op signin' (or enable the desktop-app integration) first"
  fi
}

# op_doc_id <title> — print the document id for an exact title (scoped to
# WS_OP_VAULT when set); empty output if no such document exists.
op_doc_id() {
  local title=$1
  op document list "${_op_vault[@]}" --format json |
    jq -r --arg t "$title" 'map(select(.title == $t)) | (first // empty) | .id'
}

# op_doc_list — one line per document title in scope.
op_doc_list() {
  op document list "${_op_vault[@]}" --format json | jq -r '.[].title'
}

# op_doc_get <title> <dest> — download via a temp path (--out-file refuses
# to overwrite)
op_doc_get() {
  local title=$1 dest=$2 id tmp
  id=$(op_doc_id "$title")
  [[ -n $id ]] || die "1Password document not found: '$title'"
  tmp=$(mktemp -d)
  op document get "$id" --out-file "$tmp/doc" >/dev/null
  mkdir -p "$(dirname "$dest")"
  mv -f "$tmp/doc" "$dest"
  rmdir "$tmp"
}

# op_doc_put <title> <file> — create the document, or update it in place
# when a document with that title already exists.
op_doc_put() {
  local title=$1 file=$2 id
  [[ -f $file ]] || die "no such file: $file"
  id=$(op_doc_id "$title")
  if [[ -n $id ]]; then
    op document edit "$id" "$file" >/dev/null
  else
    op document create "$file" --title "$title" \
      --file-name "$(basename "$file")" "${_op_vault[@]}" >/dev/null
  fi
}

# op_inject <template> <dest> — materialize op:// references via a temp file
op_inject() {
  local template=$1 dest=$2 tmp
  [[ -f $template ]] || die "no such template: $template"
  tmp=$(mktemp -d)
  op inject --in-file "$template" --out-file "$tmp/out" >/dev/null
  mkdir -p "$(dirname "$dest")"
  mv -f "$tmp/out" "$dest"
  rmdir "$tmp"
}
