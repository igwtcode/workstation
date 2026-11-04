return {
  "neovim/nvim-lspconfig",
  init = function()
    -- deferred underlines skip clamping and crash on out-of-range lines; re-show once loaded instead
    local underline = vim.diagnostic.handlers.underline
    local show = underline.show
    underline.show = function(ns, bufnr, diagnostics, opts)
      if vim.api.nvim_buf_is_loaded(bufnr) then
        return show(ns, bufnr, diagnostics, opts)
      end
      vim.api.nvim_create_autocmd("BufRead", {
        buffer = bufnr,
        once = true,
        callback = function()
          vim.schedule(function()
            if vim.api.nvim_buf_is_loaded(bufnr) then
              vim.diagnostic.show(ns, bufnr)
            end
          end)
        end,
      })
    end
  end,
  opts = {
    diagnostics = {
      float = {
        border = "rounded",
      },
    },
    inlay_hints = { enabled = false },
    servers = {
      vtsls = {
        settings = {
          typescript = {
            preferences = {
              includeCompletionsForModuleExports = true,
              includeCompletionsForImportStatements = true,
              importModuleSpecifier = "non-relative",
            },
          },
        },
      },
      terraformls = {
        on_attach = function() end,
      },
      yamlls = {
        filetypes = { "yaml", "yaml.ansible", "yaml.github", "yaml.cfn", "yaml.sam" },
      },
      gopls = {
        settings = {
          gopls = {
            usePlaceholders = false,
          },
        },
      },
    },
  },
}
