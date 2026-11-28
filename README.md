# workstation

Bootstrap, dotfiles, theming, package management, and identity zones for macOS, Arch Linux, and headless servers — driven by the `ws` CLI/TUI (with thin `.mise/tasks/` shims).

## Quickstart

```sh
bash -c "$(curl -fsSL https://raw.githubusercontent.com/igwtcode/workstation/main/bootstrap.sh)"
```

Detects the OS (`--profile mac|arch|server` to override), installs `git` + `mise` (plus Xcode CLT and Homebrew on macOS), clones or pulls to `~/personal/code/igwtcode/workstation` (`WS_DIR` to override), runs `mise trust` and `mise install` to fetch the pinned Go toolchain, builds `ws` to `~/.local/bin/ws`, and execs `ws bootstrap --repo <checkout> --profile <profile>` — which installs OS packages and hooks, renders theme templates and symlinks dotfiles, trusts mise, handles the desktop 1Password gate and restores, applies identity zones, and sets the theme. Re-running is safe; `--yes` skips confirmations.

To build or update the `ws` binary standalone from any checkout:

```sh
mise run ws:install
```

## Using `ws` and `mise`

From any directory, run the `ws` CLI/TUI (or `wst` to open the TUI with shell `cd`-on-select for repositories):

| Command | Purpose |
| --- | --- |
| `ws` / `wst` | Open the interactive K9s-style TUI (`ws --help` lists all CLI subcommands) |
| `ws bootstrap [--profile mac\|arch\|server] [--yes]` | Full machine setup: packages, OS hooks, links, templates, mise trust, 1Password gate, zones, restores, theme |
| `ws update` | Parallel workstation update (`git pull`, missing packages, OS upgrades/cleanup, `rustup`/`tldr`/`gh-ext`/`mise`, `ws apply`, rebuild) |
| `ws apply` / `ws diff` / `ws undo` | Render theme templates, sync symlinks, preview diffs, or roll back via `~/.ws/ledger.json` |
| `ws doctor [--fix]` | Audit symlinks, templates, PATH, backups, and zones |
| `ws gh [owner/repo]` | Interactive PRs & Issues dashboard for the current repo (`--all` across repos) |
| `ws pkg search\|info\|install\|remove` | Search `brew`/`cask`/`paru`/`pacman`/`mise`/`apt` repos, inspect metadata, and install/remove packages (`i`/`rm` aliases) |
| `ws pkg add\|remove --keep-installed` | Declaratively install+track or untrack packages in `pkg/*.txt` |
| `ws pkg list [--missing\|--untracked]` | List preset, missing, or untracked packages (`ws pkg sync\|outdated` to install/audit) |
| `ws theme [id\|edit]` / `ws preset` | Switch/edit color palettes or inspect/select declarative presets in `presets.toml` |
| `ws mise` / `ws env` / `ws hist` | Inspect/upgrade mise configs & tools, trace env provenance & secrets, fuzzy-pick shell history |
| `ws fleet` | Tailscale node inventory, file diff/sync (`diff`/`sync-files`), and headless VM sync |
| `ws zone` / `ws whoami` | Inspect, apply, migrate, or run commands in directory-scoped identity zones |
| `ws vault` / `ws db` | 1Password backups (`secrets`, `ssh`, `files`, `envfiles` `pull`/`push`) and encrypted `~/.ws/ws.db` |
| `ws clean` / `ws config` | Tiered disk cache cleanup and persistent `~/.ws/config.toml` settings |

Repo tooling (`ws:install`, `check`) and thin shims that exec `ws` live under `.mise/tasks/` (`mise tasks` / `mise run <task>`). Executables under `.mise/tasks/local/` or `ws-<name>` on `PATH` act as `ws` plugins.

## How it works

- **Symlinks, not copies** — `links/` maps repo paths into `$HOME`; the repo is the live source of truth and `git diff` shows drift. `XDG_CONFIG_HOME` & co. come from `~/.zshenv`, so macOS uses the Linux layout.
- **Presets & catalog** — `presets.toml` and `tools.toml` declare composable OS presets and cross-distro package remaps on top of `pkg/*.txt`.
- **Theming** — one `colors-template.*` per terminal app, static palettes in `themes/`, rendered by `ws theme <id>` or `mise run theme`.
- **Packages** — `paru` (Arch) / `brew` (macOS) / `mise`, rolling; package lists live in `pkg/`.
- **Unified state (`~/.ws/`)** — stores encrypted `ws.db`, `config.toml`, active `theme`, `ledger.json`, and caches (`WS_HOME` to override).
- **Identity zones** — `~/work` and `~/personal` each carry a `mise.toml` whose `[env]` applies to everything below, so git, `gh`, `aws`, `kubectl`, Claude Code, and Codex identity follow the working directory.

## Identity zones

```
~/work/       mise.toml        real file, seeded from config/identity/
  code/…      mise.local.toml  generated from WS_ZONES
              .gitconfig       generated from WS_ZONES
~/personal/   … same
```

Each zone config points `GH_CONFIG_DIR`, `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE`, `KUBECONFIG`, and `WS_CODE_ROOT` at zone-specific paths; `~/.config/git/config` includes the zone's `.gitconfig` via `includeIf gitdir:`. `CLAUDE_CONFIG_DIR` also namespaces Claude Code's macOS Keychain entry so both zones stay logged in at once, while `config/identity/work.mise.toml` sets `CODEX_HOME=~/.config/codex-work` (personal defaults to `~/.codex`).

| Command | Purpose |
| --- | --- |
| `ws zone apply` (`mise run identity`) | create/refresh zones, seed + trust zone config, write git identity |
| `ws zone status` (`mise run identity --status`) | show what each zone resolves to |
| `ws whoami` | show active zone, git, AWS, `gh`, `kube`, Claude, and Codex directories |
| `ws zone migrate <zone> [source] [--execute]` | preview or move existing dirs into a zone |
| `ws zone run --zone <zone> -- <cmd...>` | run a command with a zone's environment overlaid |
| `ws repo clone <src> --zone work` | clone or act with another zone's identity |

Launch GUI editors from a shell already inside the zone — GUI launchers inherit no zone env.

## Private layer

Secrets stay in 1Password (`op` CLI). Two gitignored files carry everything machine-private: `mise.local.toml` (env vars) and `.mise/tasks/local/` (`local:*` tasks). `ws vault secrets pull --execute` / `push --execute` (or `mise run secrets:pull` / `secrets:push`) round-trip both; `ws vault ssh`, `files`, and `envfiles` do the same for machine state (preview by default without `--execute`).

`mise.local.toml` defines `WS_ZONES`, `WS_FORGES`, `WS_OP_FILES`, `WS_OP_VAULT`, and `WS_ANDROID_AVDS`.

`mise run android` (`ws android`) installs the SDK packages in `pkg/android.txt`, downloading and checksum-verifying Android cmdline-tools when `sdkmanager` is missing, and creates the AVDs in `WS_ANDROID_AVDS` (`name|device|image` per line). Accepting the SDK licenses needs a TTY prompt, `--yes`, or `--allow all`; `-n` previews.

## Layout

| Path | Purpose |
| --- | --- |
| `ws/` | Go CLI and Bubble Tea v2 TUI source (`ws/cmd/ws`, `ws/internal/*`) |
| `presets.toml` / `tools.toml` | Declarative OS presets and cross-distro tool catalog |
| `.mise/tasks/` | `ws:install`, lint (`check`), and thin `ws` task shims |
| `config/<app>/` | Dotfiles + `colors-template.*` |
| `config/identity/` | Per-zone `mise.toml` env seeds |
| `themes/<name>/` | Static color palettes (dark/light) |
| `links/` | Symlink manifests per profile |
| `pkg/` | Package lists (`mac-brew.txt`, `mac-cask.txt`, `arch.txt`, `server.txt`, ...) |
| `os/` | OS-specific hooks invoked by `ws` (`presets.toml` `os_hooks`) |
| `lib/` | Shared bash helpers for `os/` hooks and `ws:install` |
