return {
  "neovim/nvim-lspconfig",
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
