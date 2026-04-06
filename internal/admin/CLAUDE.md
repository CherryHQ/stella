## Admin UI

Server-rendered admin panel using Go templ + Alpine.js 3 + daisyUI 5. CDN-only frontend deps — no npm, no bundler.

### Tech Stack

| Layer | Technology | Role |
|-------|-----------|------|
| HTML | [templ](https://templ.guide) | Go-compiled HTML components, type-safe |
| Styling | [daisyUI 5](https://daisyui.com) + [Tailwind CSS 4](https://tailwindcss.com) | Component classes + utility classes (CDN) |
| Interactivity | [Alpine.js 3](https://alpinejs.dev) | Lightweight reactivity, ESM modules (CDN via esm.sh) |
| API | Go JSON handlers | REST endpoints at `/api/*`, envelope: `{data}` / `{error}` |
| Models cache | `~/.anna/cache/models.json` | Shared between CLI and admin UI, updated on provider model fetch |

### Architecture

```
Browser → GET /providers → Go router → templ renders HTML → CDN loads daisyUI + Tailwind + Alpine ESM
                                                           → Alpine hydrates → page JS fetches /api/* JSON
```

Each page is a **server-rendered route** (not SPA). Alpine.js handles client-side state and API calls.

### Directory Structure

```
internal/admin/
├── server.go           # Route registration, middleware (CORS, JSON content-type)
├── render.go           # Page handlers — thin wrappers calling templ Layout().Render()
├── embed.go            # //go:embed ui/static — serves JS files at GET /static/*
├── models.go           # GET /api/models — reads from models cache (no live API calls)
├── agents.go           # API handlers (unchanged CRUD)
├── channels.go         # ...
├── providers.go        # ...includes updateModelsCache() on model fetch
├── scheduler.go        # ...
├── sessions.go         # ...
├── tools.go            # ...
├── users.go            # ...
├── server_test.go      # API + page route tests
└── ui/
    ├── layout.templ        # HTML shell: CDN links, terra theme, Alpine ESM init, content slot
    ├── navbar.templ        # Nav links, theme switcher, status indicator
    ├── components.templ    # Shared: PageHeader, EmptyState, Badge, FormField
    ├── pages/
    │   ├── providers.templ # One templ file per page
    │   ├── agents.templ
    │   ├── channels.templ
    │   ├── users.templ
    │   ├── sessions.templ
    │   └── scheduler.templ
    └── static/js/
        ├── api.js              # ESM: fetch wrapper — api(method, path, body)
        ├── utils.js            # ESM: formatTime() helper
        ├── stores/
        │   ├── toast.js        # Alpine.store('toast') — global notifications
        │   └── theme.js        # Alpine.store('theme') — theme persistence
        └── pages/
            ├── providers.js    # Alpine.data('providersPage') — one JS module per page
            ├── agents.js
            ├── channels.js
            ├── users.js
            ├── sessions.js
            └── scheduler.js
```

### Key Patterns

**Adding a new page:**

1. Create `ui/pages/mypage.templ` (package `pages`, use `ui.PageHeader`, `ui.FormField`, etc.)
2. Create `ui/static/js/pages/mypage.js` (ESM, export `register(Alpine)` → `Alpine.data('mypagePage', ...)`)
3. Add handler in `render.go`: `func (s *Server) pageMypage(...) { renderPage(w, r, "mypage", "/static/js/pages/mypage.js", pages.MypagePage()) }`
4. Add route in `server.go`: `s.mux.HandleFunc("GET /mypage", s.pageMypage)`
5. Add nav link in `navbar.templ`: append to `navItems` slice
6. Run `templ generate` (or `mise run generate`)

**templ + Alpine.js conventions:**

- templ files use `@click`, `x-data`, `x-show`, `x-for` directly (no escaping needed)
- Page-level Alpine data: `x-data="mypagePage()"` on root `<div>`
- API calls: `import { api } from '/static/js/api.js'` → `await api('GET', '/api/...')`
- Toast: `this.$store.toast.show('Saved')` or `this.$store.toast.show(err.message, 'error')`
- Theme: `$store.theme.set('terra')` — persists to localStorage, sets `data-theme` on `<html>`

**Alpine ESM loading (layout.templ):**

```
import Alpine from esm.sh → register stores → dynamic import page module → Alpine.start()
```

Page script path injected via `<meta name="page-script">` tag, read by the init script.

**daisyUI component classes to use:**

- Buttons: `btn btn-primary`, `btn btn-ghost`, `btn btn-error`, `btn-sm`, `btn-xs`
- Inputs: `input input-bordered`, `select select-bordered`, `textarea textarea-bordered`
- Toggles: `toggle toggle-primary toggle-sm`
- Badges: `badge badge-ghost`, `badge-success`, `badge-error`, `badge-sm`
- Layout: `card card-body`, `collapse`
- Feedback: `alert alert-success`, `loading loading-spinner`
- See https://daisyui.com/components/ for full list

**Custom terra theme:**

Defined via CSS custom properties `[data-theme="terra"]` in `layout.templ`. Colors mapped from the terra/warm palette. Switchable to any of 32+ built-in daisyUI themes via the navbar theme picker.

### API Conventions

- All API routes: `/api/*` with JSON `Content-Type`
- Response envelope: `{"data": ...}` on success, `{"error": "message"}` on failure
- Handler helpers: `writeData(w, status, data)`, `writeError(w, status, msg)`, `decodeJSON(r, &dst)`
- Models cache: `GET /api/models` reads `~/.anna/cache/models.json` (no provider API calls). Cache is updated when "Fetch models" is clicked on the providers page.

### Dev Workflow

```bash
mise run templ:watch        # Watch templ files, auto-regenerate + proxy
mise run build              # Build binary (runs generate first)
anna --open                 # Start admin panel at localhost:8080
```

### Rules

- All frontend deps via CDN — never add package.json or npm
- templ files: `package ui` for layout/components, `package pages` for page templates
- JS files: ESM modules with `import`/`export`, no bundler
- Each page JS exports `register(Alpine)` function
- Keep templ files under 300 lines — split into sub-components if needed
- `*_templ.go` files are auto-generated — never edit manually, excluded from lint
- Always use `mise run <task>` to run tasks — never call templ, golangci-lint, etc. directly
- Run `mise run format` then `mise run lint` before committing
