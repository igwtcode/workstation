# ws

Single-binary Go CLI and interactive Bubble Tea v2 TUI for managing workstation presets, theme templates, symlink manifests, cross-manager packages, encrypted local state (`ws.db`), environment provenance and secret rotation, mise configs, Git/GitHub/CodeCommit workflows, disk cleanup, identity zones, and Tailscale fleets across macOS and Linux.

## Build & Install

From the workstation repo root, `mise install` provides the pinned Go toolchain (`.mise/config.toml`), and the `ws:install` task builds and atomically installs `ws` into `~/.local/bin/ws` (saving the previous binary to `~/.local/bin/ws.prev` before replacing it):

```bash
mise install
mise run ws:install
```

`bootstrap.sh` performs those two steps automatically after installing `git` + `mise` (plus Xcode CLT and Homebrew on macOS) and cloning the repo, then execs `ws bootstrap --repo <checkout> --profile <profile> [--yes]`. Daily `.mise/tasks/` entries are thin shims that exec `ws`.

If a newly installed binary fails, restore the previous one or run directly from the checkout:

```bash
cp ~/.local/bin/ws.prev ~/.local/bin/ws
# or run from the checkout:
go run ./ws/cmd/ws
```

Or build manually inside `ws/`:

```bash
cd ws
go build -o dist/ws ./cmd/ws
```

## Interactive K9s-Style TUI

Launch the 6-panel + Output TUI with consistent global verbs and a per-row action menu (`a`):

- **Navigation & layout**: `1`-`6` / `0` switch panels and Output, `[`/`]` cycle sub-views, `P` jumps to `Workstation → Presets`, `j`/`k`/`ctrl+d`/`ctrl+u`/`gg`/`G` move or scroll, `ctrl+o` jumps back, `p` cycles split layout (`off` -> vertical -> horizontal), `tab` switches pane focus, `=` zooms the focused pane, `z` cycles zone scope, `:` opens the Tab-completed command palette (`:overview`, `:repo`, `:packages`, `:secrets`, `:fleet`, `:workstation`, `:presets`, `:config`, `:hist`, `:fix`, `:apply`, `:update`, plus legacy aliases), `?` opens scrollable context-aware help.
- **Global verbs**: `r` refresh, `y`/`Y` copy, `e` edit in `$VISUAL`/`$EDITOR`, `o` open external/browser, `d` diff, `i` install, `u` update/pull/upgrade, `x` remove/delete/reset, `/` fuzzy filter, `F`/`S` facet filter/sort, `space`/`*`/`\` mark rows, `a` per-row action menu (panel-specific actions with shortcuts and confirmations).
- **Back & quit**: `esc` and `q` both step back one level (modal -> zoom -> drill-down -> filter -> output -> split focus -> marks/preview) and quit at the top level; `ctrl+c` always quits.
- **Mouse**: enabled by default (`tui.mouse = true`); set `tui.mouse = false` in `6 Workstation → Settings` or `~/.ws/config.toml` to disable mouse capture for native terminal text selection.

1. **Overview** (landing view) — live workstation health summary (outdated packages, doctor findings, dirty/behind repos, PRs awaiting review, untracked packages, last backup age); press `Enter` on any section to jump to its tab (`ctrl+o` jumps back), `r` refreshes all sections.
2. **Projects (`Repos`, `Pull Requests`, `Issues`)** — filter repositories across zones and flat `~/code`, press `Enter` to `cd` on exit via the `ws` zsh wrapper (or open Repo Detail without `WS_TUI_CD_FILE`), `u` pull, `o` open remote in browser, `e` open in `$VISUAL`/`$EDITOR`, `l` open `lazygit` (shown when installed), `a` actions (`c` clone, `L` local files, `P`/`I` PRs & Issues), and manage GitHub PRs/Issues (live Markdown preview with `<details>` folding, `y` field copy picker, `o` open in browser, `a` actions for `A`/`M`/`X`/`m`/`K` approve/merge/state/comment/checkout).
3. **Packages (`Installed`, `Search`, `Mise Tools`, `Mise Configs`, `Cleanup`)** —
   - **Installed**: inspect resolved preset tools + auto-detected untracked OS packages (`i`/`u`/`x` install/update/remove, `U` run `ws update`, `a` actions for `t` track, `n` add & track, `D` untrack, `P` switch preset, `s` search).
   - **Search**: live cross-provider package repository search (`brew`, `cask`, `paru`, `pacman`, `mise`, `apt`) with side-by-side rich package inspector (`=` to zoom), `e` edit query, `i`/`Enter` Install & Track, `x` Remove, `a` actions (`P` provider, `I` Install Only, `t` Track Only).
   - **Mise Tools & Mise Configs**: inspect global, zone, and project `mise.toml`/`.tool-versions` files and declared tools, `e` edit in `$VISUAL`/`$EDITOR`, `r` refresh outdated versions, and `u` upgrade tools.
   - **Cleanup**: scan reclaimable caches by safety tier (`safe` / `costly` / `dangerous`) with dry-run default (`x`/`Enter` run, `a` actions for `d` dry-run, `m` toggle mode, `s` safe defaults).
4. **Env & Secrets (`Vars`, `Files`, `Vault`)** — index shell RC, `mise`, and `.env*` files, inspect override provenance and salted-fingerprint blast radius (`y`/`Y` copy, `a` actions for `v` reveal/mask, `R` rotate secrets with atomic rollback, `c` copy source path, `b` backup `ws.db`), and manage `ws.db` / 1Password vault scopes (`a` actions for `s`/`l` save/load 1Password, `K` init/backup DB key, `b` backup `ws.db`, `U` undo).
5. **Fleet (`Nodes`, `File Sync`)** — inspect Tailscale nodes, `d` diff files, `o` open Tailscale SSH, `a` actions (`s` sync node, `Z`/`E` toggle `--no-zones`/`--push-env`; in File Sync: `P`/`L` push/pull, `E`/`B` toggle `.env*`/`ws.db.enc`).
6. **Workstation (`Doctor`, `Themes`, `Presets`, `Settings`, `Watched Files`, `History`)** —
   - **Doctor**: workstation health checks, `y` copy CLI, `a` actions (`f` apply fix, `b` backup `ws.db`, `U` undo).
   - **Themes**: swatch/diff live preview, `Enter` apply, `e` edit template in `$EDITOR`, `a` actions (`t` preview theme, `1` apply 1 app, `A` apply all apps).
   - **Presets** (shortcut `P`): inspect composable machine presets, `Enter` switch active preset, `o` open preset in `Packages → Installed`, `y` copy `ws preset show` CLI.
   - **Settings & Watched Files**: inspect and override `~/.ws/config.toml` preferences (including `tui.mouse`) with option pickers (`Enter`/`space`), `x` reset override, `e` edit `config.toml`, `a` actions (`c` custom text, `A` auto-reload, `R` reload all).
   - **History**: fuzzy-search and multi-select unified `zsh`/`bash`/`atuin` shell history (`y` copy marked/selected).
0. **Output Console** — live and historical command execution logs.

```bash
ws                    # interactive TUI on a TTY (cds on repo Enter via zsh wrapper); help when piped
wst                   # alias for ws tui
ws gh                 # open GitHub PRs & Issues dashboard scoped to current repo
ws pkg search --tui   # open Packages -> Search repository browser
ws <cmd> --tui        # jump directly to a command's TUI view
```

## Unified State & Encrypted Vault (`~/.ws/`)

Persistent workstation state lives under `~/.ws/` (overridable via `WS_HOME`):
- `~/.ws/ws.db` — WAL SQLite database (`0600`) with AES-256-GCM encryption for secret blobs and snapshots. Keys resolve in order: `WS_DB_KEY`, OS keyring (macOS Keychain / `secret-tool`), or `$XDG_CONFIG_HOME/ws/db.key` (`0600`).
- `~/.ws/config.toml` — user TUI and workspace overrides managed via `ws config` or `6 Workstation → Settings`.
- `~/.ws/theme` — active theme ID shared with shell helpers.
- `~/.ws/ledger.json` — apply/bootstrap/update transaction ledger supporting `ws undo`.

```bash
ws db key init                        # generate 256-bit local keyfile (--keychain for OS keyring)
ws db key backup --execute            # preview by default; upload master key to 1Password
ws db key restore --execute           # preview by default; restore master key from 1Password
ws db status                          # inspect schema, row counts, key source, and dirty state
ws db backup --if-changed             # write encrypted snapshot only if state changed (--1password to upload)
ws db restore --write-files --execute # preview by default; restore ws.db tables and 0600 env files
ws config show                        # show effective settings and sources
```

## Bootstrap, Packages & Phased Workstation Updates

```bash
ws bootstrap [--profile mac|arch|server] [--yes]  # full setup: packages, OS hooks, links/templates, mise trust, 1Password gate, zones, restores, theme
ws pkg list [--missing|--untracked]        # list preset, missing, or untracked packages
ws pkg sync                                # install missing packages and shims
ws pkg outdated                            # read-only outdated digest across OS/mise/gh-ext
ws pkg search [--provider brew|paru|...] q # search available package repositories
ws pkg info [package]                      # inspect rich package metadata (fuzzy-picks if omitted on TTY)
ws pkg install [--track] [pkg...]          # install package(s) or open search browser (alias: i)
ws pkg add [--no-install] pkg...           # install and append to pkg/*.txt
ws pkg remove [--keep-installed] [pkg...]  # uninstall and untrack (alias: rm; --keep-installed only untracks)
ws update --digest                         # categorized outdated digest (skips updates=self tools)
ws update [--all]                          # 4-phase update: prep -> OS pkgs & cleanup -> user tools -> ws apply + rebuild
ws update schedule print --with-db-backup  # preview LaunchAgent/systemd digest & backup units
```

## Projects, GitHub & Mise

```bash
ws gh [owner/repo] [--all] [--state open|closed|all] [--refresh] [--list]
ws repo list [--zone <zone>] [--refresh]   # list repos across zones and flat ~/code
ws repo pick [query]                       # fuzzy-match and print repo path (used by gg)
ws repo clone owner/repo --zone work       # clone with zone identity (--no-zones for flat ~/code)
ws repo pull [--all]                       # concurrent ff-only pull; sequential CodeCommit handoff
ws repo locals [repo]                      # show untracked local files and ws.db backup status
ws mise configs                            # list global, zone, and project mise configs
ws mise list [--outdated]                  # list declared/active mise tools
ws mise outdated                           # check outdated mise tools across projects
ws mise edit <path-or-project>             # open mise config in $EDITOR
```

## Environment Provenance, Fleet & Vault

```bash
ws env scan                                         # index shell RC, mise, and project .env* files
ws env list --secrets-only                          # list masked variables and active sources
ws env find TOKEN                                   # search across keys, paths, zones, fingerprints
ws env provenance API_TOKEN --dir ./service         # show precedence chain and cross-project impact
ws env impact fp:0123456789abcdef                   # list all files sharing a secret fingerprint
ws env rotate API_TOKEN --value-stdin               # dry-run preview (pass --execute to write 0600)
ws env undo <tx-id> [--execute]                     # preview or restore pre-rotation snapshots
ws fleet status                                     # list Tailscale nodes, OS/arch, SSH status, and tags
ws fleet diff <node> [paths...]                     # compare local configs/secrets against a Tailscale peer
ws fleet sync-files <node> [paths...] --push|--pull # preview file sync (pass --execute to transfer)
ws fleet sync <node> --no-zones --push-env          # plan or execute headless workstation sync (--execute)
ws vault <secrets|ssh|files|envfiles> <pull|push> [--execute] # preview by default (aliases: load, save)
```

## Safe Read-Only, Guardrails & Plugins

```bash
ws doctor [--fix]                                   # diagnostics (exits 1 on error findings) and gated auto-fixes
ws whoami                                           # active zone, git, AWS, gh, kube, Claude, Codex dirs
ws zone run --zone work -- <cmd...>                 # run a command with a zone's environment overlaid
ws zone migrate work --all [--execute]              # preview or move existing dirs into a zone
ws theme [list|status|set <id>|edit [app]]          # list, apply, or edit app theme templates
ws apply --dry-run                                  # preview template renders, symlinks, and hooks
ws diff                                             # alias of ws apply -n (pending template and link diffs)
ws undo                                             # revert last apply transaction
ws clean [--tier safe|costly|dangerous] [--execute] # preview or reclaim disk caches
ws hist [query] [--copy]                            # fuzzy-pick or search zsh/bash/atuin history
```

Global flags: `--repo <dir>`, `--preset <name>`, `--dry-run`/`-n`, `--non-interactive`, `--allow <replace,sudo,remove,script,all>`, `--json`, `--tsv`. Plan flags (`--plan <file>`, `--save-plan <file>`) are local to `apply`, `bootstrap`, `clean`, `pkg sync`, `theme`, and `update`.

Preview convention: destructive or remote commands (`clean`, `db restore`, `db key backup/restore`, `env rotate/set/undo`, `fleet sync/sync-files/tags`, `vault`, `zone migrate`) preview by default and require `--execute`. All other mutating commands run by default and honor `-n`/`--dry-run` (`ws diff` is the alias of `ws apply -n`).

Exit codes: `0` success, `1` runtime error or `ws doctor` error findings, `2` usage error, `130` cancelled.

Unknown subcommands dispatch to `ws-<name>` on `PATH` or `.mise/tasks/local/<name>` with `WS_REPO`, `WS_THEME`, and active palette tokens overlaid in the environment (`ws plugin list` lists discovered plugins).
