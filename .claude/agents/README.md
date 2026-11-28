# Audit agents

Two read-only reviewers, tailored to what this repo is: the `ws/` Go CLI/TUI +
`bootstrap.sh` + `os/` hooks + thin `.mise/tasks/` shims + a symlink manifest +
presets/catalog + a theming system. Ask for them by name (they run well in
parallel); each reports findings and never edits.

- `audit-shell.md` — script safety and portability: unsafe patterns, sudo in
  the wrong layer, idempotence violations, mac/linux (BSD vs GNU) breakage,
  interactive paths with no non-interactive escape.
- `audit-consistency.md` — cross-layer drift: manifest ↔ `config/`, template
  tokens ↔ palette completeness (`themes/README.md` / `ws/internal/config/themes.go`),
  renderer registry ↔ `.gitignore`, `presets.toml` / `tools.toml` / scripts ↔
  `pkg/` lists per profile, docs ↔ reality.

Both are also wired into `opencode.json`, which is what makes them invokable
under opencode (it has no `.claude/agents/` support of its own).
