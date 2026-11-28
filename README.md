# workstation

Bootstrap, dotfiles, theming, package management, and identity zones for macOS, Arch Linux, and headless servers — driven by the `ws` CLI/TUI (with thin `.mise/tasks/` shims).

## Quickstart

```sh
bash -c "$(curl -fsSL https://raw.githubusercontent.com/igwtcode/workstation/main/bootstrap.sh)"
```

`bootstrap.sh` detects the OS (`--profile mac|arch|server` to override), installs `git` + `mise` (plus Xcode CLT and Homebrew on macOS), clones or pulls to `~/personal/code/igwtcode/workstation` (`WS_DIR` to override), runs `mise trust` and `mise install` to fetch the pinned Go toolchain, builds `ws` to `~/.local/bin/ws`, and execs `ws bootstrap --repo <checkout> --profile <profile>`. `ws bootstrap` runs seven phases:

1. packages and OS hooks
2. links and theme templates
3. `mise trust` for the repo
4. 1Password secrets pull (desktop profiles, skipped once `WS_ZONES` is set)
5. identity zones
6. `~/.ssh` and `WS_OP_FILES` restores
7. theme

Phases 4–6 are skipped on `server`. Re-running is safe. `--yes` approves every consent scope and skips confirmations; without it, actions that need consent (`--allow replace,sudo,remove,script`) are skipped one by one, the phase reports `partial`, and the run exits non-zero.

To build or update the `ws` binary standalone from any checkout:

```sh
mise run ws:install
```

## Using `ws` and `mise`

From any directory, run the `ws` CLI/TUI (or `wst` to open the TUI with shell `cd`-on-select for repositories):

| Command | Purpose |
| --- | --- |
| `ws` / `wst` | Open the interactive K9s-style TUI (`ws --help` lists all CLI subcommands) |
| `ws status` | Read-only overview: outdated packages, doctor findings, repos, PRs, last backup |
| `ws bootstrap [--profile mac\|arch\|server] [--yes]` | Full machine setup (phases above); `--packages-only` for packages and OS hooks |
| `ws update` | Parallel workstation update (`git pull`, missing packages, OS upgrades/cleanup, `rustup`/`tldr`/`gh-ext`/`mise`, `ws apply`, rebuild); `--digest` previews, `--pkg-only` limits it to packages |
| `ws apply` / `ws diff` / `ws undo` | Render theme templates, sync symlinks, preview diffs, or roll back via `~/.ws/ledger.json` |
| `ws doctor [--fix]` | Audit symlinks, templates, PATH, backups, and zones |
| `ws gh [owner/repo]` | Interactive PRs & Issues dashboard for the current repo (`--all` across repos) |
| `ws pkg search\|info\|install\|update\|remove` | Search `brew`/`cask`/`paru`/`pacman`/`mise`/`apt` repos, inspect metadata, and install/upgrade/remove packages (`i`/`rm` aliases) |
| `ws pkg add\|remove --keep-installed` | Declaratively install+track or untrack packages in `pkg/*.txt` |
| `ws pkg list [--missing\|--untracked]` | List preset, missing, or untracked packages (`ws pkg sync\|outdated` to install/audit) |
| `ws theme [id\|edit]` / `ws preset` | Switch/edit color palettes or inspect/select declarative presets in `presets.toml` |
| `ws mise` / `ws env` / `ws hist` | Inspect/upgrade mise configs & tools, trace env provenance & rotate secrets, fuzzy-pick shell history |
| `ws repo` / `ws aws` | List, pick, clone, and pull repos across zones; list or pick AWS profiles |
| `ws fleet` | Tailscale node inventory, file diff/sync (`diff`/`sync-files`), headless VM sync, `join` command, and tags |
| `ws zone` / `ws whoami` | Inspect, apply, migrate, or run commands in directory-scoped identity zones |
| `ws vault` / `ws db` | 1Password backups (`secrets`, `ssh`, `files`, `envfiles` `pull`/`push`) and encrypted `~/.ws/ws.db` |
| `ws clean` / `ws config` | Tiered disk cache cleanup and persistent `~/.ws/config.toml` settings |
| `ws android` | Android SDK packages and AVDs (see [Private layer](#private-layer)) |

Repo tooling (`ws:install`, `check`) and thin shims that exec `ws` live under `.mise/tasks/` (`mise tasks` / `mise run <task>`). Executables under `.mise/tasks/local/` or `ws-<name>` on `PATH` act as `ws` plugins (`ws plugin list`).

## How it works

- **Symlinks, not copies** — `links/` maps repo paths into `$HOME`; the repo is the live source of truth and `git diff` shows drift. `XDG_CONFIG_HOME` & co. come from `~/.zshenv`, so macOS uses the Linux layout.
- **Presets & catalog** — `presets.toml` declares composable OS presets on top of `pkg/*.txt`; `tools.toml` catalogs the exceptions (non-default installers, distro remaps, self-updating tools, conflicts).
- **Theming** — one template per terminal app (`colors-template.*` or `config-template.*`, listed in `themes/registry`), static palettes in `themes/`, rendered by `ws theme <id>` or `mise run theme`.
- **Packages** — `paru` (Arch) / `brew` (macOS) / `mise`, rolling; package lists live in `pkg/`.
- **Unified state (`~/.ws/`)** — stores encrypted `ws.db`, `config.toml`, active `theme`, `ledger.json`, and caches (`WS_HOME` to override).
- **Identity zones** — `~/work` and `~/personal` each carry a `mise.toml` whose `[env]` applies to everything below, so git, `gh`, `aws`, `kubectl`, Claude Code, and Codex identity follow the working directory.

## Identity zones

```
~/work/       mise.toml        real file, seeded once from config/identity/
  code/…      mise.local.toml  generated from WS_ZONES (only with an aws_profile)
              .gitconfig       generated from WS_ZONES
~/personal/   … same
```

Each zone config points `GH_CONFIG_DIR`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE`, `KUBECONFIG`, and `WS_CODE_ROOT` at zone-specific paths; `~/.config/git/config` includes the zone's `.gitconfig` via `includeIf gitdir:`. `CLAUDE_CONFIG_DIR` also namespaces Claude Code's macOS Keychain entry so both zones stay logged in at once, while `config/identity/work.mise.toml` sets `CODEX_HOME=~/.config/codex-work` (personal uses `~/.codex`). After seeding, the zone `mise.toml` belongs to the machine — zone-only secrets go there, never into the seed — and a hand-written `.gitconfig` or `mise.local.toml` (first line without `generated by`) is left alone.

| Command | Purpose |
| --- | --- |
| `ws zone apply` (`mise run identity`) | create/refresh zones, seed + trust zone config, write git identity |
| `ws zone status` (`mise run identity --status`) | show what each zone resolves to |
| `ws whoami` | show active zone, git, AWS, `gh`, `kube`, Claude, and Codex directories |
| `ws zone migrate <zone> [source] [--execute]` | preview or move existing dirs into a zone |
| `ws zone run --zone <zone> -- <cmd...>` | run a command with a zone's environment overlaid |
| `ws zone fix-symlinks [zone]` | turn a symlinked zone config back into a real file |
| `ws repo clone <src> --zone work` | clone with another zone's identity (`--all` bulk-clones `WS_FORGES`) |

Launch GUI editors from a shell already inside the zone — GUI launchers inherit no zone env.

## Private layer

Secrets stay in 1Password (`op` CLI). Two gitignored files carry everything machine-private: `mise.local.toml` (env vars) and `.mise/tasks/local/` (`local:*` tasks). `ws vault secrets pull --execute` / `push --execute` (or `mise run secrets:pull` / `secrets:push`) round-trip both as the 1Password document `workstation-secrets.zip`; `ws vault ssh`, `files`, and `envfiles` do the same for `~/.ssh`, the `WS_OP_FILES` paths, and each zone's `.env*` files (preview by default without `--execute`; restoring over existing files needs `--allow replace`).

`mise.local.toml` `[env]` keys read by `ws`:

| Key | Format |
| --- | --- |
| `WS_ZONES` | one zone per line: `name\|git_name\|git_email[\|signingkey[\|aws_profile]]` |
| `WS_OP_VAULT` | 1Password vault for every `ws vault` / `ws db` document (unset searches all vaults) |
| `WS_OP_FILES` | one `~/`-relative file or directory per line; a directory travels as `<path>.zip` |
| `WS_FORGES` | one `kind\|source\|dest` per line for `ws repo clone --all` (`github\|owner[/repo]\|~/zone/code/owner`, `codecommit\|<aws-profile>\|<dest>`) |
| `WS_ANDROID_AVDS` | one `name\|device\|image` per line; the host ABI is appended to the image |
| `WS_AWS_DEFAULT_ZONE` | optional: `ws apply` links `~/.aws/credentials` to that zone's credentials file when missing |
| `WS_SYNC_PATHS` | optional: extra `ws fleet` sync paths (prefix `=` to replace the defaults) |
| `WS_EXCLUDE_PATHS` | optional: comma-separated link targets or template outputs `ws apply` skips on this machine |
| `WS_OP_SECRETS_DOC` / `WS_OP_SSH_DOC` / `WS_OP_ENVFILES_DOC` | optional 1Password document titles (defaults `workstation-secrets.zip`, `~/.ssh.zip`, `workstation-envfiles-<zone>.zip`) |

The `ws.db` key and snapshots use their own documents: `ws db key backup|restore --execute` (`ws-db.key`) and `ws db backup --1password` / `ws db restore --from-1password` (`ws-db-backup.enc`).

`mise run android` (`ws android`) installs the SDK packages in `pkg/android.txt`, downloading and checksum-verifying Android cmdline-tools when `sdkmanager` is missing, and creates the AVDs in `WS_ANDROID_AVDS`. Accepting the SDK licenses needs a TTY prompt, `--yes`, or `--allow all`; `-n` previews.

### New machine

1. Install and sign in to 1Password with CLI integration enabled (bootstrap installs the app and `op` on mac/arch and pauses for this).
2. Run the Quickstart. With 1Password ready, bootstrap pulls the private layer, applies the zones, and restores `~/.ssh` and `WS_OP_FILES`.
3. In each zone, sign in the per-zone tools (`gh auth login`, Claude Code, Codex, `aws`).
4. Clone repos (`ws repo clone --all`), then `ws vault envfiles pull --all --execute`.
5. Optional: `ws db key restore --execute` and `ws db restore --from-1password --execute`.
6. Machine-local files that never leave the machine: `config/niri/local.kdl` and `config/hypr/local.lua` (monitor layout).
7. Check with `ws doctor` and `ws whoami`.

## Layout

| Path | Purpose |
| --- | --- |
| `ws/` | Go CLI and Bubble Tea v2 TUI source (`ws/cmd/ws`, `ws/internal/*`) |
| `presets.toml` / `tools.toml` | Declarative OS presets and exception tool catalog |
| `.mise/tasks/` | `ws:install`, lint (`check`), and thin `ws` task shims |
| `config/<app>/` | Dotfiles + theme templates (`colors-template.*` / `config-template.*`) |
| `config/identity/` | Per-zone `mise.toml` env seeds |
| `themes/<name>/` | Static color palettes (dark/light) |
| `links/` | Symlink manifests per profile |
| `pkg/` | Package lists (`mac-brew.txt`, `mac-cask.txt`, `arch.txt`, `server.txt`, ...) |
| `os/` | OS-specific hooks invoked by `ws` (`presets.toml` `os_hooks`) |
| `lib/` | Shared bash helpers for `os/` hooks and `ws:install` |
