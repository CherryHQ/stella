# anna docs site

Fumadocs on TanStack Start, styled with Tailwind v4, deployed to Cloudflare Workers.

## Commands

```bash
pnpm dev        # local dev server
pnpm build      # production build (prerendered)
pnpm deploy     # build + wrangler deploy
pnpm lint       # biome check
pnpm format     # biome format --write
```

## Structure

```
docs/
  content/docs/           # markdown/mdx content (the actual docs)
    index.mdx             # docs landing page
    changelog.mdx         # changelog
    getting-started/      # configuration.md, deployment.md
    core/                 # architecture.md, models.md, memory-system.md, session-compaction.md
    channels/             # telegram.md, qq.md, feishu.md
    features/             # cron-system.md, notification-system.md
  src/
    routes/index.tsx      # home landing page (custom, not from mdx)
    routes/docs/$.tsx     # docs page renderer
    routes/__root.tsx     # html root, loads fonts + fumadocs provider
    styles/app.css        # tailwind imports, custom theme, animations
    lib/layout.shared.tsx # shared nav config (title, github link)
    lib/source.ts         # fumadocs content source setup
    components/           # mdx.tsx, not-found.tsx
  source.config.ts        # fumadocs-mdx config
  wrangler.jsonc          # cloudflare workers deploy config
```

## Adding / editing docs

1. Add or edit `.md`/`.mdx` files in `content/docs/`. Frontmatter requires `title` at minimum.
2. If adding a new file, add its slug to the folder's `meta.json` to control sidebar order.
3. Run `pnpm dev` to preview.

## Design

The landing page (`src/routes/index.tsx`) uses an editorial aesthetic:

- **Fonts**: DM Serif Text (headings), DM Sans (body), JetBrains Mono (code). Loaded via Google Fonts in `app.css`.
- **Palette**: warm neutrals with terracotta accent (`--color-terra`). All custom colors use OKLCH in `app.css` under `@theme`.
- **Layout**: two-column hero (copy left, terminal right) on lg+, stacks on mobile. Numbered feature grid below. Footer CTA with install command.
- **Animation**: staggered fade-up on page load, defined in `app.css` (`.animate-fade-up`, `.stagger-1` through `.stagger-5`).

The docs pages use the default fumadocs-ui neutral theme with no customization.

## Rules

- Keep the landing page in a single file (`src/routes/index.tsx`). Extract components only if reused elsewhere.
- Docs content goes in `content/docs/`, not in React components.
- Use fumadocs-ui components (`Cards`, `Card`, etc.) inside mdx when needed.
- Run `pnpm format` before committing.
