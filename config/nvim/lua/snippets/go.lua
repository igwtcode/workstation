local ls = require("luasnip")

ls.add_snippets("go", {
  ls.snippet("def", {
    ls.text_node("defer func() {"),
    ls.insert_node(1), -- Cursor position
    ls.text_node({ "", "}()" }), -- Closing brackets
  }),
})
