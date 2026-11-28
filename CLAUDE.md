# workstation

Bootstrap + dotfiles + theming + Go CLI/TUI (`ws`) for macOS, Arch Linux, and a
minimal server profile. Machine setup and daily workflows run through `ws`;
mise (`.mise/tasks/`) keeps `ws:install`, `check`, and thin shims that exec
`ws`. `mise run check` lints all shell, and `cd ws && go vet ./... && go test -race ./...` verifies Go.

## Rules

1. **Verify, never assume.** Check current docs (Context7, then web) before
   scripting any tool's behavior — flags, paths and defaults change fast.
2. **These scripts and `ws` commands mutate machines.** Never run
   bootstrap/sync/update/os tasks or mutating `ws` commands while developing.
   Dev loop is `bash -n`, `mise run check`, `go vet ./...`,
   `go test -race ./...`, and `ws --dry-run` / `ws diff`. Real runs only when
   asked, on the machine meant.
3. **Public repo.** No personal or machine-local content (paths, usernames,
   hostnames, accounts) in files or commit messages, and no mention of AI
   assistance. Keep comments minimal.
4. **Never delete files without asking.** Destructive steps need
   confirmation, `--execute`, or `--yes`.
5. **Ask before committing.**

## Architecture

- **Deploy** — `links/` maps repo paths into `$HOME` (`common` + one of
  `linux`/`mac`/`server`); `ws apply` (or `mise run sync`) applies it, renders
  theme templates, and runs post-sync hooks. The repo is live, so apps write
  generated files back into it — gitignore every one.
- **Theming** — one template per app (`colors-template.*`, or
  `config-template.*` when the whole config is themed), listed in
  `themes/registry`; plain `{{token}}` substitution, static palettes in
  `themes/`, rendered by `ws theme` / `ws apply`. Terminal scope only.
- **Desktop (arch)** — niri + hyprland × noctalia + DMS, one greetd session
  per combination. Keep the compositor configs at parity.
- **Packages** — paru/brew own global tools, rolling; `ws bootstrap`,
  `ws pkg`, and `ws update` manage installs, upgrades, and cleanup from `pkg/`,
  `presets.toml`, and `tools.toml`.
- **Go CLI & TUI (`ws/`)** — single binary built by `mise run ws:install`
  (using the Go version pinned in `.mise/config.toml`) into `~/.local/bin/ws`;
  owns machine setup (`ws bootstrap`), daily operations, and unified state
  (`ws.db`, `config.toml`, `theme`, `ledger.json`) under `~/.ws/` (`WS_HOME`
  to override).
- **Identity zones** — `~/work` and `~/personal` carry a `mise.toml` whose
  `[env]` applies below it. `ws zone apply` (or `mise run identity`) seeds it
  from `config/identity/` once as a real file, then never overwrites.
- **Secrets** — 1Password via `ws vault` (`mise run secrets:*` / `op:*` shim
  to it). `mise.local.toml` and `.mise/tasks/local/` are the gitignored
  private layer.

## Layout

```
bootstrap.sh     curl-able entry -> ws  links/         symlink manifests
.mise/tasks/     ws:install, check, shims  pkg/        package lists
ws/              Go CLI & TUI module    presets.toml   composable OS presets
tools.toml       exception tool catalog os/            OS-specific hooks
lib/             hook bash helpers      themes/        palettes
config/<app>/    dotfiles               config/identity/  per-zone mise seeds
.claude/agents/  audit agents           .local/        machine-local (gitignored)
```

## Standards

- `#!/usr/bin/env bash` + `set -euo pipefail`; shellcheck + shfmt clean.
- Runs on macOS and Linux — no GNU-only flags on mac's BSD tools.
- Idempotent: `mkdir -p`, `ln -sfn`, `--needed`; `|| true` only with a reason.
- Machine-mutating shell lives in `os/` hooks and `bootstrap.sh`, never `lib/`.
- gum for prompts, fzf for pickers, and a non-interactive escape for each.
- Tasks are thin shims that exec `ws` (except `check` and `ws:install`); first
  lines are shebang + `#MISE description=`.
- Package lists: one per line, `#` section headers, alphabetical.
- Conventional commits, one concern each.
- Comments: none in Go or shell — only directives (`//go:`/`//nolint` in Go;
  shebang, `#MISE`/`#USAGE`, `# shellcheck` in shell), enforced by
  `mise run check` and `ws/internal/lint`.

## Adding a tool

Verify its current config path → `config/<tool>/` → `links/` entry → if
themeable, `colors-template.*` (or `config-template.*`) + registry entry +
all palettes + gitignore the output → `pkg/` entry per profile → sync hook if
needed → verify with `mise run check`, `go test -race ./...`, and
`ws apply --dry-run`.

## Traps

- Generated theme outputs land in the repo — gitignore each new one with its
  template.
- Templates do plain `{{token}}` substitution and die on any unknown token,
  including in comments and in configs whose own syntax uses `{{ }}`.
- macOS ships bash 3.2 and brew's bash is installed *by* `ws bootstrap` —
  keep `bootstrap.sh` (and anything it runs before brew's bash exists:
  `.mise/tasks/ws/install`, `lib/common.sh`) bash 3.2 compatible (no `mapfile`
  or associative arrays).
- Homebrew 6: `HOMEBREW_NO_ASK=1` for unattended installs; third-party taps
  need `brew tap` + `brew trust --formula`; `--eval-all` is deprecated.
- Arch: `--noconfirm` cannot resolve conflicts, so `pkg/arch-conflicts.txt`
  lists packages replaced by `pkg/arch.txt` (`jack`, `yq`, the v4 noctalia and
  old DMS packages). `ws` reads it for confirmed pre-upgrade removals — add
  new renames there.
- noctalia v5 is the native `noctalia` package (`noctalia msg …` IPC,
  `config.toml`); `noctalia-shell` is the dead v4 name. DMS is now
  `dux-shell`/`dux-greeter-bin`, still `dms`/`dms-greeter` binaries.
- Niri includes take `optional=true`. Hyprland config is Lua since 0.55;
  validate with `Hyprland --verify-config -c`.
- XDG vars come from `config/shell/xdg.sh` via `~/.zshenv`; only
  terminal-launched tools need them. GUI apps get a manifest entry instead.
- Shell env belongs in `~/.zshenv`, not `~/.zshrc`. PATH is the exception —
  macOS `path_helper` runs after it.
- mac needs brew's GNU tools; they are `g`-prefixed unless gnubin is on PATH.
- An untrusted mise config makes mise *error*. Zone configs start untrusted;
  trust is per directory and survives edits.
- Repo tasks run in the repo's own zone — hence `clone --zone`.
- `yq` is two programs: use `go-yq` on arch, `yq` on mac (both mikefarah).
  Scripts use `jq`.
- Claude Code namespaces its macOS Keychain entry by `CLAUDE_CONFIG_DIR`, so
  each zone stays logged in; renaming a zone dir orphans the entry.
- Don't create `AGENTS.md` — opencode would use it instead of this file.
  opencode needs `opencode.json` entries to see `.claude/agents/*`.

## Settled decisions

Symlink manifest over chezmoi/stow. Static palettes over dynamic theming.
Both compositors and both shells, picked at login. greetd + DMS greeter.
paru/brew for global tools, mise for tasks and per-project pinning. Plain
package lists, not Brewfile. Directory-scoped env for identity, not a second
OS user. Zone `mise.toml` is a real file, never a symlink into this repo.
XDG from `~/.zshenv`. `arch` covers derivatives via `ID_LIKE` (CachyOS by
`ID`). orbstack on mac. Secrets only in 1Password.
