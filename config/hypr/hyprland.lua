-- hyprland config (Lua — the config format since 0.55) — keybindings,
-- default apps, workspace/monitor behavior and autostart mirror
-- config/desktop/README.md (parity with config/niri). Authored fresh
-- against the 0.55 Lua API; not ported from the old hyprlang configs.
--
-- Machine-local pieces (monitor modes/positions) live in ./local.lua —
-- gitignored, optional.

------------------------
---- DEFAULT APPS ------
------------------------

local terminal = "ghostty"
local browser = "firefox"
local fileManager = "nautilus"

-- desktop shell IPC — ws-shell-ipc (installed by os/arch/desktop) maps
-- each verb to the running shell's own IPC (same verbs as the niri config)
local function shell_ipc(verb)
  return "ws-shell-ipc " .. verb
end

------------------------
---- AUTOSTART ---------
------------------------

hl.on("hyprland.start", function()
  -- shell picked at login (noctalia or DMS) via WS_SHELL, see os/arch/desktop
  hl.exec_cmd("ws-session-shell")
  hl.exec_cmd("/usr/lib/polkit-gnome/polkit-gnome-authentication-agent-1")
  hl.exec_cmd(terminal)
end)

------------------------
---- ENVIRONMENT -------
------------------------

hl.env("QT_QPA_PLATFORMTHEME", "qt6ct")
hl.env("XCURSOR_THEME", "capitaine-cursors")
hl.env("XCURSOR_SIZE", "36")
hl.env("HYPRCURSOR_SIZE", "36")

------------------------
---- LOOK AND FEEL -----
------------------------

-- No gaps, borders, rounding, shadows or blur — matches the niri layout
hl.config({
  general = {
    gaps_in = 0,
    gaps_out = 0,
    border_size = 0,
    resize_on_border = false,
    allow_tearing = false,
    layout = "dwindle",
  },

  decoration = {
    rounding = 0,
    shadow = { enabled = false },
    blur = { enabled = false },
  },

  dwindle = {
    preserve_split = true,
  },

  cursor = {
    hide_on_key_press = true,
    inactive_timeout = 3,
  },

  misc = {
    disable_hyprland_logo = true,
    force_default_wallpaper = 0, -- the desktop shell draws the wallpaper
  },
})

------------------------
---- INPUT -------------
------------------------

hl.config({
  input = {
    kb_layout = "us,de",
    repeat_rate = 60,
    repeat_delay = 240,

    follow_mouse = 1,
    sensitivity = 0,

    touchpad = {
      natural_scroll = true,
      tap_to_click = true,
    },
  },
})

hl.gesture({
  fingers = 3,
  direction = "horizontal",
  action = "workspace",
})

------------------------
---- KEYBINDINGS -------
------------------------

local mod = "SUPER"

-- Applications
hl.bind(mod .. " + Return", hl.dsp.exec_cmd(terminal))
hl.bind(mod .. " + Space", hl.dsp.exec_cmd(shell_ipc("launcher")))
hl.bind(mod .. " + B", hl.dsp.exec_cmd(browser))
hl.bind(mod .. " + E", hl.dsp.exec_cmd(fileManager))
hl.bind(mod .. " + Q", hl.dsp.window.close())

-- Desktop shell (ws-shell-ipc dispatches to the running shell)
hl.bind(mod .. " + S", hl.dsp.exec_cmd(shell_ipc("control-center")))
hl.bind(mod .. " + comma", hl.dsp.exec_cmd(shell_ipc("settings")))
hl.bind(mod .. " + CTRL + Q", hl.dsp.exec_cmd(shell_ipc("lock")))

-- Media keys
hl.bind("XF86AudioRaiseVolume", hl.dsp.exec_cmd(shell_ipc("volume-up")), { locked = true, repeating = true })
hl.bind("XF86AudioLowerVolume", hl.dsp.exec_cmd(shell_ipc("volume-down")), { locked = true, repeating = true })
hl.bind("XF86AudioMute", hl.dsp.exec_cmd(shell_ipc("volume-mute")), { locked = true })
hl.bind("XF86AudioMicMute", hl.dsp.exec_cmd(shell_ipc("mic-mute")), { locked = true })
hl.bind("XF86AudioNext", hl.dsp.exec_cmd(shell_ipc("media-next")), { locked = true })
hl.bind("XF86AudioPrev", hl.dsp.exec_cmd(shell_ipc("media-prev")), { locked = true })
hl.bind("XF86AudioPlay", hl.dsp.exec_cmd(shell_ipc("media-play-pause")), { locked = true })
hl.bind("XF86MonBrightnessUp", hl.dsp.exec_cmd(shell_ipc("brightness-up")), { locked = true, repeating = true })
hl.bind("XF86MonBrightnessDown", hl.dsp.exec_cmd(shell_ipc("brightness-down")), { locked = true, repeating = true })

-- Screenshots (hyprshot; niri uses its built-in equivalent)
hl.bind(mod .. " + SHIFT + 1", hl.dsp.exec_cmd("hyprshot -m region"))
hl.bind(mod .. " + SHIFT + 2", hl.dsp.exec_cmd("hyprshot -m output"))
hl.bind(mod .. " + SHIFT + 3", hl.dsp.exec_cmd("hyprshot -m window"))

-- Window focus
local focusDirs = { H = "left", J = "down", K = "up", L = "right", left = "left", down = "down", up = "up", right = "right" }
for key, dir in pairs(focusDirs) do
  hl.bind(mod .. " + " .. key, hl.dsp.focus({ direction = dir }))
  hl.bind(mod .. " + CTRL + " .. key, hl.dsp.window.move({ direction = dir }))
end

-- Focus monitor / move window to monitor
local monitorDirs = { left = "l", down = "d", up = "u", right = "r" }
for key, m in pairs(monitorDirs) do
  hl.bind(mod .. " + SHIFT + " .. key, hl.dsp.focus({ monitor = m }))
  hl.bind(mod .. " + SHIFT + CTRL + " .. key, hl.dsp.window.move({ monitor = m, follow = true }))
end

-- Workspaces
for i = 1, 9 do
  hl.bind(mod .. " + " .. i, hl.dsp.focus({ workspace = i }))
  hl.bind(mod .. " + CTRL + " .. i, hl.dsp.window.move({ workspace = i }))
end
hl.bind(mod .. " + Tab", hl.dsp.focus({ workspace = "previous" }))

-- Resize active window (niri: column width / window height)
hl.bind(mod .. " + minus", hl.dsp.window.resize({ x = -60, y = 0, relative = true }), { repeating = true })
hl.bind(mod .. " + equal", hl.dsp.window.resize({ x = 60, y = 0, relative = true }), { repeating = true })
hl.bind(mod .. " + SHIFT + minus", hl.dsp.window.resize({ x = 0, y = -60, relative = true }), { repeating = true })
hl.bind(mod .. " + SHIFT + equal", hl.dsp.window.resize({ x = 0, y = 60, relative = true }), { repeating = true })

-- Fullscreen, maximize & floating
hl.bind(mod .. " + F", hl.dsp.window.fullscreen({ mode = "maximized" }))
hl.bind(mod .. " + CTRL + F", hl.dsp.window.fullscreen({ mode = "fullscreen" }))
hl.bind(mod .. " + T", hl.dsp.window.float({ action = "toggle" }))

-- Move/resize with mouse
hl.bind(mod .. " + mouse:272", hl.dsp.window.drag(), { mouse = true })
hl.bind(mod .. " + mouse:273", hl.dsp.window.resize(), { mouse = true })

-- System
hl.bind("CTRL + ALT + Delete", hl.dsp.exit())

------------------------
---- RULES -------------
------------------------

hl.window_rule({
  name = "float-utilities",
  match = { class = "^(gnome-calculator|galculator|blueman-manager)$" },
  float = true,
})

hl.window_rule({
  name = "float-pip",
  match = { class = "firefox$", title = "^Picture-in-Picture$" },
  float = true,
})

hl.window_rule({
  name = "float-zoom",
  match = { class = "zoom" },
  float = true,
})

-- Privacy: keep credential/messaging windows out of screen shares
hl.window_rule({
  name = "no-screenshare-private",
  match = { class = "^(org\\.telegram\\.desktop|org\\.keepassxc\\.KeePassXC|org\\.gnome\\.World\\.Secrets|1password)$" },
  no_screen_share = true,
})

------------------------
---- MACHINE-LOCAL -----
------------------------

-- Monitor layout (gitignored; missing on a fresh machine is fine)
pcall(require, "local")
