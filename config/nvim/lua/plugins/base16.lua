return {
  "RRethy/base16-nvim",
  config = function()
    -- colors.lua is theme-rendered; missing before the first `mise run theme`
    local ok, colors = pcall(require, "colors")
    if ok then
      colors.setup()
    end
  end,
}
