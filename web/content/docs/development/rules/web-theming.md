# Web Theming

How Stella's web visual style is structured and how to swap it for a new one.

## Architecture

The visual style is fully described by two files; swapping a style touches **only** them:

| File                               | Role                                                                |
| ---------------------------------- | ------------------------------------------------------------------- |
| `web/src/tokens.css`               | All token values: font `@import`, `:root` (light), `.dark` (dark)   |
| [`web-design.md`](./web-design.md) | Visual direction, palette intent, motion rules, theme anti-patterns |

Everything else is invariant: `web/src/globals.css` (the `@theme inline` bridge and `@layer base`), CossUI components, and all feature code stay untouched. The landing page (`routes/index.css`) uses the same shadcn tokens directly.

The schema is shadcn's, end to end. External sources (designer design-system packages, tweakcn presets, hand-made palettes) are raw material to be **translated into** this schema — never adopted as a parallel schema with an adapter layer.

## Swap procedure

1. **Translate the source palette into `web/src/tokens.css`** using the shadcn schema. Keep both `:root` and `.dark` blocks — designer packages ship one mode; take the other mode's values from the package `DESIGN.md` color tables, or derive a faithful counterpart. Do not add project-specific color aliases; status colors use the existing `chart-*` tokens. Put the font `@import` on the first line.

2. **Rewrite [`web-design.md`](./web-design.md)** from the source's design doc, keeping the section skeleton (visual direction, color tables with Tailwind classes, typography, spacing & density, radius, elevation, motion, voice, theme anti-patterns). Translate values into semantic Tailwind classes so the doc matches what code actually uses.

3. **Verify:** `mise run build`, then screenshot light + dark across chat, settings, and the landing page. Check against the new `web-design.md`: accent frequency, surface depth levels, contrast. Run this token scan before review:

   ```bash
   rg -n --glob '!**/tokens.css' '#[0-9A-Fa-f]{3,8}\b|oklch\(|\b(?:bg|text|border|from|to|via)-(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-[0-9]{2,3}\b' web/src
   ```

## Designer package → shadcn token mapping

The designer skill's design-system packages (`~/.claude/skills/designer/references/design-systems/<name>/`) share one uniform schema. Translate as follows:

| designer token                                   | shadcn token(s)                                           |
| ------------------------------------------------ | --------------------------------------------------------- |
| `--bg`                                           | `--background`                                            |
| `--surface`                                      | `--card`, `--secondary`, `--sidebar`                      |
| elevated tier (see package notes)                | `--popover`, `--muted`                                    |
| `--fg`                                           | `--foreground` and all `*-foreground` surface pairs       |
| `--muted` (a text color!)                        | `--muted-foreground`                                      |
| `--border`                                       | `--border`, `--input`, `--sidebar-border`                 |
| `--accent` (brand color)                         | `--primary`, `--ring`, `--sidebar-primary`, `--chart-1`   |
| `--accent-on`                                    | `--primary-foreground`                                    |
| `--accent-hover`                                 | `--accent-foreground` (dark mode)                         |
| accent tint (derive)                             | `--accent` (shadcn subtle background), `--sidebar-accent` |
| `--danger` / `--warn` / `--success`              | `--destructive` / `--chart-4` / `--chart-3`               |
| `--font-body` / `--font-display` / `--font-mono` | `--font-sans` / `--font-serif` / `--font-mono`            |
| `--radius-md` (or `-lg`)                         | `--radius` (base; Tailwind scale derives sm–4xl from it)  |
| `--elev-*`                                       | `--shadow-*` (keep `none` for flat themes)                |

Beware the same-name traps: designer `--accent` is the strong brand color (→ `--primary`), while shadcn `--accent` is a subtle tint background; designer `--muted` is a text color (→ `--muted-foreground`). Don't adopt the package's `--text-*` / `--space-*` scales — CossUI sizing assumes Tailwind's defaults.

## Known quirks

- The bare `rounded` utility resolves a `--radius` literal in `globals.css` `@theme inline` (Tailwind inlines it at build time) — adjust it there if the new theme changes the radius base.
- Shadow utilities resolve to the `--shadow-*` tokens, so a shadow-friendly theme only needs new token values.
- Tailwind scans markdown files in `web/` for class names — utility classes quoted in docs (including this one) generate CSS. Don't put banned-pattern class names in code examples.
