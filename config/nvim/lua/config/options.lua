-- Options are automatically loaded before lazy.nvim startup
-- Default options that are always set: https://github.com/LazyVim/LazyVim/blob/main/lua/lazyvim/config/options.lua
-- Add any additional options here
-- Snacks animations
-- Set to `false` to globally disable all snacks animations
vim.g.snacks_animate = false

local opt = vim.opt
-- opt.relativenumber = false -- Relative line numbers
opt.list = false -- hide ">" symbol for tabs
opt.conceallevel = 0

-- LazyVim root dir detection
-- Each entry can be:
-- * the name of a detector function like `lsp` or `cwd`
-- * a pattern or array of patterns like `.git` or `lua`.
-- * a function with signature `function(buf) -> string|string[]`
-- vim.g.root_spec = { "lsp", { ".git", "lua" }, "cwd" }
vim.g.root_spec = { "cwd", { ".git", "lua" } }
-- vim.g.root_spec = { ".git", "cwd" }

---- Enable this option to avoid conflicts with Prettier.
vim.g.lazyvim_prettier_needs_config = true
vim.g.base16_transparent_background = 1
