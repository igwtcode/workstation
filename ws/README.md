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
cd ws && go run ./cmd/ws
```

Or build manually inside `ws/`:

```bash
cd ws
go build -o dist/ws ./cmd/ws
```

## Interactive K9s-Style TUI

Launch the 6-panel + Output TUI with shared verbs and a per-row action menu (`a`):

- **Navigation & layout**: `1`-`6` / `0` switch panels and toggle Output, `[`/`]` cycle sub-views (runs on Output), `P` jumps to `Workstation → Presets`, `j`/`k`/`ctrl+d`/`ctrl+u`/`ctrl+f`/`ctrl+b`/`gg`/`G` move or scroll, `ctrl+o` jumps back, `p` cycles split layout (`off` -> vertical -> horizontal), `tab` switches pane focus, `=` zooms the focused pane, `z` cycles zone scope, `ctrl+x` cancels the running command, `ctrl+y` copies the view's CLI equivalent, `?` opens scrollable context-aware help.
- **Command palette** (`:`, Tab-completed, with history): views `:overview`, `:repo`, `:gh`/`:prs`/`:issues`, `:packages`, `:search`, `:mise-tools`, `:mise`, `:clean`, `:env`, `:envfiles`, `:vault`, `:fleet`, `:filesync`, `:workstation`, `:doctor`, `:themes`, `:presets`, `:config`, `:watched`, `:hist`, `:out`; context `:zone`, `:split`, `:zoom`, `:clear`; actions `:reload`, `:scan`, `:status`, `:backup`, `:pull`, `:clean-exec`, `:fix`, `:apply`, `:update`, `:undo`, `:!ws <args>`, `:q` (plus aliases such as `:secrets`, `:1`-`:6`, `:0`).
- **Verbs** (where the view supports them): `r` refresh, `y`/`Y` copy, `e` edit (`$VISUAL`/`$EDITOR` for repos, `$EDITOR` elsewhere), `o` open external/browser, `d` diff, `i` install, `u` update/pull/upgrade, `x` remove/delete/reset, `/` fuzzy filter, `F`/`S` facet filter/sort, `space`/`*` mark rows, `\` clear marks and facet filters, `<`/`>` previous/next repo or peer, `a` per-row action menu (panel-specific shortcuts; dangerous ones confirm).
- **Back & quit**: `esc` and `q` both step back one level (modal -> zoom -> drill-down -> filter -> output -> split focus -> marks/theme preview) and quit at the top level unless a mutation is running; `ctrl+c` always quits.
- **Mouse**: enabled by default (`tui.mouse = true`) for wheel scroll and clicks on tabs, sub-views, and split panes; set `tui.mouse = false` in `6 Workstation → Settings` or `~/.ws/config.toml` for native terminal text selection. `tui.start_tab` picks the landing view.

1. **Overview** (landing view) — live workstation health summary (outdated packages, doctor findings, dirty/behind repos, PRs awaiting review, untracked packages, last backup age); `Enter` jumps to a section's tab (`ctrl+o` jumps back), `r` refreshes all sections, `y` copies `ws status`.
2. **Projects (`Repos`, `Pull Requests`, `Issues`)** — filter repositories across zones and flat `~/code`; `Enter` `cd`s on exit via the `ws` zsh wrapper (or opens Repo Detail without `WS_TUI_CD_FILE`), `u` pull (marked or current), `o` open remote in browser, `e` open in `$VISUAL`/`$EDITOR`, `l` open `lazygit` (when installed), `y`/`Y` copy path/URL, `a` actions (`c` clone, `L` repo detail & locals, `P`/`I` PRs & Issues). GitHub PRs/Issues: live Markdown preview (`<details>` shown expanded as indented sections), `Enter` read detail, `<`/`>` previous/next repo, `y` field copy picker, `Y` copy `#N`, `o` open in browser, `a` actions (`A` approve, `M` merge, `X` close/reopen, `m` comment, `K` checkout, `c` copy gh command, `t` PR/Issue, `g` repo/all mine, `s` state filter).
3. **Packages (`Installed`, `Search`, `Mise Tools`, `Mise Configs`, `Cleanup`)** —
   - **Installed**: resolved preset tools + auto-detected untracked OS packages (`Enter` detail, `i`/`u`/`x` install/update/remove, `U` run `ws update`, `a` actions for `t` track, `n` add & track, `D` untrack, `P` switch preset, `s` search, `U` update).
   - **Search**: live cross-provider repository search (`all`, `brew`, `cask`, `paru`, `pacman`, `mise`, `apt`; `tab` cycles the provider in the query box) with side-by-side package inspector (`=` to zoom), `e` edit query, `i`/`Enter` install & track, `x` remove, `a` actions (`s` edit query, `P` provider, `I` install only, `t` track only).
   - **Mise Tools & Mise Configs**: global, zone, and project `mise.toml`/`.tool-versions` files and declared tools, `Enter` detail, `e` edit in `$EDITOR`, `r` refresh outdated versions, `u` upgrade tools, `y` copy config path.
   - **Cleanup**: reclaimable caches by safety tier (`safe` / `costly` / `dangerous`) with dry-run default (`x`/`Enter` run, `a` actions for `d` dry-run, `m` toggle mode, `s` safe defaults).
4. **Env & Secrets (`Vars`, `Files`, `Vault`)** — index shell RC, `mise`, and `.env*` files; `Enter` opens a variable's precedence chain and the other checkouts sharing its salted fingerprint (`tab` switches lists), `y`/`Y` copy, `a` actions (`v` reveal/mask, `R` rotate with snapshot rollback, `c` copy source path, `b` backup `ws.db`); in Files, `Enter` filters Vars by file; Vault manages `ws.db` / 1Password scopes (`a` actions for `s`/`l` save/load 1Password, `K` init/backup DB key, `b` backup `ws.db`, `U` undo).
5. **Fleet (`Nodes`, `File Sync`)** — Tailscale nodes with an `Enter` detail checklist, `d` diff files, `o` open Tailscale SSH, `y`/`Y` copy checklist/join command, `a` actions (`s` sync node, `t` SSH, `d` diff, `Z`/`E` toggle `--no-zones`/`--push-env`); File Sync: `<`/`>` switch peer, `a` actions (`P`/`L` push/pull, `E`/`B` toggle `.env*`/`ws.db.enc`), `esc` back to Nodes.
6. **Workstation (`Doctor`, `Themes`, `Presets`, `Settings`, `Watched Files`, `History`)** —
   - **Doctor**: health checks, `Enter` full-screen diff, `space`/`*` mark findings, `y` copy fix CLI, `a` actions (`f` apply fix, `b` backup `ws.db`, `U` undo).
   - **Themes**: swatch/diff preview, `tab` theme/template list, `Enter` apply (all apps, or the selected template's app), `e` edit template in `$EDITOR`, `a` actions (`t` live preview, `1` apply one app, `A` apply all apps).
   - **Presets** (shortcut `P`): composable machine presets, `Enter` picks the preset the Packages view shows (this session only; `ws preset use` persists it), `o` pick and open `Packages → Installed`, `y` copy `ws preset show` CLI.
   - **Settings & Watched Files**: inspect and override `~/.ws/config.toml` preferences (including `tui.mouse`) — `Enter`/`space` toggles bools or opens option pickers, `x` reset override, `e` edit `config.toml`, `a` actions (`c` custom text, `A` auto-reload, `R` reload all); in Watched Files, `Enter`/`e` edits the file.
   - **History**: fuzzy-search and multi-select unified `zsh`/`bash`/`atuin` shell history (`Enter` inspect, `y` copy marked/selected).
0. **Output Console** — live and historical command logs; `[`/`]` previous/next run, `w` wrap, `y`/`Y` copy output/command, `r` re-run, `x` cancel.

```bash
ws                    # interactive TUI on a TTY (cds on repo Enter via zsh wrapper); help when piped
wst                   # alias for ws tui
ws tui [view]         # open a specific view (e.g. prs, pkg-search, vault, history)
ws gh                 # Projects → Pull Requests for the current repo (-k issue for Issues, --all across repos)
ws pkg search --tui   # open Packages -> Search repository browser
ws <cmd> --tui        # jump to a command's view (status, doctor, theme, preset, config, repo, mise, hist, env, vault, pkg, clean, fleet)
```

## Unified State & Encrypted Vault (`~/.ws/`)

Persistent workstation state lives under `~/.ws/` (overridable via `WS_HOME`):
- `~/.ws/ws.db` — WAL SQLite database (`0600`) with AES-256-GCM encryption for secret blobs and snapshots. Keys resolve in order: `WS_DB_KEY`, OS keyring (macOS Keychain / `secret-tool`), or `~/.ws/db.key` (`0600`).
- `~/.ws/config.toml` — user TUI and workspace overrides managed via `ws config` or `6 Workstation → Settings`.
- `~/.ws/theme` — active theme ID shared with shell helpers.
- `~/.ws/ledger.json` — apply/bootstrap/update transaction ledger supporting `ws undo`.
- `~/.ws/backups/` — encrypted `ws db backup` snapshots (`ws-backup-latest.enc`); 1Password copies use the documents `ws-db.key` and `ws-db-backup.enc`.

```bash
ws db key init                        # generate 256-bit local keyfile (--keychain for OS keyring)
ws db key backup --execute            # preview by default; upload master key to 1Password
ws db key restore --execute           # preview by default; restore master key from 1Password
ws db status                          # inspect schema, row counts, key source, and dirty state
ws db backup --if-changed             # write encrypted snapshot only if state changed (--1password to upload)
ws db restore --write-files --execute # preview by default; restore ws.db tables and 0600 env files (--from-1password)
ws config show                        # show effective settings and sources
ws config set|unset <key> [value]     # set or clear an override (ws config path prints the file)
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
ws pkg update pkg...                       # upgrade specific packages
ws update --digest                         # categorized outdated digest (skips updates=self tools)
ws update [--all]                          # 4-phase update: prep -> OS pkgs & cleanup -> user tools -> ws apply + rebuild
ws update --pkg-only                       # managed packages only (no repo pull, shared tools, or apply)
ws update schedule print --with-db-backup  # preview LaunchAgent/systemd digest & backup units (install|remove|status)
ws android [--list] [--yes]                # Android SDK packages from pkg/android.txt + WS_ANDROID_AVDS emulators
```

## Projects, GitHub & Mise

```bash
ws status [--refresh] [--zone <zone>]      # read-only overview (packages, doctor, repos, PRs, backup)
ws gh [owner/repo] [--all] [--state open|closed|merged|all] [--refresh] [--list]
ws repo list [--zone <zone>] [--refresh]   # list repos across zones and flat ~/code
ws repo pick [query]                       # fuzzy-match and print repo path (used by gg)
ws repo clone owner/repo --zone work       # clone with zone identity (--no-zones for flat ~/code)
ws repo clone --all [kind[:source]]        # bulk-clone every repo matched by WS_FORGES
ws repo pull [--all]                       # concurrent ff-only pull; sequential CodeCommit handoff
ws repo locals [repo]                      # show untracked local files and ws.db backup status
ws mise configs                            # list global, zone, and project mise configs
ws mise list [--outdated]                  # list declared/active mise tools
ws mise outdated                           # check outdated mise tools across projects
ws mise edit <path-or-project>             # open mise config in $EDITOR
ws aws list|pick [query] [--zone <zone>]   # list or fuzzy-pick AWS profiles
```

## Environment Provenance, Fleet & Vault

```bash
ws env scan                                         # index shell RC, mise, and project .env* files
ws env list --secrets-only                          # list masked variables and active sources
ws env find TOKEN                                   # search across keys, paths, zones, fingerprints
ws env provenance API_TOKEN --dir ./service         # show precedence chain and cross-project impact
ws env impact fp:0123456789abcdef                   # list all files sharing a secret fingerprint
ws env rotate API_TOKEN --value-stdin               # dry-run preview (pass --execute to write 0600)
ws env set API_TOKEN -f ./service/.env --value-stdin # preview setting one variable (--execute to write)
ws env restore [--path <filter>] [--execute]        # restore backed-up .env* files from ws.db (0600)
ws env undo <tx-id> [--execute]                     # preview or restore pre-rotation snapshots
ws fleet status                                     # list Tailscale nodes, OS/arch, SSH status, and tags
ws fleet diff <node> [paths...]                     # compare local configs/secrets against a Tailscale peer
ws fleet sync-files <node> [paths...] --push|--pull # preview file sync (pass --execute to transfer)
ws fleet sync <node> --no-zones --push-env          # plan or execute headless workstation sync (--execute)
ws fleet join [--hostname <name>] [--tags tag:dev]  # print a tailscale up command for a new node
ws fleet tags <node> tag:a,tag:b [--execute]        # preview or set remote Tailscale tags
ws vault <secrets|ssh|files|envfiles> <pull|push> [--execute] # preview by default (aliases: load, save)
```

## Safe Read-Only, Guardrails & Plugins

```bash
ws doctor [--fix]                                   # diagnostics (exits 1 on error findings) and gated auto-fixes
ws whoami                                           # active zone, git, AWS, gh, kube, Claude, Codex dirs
ws zone run --zone work -- <cmd...>                 # run a command with a zone's environment overlaid
ws zone migrate work --all [--execute]              # preview or move existing dirs into a zone
ws zone fix-symlinks [zone]                         # convert a symlinked zone mise.toml into a real file
ws theme [list|status|set <id>|edit [app]]          # list, apply, or edit app theme templates
ws apply --dry-run                                  # preview template renders, symlinks, and hooks
ws diff                                             # alias of ws apply -n (pending template and link diffs)
ws undo                                             # revert last apply transaction
ws clean [--tier safe|costly|dangerous] [--execute] # preview or reclaim disk caches
ws hist [query] [--copy]                            # fuzzy-pick or search zsh/bash/atuin history
```

Global flags: `--repo <dir>`, `--preset <name>`, `--dry-run`/`-n`, `--non-interactive`, `--allow <replace,sudo,remove,script,all>`, `--json`, `--tsv`. Plan flags (`--plan <file>`, `--save-plan <file>`) are local to `apply`, `bootstrap`, `clean`, `pkg sync`, `theme`, and `update`.

Consent: actions that replace files, need sudo, remove packages, or run install scripts need the matching `--allow` scope (or `--yes` on `bootstrap`, `--allow all` elsewhere). On a TTY `ws` asks; otherwise each unapproved action is skipped, the rest still run, and the command exits `1` with the scopes to rerun with.

Preview convention: destructive or remote commands (`clean`, `db restore`, `db key backup/restore`, `env rotate/set/restore/undo`, `fleet sync/sync-files/tags`, `vault`, `zone migrate`) preview by default and require `--execute`. All other mutating commands run by default and honor `-n`/`--dry-run` (`ws diff` is the alias of `ws apply -n`).

Exit codes: `0` success, `1` runtime error, `ws doctor` error findings, or consent-skipped steps, `2` usage error, `130` cancelled; plugins pass through their own exit code.

Unknown subcommands dispatch to `ws-<name>` on `PATH` or `.mise/tasks/local/<name>` with `WS_REPO`, `WS_THEME`, and active palette tokens overlaid in the environment (`ws plugin list` lists discovered plugins).
