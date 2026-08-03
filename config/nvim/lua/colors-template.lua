-- nvim colors for the active theme — rendered by `mise run theme` into
-- lua/colors.lua; edit this template only. Tokens follow themes/README.md;
-- base16 slots use the standard semantic mapping onto the ANSI palette.

local M = {}

function M.setup()
  require("base16-colorscheme").setup({
    -- Background tones (base00 transparent: terminal bg shows through)
    base00 = "NONE", -- Default Background
    base01 = "{{bg_alt}}", -- Lighter Background (status bars)
    base02 = "{{selection_bg}}", -- Selection Background
    base03 = "{{fg_dim}}", -- Comments, Invisibles
    -- Foreground tones
    base04 = "{{fg_dim}}", -- Dark Foreground (status bars)
    base05 = "{{fg}}", -- Default Foreground
    base06 = "{{fg}}", -- Light Foreground
    base07 = "{{fg}}", -- Lightest Foreground
    -- Accent colors
    base08 = "{{color1}}", -- Variables, XML Tags, Errors
    base09 = "{{accent}}", -- Integers, Constants
    base0A = "{{color3}}", -- Classes, Search Background
    base0B = "{{color2}}", -- Strings, Diff Inserted
    base0C = "{{color6}}", -- Regex, Escape Chars
    base0D = "{{color4}}", -- Functions, Methods
    base0E = "{{color5}}", -- Keywords, Storage
    base0F = "{{fg_dim}}", -- Deprecated, Embedded Tags
  })

  local hl = vim.api.nvim_set_hl

  -- Solid background for floating/popup windows (Oil, LSP hover, etc.)
  local solid_bg = "{{bg_alt}}"
  hl(0, "NormalFloat", { bg = solid_bg, fg = "{{fg}}" })
  hl(0, "FloatBorder", { bg = solid_bg, fg = "{{border}}" })
  hl(0, "FloatTitle", { bg = solid_bg, fg = "{{accent}}" })
  hl(0, "MyHighlight", { bg = solid_bg, fg = "{{fg}}" })

  -- Transparent background for sidebar/tree panels (neo-tree)
  hl(0, "NeoTreeNormal", { bg = "NONE", fg = "{{fg}}" })
  hl(0, "NeoTreeNormalNC", { bg = "NONE", fg = "{{fg}}" })
  hl(0, "NeoTreeEndOfBuffer", { bg = "NONE", fg = "NONE" })
  hl(0, "NeoTreeWinSeparator", { bg = "NONE", fg = "{{bg_alt}}" })

  -- Statusline base highlights
  hl(0, "StatusLine", { bg = "NONE", fg = "{{fg}}" })
  hl(0, "StatusLineNC", { bg = "NONE", fg = "{{fg_dim}}" })
end

function M.lualine_theme()
  local colors = {
    bg = "{{bg_alt}}",
    fg = "{{fg}}",
    fg_dim = "{{fg_dim}}",
    on_mode = "{{bg}}",
    normal = "{{accent}}",
    insert = "{{color2}}",
    visual = "{{color5}}",
    replace = "{{color1}}",
    command = "{{color3}}",
  }

  local function mode(color)
    return {
      a = { bg = color, fg = colors.on_mode, gui = "bold" },
      b = { bg = colors.bg, fg = colors.fg },
      c = { bg = "NONE", fg = colors.fg_dim },
    }
  end

  return {
    normal = mode(colors.normal),
    insert = mode(colors.insert),
    visual = mode(colors.visual),
    replace = mode(colors.replace),
    command = mode(colors.command),
    inactive = {
      a = { bg = "NONE", fg = colors.fg_dim },
      b = { bg = "NONE", fg = colors.fg_dim },
      c = { bg = "NONE", fg = colors.fg_dim },
    },
  }
end

-- Reload colors on SIGUSR1 (the theme switcher's post-hook signals nvim)
local signal = vim.uv.new_signal()
signal:start(
  "sigusr1",
  vim.schedule_wrap(function()
    package.loaded["colors"] = nil
    require("colors").setup()
    local ok, lualine = pcall(require, "lualine")
    if ok then
      lualine.setup({ options = { theme = require("colors").lualine_theme() } })
    end
  end)
)

return M
