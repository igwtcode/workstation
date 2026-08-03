# Audit agents

Two read-only reviewers, tailored to what this repo is: machine-mutating
bash + a symlink manifest + a theming system. Ask for them by name (they run
well in parallel); each reports findings and never edits.

- `audit-shell.md` — script safety and portability: unsafe patterns, sudo in
  the wrong layer, idempotence violations, mac/linux (BSD vs GNU) breakage,
  interactive paths with no non-interactive escape.
- `audit-consistency.md` — cross-layer drift: manifest ↔ `config/`, template
  tokens ↔ palette completeness, renderer registry ↔ `.gitignore`, scripts'
  tool usage ↔ pkg lists per profile, docs ↔ reality.

Both are also wired into `opencode.json`, which is what makes them invokable
under opencode (it has no `.claude/agents/` support of its own).
