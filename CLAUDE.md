# workstation

Bootstrap + dotfiles + theming for macOS, Arch Linux, and a minimal server
profile. Task runner is mise (`.mise/tasks/`); `mise run check` lints all
shell.

## Rules

1. **Verify, never assume.** Check current docs (Context7, then web) before
   scripting any tool's behavior — flags, paths and defaults change fast.
2. **These scripts mutate machines.** Never run bootstrap/sync/update/os
   tasks while developing. Dev loop is `bash -n`, `mise run check`,
   `--dry-run`. Real runs only when asked, on the machine meant.
3. **Public repo.** No personal or machine-local content (paths, usernames,
   hostnames, accounts) in files or commit messages, and no mention of AI
   assistance. Keep comments minimal.
4. **Never delete files without asking.** Destructive steps need
   `gum confirm` or `--yes`.
5. **Ask before committing.**

## Architecture

- **Deploy** — `links/` maps repo paths into `$HOME` (`common` + one of
  `linux`/`mac`/`server`); `mise run sync` applies it. The repo is live, so
  apps write generated files back into it — gitignore every one.
- **Theming** — one `colors-template.*` per app, plain `{{token}}`
  substitution, static palettes in `themes/`. Terminal scope only.
- **Desktop (arch)** — niri + hyprland × noctalia + DMS, one greetd session
  per combination. Keep the compositor configs at parity.
- **Packages** — paru/brew own global tools, rolling. Lists in `pkg/`.
- **Identity zones** — `~/work` and `~/personal` carry a `mise.toml` whose
  `[env]` applies below it. `mise run identity` seeds it from
  `config/identity/` once as a real file, then never overwrites.
- **Secrets** — 1Password via `lib/op.sh`. `mise.local.toml` and
  `.mise/tasks/local/` are the gitignored private layer.

## Layout

```
bootstrap.sh   curl-able entry      links/     symlink manifests
.mise/tasks/   all entry points     pkg/       package lists
lib/           shared bash          os/        OS-specific setup
config/<app>/  dotfiles             themes/    palettes
.claude/agents/  audit agents       .local/    machine-local (gitignored)
```

## Standards

- `#!/usr/bin/env bash` + `set -euo pipefail`; shellcheck + shfmt clean.
- Runs on macOS and Linux — no GNU-only flags on mac's BSD tools.
- Idempotent: `mkdir -p`, `ln -sfn`, `--needed`; `|| true` only with a reason.
- Machine-mutating code lives in `os/` and the tasks calling it, never `lib/`.
- gum for prompts, fzf for pickers, and a non-interactive escape for each.
- Tasks are thin; first lines are shebang + `#MISE description=`.
- Package lists: one per line, `#` section headers, alphabetical.
- Conventional commits, one concern each.

## Adding a tool

Verify its current config path → `config/<tool>/` → `links/` entry → if
themeable, `colors-template.*` + registry entry + all palettes + gitignore
the output → `pkg/` entry per profile → sync hook if needed → verify with
`mise run check` and `sync --dry-run`.

## Traps

- Generated theme outputs land in the repo — gitignore each new one with its
  template.
- Templates do plain `{{token}}` substitution and die on any unknown token,
  including in comments and in configs whose own syntax uses `{{ }}`.
- macOS ships bash 3.2 and brew's bash is installed *by* the mac bootstrap —
  no `mapfile` in `os/mac/bootstrap.sh` or `.mise/tasks/sync`.
- Homebrew 6: `HOMEBREW_NO_ASK=1` for unattended installs; third-party taps
  need `brew tap` + `brew trust --formula`; `--eval-all` is deprecated.
- Arch: `jack` conflicts with `pipewire-jack` — remove it first.
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
XDG from `~/.zshenv`. `arch` covers derivatives via `ID_LIKE`. orbstack on
mac. Secrets only in 1Password.
