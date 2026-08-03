# Themes — palette schema and authoring rules

Static color palettes for the terminal-scope theme system. `mise run theme`
renders every template listed in [`registry`](registry) against one palette
and runs the template's post-hook. See `CLAUDE.md` § Architecture.

## Layout

```
themes/<name>/dark.toml    # one palette per mode
themes/<name>/light.toml   # only where upstream defines a light variant
```

The theme id is `<name>-<mode>` (e.g. `gruvbox-dark`) — `<name>` must not
contain `-`.

## Palette file format

Flat TOML, trivially parseable from bash — every line is either a `#`
comment, blank, or `key = "value"` (double quotes, no tables, no arrays).
Color values are lowercase `#rrggbb`.

## Token vocabulary (the contract)

Every palette must define **all** of these keys; every template may use
**only** these (plus the derived `_strip` forms). Adding a token to a
template means adding it to `WS_THEME_TOKENS` in `lib/theme.sh` and to
every palette file in the same change.

| Token | Meaning |
| --- | --- |
| `name` | theme id, `<name>-<mode>` |
| `mode` | `dark` or `light` |
| `bg` / `bg_alt` | default background / secondary background (statusline-ish) |
| `fg` / `fg_dim` | default text / dimmed text (comments, placeholders) |
| `accent` | the theme's signature highlight color |
| `selection_bg` / `selection_fg` | selected-text colors |
| `cursor` | terminal cursor color |
| `border` | subtle UI border/separator |
| `color0`..`color15` | the ANSI 16, using the upstream terminal mapping |

## Template token formats

Templates use plain `{{token}}` substitution — **no filters, pipes, or
logic** (the renderer supports none, by design):

- `{{bg}}` → `#282828`
- `{{bg_strip}}` → `282828` (derived automatically for every `#rrggbb`
  value; for configs that want bare hex)

## Authoring rules

- **Published upstream hexes only** — never guessed, blended, or derived
  colors. Link the exact upstream source file(s) in the palette header.
- Where upstream ships its own terminal port (kitty conf, color table,
  vim `g:terminal_color_*`), use its ANSI mapping verbatim.
- Semantic tokens are a documented *slot mapping* onto published colors —
  note non-obvious choices in the header comment (aliasing is fine; e.g.
  `fg_dim` may equal `color8`).
- Dark + light palettes only where upstream defines both.
