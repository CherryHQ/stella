## Web UI

Full React SPA using TanStack Router, TanStack Query, and CossUI components. The Go server serves a single HTML shell (`web/spa.templ`) for all page routes; React owns routing and rendering.

**Stack:** React 19 · TanStack Router (file-based) · TanStack Query · CossUI (`web/frontend/src/components/ui/`) · Tailwind v4 · pnpm

### Directory structure

```
web/
├── spa.templ               # Minimal HTML shell — <div id="app-root"> + ViteEntry("app")
├── spa_templ.go            # Auto-generated — never edit manually
├── vite.go                 # ViteEntry() helper — manifest in prod, dev server in dev
├── static/                 # Served at GET /static/ — fonts, images, legacy assets
└── frontend/               # All React/TS source
    ├── src/
    │   ├── entries/app.tsx         # SPA entry — creates router + QueryClientProvider
    │   ├── routes/
    │   │   ├── __root.tsx          # Root route — bare <Outlet>, no providers
    │   │   ├── login.tsx           # /login — unauthenticated; redirects to /agents if authed
    │   │   ├── _app.tsx            # Authenticated layout route — runs meQuery loader
    │   │   └── _app/               # Page routes (all require auth via _app parent)
    │   ├── components/
    │   │   ├── layout/AppLayout.tsx  # Navbar + <Outlet> shell
    │   │   ├── ui/                   # CossUI components (Button, Separator, etc.)
    │   │   └── {page}/               # One directory per page
    │   ├── lib/
    │   │   ├── queryClient.ts        # Singleton QueryClient
    │   │   └── queries/me.ts         # meQueryOptions — auth query
    │   └── globals.css             # Tailwind + CossUI design tokens + font imports
    ├── vite.config.ts
    └── package.json
```

### Auth pattern

Auth state lives in the TanStack Query cache via `meQueryOptions` (`src/lib/queries/me.ts`). The `GET /api/auth/me` response shape is `{ id, username, role, is_admin }` (note: `is_admin` is snake_case).

- Route loaders: `await queryClient.ensureQueryData(meQueryOptions)` — network fetch, throws on 401
- Components: `useQuery(meQueryOptions)` — reads from same cache entry, no extra request
- Admin guards: `queryClient.getQueryData(meQueryOptions.queryKey)` — sync cache read (parent loader already fetched)

### Adding a new page

1. Create `src/components/mypage/MyPage.tsx` — React component
2. Create `src/routes/_app/mypage.tsx`:
   ```typescript
   import { createFileRoute } from '@tanstack/react-router'
   import { MyPage } from '@/components/mypage/MyPage'
   export const Route = createFileRoute('/_app/mypage')({ component: MyPage })
   ```
   For admin-only pages, add a `beforeLoad` that checks `queryClient.getQueryData(meQueryOptions.queryKey)?.is_admin` and throws `redirect({ to: '/agents' })` if false.
3. Add the nav link to `src/components/layout/AppLayout.tsx` → `navItems` array
4. Run `pnpm build` — router plugin regenerates `src/routeTree.gen.ts` automatically

No Go server changes needed — the wildcard `GET /{path...}` already serves all page routes.

### URL as state

Use TanStack Router search params, not `history.pushState` directly:

```typescript
export const Route = createFileRoute('/_app/mypage')({
  validateSearch: (search) => ({
    status: (search.status as string) || 'all',
    page: Number(search.page) || 1,
  }),
  component: MyPage,
})

// Inside component:
const { status, page } = Route.useSearch()
const navigate = useNavigate({ from: Route.fullPath })
navigate({ search: (prev) => ({ ...prev, status: 'active' }) })
```

### Dev workflow

```bash
cd web/frontend
pnpm dev          # Vite dev server at localhost:5173
pnpm build        # Production build → web/static/dist/

# In another terminal:
APP_ENV=development anna --open   # Go server proxies Vite assets from :5173
```

### CossUI components

All components live in `src/components/ui/`. Import directly:
```typescript
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
```

Based on `@base-ui/react`. Design tokens are in `src/globals.css` under `:root` (light) and `.dark`.
