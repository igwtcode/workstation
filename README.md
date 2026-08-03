# workstation

Bootstrap, dotfiles, theming and updates for macOS, Arch Linux, and a
minimal server profile for any distro.

## Quickstart

```sh
bash -c "$(curl -fsSL https://raw.githubusercontent.com/igwtcode/workstation/main/bootstrap.sh)"
```

Detects the OS (`--profile mac|arch|server` to override), installs git +
mise, clones to `~/personal/code/igwtcode/workstation` (`WS_DIR` to
override), then runs `mise run bootstrap`. Re-running is safe. `--yes` skips
confirmations.

`mise tasks` lists every entry point.

## How it works

- **Symlinks, not copies** — `links/` maps repo paths into `$HOME`; the repo
  is the live source of truth and `git diff` shows drift. `XDG_CONFIG_HOME`
  & co. come from `~/.zshenv`, so macOS uses the linux layout.
- **Theming** — one `colors-template.*` per terminal app, static palettes in
  `themes/`, rendered by `mise run theme`. GUI apps use their own themes.
- **Packages** — paru (arch) / brew (mac), rolling. Lists in `pkg/`.
- **Identity zones** — `~/work` and `~/personal` each carry a `mise.toml`
  whose `[env]` applies to everything below, so git, gh, aws, kube and
  Claude Code identity follow the working directory.

## Identity zones

```
~/work/       mise.toml        real file, seeded from config/identity/
  code/…      mise.local.toml  generated from WS_ZONES
              .gitconfig       generated from WS_ZONES
~/personal/   … same
```

Each zone config points `GH_CONFIG_DIR`, `CLAUDE_CONFIG_DIR`,
`AWS_CONFIG_FILE`, `AWS_SHARED_CREDENTIALS_FILE`, `KUBECONFIG` and
`WS_CODE_ROOT` at zone-specific paths; `~/.config/git/config` includes the
zone's `.gitconfig` via `includeIf gitdir:`. `CLAUDE_CONFIG_DIR` also
namespaces Claude Code's macOS Keychain entry, so both zones stay logged in
at once.

| Command | Purpose |
| --- | --- |
| `mise run identity` | create/refresh zones, seed + trust the zone config, write the git identity |
| `mise run identity --status` | what each zone resolves to |
| `mise run identity:migrate <zone> [source]` | move existing dirs into a zone |
| `mise run clone --zone work …` | act with another zone's identity |

Launch editors from a shell already inside the zone — GUI launchers inherit
no zone env.

## Private layer

Secrets stay in 1Password (`op` CLI). Two gitignored files carry everything
machine-private: `mise.local.toml` (env vars) and `.mise/tasks/local/`
(`local:*` tasks). `mise run secrets:push` / `secrets:pull` round-trip both;
`op:ssh`, `op:files` and `op:envfiles` do the same for machine state.

`mise.local.toml` defines `WS_ZONES`, `WS_FORGES`, `WS_OP_FILES` and
`WS_OP_VAULT`. Their formats are documented in the file itself, which is
restored from 1Password on a new machine.

## Layout

| Path | Purpose |
| --- | --- |
| `.mise/tasks/` | all entry points |
| `config/<app>/` | dotfiles + `colors-template.*` |
| `config/identity/` | per-zone mise env seeds |
| `themes/<name>/` | palettes, dark/light |
| `links/` | symlink manifest per profile |
| `pkg/` | package lists |
| `os/` | OS-specific setup |
| `lib/` | shared bash library |
