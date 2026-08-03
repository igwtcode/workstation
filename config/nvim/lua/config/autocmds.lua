-- Check if the file is an aws CloudFormation template
local function is_cloudformation_template(filepath)
  local lines = {}
  local file = io.open(filepath, "r")
  if file then
    for i = 1, 20 do
      local line = file:read("*line")
      if not line then
        break
      end
      table.insert(lines, line)
    end
    file:close()
  end

  for _, line in ipairs(lines) do
    if line:match("^AWSTemplateFormatVersion") or line:match("^ *Resources:") then
      return true
    end
  end
  return false
end

-- Check if the file is an aws Serverless Framework template
local function is_serverless_template(filepath)
  local lines = {}
  local file = io.open(filepath, "r")
  if file then
    for i = 1, 20 do
      local line = file:read("*line")
      if not line then
        break
      end
      table.insert(lines, line)
    end
    file:close()
  end

  for _, line in ipairs(lines) do
    if line:match("^Transform: .*::Serverless.*") then
      return true
    end
  end
  return false
end

-- Set the filetype based on the detected template
-- Detects Serverless Framework and CloudFormation templates (yaml)
local function set_aws_cloudformation_fileTypes(event)
  local filepath = vim.api.nvim_buf_get_name(event.buf)
  if is_serverless_template(filepath) then
    vim.bo[event.buf].filetype = "yaml.sam"
  elseif is_cloudformation_template(filepath) then
    vim.bo[event.buf].filetype = "yaml.cfn"
  end
end

-- Set the yamlls config based on the detected template
local function set_aws_cloudformation_schemas(event)
  local client = vim.lsp.get_client_by_id(event.data.client_id)

  if not client or client.name ~= "yamlls" then
    return
  end

  local yamlls_config = client.config.settings.yaml
  local filepath = vim.api.nvim_buf_get_name(event.buf)
  local filetype = vim.bo[event.buf].filetype
  local is_cfn = filetype == "yaml.cfn"
  local is_sam = filetype == "yaml.sam"

  if is_sam then
    yamlls_config.schemas = {
      ["https://raw.githubusercontent.com/aws/serverless-application-model/main/samtranslator/schema/schema.json"] = {
        filepath,
      },
    }
  elseif is_cfn then
    yamlls_config.schemas = {
      ["https://raw.githubusercontent.com/awslabs/goformation/master/schema/cloudformation.schema.json"] = {
        filepath,
      },
    }
  end

  if is_cfn or is_sam then
    yamlls_config.customTags = {
      "!And scalar",
      "!And mapping",
      "!And sequence",
      "!If scalar",
      "!If mapping",
      "!If sequence",
      "!Not scalar",
      "!Not mapping",
      "!Not sequence",
      "!Equals scalar",
      "!Equals mapping",
      "!Equals sequence",
      "!Or scalar",
      "!Or mapping",
      "!Or sequence",
      "!FindInMap scalar",
      "!FindInMap mappping",
      "!FindInMap sequence",
      "!Base64 scalar",
      "!Base64 mapping",
      "!Base64 sequence",
      "!Cidr scalar",
      "!Cidr mapping",
      "!Cidr sequence",
      "!Ref scalar",
      "!Ref mapping",
      "!Ref sequence",
      "!Sub scalar",
      "!Sub mapping",
      "!Sub sequence",
      "!GetAtt scalar",
      "!GetAtt mapping",
      "!GetAtt sequence",
      "!GetAZs scalar",
      "!GetAZs mapping",
      "!GetAZs sequence",
      "!ImportValue scalar",
      "!ImportValue mapping",
      "!ImportValue sequence",
      "!Select scalar",
      "!Select mapping",
      "!Select sequence",
      "!Split scalar",
      "!Split mapping",
      "!Split sequence",
      "!Join scalar",
      "!Join mapping",
      "!Join sequence",
      "!Condition scalar",
      "!Condition mapping",
      "!Condition sequence",
    }
    client.notify("workspace/didChangeConfiguration", { settings = yamlls_config })
  end
end

vim.api.nvim_create_autocmd("LspAttach", {
  pattern = { "*.yaml", "*.yml" },
  callback = set_aws_cloudformation_schemas,
})

vim.api.nvim_create_autocmd({ "BufRead", "BufNewFile" }, {
  pattern = { "*.yaml", "*.yml" },
  callback = set_aws_cloudformation_fileTypes,
})

-- https://github.com/hashicorp/terraform-ls/blob/main/docs/USAGE.md
-- expects a terraform filetype and not a tf filetype
vim.api.nvim_create_autocmd({ "BufRead", "BufNewFile" }, {
  pattern = { "*.tf" },
  callback = function()
    vim.api.nvim_command("set filetype=terraform")
  end,
  desc = "detect terraform filetype",
})

vim.api.nvim_create_autocmd({ "BufRead", "BufNewFile" }, {
  pattern = "terraform-vars",
  callback = function()
    vim.api.nvim_command("set filetype=hcl")
  end,
  desc = "detect terraform vars",
})

vim.api.nvim_create_autocmd({ "BufRead", "BufNewFile" }, {
  group = vim.api.nvim_create_augroup("FixTerraformCommentString", { clear = true }),
  callback = function(ev)
    vim.bo[ev.buf].commentstring = "# %s"
  end,
  pattern = { "*tf", "*.hcl" },
  desc = "fix terraform and hcl comment string",
})

-- copy file path to clipboard
vim.api.nvim_create_user_command("CopyFilePathToClipboard", function()
  vim.fn.setreg("+", vim.fn.expand("%:p"))
end, {})

vim.api.nvim_create_autocmd({ "FileType" }, {
  group = vim.api.nvim_create_augroup("edit_raw_text", { clear = true }),
  pattern = { "gitcommit", "markdown", "text" },
  callback = function()
    vim.opt_local.wrap = true
    vim.opt_local.spell = false
    vim.opt_local.conceallevel = 0
    -- vim.opt_local.spelllang = "en_us"
    -- vim.opt_local.shiftwidth = 2
    -- vim.opt_local.softtabstop = 2
    -- vim.opt_local.expandtab = true
  end,
})
