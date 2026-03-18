# Plan: Admin UI Migration to templ + daisyUI

## Overview

Migrate the admin UI from a `{@include}` HTML preprocessor SPA to server-rendered templ components with daisyUI 5 styling. Alpine.js 3 stays for client-side interactivity but gets split into ESM modules. All frontend deps remain CDN-only.

### Goals

- Replace `{@include}` assembly with type-safe templ components
- Move from SPA tab-switching to server-rendered routes (`GET /providers`, `GET /agents`, etc.)
- Adopt daisyUI 5 + Tailwind CSS 4 (CDN) for consistent component styling
- Split monolithic `app.js` into ESM modules (api, stores, page controllers)
- Custom `terra` daisyUI theme matching current palette, switchable to built-in themes
- Zero changes to JSON API handlers

### Success Criteria

- [ ] All 7 admin pages render via templ components
- [ ] Each page has its own server route (`GET /providers`, `GET /agents`, etc.)
- [ ] daisyUI 5 components used for all controls (inputs, buttons, toggles, tables, badges)
- [ ] Custom terra theme active, theme switcher allows switching to built-in daisyUI themes
- [ ] JS split into ESM modules: api.js, stores/, pages/
- [ ] `templ generate` integrated into `mise run generate`
- [ ] All existing admin functionality preserved (CRUD, session viewer, settings editor, etc.)
- [ ] No package.json — all frontend deps via CDN
- [ ] `mise run lint` passes
- [ ] Existing API tests in `server_test.go` pass

### Out of Scope

- New admin features or pages
- Changes to JSON API handlers or routes
- Authentication/authorization
- SSR data injection (pages still fetch data via JSON API from Alpine)
- Offline support or service workers

## Technical Approach

### Architecture

```
Browser request → Go router → templ renders HTML shell → browser loads CDN deps → Alpine.js hydrates → fetches JSON API
```

Each admin page is a **server route** returning a full HTML page (templ-rendered). Alpine.js ESM modules handle interactivity and API calls client-side. daisyUI provides component classes via CDN.

### Key Decisions

1. **Server-rendered routes**: Each tab becomes `GET /<page>`. The navbar links are regular `<a>` tags. No client-side routing — full page loads. This simplifies bookmarking and removes the tab state from Alpine.

2. **CDN deps — verified URLs**:
   ```html
   <!-- daisyUI 5 CSS (must be before Tailwind) -->
   <link href="https://cdn.jsdelivr.net/npm/daisyui@5" rel="stylesheet" type="text/css" />
   <!-- All 33+ daisyUI themes (for theme switcher) -->
   <link href="https://cdn.jsdelivr.net/npm/daisyui@5/themes.css" rel="stylesheet" type="text/css" />
   <!-- Tailwind CSS 4 browser runtime -->
   <script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4"></script>
   <!-- Alpine.js 3 ESM from esm.sh -->
   <!-- Loaded as ESM module, NOT via defer script tag — see Alpine ESM pattern below -->
   ```

3. **Alpine.js ESM loading pattern**: Import Alpine as ESM from `esm.sh`, register all stores and `Alpine.data()` components, then manually call `Alpine.start()`. This avoids the race condition between `defer` auto-start and module execution.
   ```html
   <!-- In layout.templ, at end of <body> -->
   <script type="module">
     import Alpine from 'https://esm.sh/alpinejs@3'
     // Import stores
     import { registerToast } from '/static/js/stores/toast.js'
     import { registerTheme } from '/static/js/stores/theme.js'
     // Import page module (injected per-page by templ)
     // e.g.: import { register } from '/static/js/pages/settings.js'

     registerToast(Alpine)
     registerTheme(Alpine)
     // register(Alpine) — page-specific

     window.Alpine = Alpine
     Alpine.start()
   </script>
   ```
   Each page templ component specifies which page JS module to load. The layout templ renders the init script with the correct page import.

4. **templ + Alpine.js attributes**: templ natively supports `@click`, `x-data`, `x-show`, `x-for`, etc. as standard HTML attributes. No escaping issues — verified. Convention: use `@click` (not `x-on:click`) for consistency with current codebase.

5. **templ package**: All templ files in `internal/admin/ui/`. templ generates `*_templ.go` in the same package. The embed directive scopes to `//go:embed ui/static` to only embed JS files served to browsers (not `.templ` source or `*_templ.go`).

6. **Custom terra theme**: Defined via `<style>` block in layout.templ using daisyUI 5 CSS custom properties:
   ```css
   [data-theme="terra"] {
     --color-base-100: oklch(from #faf9f7 l c h);
     --color-base-200: oklch(from #f5f3ef l c h);
     --color-base-300: oklch(from #e8e4dd l c h);
     --color-base-content: oklch(from #252320 l c h);
     --color-primary: oklch(from #b8593a l c h);
     --color-primary-content: oklch(from #fdf5f2 l c h);
     /* ... mapped from current terra/warm palette */
   }
   ```
   Theme switcher sets `data-theme` on `<html>` and persists to localStorage. Built-in daisyUI themes available via the themes.css CDN import.

7. **render.go pattern**: Each page has a handler function that calls the templ component's `Render(r.Context(), w)` method. The layout component takes `activePage string` (for navbar highlighting) and a `templ.Component` content slot. Concrete example:
   ```go
   func (s *Server) pageSettings(w http.ResponseWriter, r *http.Request) {
       w.Header().Set("Content-Type", "text/html; charset=utf-8")
       ui.Layout("settings", ui.SettingsPage()).Render(r.Context(), w)
   }
   ```

8. **Transition strategy**: Phase 1 registers all 7 page routes immediately, each rendering a placeholder "Coming soon" page using the layout template. Old `serveUI` (`GET /`) becomes a redirect to `/providers`. Unknown paths return 404. Subsequent phases replace placeholders with real content. No 404s for known pages during incremental migration.

9. **Page script injection in layout.templ**: The layout component accepts a `pageScript string` parameter (e.g., `"/static/js/pages/settings.js"`). The init `<script type="module">` block uses dynamic import to load it:
   ```html
   <script type="module">
     import Alpine from 'https://esm.sh/alpinejs@3'
     import { registerToast } from '/static/js/stores/toast.js'
     import { registerTheme } from '/static/js/stores/theme.js'
     registerToast(Alpine)
     registerTheme(Alpine)
     // Dynamic page module — rendered by templ based on pageScript param
     const page = await import('/static/js/pages/settings.js')
     if (page.register) page.register(Alpine)
     window.Alpine = Alpine
     Alpine.start()
   </script>
   ```
   In templ, the script block is rendered with the `pageScript` value interpolated into the import path. For placeholder pages (no JS yet), `pageScript` is empty and the dynamic import is skipped via a templ `if` conditional.

10. **Tailwind browser runtime is required**: daisyUI 5 CDN provides pre-built component classes (e.g., `btn`, `card`, `input`), but standard Tailwind utilities (`mt-4`, `flex`, `gap-2`, `text-sm`) used in templ templates need the `@tailwindcss/browser@4` runtime to compile in the browser. Both CDN imports are necessary.

### Components

- **`ui/layout.templ`**: HTML shell — `<head>` (CDN links, theme CSS), `<body>`, footer, toast, confirm dialog. Takes `activePage string` + `content templ.Component` + `pageScript string` (JS module path).
- **`ui/navbar.templ`**: Navigation bar with `<a>` links for all 7 pages, active page highlighting, theme switcher dropdown, status indicator.
- **`ui/components.templ`**: Shared partials — FormField, EmptyState, Badge, PageHeader.
- **`ui/pages/*.templ`**: One per page — providers, agents, channels, users, sessions, scheduler, settings.
- **`ui/static/js/api.js`**: ESM module — `export async function api(method, path, body)`.
- **`ui/static/js/utils.js`**: ESM module — `export function formatTime(ts)`, model combo helpers.
- **`ui/static/js/stores/toast.js`**: ESM module — `export function registerToast(Alpine)` registers `Alpine.store('toast', ...)`.
- **`ui/static/js/stores/theme.js`**: ESM module — `export function registerTheme(Alpine)` registers `Alpine.store('theme', ...)`.
- **`ui/static/js/pages/*.js`**: ESM modules — `export function register(Alpine)` registers `Alpine.data('providersPage', ...)`, etc.
- **`render.go`**: Page handler functions (thin wrappers: set Content-Type, call `Layout(...).Render()`).
- **`embed.go`**: `//go:embed ui/static` — serves `GET /static/*` files.
- **`server.go`**: Updated routes — `GET /` redirects to `/providers`, `GET /{page}` renders templ page, `GET /static/*` serves embedded JS.

### Directory Structure

```
internal/admin/
├── ui/
│   ├── layout.templ          # HTML shell, head, body wrapper
│   ├── navbar.templ          # Navigation component
│   ├── components.templ      # Shared UI partials (form fields, badges, empty state)
│   ├── pages/
│   │   ├── providers.templ
│   │   ├── agents.templ
│   │   ├── channels.templ
│   │   ├── users.templ
│   │   ├── sessions.templ
│   │   ├── scheduler.templ
│   │   └── settings.templ
│   └── static/
│       └── js/
│           ├── api.js         # ESM: fetch wrapper
│           ├── utils.js       # ESM: formatTime, helpers
│           ├── stores/
│           │   ├── toast.js   # ESM: Alpine.store('toast')
│           │   └── theme.js   # ESM: Alpine.store('theme')
│           └── pages/
│               ├── providers.js
│               ├── agents.js
│               ├── channels.js
│               ├── users.js
│               ├── sessions.js
│               ├── scheduler.js
│               └── settings.js
├── server.go                  # Routes (updated)
├── server_test.go             # API tests (preserved, extended with page route tests)
├── embed.go                   # Embed ui/static + serve
├── render.go                  # templ page handlers
├── agents.go                  # API handler (unchanged)
├── channels.go                # API handler (unchanged)
├── providers.go               # API handler (unchanged)
├── scheduler.go               # API handler (unchanged)
├── sessions.go                # API handler (unchanged)
├── settings.go                # API handler (unchanged)
├── tools.go                   # API handler (unchanged)
└── users.go                   # API handler (unchanged)
```

## Implementation Phases

### Phase 1: Foundation — templ + daisyUI shell + static serving

1. Add `github.com/a-h/templ` to `go.mod` and add `"github:a-h/templ" = "latest"` to `[tools]` in `mise.toml` so the `templ` CLI is available via mise
2. Update `mise run generate` to run `templ generate && sqlc generate` so the standard workflow works from the start
3. Create `ui/layout.templ` — HTML shell with verified CDN links (daisyUI 5 CSS, Tailwind 4 browser, Alpine.js 3 ESM from esm.sh), custom terra theme `<style>` block, `data-theme` attribute, content slot, page-specific ESM script loader via dynamic import (see Key Decision #9)
4. Create `ui/navbar.templ` — `<a>` links for all 7 pages with active page highlighting, theme switcher dropdown (terra + built-in daisyUI themes), status indicator
5. Create placeholder `ui/pages/*.templ` — all 7 pages as minimal "Coming soon" placeholders using layout, so no routes return 404 during migration
6. Create `ui/static/js/api.js` — ESM fetch wrapper extracted from app.js `api()` method
7. Create `ui/static/js/stores/toast.js` — `export function registerToast(Alpine)` with Alpine.store
8. Create `ui/static/js/stores/theme.js` — `export function registerTheme(Alpine)` with Alpine.store, localStorage persistence
9. Rewrite `embed.go` — change embed directive to `//go:embed ui/static`, add handler to serve `GET /static/*` files with proper MIME types
10. Create `render.go` — page handler functions: set `Content-Type: text/html`, call `ui.Layout(page, content).Render(ctx, w)`
11. Update `server.go` — add `GET /static/*` route, `GET /` redirects to `/providers`, register all 7 `GET /{page}` routes pointing to placeholder pages
12. Update `server_test.go` — add tests for `GET /` redirect (302), `GET /providers` returns 200 with `text/html`, keep existing API tests passing
13. Check `.golangci.yml` for templ compatibility — add exclude for `*_templ.go` if linter flags generated code
14. Run `templ generate && go build ./...`, verify shell renders in browser with daisyUI theme, verify ESM Alpine.js starts without console errors

15. Verify unknown paths like `GET /nonexistent` return 404 (not redirect) — only `GET /` redirects to `/providers`

Files: `go.mod`, `mise.toml`, `ui/layout.templ`, `ui/navbar.templ`, `ui/pages/*.templ` (7 placeholders), `ui/static/js/api.js`, `ui/static/js/stores/toast.js`, `ui/static/js/stores/theme.js`, `embed.go`, `render.go`, `server.go`, `server_test.go`

### Phase 2: Shared components + Settings page (simplest page)

1. Create `ui/components.templ` — FormField, EmptyState, Badge, PageHeader partials using daisyUI classes (`input`, `btn`, `badge`, `alert`)
2. Create `ui/pages/settings.templ` — replace placeholder with settings page: textarea per key + save buttons using daisyUI `textarea`, `btn`
3. Create `ui/static/js/pages/settings.js` — `export function register(Alpine)` registering `Alpine.data('settingsPage', ...)` extracted from app.js settings logic (loadSettings, saveSetting)
4. Update `render.go` — settings page handler passes `pageScript: "/static/js/pages/settings.js"`
5. Verify settings load/save works end-to-end in browser

Files: `ui/components.templ`, `ui/pages/settings.templ`, `ui/static/js/pages/settings.js`, `render.go`

### Phase 3: Providers page

1. Create `ui/pages/providers.templ` — provider list (daisyUI `card`, `table`), add dropdown, form (daisyUI `input`, `btn`), model fetch/display
2. Create `ui/static/js/pages/providers.js` — `Alpine.data('providersPage', ...)` with CRUD + model fetching, providerDefaults
3. Update `render.go` — providers page handler
4. Verify provider CRUD, model fetching, model list display

Files: `ui/pages/providers.templ`, `ui/static/js/pages/providers.js`, `render.go`

### Phase 4: Agents page

1. Create `ui/static/js/utils.js` — ESM module with `formatTime()`, model combo helpers (allProviderModels, filteredProviderModels)
2. Create `ui/pages/agents.templ` — agent list, create/edit form with model combo dropdown
3. Create `ui/static/js/pages/agents.js` — `Alpine.data('agentsPage', ...)` with CRUD + model autocomplete (imports from utils.js and api.js)
4. Update `render.go` — agents page handler
5. Verify agent CRUD, model dropdown works with provider models

Files: `ui/static/js/utils.js`, `ui/pages/agents.templ`, `ui/static/js/pages/agents.js`, `render.go`

### Phase 5: Channels + Users pages

1. Create `ui/pages/channels.templ` — three platform blocks (Telegram, QQ, Feishu) with daisyUI `toggle`, `collapse`, `input`, `select`
2. Create `ui/static/js/pages/channels.js` — `Alpine.data('channelsPage', ...)` with platform-specific config handling
3. Create `ui/pages/users.templ` — user list, default agent select, collapsible memories with daisyUI `collapse`, `textarea`
4. Create `ui/static/js/pages/users.js` — `Alpine.data('usersPage', ...)` with memory CRUD
5. Update `render.go` — channels and users page handlers
6. Verify channel save, user default agent, memory CRUD

Files: `ui/pages/channels.templ`, `ui/static/js/pages/channels.js`, `ui/pages/users.templ`, `ui/static/js/pages/users.js`, `render.go`

### Phase 6: Scheduler page

1. Create `ui/pages/scheduler.templ` — job form (daisyUI `radio`, `input`, `select`, `textarea`, `toggle`), job list
2. Create `ui/static/js/pages/scheduler.js` — `Alpine.data('schedulerPage', ...)` with CRUD + toggle
3. Update `render.go` — scheduler page handler
4. Verify job create/edit/delete/toggle, schedule type switching

Files: `ui/pages/scheduler.templ`, `ui/static/js/pages/scheduler.js`, `render.go`

### Phase 7: Sessions page (most complex)

1. Create `ui/pages/sessions.templ` — session list + session detail (Alpine-driven two-mode: list ↔ detail via `x-show`)
2. Create `ui/static/js/pages/sessions.js` — `Alpine.data('sessionsPage', ...)` with list/detail toggle, openSession, message rendering, tool display, system prompt (imports formatTime from utils.js)
3. Update `render.go` — sessions page handler
4. Verify session list, detail view, message transcript, tools section, system prompt display

Files: `ui/pages/sessions.templ`, `ui/static/js/pages/sessions.js`, `render.go`

### Phase 8: Cleanup + polish

1. Delete old files: `ui/index.html`, `ui/sections/*.html`, `ui/js/app.js`
2. Remove `{@include}` assembly code from `embed.go` (assembleHTML, includeRE, assembledUI, assembleOnce, old embed directive)
3. Run `mise run format` then `mise run lint`, fix any issues
4. Final manual test of all 7 pages — CRUD, theme switching, dark mode, mobile nav
5. Verify `server_test.go` passes (existing API tests + new page route tests)
6. Update docs: `README.md` admin section (URL changes), check `docs/content/docs/` for admin references, update builtin anna skill if admin URLs changed

Files: `ui/index.html` (delete), `ui/sections/` (delete), `ui/js/` (delete), `embed.go`, docs as needed

## Testing Strategy

- **Build verification**: `templ generate && go build ./...` passes after every phase
- **Unit tests**: `server_test.go` — existing API endpoint tests preserved, new page route tests (200 + text/html content-type, redirect for `/`)
- **Lint verification**: `mise run lint` passes (check `.golangci.yml` excludes `*_templ.go` generated code)
- **Manual verification** per phase: each page's CRUD operations work via browser
- **ESM loading**: verify no module loading errors in browser console (check Alpine.start() fires after all registrations)
- **Theme check**: custom terra theme renders correctly, theme switcher works, dark/light mode works
- **Mobile**: responsive nav works (daisyUI responsive patterns)
- **Regression check**: all JSON API endpoints still return correct responses

## Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| daisyUI 5 CDN custom theme limitations | Medium | CDN version may not support `@plugin "daisyui/theme"` syntax; use CSS custom properties (`[data-theme="terra"]`) instead, which works with CDN |
| ESM import from esm.sh latency | Low | esm.sh is CDN-backed; for self-hosted without internet, document as known limitation |
| Large session transcript rendering | Low | Keep current approach (Alpine renders client-side from JSON); no architecture change |
| CDN availability for self-hosted | Low | Document that internet access is required for admin UI; future enhancement: vendor deps |
| Linter warnings on templ-generated code | Low | Add `*_templ.go` to `.golangci.yml` exclude if needed |

## Open Questions

None — all questions resolved in discovery and CDN/ESM verification.

## Review Feedback

### Round 1 (Reviewer)

Addressed all items:
- **C1**: Preserved `server_test.go`, added page route tests to Phase 1 (step 12)
- **C2**: Verified exact CDN URLs — daisyUI 5 (`cdn.jsdelivr.net/npm/daisyui@5`), Tailwind 4 (`@tailwindcss/browser@4`), Alpine.js 3 ESM (`esm.sh/alpinejs@3`)
- **C3**: Decided Alpine ESM pattern — import from esm.sh, register stores/data, manual `Alpine.start()`. Documented in Key Decision #3
- **I1**: templ supports `@click` natively — verified. Documented as convention in Key Decision #4
- **I2**: Phase 1 now registers all 7 routes with placeholder pages — no 404s during migration. Documented in Key Decision #8
- **I3**: Embed directive scoped to `//go:embed ui/static` — documented in Key Decision #5
- **I4**: `mise run generate` updated in Phase 1 (step 2), not Phase 8
- **M1**: render.go pattern specified concretely in Key Decision #7
- **M2**: Custom theme via CSS custom properties `[data-theme="terra"]` — works with CDN. Documented in Key Decision #6
- **M3**: golangci-lint check added to Phase 1 (step 13)

### Round 2 (Reviewer)

All Round 1 items verified as properly addressed. 4 minor gaps found and resolved:
- **Gap 1** (blocker): templ CLI availability — added `"github:a-h/templ"` to `[tools]` in `mise.toml` (Phase 1 step 1)
- **Gap 2**: Tailwind browser runtime necessity — added Key Decision #10 confirming both CDN imports are required
- **Gap 3**: Unknown paths behavior — Phase 1 step 15 verifies 404 for unknown paths; Key Decision #8 updated
- **Gap 4**: Page script injection mechanism — added Key Decision #9 with concrete templ + dynamic import pattern
- **Docs**: Phase 8 now has explicit doc update tasks (step 6)
- **Builtin skill sync**: Phase 8 step 6 includes checking anna builtin skill

## Final Status

(Updated after implementation completes)
