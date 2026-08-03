#!/usr/bin/env zsh
# Shared zsh core for every profile. Sourced by zshrc after the OS layer
# (mac.zsh/linux.zsh), which provides PATH/env groundwork and clipcopy().
# Everything tool-specific is guarded so a machine without the tool works.

# --- options ------------------------------------------------------------

setopt HIST_EXPIRE_DUPS_FIRST
setopt HIST_IGNORE_DUPS
setopt HIST_IGNORE_ALL_DUPS
setopt HIST_IGNORE_SPACE
setopt HIST_FIND_NO_DUPS
setopt HIST_SAVE_NO_DUPS
setopt HIST_NO_FUNCTIONS # do not save function definitions
setopt APPEND_HISTORY
setopt INC_APPEND_HISTORY
setopt SHARE_HISTORY
setopt prompt_subst
setopt AUTO_CD

# --- completion ---------------------------------------------------------

zstyle ':completion:*' use-cache on
zstyle ':completion:*' cache-path ~/.zsh/cache
zstyle ':completion:*' matcher-list 'r:|=*' 'l:|=*' 'r:|=* m:{a-z\-}={A-Z\_}'
zstyle ':completion:*' list-dirs-first true

fpath=(~/.local/share/zsh/site-functions $fpath)

autoload -U +X bashcompinit && bashcompinit
autoload -Uz compinit
compinit -u

# --- environment ----------------------------------------------------------

# XDG base dirs come from ~/.zshenv (config/shell/xdg.sh) — set there, not
# here, so scripts and remote commands get them too. Guarded anyway: this
# file must not break if sourced without that layer.
: "${XDG_CONFIG_HOME:=$HOME/.config}" "${XDG_CACHE_HOME:=$HOME/.cache}"

export HISTORY_TIME_FORMAT="%Y-%m-%d %T "
export HISTFILE=$HOME/.zsh_history
export HISTSIZE=9999
export SAVEHIST=9999
export EDITOR=nvim
export VISUAL=nvim
# WS_CODE_ROOT is set per identity zone (~/work, ~/personal) by mise; this
# is only the fallback for shells sitting outside every zone.
export WS_CODE_ROOT=${WS_CODE_ROOT:-$HOME}
# starship's own default is ~/.config/starship.toml, not a starship/ dir
export STARSHIP_CONFIG=$XDG_CONFIG_HOME/starship/config.toml
export TF_PLUGIN_CACHE_DIR=$XDG_CACHE_HOME/terraform-plugins
export SAM_CLI_TELEMETRY=0
export TANSTACK_CLI_TELEMETRY_DISABLED=1
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin:$HOME/.cargo/bin:$HOME/.bun/bin:$HOME/.local/bin

# Base fzf look; colors come from the theme system below.
export FZF_DEFAULT_OPTS=" \
--preview-window='border-rounded' --prompt='❯ ' \
--marker='● ' --pointer='▶ ' --separator='─' --scrollbar='▌' --highlight-line \
--layout=reverse"

# Theme-rendered color snippets (`mise run theme` writes these; the config
# dirs are symlinked, so they resolve on every machine once rendered).
[[ -f $XDG_CONFIG_HOME/fzf/colors.sh ]] && source $XDG_CONFIG_HOME/fzf/colors.sh
[[ -f $XDG_CONFIG_HOME/gum/colors.sh ]] && source $XDG_CONFIG_HOME/gum/colors.sh

# --- aliases --------------------------------------------------------------

alias .....='cd ../../../..'
alias ....='cd ../../..'
alias ...='cd ../..'
alias ..='cd ..'
alias b=bat
alias c=clear
alias cp-awsprofile=' sel-awsprofile | tr -d "\n" | clipcopy'
alias cr='cargo run -- '
alias crq='cargo run -q -- '
alias gd='git diff'
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
# lazydocker takes an explicit CONFIG_DIR rather than following XDG
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

# --- functions ------------------------------------------------------------

ytv() { yt-dlp --cookies-from-browser firefox --no-playlist --progress -f "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]" --merge-output-format mp4 -o "$HOME/Downloads/%(title)s.%(ext)s" "$1"; }
yta() { yt-dlp --cookies-from-browser firefox --no-playlist --progress -x --audio-format mp3 --audio-quality 0 -o "$HOME/Downloads/%(title)s.%(ext)s" "$1"; }

# switch git branch via fzf, then pull
gb() {
  local branch
  branch=$(git branch -a | grep -v 'HEAD ->' | sed 's/^[* ]*//' | sed 's|remotes/||' | sort -u |
    fzf --preview "git log --oneline --color=always {}" --prompt "Git branch: ")
  [[ -n "$branch" ]] && { git switch "${branch#origin/}"; gp || true }
}

# stage picked files and commit with a gum-prompted message
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

goto_repodir() {
  local base_dir="$1" query="$2" max_depth=4 dir
  dir=$(fd -t d -H --max-depth $max_depth -g '.git' \
    --base-directory "$base_dir" --prune |
    sed 's:/\.git/*$::' |
    fzf --height=36% --query "$query" --prompt="Git Repo: ")
  [[ -n "$dir" ]] && cd "$base_dir/$dir"
}

# repo picker over the active zone's code root (~/work/code, ~/personal/code)
gg() { goto_repodir $WS_CODE_ROOT "$1" }

# jump into a zone; identity (git/gh/aws/claude) follows via mise
zw() { cd ${1:+$HOME/work/code/}${1:-$HOME/work} }
zp() { cd ${1:+$HOME/personal/code/}${1:-$HOME/personal} }

# pick an AWS profile from the *active zone's* files (the env vars the zone
# sets are what `aws configure list-profiles` reads)
sel-awsprofile() {
  if command -v aws &>/dev/null; then
    aws configure list-profiles 2>/dev/null | sort -u |
      fzf --height=40% --prompt="AWS profile: "
  else
    grep -hE '^\[.+\]$' ${AWS_SHARED_CREDENTIALS_FILE:-$HOME/.aws/credentials} |
      sed -E 's/^\[(.+)\]$/\1/' | sort -u | fzf --height=40% --prompt="AWS profile: "
  fi
}

# switch the active gh account
ghs() {
  gh auth status --json 'hosts' --jq '.hosts."github.com".[].login' |
    fzf --height=4 --query "$1" --prompt "Github user: " |
    xargs -r -I {} gh auth switch -u {}
}

kwhere() {
  printf 'Ctx: %s\nNS : %s\n' \
    "$(kubectl config current-context)" \
    "$(kubectl config view --minify -o jsonpath='{..namespace}')" | sed 's/NS : $/NS : default/'
}

kn() {
  local ns
  ns=$(kubectl get ns -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | fzf \
    --preview "kubectl describe ns {}" \
    --preview-window=right:78%:wrap \
    --height=18 \
    --prompt="Switch Namespace: ")
  [[ -n "$ns" ]] && kubectl config set-context --current --namespace $ns
}

kc() {
  local ctx
  ctx=$(kubectl config get-contexts -o name | fzf \
    --height=9 \
    --prompt="Switch Context: ")
  [[ -n "$ctx" ]] && kubectl config use-context $ctx
}

# --- tool inits & completions ----------------------------------------------

command -v mise &>/dev/null && eval "$(mise activate zsh)"
command -v starship &>/dev/null && eval "$(starship init zsh)"
command -v zoxide &>/dev/null && eval "$(zoxide init zsh)"
command -v fzf &>/dev/null && source <(fzf --zsh)
command -v gh &>/dev/null && source <(gh completion -s zsh)
command -v go-task &>/dev/null && eval "$(go-task --completion zsh)"
command -v aws_completer &>/dev/null && complete -C "$(command -v aws_completer)" aws

# Plugins: first readable candidate wins (pacman path on arch, brew on mac).
for _p in \
  /usr/share/zsh/plugins/zsh-autosuggestions/zsh-autosuggestions.zsh \
  ${HOMEBREW_PREFIX:+$HOMEBREW_PREFIX/share/zsh-autosuggestions/zsh-autosuggestions.zsh}; do
  [[ -r $_p ]] && source $_p && break
done
# syntax highlighting wants to be sourced last
for _p in \
  /usr/share/zsh/plugins/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh \
  ${HOMEBREW_PREFIX:+$HOMEBREW_PREFIX/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh}; do
  [[ -r $_p ]] && source $_p && break
done
unset _p
