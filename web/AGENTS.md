## Web UI

React SPA embedded by Go. Go serves built assets and falls back to `index.html`; TanStack Router owns client-side routing.

**Stack:** React 19 · TanStack Router · TanStack Query · CossUI (`src/components/ui/`) · Tailwind v4 · Vite+ (`vp`)

> **Agent note:** When building or editing UI, read `.agents/skills/coss/SKILL.md` for CossUI component patterns, imports, composition rules, and particle examples.

**CossUI first, zero custom styling:** Always use CossUI components (`src/components/ui/`) before hand-writing UI primitives. Never override CossUI visual styles (colors, radius, shadows, padding, font) at the call site — use variant/size props. Feature components in `src/features/` should only add layout Tailwind classes (`flex`, `grid`, `gap-*`, `p-*`, `w-*`); all visual identity comes from CossUI + the theme. See `DESIGN.md` § Components for the full rules and examples.

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

### Design system

**Read `DESIGN.md` before building or reviewing any UI.** It defines the visual direction (Perplexity-based), token usage rules, layout patterns, component conventions, overlay decision tree, toast/loading/empty state patterns, z-index layers, and anti-patterns.

### I18n

All user-facing strings must use the `useI18n()` hook. Never hardcode display text.

```tsx
const { t } = useI18n();
<Button>{t("common.save")}</Button>;
```

- Supported locales: `en`, `zh`. Config in `src/lib/i18n/config.ts`.
- Locale is stored in localStorage.
- When adding new UI text, add keys to both locale files.

### API client

The API client in `src/lib/api-client/` is **auto-generated from OpenAPI**. Never write custom `fetch()` calls.

```tsx
import { listAgents, createGroup } from "@/lib/api-client/sdk.gen";
const { data } = await createGroup({ body: { group_name, agent_ids }, throwOnError: true });
```

### Query patterns

- Define query options as `<resource>QueryOptions` or `<resource>InfiniteQueryOptions` in `src/lib/queries/`.
- Use `infiniteQueryOptions` with `page_token` / `next_page_token` for paginated APIs.
- Batch helpers in `src/lib/paginated.ts` (e.g., `fetchAllTasks()`) auto-fetch all pages — use these for non-UI data fetching.
- State hierarchy: **URL params > TanStack Query cache > React Context > useState**. Only use `useState` for ephemeral UI state (form drafts, open/closed toggles).

### File naming

| Kind          | Convention                 | Example                                  |
| ------------- | -------------------------- | ---------------------------------------- |
| Components    | PascalCase                 | `AgentForm.tsx`, `SkillInstallModal.tsx` |
| Hooks         | kebab-case with `use-`     | `use-toast.tsx`, `use-media-query.ts`    |
| Utils / types | kebab-case                 | `agent-colors.ts`, `auth-error.ts`       |
| Routes        | kebab-case with `$` params | `agents.$agentId.tsx`                    |
| Lazy routes   | `.lazy.tsx` suffix         | `agents.$agentId.lazy.tsx`               |

Import alias: always use `@/` for `src/`. Never use relative paths like `../../../lib/`.

### URL state

**The URL is the single source of truth.** When the URL changes, everything on the page must update to reflect it. A component that ignores a URL param change is a bug.

**Hard rules:**

- All navigable UI state MUST live in the URL — route params or search params. Never use `useState` for anything that should survive a page refresh or browser back/forward.
- Derive component state from URL params, never the reverse. Read params with `useParams` or `useSearch`; write them with `navigate` or `Link`.
- When the same component renders under different routes (e.g., `/sessions/$sessionId` and `/projects/$projectId/sessions/$sessionId`), use a `key` derived from all URL params in the route wrapper so React fully remounts the component when the URL identity changes. This guarantees all internal state, hooks, and queries reset.
- Never use `useEffect` to "sync" URL params into local state — that creates two sources of truth and stale-state bugs. If you need derived values, use `useMemo` on the params directly.
- Never use `history.pushState` directly.

**TanStack Router patterns:**

- Use file-based routes for resource identity (e.g., `/agents/$agentId/sessions/$sessionId`).
- Use `useParams` with a `from` route ID for type-safe param access.
- Use search params (`useSearch` / `navigate({ search })`) for secondary UI state (filters, tabs, view modes).
- Route wrapper components should add `key` props when the same feature component is shared across routes:
  ```tsx
  function SessionViewKeyed() {
    const { agentId, sessionId } = useParams({ from: "/_app/agents/$agentId/sessions/$sessionId" });
    return <SessionView key={`${agentId}/${sessionId}`} />;
  }
  ```

**TanStack Query patterns:**

- Query keys MUST include all URL params that affect the query result. When params change, the query automatically refetches.
- Use `enabled: !!param` to defer queries until required params are available.
- Never capture derived variables (like `encodeURIComponent(id)`) in `queryFn` closures — compute them inline or pass params directly to avoid stale closures.

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
