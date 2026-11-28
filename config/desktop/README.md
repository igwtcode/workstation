# Desktop parity reference

The single source both compositor configs mirror — `config/niri/config.kdl`
and `config/hypr/hyprland.lua`. The formats can't share literal files (KDL
vs Lua), so this table is the contract: **change behavior here first, then
mirror it in both configs** (the parity rule in `CLAUDE.md` § Architecture).

Machine-local monitor layout lives outside git in each config's optional
local file: `config/niri/local.kdl`, `config/hypr/local.lua`.

## Default apps & autostart

| Role | Value |
| --- | --- |
| Terminal | `ghostty` |
| Browser | `firefox` |
| File manager | `nautilus` |
| Shell (bar/launcher/osd) | picked at login — noctalia or DMS. The session entry (`os/arch/desktop/sessions/`) sets `WS_SHELL`; both configs spawn `ws-session-shell` and drive the shell via `ws-shell-ipc` neutral verbs (dispatchers installed to `/usr/local/bin` by `os/arch/desktop/setup.sh`) |
| Autostart | shell (via `ws-session-shell`), polkit agent, terminal (+ compositor-specific plumbing: xwayland-satellite & portal-gnome on niri only) |

## Keymap (Mod = Super)

| Keys | Action |
| --- | --- |
| `Mod+Return` | terminal |
| `Mod+Space` | launcher (shell IPC) |
| `Mod+B` | browser |
| `Mod+E` | file manager |
| `Mod+Q` | close window |
| `Mod+S` | control center (shell IPC) |
| `Mod+Comma` | shell settings (shell IPC) |
| `Mod+Ctrl+Q` | lock session (shell IPC) |
| `XF86Audio*`, `XF86MonBrightness*` | volume/media/brightness via shell IPC |
| `Mod+Shift+1/2/3` | screenshot region / screen / window |
| `Mod+H/J/K/L`, `Mod+Arrows` | focus window/column in direction |
| `Mod+Ctrl+H/J/K/L`, `Mod+Ctrl+Arrows` | move window/column in direction |
| `Mod+Shift+Arrows` | focus monitor in direction |
| `Mod+Shift+Ctrl+Arrows` | move window/column to monitor in direction |
| `Mod+1..9` | focus workspace N |
| `Mod+Ctrl+1..9` | move window/column to workspace N |
| `Mod+Tab` | previous workspace |
| `Mod+Minus/Equal` | shrink/grow window horizontally |
| `Mod+Shift+Minus/Equal` | shrink/grow window vertically |
| `Mod+F` | maximize |
| `Mod+Ctrl+F` | fullscreen |
| `Mod+T` | toggle floating |
| `Ctrl+Alt+Delete` | quit compositor |

Compositor-specific extras (no counterpart expected): niri column and
layout handling (`Mod+R`, `Mod+C`/`Mod+Ctrl+C`, `Mod+W`, `Mod+O` overview,
`Mod+[`/`Mod+]` consume/expel, `Mod+(Ctrl+)Home/End`,
`Mod+(Ctrl+)Page_Up/Down`, `Mod+Shift+Escape`, `Mod+Shift+P`,
`Mod+Shift+Ctrl+T`); hyprland mouse move/resize (`Mod+LMB`/`Mod+RMB`) and a
3-finger workspace swipe — keep them from colliding with the shared table
above.

## Shared behavior

- Workspaces: 9, numbered; windows open tiled; 6px gaps between windows,
  none at screen edges; no borders/rounding
  (shell provides visual focus cues).
- Keyboard: `us,de` layouts, repeat rate 60, repeat delay 240.
- Touchpad: tap-to-click, natural scroll; focus follows mouse.
- Cursor: capitaine-cursors, size 36, hide while typing / after 3s idle.
- Screenshot tooling: built-in on niri; `hyprshot` on hyprland (pkg list
  must carry it for the arch profile).
- Privacy: telegram/keepassxc/secrets/1password windows and noctalia
  notifications blocked from screen capture on both.
- Focus: Playwright's browsers (`chromium-browser`, `firefox-default`) open
  without taking focus; activation requests follow each compositor's
  default (niri honors only valid activation tokens, hyprland leaves
  `focus_on_activate` off).
