return {
  "stevearc/conform.nvim",
  opts = function()
    ---@type conform.setupOpts
    local opts = {
      default_format_opts = {
        timeout_ms = 3000,
        async = false, -- not recommended to change
        quiet = false, -- not recommended to change
        lsp_format = "fallback", -- not recommended to change
      },
      formatters_by_ft = {
        ["css"] = { "biome", "prettier", stop_after_first = true },
        ["javascript"] = { "biome", "prettier", stop_after_first = true },
        ["javascriptreact"] = { "biome", "prettier", stop_after_first = true },
        ["json"] = { "biome", "prettier", stop_after_first = true },
        ["jsonc"] = { "biome", "prettier", stop_after_first = true },
        lua = { "stylua" },
        ["markdown"] = { "prettier", stop_after_first = true },
        ["md"] = { "prettier", stop_after_first = true },
        sh = { "shfmt" },
        terraform = { "terraform_fmt" },
        ["terraform-vars"] = { "terraform_fmt" },
        tf = { "terraform_fmt" },
        ["typescript"] = { "biome", "prettier", stop_after_first = true },
        ["typescriptreact"] = { "biome", "prettier", stop_after_first = true },
        yaml = { "biome", "prettier" },
        yml = { "biome", "prettier" },
        go = { "goimports", "gofumpt", "gofmt" },
        --kdl = { "kdlfmt" },
      },
      -- The options you set here will be merged with the builtin formatters.
      -- You can also define any custom formatters here.
      ---@type table<string, conform.FormatterConfigOverride|fun(bufnr: integer): nil|conform.FormatterConfigOverride>
      formatters = {
        biome = {
          command = "biome",
          args = {
            "check",
            "--formatter-enabled=true",
            "--linter-enabled=true",
            "--enforce-assist=true",
            "--write",
            "--stdin-file-path",
            "$FILENAME",
          },
        },
      },
    }
    return opts
  end,
}
