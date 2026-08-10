vim.keymap.set("n", "<leader>o", "<cmd>Oil --float<CR>", { desc = "Explorer" })

vim.keymap.set("n", "<leader>z", "<cmd>ZenMode<CR>", { desc = "Toggle [Z]en mode" })

-- Center buffer while navigating
vim.keymap.set("n", "<C-u>", "<C-u>zz")
vim.keymap.set("n", "<C-d>", "<C-d>zz")
vim.keymap.set("n", "<C-i>", "<C-i>zz")
vim.keymap.set("n", "<C-o>", "<C-o>zz")
vim.keymap.set("n", "{", "{zz")
vim.keymap.set("n", "}", "}zz")
vim.keymap.set("n", "N", "Nzz")
vim.keymap.set("n", "n", "nzz")
vim.keymap.set("n", "G", "Gzz")
vim.keymap.set("n", "gg", "ggzz")
vim.keymap.set("n", "%", "%zz")
vim.keymap.set("n", "*", "*zz")
vim.keymap.set("n", "#", "#zz")

vim.keymap.set("x", "p", '"_dP', { noremap = true, silent = true })

if vim.env.HERDR_ENV then
	local herdr_nav = require("config.herdr-nav")
	vim.keymap.set("n", "<C-h>", function()
		herdr_nav.navigate("h")
	end, { noremap = true, silent = true })
	vim.keymap.set("n", "<C-j>", function()
		herdr_nav.navigate("j")
	end, { noremap = true, silent = true })
	vim.keymap.set("n", "<C-k>", function()
		herdr_nav.navigate("k")
	end, { noremap = true, silent = true })
	vim.keymap.set("n", "<C-l>", function()
		herdr_nav.navigate("l")
	end, { noremap = true, silent = true })
else
	vim.keymap.set("n", "<C-h>", ":NvimTmuxNavigateLeft<CR>", { noremap = true, silent = true })
	vim.keymap.set("n", "<C-j>", ":NvimTmuxNavigateDown<CR>", { noremap = true, silent = true })
	vim.keymap.set("n", "<C-k>", ":NvimTmuxNavigateUp<CR>", { noremap = true, silent = true })
	vim.keymap.set("n", "<C-l>", ":NvimTmuxNavigateRight<CR>", { noremap = true, silent = true })
end
