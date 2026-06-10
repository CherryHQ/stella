## Web UI

React SPA embedded by Go. TanStack Router owns client-side routing.

**Stack:** React 19 · TanStack Router · TanStack Query · CossUI · Tailwind v4 · Vite+ (`vp`)

Everything in this file is theme-independent and survives any visual restyle. The current theme (visual direction, palette intent, motion rules) lives in **`DESIGN.md`** — read it before building or reviewing any UI. Token values live in `src/tokens.css`. To change the visual style, see § Theming below.

### References

- **`DESIGN.md`** — current theme: visual direction, color/typography/motion rules, theme anti-patterns. Swapped wholesale when the style changes.
- **`.agents/skills/coss/SKILL.md`** — CossUI imports, composition rules, particle examples.

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

### Design tokens

The token pipeline: `src/tokens.css` defines values → `globals.css` `@theme inline` maps them to Tailwind utilities → components consume semantic classes. This is what makes theme swaps a two-file change — every rule below protects that contract.

- Always use semantic Tailwind utilities: `bg-background`, `text-foreground`, `text-primary`, `bg-muted`, `border-border`, etc.
- Never hardcode color values: no `text-[#abc]`, no `bg-[oklch(...)]`, no inline `style={{ color }}`, no palette classes like `bg-violet-500`.
- Never reference `var(--some-color)` directly in JSX when a Tailwind utility exists.
- Never use arbitrary spacing values like `p-[13px]`. Stick to the 4px grid; prefer `gap-*` on flex/grid parents over margin on children.
- For app-level surfaces beyond shadcn's vocabulary, use the `--stella-os-*` semantic layer (defined in `tokens.css` as aliases): `canvas`/`canvas-muted`, `surface`/`surface-raised`, `ink`/`muted`, `rule`, `accent`/`accent-soft`, `warning`/`success`/`danger`. Reference via `var(--stella-os-*)` only when no Tailwind utility exists (e.g. the landing page `routes/index.css`).

### Dark mode

Theme is class-based: `.dark` on `<html>`, managed by `src/lib/theme.ts` (user-selectable: light / dark / system).

- Never use `dark:` variants to override semantic tokens — they switch automatically via `tokens.css`.
- Use `dark:` only for non-token properties (e.g., `dark:opacity-80` on images).
- Test both modes. A component that looks correct in one mode but broken in the other is a bug.

### CossUI — zero custom styling

Always use CossUI primitives from `src/components/ui/` (53+ components on `@base-ui/react`). Hard rules:

- **Never hand-write a primitive that CossUI covers.** Button, Dialog, Select, Card, Tabs, etc. — no exceptions.
- **Never override CossUI visual styles** (colors, radius, shadows, padding, font) inline or with extra Tailwind classes. Use variant/size props. If a variant doesn't exist, propose adding it to the CossUI component — don't patch the call site.
- **Never create a one-off styled wrapper** around a CossUI component. If a wrapper only adds Tailwind classes, those classes belong in the component's variant system or in the theme.
- **Compose, don't customize.** A settings form is Field + Input + Select + Button composed — not a custom `<SettingsInput>` with bespoke styles.
- **Feature components inherit, not override.** The only Tailwind classes allowed on feature components are layout concerns: `flex`, `grid`, `gap-*`, `p-*`, `w-*`, `h-*`, `overflow-*`. Color, typography, radius, and shadow come from CossUI and the theme.

**Why:** when the theme changes (swap `tokens.css` + `DESIGN.md`), every page must update automatically. A component that hardcodes `bg-violet-500` or `rounded-xl` breaks this contract and becomes a manual migration target.

Button variants: `default` (primary action — one per view), `secondary`, `ghost` (toolbar/sidebar), `outline`, `destructive`, `link`, `premium`/`premium-outline` (paid features).

Read `.agents/skills/coss/SKILL.md` for imports, composition, and particle examples.

### UI patterns

**Layout shell:** SiteHeader (h-14, border-b) over Sidebar (16rem desktop, 18rem mobile, 3rem collapsed icon-only, offcanvas modal on mobile) + content inset with sub-header (h-12, backdrop-blur). Split-pane views use CSS `flex` with a draggable divider (not Grid), min-width constraints, and fall back to Sheet on mobile. Mobile breakpoint: `max-md` (< 768px); multi-column layouts collapse to single column.

**Overlay decision tree:**

| Need                                     | Component                    | Example                          |
| ---------------------------------------- | ---------------------------- | -------------------------------- |
| Blocking confirmation or multi-step form | **Dialog** (`DialogPopup`)   | Create group, confirm delete     |
| Side panel with detail/inspector content | **Sheet** (`SheetPopup`)     | Mobile inspector, session detail |
| Small contextual options from a trigger  | **Popover** (`PopoverPopup`) | Color picker, quick settings     |
| Action menu from a trigger               | **Menu** (`DropdownMenu`)    | Row actions, user menu           |

On mobile, replace side panels with Sheet (bottom or right), not Dialog. Prefer inline resolution over spawning a blocking Dialog. Never nest overlays — a Dialog inside a Sheet is a bug.

**Toasts:** use `useToast()` from `@/hooks/use-toast` (not a shadcn toast). Kinds: `"success" | "error"`, default 3000ms.

**Loading / empty / error:** use TanStack Query's `isLoading` with a Spinner or brief text — never a full-page skeleton unless the page is data-heavy. Empty states use the shared `SettingsEmptyState` (`@/features/settings/SettingsEmptyState`). Errors display inline, direct tone, no apology.

**Form validation:** no form library (no Zod, no React Hook Form). Plain `useState` per field with inline validation; errors via Field's error slot. Wrap inputs in `<Field>`, group with `<Fieldset>`.

**Z-index layers:** sticky headers `z-30`, resizable dividers `z-10`, overlays managed by CossUI (don't override), toasts `z-[9999]`. Don't invent arbitrary z-index values — extend this list if a new layer is needed.

**Icons:** `lucide-react` only. Sizes 16px and 20px. In buttons with text: `size={16}` with `gap-2`; standalone: `size="icon-sm"`/`size="icon"`. All UI icons monochrome at `text-muted-foreground`. Never use emoji as icons.

### Theming

The visual style is fully described by two files; swapping a style touches **only** them:

| File             | Role                                                                |
| ---------------- | ------------------------------------------------------------------- |
| `src/tokens.css` | All token values: font `@import`, `:root` (light), `.dark` (dark)   |
| `DESIGN.md`      | Visual direction, palette intent, motion rules, theme anti-patterns |

`globals.css` (the `@theme inline` bridge and `@layer base`), CossUI components, and all feature code stay untouched. The landing page (`routes/index.css`) follows automatically via the `--stella-os-*` aliases.

**To adopt a new style** (e.g. from a designer design-system package, a tweakcn preset, or hand-made):

1. **Translate the source palette into `src/tokens.css`** using the shadcn schema. Keep both `:root` and `.dark` blocks — designer packages ship one mode; take the other mode's values from the package `DESIGN.md` color tables, or derive a faithful counterpart. Keep the `--stella-os-*` alias block verbatim (only `warning`/`success` carry theme values). Put the font `@import` on the first line.

   Mapping from the designer package schema (uniform across all packages):

   | designer token                                   | shadcn token(s)                                                 |
   | ------------------------------------------------ | --------------------------------------------------------------- |
   | `--bg`                                           | `--background`                                                  |
   | `--surface`                                      | `--card`, `--secondary`, `--sidebar`                            |
   | elevated tier (see pkg)                          | `--popover`, `--muted`                                          |
   | `--fg`                                           | `--foreground` and all `*-foreground` surface pairs             |
   | `--muted` (a text color!)                        | `--muted-foreground`                                            |
   | `--border`                                       | `--border`, `--input`, `--sidebar-border`                       |
   | `--accent` (brand color)                         | `--primary`, `--ring`, `--sidebar-primary`, `--chart-1`         |
   | `--accent-on`                                    | `--primary-foreground`                                          |
   | `--accent-hover`                                 | `--accent-foreground` (dark mode)                               |
   | accent tint (derive)                             | `--accent` (shadcn subtle background), `--sidebar-accent`       |
   | `--danger` / `--warn` / `--success`              | `--destructive` / `--stella-os-warning` / `--stella-os-success` |
   | `--font-body` / `--font-display` / `--font-mono` | `--font-sans` / `--font-serif` / `--font-mono`                  |
   | `--radius-md` (or `-lg`)                         | `--radius` (base; Tailwind scale derives sm–4xl from it)        |
   | `--elev-*`                                       | `--shadow-*` (keep `none` for flat themes)                      |

   Beware the same-name traps: designer `--accent` is the strong brand color (→ `--primary`), while shadcn `--accent` is a subtle tint background; designer `--muted` is a text color (→ `--muted-foreground`). Don't adopt the package's `--text-*` / `--space-*` scales — CossUI sizing assumes Tailwind's defaults.

2. **Rewrite `DESIGN.md`** from the source's design doc, keeping the section skeleton (visual direction, color tables with Tailwind classes, typography, spacing & density, radius, elevation, motion, voice, theme anti-patterns). Translate values into semantic Tailwind classes so the doc matches what code actually uses.

3. **Known quirks:** the bare `rounded` utility resolves a `--radius` literal in `globals.css` `@theme inline` (Tailwind inlines it at build time) — adjust it there if the new theme changes the radius base. Shadow utilities resolve to the `--shadow-*` tokens, so a shadow-friendly theme only needs new token values.

4. **Verify:** `vp build`, then screenshot light + dark across chat, settings, and the landing page. Check against the new `DESIGN.md`: accent frequency, surface depth levels, contrast. Eyeball the known hardcoded surfaces (`lib/agent-colors.ts` avatar gradients, the Google brand color in `features/login/LoginPage.tsx`) for clashes.

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
