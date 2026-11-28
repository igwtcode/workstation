#!/usr/bin/env zsh

setopt HIST_EXPIRE_DUPS_FIRST
setopt HIST_IGNORE_DUPS
setopt HIST_IGNORE_ALL_DUPS
setopt HIST_IGNORE_SPACE
setopt HIST_FIND_NO_DUPS
setopt HIST_SAVE_NO_DUPS
setopt HIST_NO_FUNCTIONS
setopt APPEND_HISTORY
setopt INC_APPEND_HISTORY
setopt SHARE_HISTORY
setopt prompt_subst
setopt AUTO_CD

bindkey -v
export KEYTIMEOUT=1

zstyle ':completion:*' use-cache on
zstyle ':completion:*' cache-path ~/.zsh/cache
zstyle ':completion:*' matcher-list 'r:|=*' 'l:|=*' 'r:|=* m:{a-z\-}={A-Z\_}'
zstyle ':completion:*' list-dirs-first true
zstyle ':completion:*:descriptions' format '[%d]'
zstyle ':completion:*' menu no
zstyle ':fzf-tab:*' disabled-on files

fpath=(~/.local/share/zsh/site-functions $fpath)

autoload -U +X bashcompinit && bashcompinit
autoload -Uz compinit
_ws_dump=(${ZDOTDIR:-$HOME}/.zcompdump(N.mh+2))
if (( $#_ws_dump )); then
  compinit -u && touch $_ws_dump
else
  compinit -C
fi
unset _ws_dump

: "${XDG_CONFIG_HOME:=$HOME/.config}" "${XDG_CACHE_HOME:=$HOME/.cache}"

export HISTORY_TIME_FORMAT="%Y-%m-%d %T "
export HISTFILE=$HOME/.zsh_history
export HISTSIZE=9999
export SAVEHIST=9999
export EDITOR=nvim
export VISUAL=nvim
export WS_CODE_ROOT=${WS_CODE_ROOT:-$HOME}
export STARSHIP_CONFIG=$XDG_CONFIG_HOME/starship/config.toml
export TF_PLUGIN_CACHE_DIR=$XDG_CACHE_HOME/terraform-plugins
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin:$HOME/.cargo/bin:$HOME/.bun/bin:$HOME/.local/bin

export FZF_DEFAULT_OPTS=" \
--preview-window='border-rounded' --prompt='❯ ' \
--marker='● ' --pointer='▶ ' --separator='─' --scrollbar='▌' --highlight-line \
--multi --bind 'ctrl-a:select-all,ctrl-alt-a:deselect-all' \
--layout=reverse"

[[ -f $XDG_CONFIG_HOME/fzf/colors.sh ]] && source $XDG_CONFIG_HOME/fzf/colors.sh
[[ -f $XDG_CONFIG_HOME/gum/colors.sh ]] && source $XDG_CONFIG_HOME/gum/colors.sh
[[ -f $_ws_zdir/colors.sh ]] && source $_ws_zdir/colors.sh

alias .....='cd ../../../..'
alias ....='cd ../../..'
alias ...='cd ../..'
alias ..='cd ..'
alias b=bat
alias c=clear
alias cr='cargo run -- '
alias crq='cargo run -q -- '
alias gd='git diff'
alias ghd=' GH_TOKEN=$(gh auth token) gh dash'
alias gl="git log --graph --abbrev-commit --decorate --format=format:'%C(bold blue)%h%C(reset) %C(dim white)%<(16)%an%C(reset) %C(bold green)%<(14)%ar%C(reset) %C(white)%s%C(reset)%C(auto)%d%C(reset)' --all"
alias gp='git pull --all --prune'
alias hist='history -E'
alias histcopy=' fc -ln 1 | fzf --tac --scheme=history | xargs -r -I {} clipcopy "{}"'
alias k=kubectl
alias kd='kubectl describe'
alias kdd='kubectl describe deploy'
alias kdp='kubectl describe pod'
alias kds='kubectl describe service'
alias ke='kubectl exec'
alias kg='kubectl get'
alias kgd='kubectl get deploy'
alias kgp='kubectl get pods'
alias kgs='kubectl get service'
alias kl='kubectl logs'
alias lazydocker=' CONFIG_DIR=$XDG_CONFIG_HOME/lazydocker lazydocker'
alias lazygit=' lazygit'
alias lg=lazygit
alias ll=' eza -l --group-directories-first --git --git-repos --icons --ignore-glob ".DS_Store"'
alias lla='ll -A'
alias ls='ls --color=auto'
alias lt=' eza -l --group-directories-first --ignore-glob ".git|.DS_Store" -aTL'
alias lzd=lazydocker
alias m='mise'
alias ml='mise ls --local'
alias mr='mise run'
alias mt='mise tasks'
alias oc='opencode'
alias randbase64=' openssl rand 60 | base64 -w 0 | tr -d "\n" | clipcopy'
alias randraw=' openssl rand -hex 32 | tr -d "\n" | clipcopy'
alias tf=terraform
alias ts='tailscale'
alias t='tmux a || tmux'
alias v='nvim'

if command -v go-task &>/dev/null; then
  alias task='go-task'
  export TASK_EXE='go-task'
fi

ytv() { yt-dlp --cookies-from-browser firefox --no-playlist --progress -f "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]" --merge-output-format mp4 -o "$HOME/Downloads/%(title)s.%(ext)s" "$1"; }
yta() { yt-dlp --cookies-from-browser firefox --no-playlist --progress -x --audio-format mp3 --audio-quality 0 -o "$HOME/Downloads/%(title)s.%(ext)s" "$1"; }

gb() {
  local branch
  branch=$(git branch -a | grep -v 'HEAD ->' | sed 's/^[* ]*//' | sed 's|remotes/||' | sort -u |
    fzf --no-multi --preview "git log --oneline --color=always {}" --prompt "Git branch: ")
  [[ -n "$branch" ]] && { git switch "${branch#origin/}"; gp || true }
}

function lazygit {
  local f
  f=$(mktemp) || return
  LAZYGIT_NEW_DIR_FILE=$f command lazygit "$@"
  [[ -s $f ]] && cd "$(<$f)"
  rm -f "$f"
}

gc() {
  local files msg
  files=$(git -c color.status=always status --short |
    fzf --multi --ansi --bind 'ctrl-a:select-all' \
      --preview 'git diff -- {2} | delta --width=$FZF_PREVIEW_COLUMNS' \
      --preview-window=right:81% | awk '{print $2}')
  [[ -z "$files" ]] && return
  msg=$(gum input --placeholder "Commit message...")
  [[ -z "$msg" ]] && return
  echo "$files" | xargs git add && git commit -m "$msg"
}

#

_ws_zone_get() {
  sed -n "s|^[[:space:]]*$2[[:space:]]*=[[:space:]]*\"\(.*\)\"[[:space:]]*\$|\1|p" "$1" 2>/dev/null |
    head -1 | sed "s|{{env.HOME}}|$HOME|g"
}

ws_zones() {
  local f zone
  local -a first=() rest=()
  for f in $HOME/*/mise.toml(N); do
    zone=$(_ws_zone_get "$f" WS_ZONE)
    [[ -n $zone ]] || continue
    if [[ $zone == "${WS_ZONE:-}" ]]; then first+=("$zone	$f"); else rest+=("$zone	$f"); fi
  done
  print -l -- "${first[@]}" "${rest[@]}"
}

_ws_repos_in() {
  fd -t d -H --max-depth 4 -g '.git' --base-directory "$1" --prune 2>/dev/null |
    sed 's:/\.git/*$::'
}

goto_repodir() {
  local base="$1" query="$2" zone f root rel choice
  local -a repos=()
  if [[ -n $base ]]; then
    while IFS= read -r rel; do repos+=("$base/$rel"); done < <(_ws_repos_in "$base")
  else
    while IFS=$'\t' read -r zone f; do
      root=$(_ws_zone_get "$f" WS_CODE_ROOT)
      [[ -n $root && -d $root ]] || continue
      while IFS= read -r rel; do repos+=("$root/$rel"); done < <(_ws_repos_in "$root")
    done < <(ws_zones)
  fi
  (( ${#repos} )) || { print -ru2 "goto_repodir: no git repos found"; return 1 }
  choice=$(print -l -- "${repos[@]}" | sed "s|^$HOME/|~/|" |
    fzf --no-multi --height=36% --query "$query" --prompt="Git repo: ")
  [[ -n $choice ]] && cd "${choice/#\~/$HOME}"
}

gg() {
  if (( $+commands[ws] )); then
    local target
    if (( $# )); then
      target=$(command ws repo pick "$1") || return
    else
      target=$(command ws repo pick) || return
    fi
    [[ -n $target && -d $target ]] && cd "$target"
    return
  fi
  goto_repodir "" "$1"
}

zw() { cd ${1:+$HOME/work/code/}${1:-$HOME/work} }
zp() { cd ${1:+$HOME/personal/code/}${1:-$HOME/personal} }

ws() {
  (( $+commands[ws] )) || { print -ru2 "ws: ws binary not found in PATH"; return 127 }
  if (( $# == 0 )) || [[ $1 == tui ]]; then
    local f dir rc
    f=$(mktemp) || return
    WS_TUI_CD_FILE=$f command ws "$@"
    rc=$?
    if [[ -s $f ]]; then
      dir=$(<"$f")
      dir=${dir%$'\n'}
      [[ -n $dir && -d $dir ]] && cd "$dir"
    fi
    rm -f "$f"
    return $rc
  fi
  command ws "$@"
}
alias wst='ws tui'

sel-awsprofile() {
  if (( $+commands[ws] )); then
    if (( $# )); then
      command ws aws pick "$1"
    else
      command ws aws pick
    fi
    return
  fi
  local zone f cfg cred
  {
    while IFS=$'\t' read -r zone f; do
      cfg=$(_ws_zone_get "$f" AWS_CONFIG_FILE)
      cred=$(_ws_zone_get "$f" AWS_SHARED_CREDENTIALS_FILE)
      if command -v aws &>/dev/null; then
        AWS_CONFIG_FILE=$cfg AWS_SHARED_CREDENTIALS_FILE=$cred \
          aws configure list-profiles 2>/dev/null
      else
        grep -hE '^\[.+\]$' "$cred" "$cfg" 2>/dev/null |
          sed -E 's/^\[(profile )?(.+)\]$/\2/'
      fi | sort -u | sed "s|^|$zone\t|"
    done < <(ws_zones)
  } | fzf --no-multi --height=40% --query "$1" --prompt="AWS profile: " | cut -f2
}

cp-awsprofile() {
  local prof
  prof=$(sel-awsprofile "$@") || return
  [[ -n $prof ]] && print -rn -- "$prof" | clipcopy
}

ghs() {
  gh auth status --json 'hosts' --jq '.hosts."github.com".[].login' |
    fzf --no-multi --height=4 --query "$1" --prompt "Github user: " |
    xargs -r -I {} gh auth switch -u {}
}

kwhere() {
  printf 'Ctx: %s\nNS : %s\n' \
    "$(kubectl config current-context)" \
    "$(kubectl config view --minify -o jsonpath='{..namespace}')" | sed 's/NS : $/NS : default/'
}

kn() {
  local ns
  ns=$(kubectl get ns -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | fzf --no-multi \
    --preview "kubectl describe ns {}" \
    --preview-window=right:78%:wrap \
    --height=18 \
    --prompt="Switch Namespace: ")
  [[ -n "$ns" ]] && kubectl config set-context --current --namespace $ns
}

kc() {
  local ctx
  ctx=$(kubectl config get-contexts -o name | fzf --no-multi \
    --height=9 \
    --prompt="Switch Context: ")
  [[ -n "$ctx" ]] && kubectl config use-context $ctx
}

command -v mise &>/dev/null && eval "$(mise activate zsh)"
_ws_cached starship init zsh --print-full-init
_ws_cached zoxide init zsh
_ws_cached fzf --zsh
_ws_cached gh completion -s zsh
_ws_cached go-task --completion zsh
(( $+commands[ws] )) && _ws_cached ws completion zsh
(( $+commands[aws_completer] )) && complete -C $commands[aws_completer] aws

for _p in \
  /usr/share/zsh/plugins/fzf-tab/fzf-tab.plugin.zsh \
  ${HOMEBREW_PREFIX:+$HOMEBREW_PREFIX/share/fzf-tab/fzf-tab.zsh}; do
  [[ -r $_p ]] && source $_p && break
done
for _p in \
  /usr/share/zsh/plugins/zsh-autosuggestions/zsh-autosuggestions.zsh \
  ${HOMEBREW_PREFIX:+$HOMEBREW_PREFIX/share/zsh-autosuggestions/zsh-autosuggestions.zsh}; do
  [[ -r $_p ]] && source $_p && break
done
for _p in \
  /usr/share/zsh/plugins/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh \
  ${HOMEBREW_PREFIX:+$HOMEBREW_PREFIX/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh}; do
  [[ -r $_p ]] && source $_p && break
done
unset _p
