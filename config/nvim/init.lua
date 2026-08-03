-- bootstrap lazy.nvim, LazyVim and your plugins
require("config.lazy")
-- colors.lua is rendered by `mise run theme` — absent until the first
-- render on a fresh machine, and nvim should still start
pcall(require, "colors")
