-- Herdr forwards bare ctrl+h/j/k/l into nvim when it detects nvim in the
-- foreground; this handles the other half, crossing into herdr's panes once
-- a move hits the edge of vim's own window layout.
local M = {}

local directions = { h = "left", j = "down", k = "up", l = "right" }

function M.navigate(key)
	local winnr = vim.fn.winnr()
	pcall(vim.cmd, "wincmd " .. key)
	if vim.fn.winnr() == winnr then
		vim.system({ "herdr", "pane", "focus", "--current", "--direction", directions[key] }, { detach = true })
	end
end

return M
