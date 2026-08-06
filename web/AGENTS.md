## Web UI

React SPA embedded by Go. TanStack Router owns client-side routing.

**Stack:** React 19 · TanStack Router · TanStack Query · CossUI · Tailwind v4 · Vite+ (`vp`)

### Rules

Detailed conventions live in `content/docs/development/rules/`. Read the relevant rule before designing or changing anything in that domain:

| Domain            | Rule file          | Read before                                                           |
| ----------------- | ------------------ | --------------------------------------------------------------------- |
| UI engineering    | `web-ui.md`        | Building or reviewing any UI (tokens, CossUI, overlays, forms, icons) |
| Current theme     | `web-design.md`    | Any visual/design decision                                            |
| Theming / restyle | `web-theming.md`   | Changing the visual style or editing `src/tokens.css`                 |
| Web UI testing    | `web-ui-test.md`   | Verifying the UI with browser automation                              |
| Web perf testing  | `web-perf-test.md` | Measuring or optimizing web UI performance                            |
| Marketing copy    | `marketing.md`     | Building a landing page, hero, or any marketing-facing surface        |

Also: `.agents/skills/coss/SKILL.md` for CossUI imports, composition rules, and particle examples.

### Structure

```txt
web/src/
  routes/       Thin route files: params, loaders, guards only
  features/     Page-specific UI, hooks, helpers
  components/   Shared reusable UI; components/ui/ = CossUI primitives
  layouts/      App shells (AppLayout, AppShell)
  hooks/        Shared React hooks
  lib/          API client, queries, types, utilities
  globals.css   Invariant: @theme bridge + base layer (zero theme values)
  tokens.css    Theme values: :root light / .dark dark (the swappable file)
```

- Route files import full-screen UI from `features/`, not inline.
- Page-specific code belongs in `features/`, not `components/`.
- Import alias: always `@/` for `src/`. No relative `../../../` paths.

### File naming

| Kind          | Convention                 | Example                    |
| ------------- | -------------------------- | -------------------------- |
| Components    | PascalCase                 | `AgentForm.tsx`            |
| Hooks         | kebab-case with `use-`     | `use-toast.tsx`            |
| Utils / types | kebab-case                 | `agent-colors.ts`          |
| Routes        | kebab-case with `$` params | `agents.$agentId.lazy.tsx` |

### CossUI

Always use CossUI components before hand-writing primitives. Never override visual styles (colors, radius, shadows, padding, font) at the call site — use variant/size props. Feature components only add layout classes (`flex`, `grid`, `gap-*`, `p-*`, `w-*`). Full contract and token discipline: `web-ui.md`.

### Theme tokens

Do not hardcode palette utilities or raw colors in UI code (`bg-emerald-500`, `text-blue-600`, `bg-black/50`, `#fff`, `rgba(...)`, arbitrary color shadows, etc.). Use the existing shadcn tokens only: `background`, `foreground`, `card`, `popover`, `muted`, `accent`, `primary`, `destructive`, `border`, `ring`, `sidebar-*`, and `chart-1..5`. Status verdicts use `success` / `warning` / `info` and their `-foreground` pairs; `chart-1..5` are fills for plotted and categorical data — `bg-chart-*` only, never `text-`/`fill-`/`stroke-chart-*` or `color: var(--chart-*)`, which fail contrast as words. For red text use `destructive-foreground` — bare `destructive` is the solid fill CossUI hardcodes white on. Do not add project-specific Tailwind color tokens for one-off states.

### I18n

All user-facing strings use `useI18n()`. Never hardcode display text. Locales: `en`, `zh` — add keys to both when creating new text. Config: `src/lib/i18n/config.ts`.

### Data layer

**API client:** Auto-generated from OpenAPI in `src/lib/api-client/`. Import functions from `@/lib/api-client/sdk.gen`. Never write custom `fetch()`.

**Query options:** Define as `<resource>QueryOptions` or `<resource>InfiniteQueryOptions` in `src/lib/queries/`. Use `infiniteQueryOptions` with `page_token` / `next_page_token` for paginated APIs. Batch helpers in `src/lib/paginated.ts` auto-fetch all pages for non-UI use.

**Query keys** must include all URL params that affect the result. Use `enabled: !!param` for conditional queries. Never capture derived variables in `queryFn` closures.

**State hierarchy:** URL params > TanStack Query cache > React Context > `useState`. Only use `useState` for ephemeral UI state (form drafts, open/closed toggles).

### URL state

The URL is the single source of truth. A component that ignores a URL param change is a bug.

- All navigable state lives in URL params. Never `useState` for anything that should survive refresh or back/forward.
- Derive state from params (`useParams`, `useSearch`), write with `navigate` or `Link`.
- When the same component renders under multiple routes, add a `key` from all URL params so React remounts on identity change.
- Never `useEffect` to sync URL → local state. Use `useMemo` for derived values.
- Never `history.pushState` directly.

### Auth

Cached via `meQueryOptions` in `src/lib/queries/me.ts`.

- Loaders: `queryClient.ensureQueryData(meQueryOptions)`
- Components: `useQuery(meQueryOptions)`
- Admin: `queryClient.getQueryData(meQueryOptions.queryKey)?.is_admin`

Response is snake_case: `{ id, username, role, is_admin }`.

### Add a page

1. Create page in `src/features/<feature>/<Page>.tsx`.
2. Create thin route in `src/routes/_app/<path>.tsx`.
3. Add navigation in `src/layouts/AppLayout.tsx` if needed.
4. Run `vp build` to regenerate `src/routeTree.gen.ts`.

### Commands

```bash
vp dev            # Vite dev server at localhost:5173
vp check --fix    # format, lint, type-check with auto-fix
vp test           # run frontend tests
vp build          # build to web/static/dist/
vp add <pkg>      # add dependency
```

Always `vp check --fix` before committing. Full-stack dev: `mise run dev` from repo root (proxies `/api/*` to Go).
