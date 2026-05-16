## Web UI

React SPA embedded by Go. Go serves built assets and falls back to `index.html`; TanStack Router owns client-side routing.

**Stack:** React 19 · TanStack Router · TanStack Query · CossUI (`src/components/ui/`) · Tailwind v4 · Vite+ (`vp`)

> **Agent note:** When building or editing UI, read `.agents/skills/coss/SKILL.md` for CossUI component patterns, imports, composition rules, and particle examples.

### Structure

```txt
web/src/
  routes/       TanStack Router route files; keep thin: route params, loaders, guards
  features/     Page and feature-specific UI, hooks, helpers
  components/   Shared reusable UI only; `components/ui/` is for CossUI primitives
  layouts/      App shells and structural layouts such as `AppLayout`
  hooks/        Shared React hooks
  lib/          Shared API, query, type, and utility code
```

Route files should import full-screen UI from `features/`, not define large pages inline. Do not put page-specific code in `components/`.

### Auth

Auth state is cached through `meQueryOptions` in `src/lib/queries/me.ts`.

- Loaders: `queryClient.ensureQueryData(meQueryOptions)`
- Components: `useQuery(meQueryOptions)`
- Admin guards: read `queryClient.getQueryData(meQueryOptions.queryKey)?.is_admin`

The `/api/auth/me` response uses snake_case: `{ id, username, role, is_admin }`.

### Add a page

1. Create the page in `src/features/<feature>/<FeaturePage>.tsx`.
2. Create a thin route in `src/routes/_app/<path>.tsx`.
3. Add navigation in `src/layouts/AppLayout.tsx` if needed.
4. Run `vp build` to regenerate `src/routeTree.gen.ts`.

Example:

```tsx
import { createFileRoute } from "@tanstack/react-router";
import { MyPage } from "@/features/mypage/MyPage";

export const Route = createFileRoute("/_app/mypage")({ component: MyPage });
```

### Theming

All colors, fonts, radii, and shadows are defined as CSS custom properties in `src/globals.css`. The theme is a **tweakcn shadcn preset** — do not edit the token values in `globals.css` manually.

To swap or update the theme, run:

```bash
pnpm dlx shadcn@latest add https://tweakcn.com/r/themes/cmlk6zefr000004lbe9jygsqc
```

Tailwind utility classes map to those tokens via `@theme inline`.

**Rules:**

- Always use semantic Tailwind utilities: `bg-background`, `text-foreground`, `text-primary`, `bg-muted`, `border-border`, `text-muted-foreground`, etc.
- Never hardcode color values inline (no `text-[#abc]`, no `bg-[oklch(...)]`, no `style={{ color: '...' }}`).
- Never reference raw CSS variables directly in JSX (no `var(--some-color)`) unless no Tailwind utility exists for it.
- To change the theme, run the shadcn command above — no component changes required.

### URL state

**All navigable UI state MUST be represented in the URL.** Every panel, selected item, and view mode must have its own addressable route or search param. Never use React state (`useState`) for navigation or selection that should be bookmarkable, shareable, or work with browser back/forward.

- Use TanStack Router file-based routes for resource selection (e.g., `/agents/{id}/tasks/{taskId}`).
- Use TanStack Router search params for secondary state (filters, tabs) when a full route is overkill.
- Never use `history.pushState` directly.
- Derive selected items from URL params, not internal component state.

### Commands

Run web commands from `web/`:

```bash
vp install        # install dependencies
vp dev            # start Vite dev server at localhost:5173
vp check          # format, lint, and type-check
vp check --fix    # auto-fix formatting and lint issues
vp test           # run frontend tests
vp build          # build to web/static/dist/
vp preview        # preview production build
vp add <pkg>      # add dependency
vp remove <pkg>   # remove dependency
vp run <script>   # run package.json script or Vite Task task
```

Always run `vp check --fix` before every commit to ensure formatting is consistent.

For full-stack dev, run `mise run dev` from the repository root. The Vite dev server proxies `/api/*` to the Go server.
