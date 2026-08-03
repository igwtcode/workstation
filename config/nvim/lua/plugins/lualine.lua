return {
  "nvim-lualine/lualine.nvim",
  opts = function(_, opts)
    -- colors.lua is theme-rendered; missing before the first `mise run theme`
    local ok, colors = pcall(require, "colors")
    if ok then
      opts.options = opts.options or {}
      opts.options.theme = colors.lualine_theme()
    end
  end,
}
