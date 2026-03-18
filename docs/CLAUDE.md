# anna docs site

Fumadocs on TanStack Start, styled with Tailwind v4, deployed to Cloudflare Workers.

## Commands

This project uses [Vite+](https://viteplus.dev) (`vp`) as its unified toolchain. Do not use `pnpm` directly.

```bash
vp dev          # local dev server
vp build        # production build (prerendered)
vp check        # format + lint + type checks
vp lint         # lint (oxlint)
vp fmt          # format (oxfmt)
vp install      # install dependencies
vp run deploy   # build + wrangler deploy
```

## Adding / editing docs

1. Add or edit `.md`/`.mdx` files in `content/docs/`. Frontmatter requires `title` at minimum.
2. If adding a new file, add its slug to the folder's `meta.json` to control sidebar order.
3. Run `vp dev` to preview.

## i18n

Languages: en (default), zh, ja. Configured in `src/lib/i18n.ts`. `/` redirects to `/en`.

- **Doc content**: add `filename.zh.md` / `filename.ja.md` alongside the English file.
- **Sidebar titles**: add `meta.zh.json` / `meta.ja.json` alongside each `meta.json`.
- **UI strings** (landing, about, nav): edit `src/lib/translations.ts`, keyed by locale.
- **Fumadocs UI strings** (search, toc, etc.): edit `defineI18nUI` translations in `src/routes/__root.tsx`.

## Design

The landing page (`src/routes/$lang/index.tsx`) uses an editorial aesthetic:

- **Fonts**: DM Serif Text (headings), DM Sans (body), JetBrains Mono (code). Loaded via Google Fonts in `app.css`.
- **Palette**: warm neutrals with terracotta accent (`--color-terra`). All custom colors use OKLCH in `app.css` under `@theme`.
- **Layout**: two-column hero (copy left, terminal right) on lg+, stacks on mobile. Numbered feature grid below. Footer CTA with install command.
- **Animation**: staggered fade-up on page load, defined in `app.css` (`.animate-fade-up`, `.stagger-1` through `.stagger-5`).

The docs pages use the default fumadocs-ui neutral theme with no customization.

## Rules

- Keep the landing page in a single file (`src/routes/$lang/index.tsx`). Extract components only if reused elsewhere.
- Docs content goes in `content/docs/`, not in React components.
- Use fumadocs-ui components (`Cards`, `Card`, etc.) inside mdx when needed.
- Run `vp check` before committing.
