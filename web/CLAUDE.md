## Web UI

Server-rendered web UI using Go templ + Alpine.js 3 + daisyUI 5. CDN-only frontend deps — no npm, no bundler.

### Tech Stack

| Layer | Technology | Role |
|-------|-----------|------|
| HTML | [templ](https://templ.guide) | Go-compiled HTML components, type-safe |
| Styling | [daisyUI 5](https://daisyui.com) + [Tailwind CSS 4](https://tailwindcss.com) | Component classes + utility classes (CDN) |
| Interactivity | [Alpine.js 3](https://alpinejs.dev) | Lightweight reactivity, ESM modules (CDN via esm.sh) |

### Architecture

```
Browser → GET /providers → Go router (internal/admin) → templ renders HTML → CDN loads daisyUI + Tailwind + Alpine ESM
                                                                            → Alpine hydrates → page JS fetches /api/* JSON
```

Each page is a **server-rendered route** (not SPA). Alpine.js handles client-side state and API calls.

### Directory Structure

```
web/
├── embed.go                # //go:embed static — exports StaticHandler() for GET /static/*
├── layout.templ            # HTML shell: CDN links, terra theme, Alpine ESM init, content slot
├── navbar.templ            # Nav links, theme switcher, status indicator
├── components.templ        # Shared: PageHeader, EmptyState, Badge, FormField
├── loginlayout.templ       # Login-page layout (no navbar)
├── skills_drawer.templ     # Skills drawer component
├── pages/
│   ├── providers.templ     # One templ file per page
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

1. Create `web/pages/mypage.templ` (package `pages`, use `@web.PageHeader`, `@web.FormField`, etc.)
2. Create `web/static/js/pages/mypage.js` (ESM, export `register(Alpine)` → `Alpine.data('mypagePage', ...)`)
3. Add handler in `internal/admin/render.go`: `func (s *Server) pageMypage(...) { renderPage(w, r, "mypage", "/static/js/pages/mypage.js", pages.MypagePage()) }`
4. Add route in `internal/admin/routes.go`: `s.mux.HandleFunc("GET /mypage", s.pageMypage)`
5. Add nav link in `web/navbar.templ`: append to `navItems` slice
6. Run `mise run generate`

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

**URL as state:**

Pages with filters, pagination, view modes, or selected tabs must encode that state in the URL — not only in Alpine component data. This makes pages shareable, bookmarkable, and browser-back-compatible.

What belongs in the URL: search queries, filters, pagination, view modes, date ranges, selected tabs.  
What does not: unsaved form input, passwords/tokens, high-frequency transient states (e.g. hover).

Pattern for an Alpine page component:

```javascript
// On init — read URL into local state
init() {
  const params = new URLSearchParams(window.location.search);
  this.status = params.get('status') || 'all';   // omit default from URL
  this.page   = parseInt(params.get('page')) || 1;
},

// On change — push URL without reloading
pushState() {
  const params = new URLSearchParams();
  if (this.status !== 'all') params.set('status', this.status);
  if (this.page   !== 1)     params.set('page',   this.page);
  const qs = params.toString();
  window.history.pushState({}, '', qs ? `?${qs}` : window.location.pathname);
},

// On browser back/forward — restore state from URL
mounted() {
  window.addEventListener('popstate', () => {
    const params = new URLSearchParams(window.location.search);
    this.status = params.get('status') || 'all';
    this.page   = parseInt(params.get('page')) || 1;
    this.load();
  });
},
```

Rules:
- Omit parameters that equal the default value — keep URLs clean
- Use `pushState` for distinct navigation steps; `replaceState` for incremental refinements (e.g. debounced search input)
- Debounce frequent updates (search-as-you-type) to avoid flooding browser history
- Parameter names must be self-documenting (`status=active`, not `s=1`)

**Custom terra theme:**

Defined via CSS custom properties `[data-theme="terra"]` in `layout.templ`. Colors mapped from the terra/warm palette. Switchable to any of 32+ built-in daisyUI themes via the navbar theme picker.

### Dev Workflow

```bash
mise run templ:watch        # Watch templ files, auto-regenerate + proxy
mise run generate           # Regenerate all templ output after editing .templ files
```

### Rules

- All frontend deps via CDN — never add package.json or npm
- templ files: `package web` for layout/components (in `web/`), `package pages` for page templates (in `web/pages/`)
- JS files: ESM modules with `import`/`export`, no bundler
- Each page JS exports `register(Alpine)` function
- Pages with filters, pagination, or view modes must encode that state in the URL (see URL-as-state pattern above)
- Keep templ files under 300 lines — split into sub-components if needed
- `*_templ.go` files are auto-generated — never edit manually, excluded from lint
- Always use `mise run <task>` to run tasks — never call templ, golangci-lint, etc. directly
- Run `mise run format` before committing
