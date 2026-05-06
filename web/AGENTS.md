## Web UI

Server-rendered pages using Go templ + Alpine.js 3 + daisyUI 5. CDN-only — no npm, no bundler.

**Stack:** templ (type-safe HTML) · daisyUI 5 + Tailwind 4 (CDN) · Alpine.js 3 ESM (CDN via esm.sh)

### Design system

`web/DESIGN.md` is the visual identity spec for this project. Read it before touching any UI. It defines:
- **Colors** — use the named daisyUI semantic tokens (`primary`, `secondary`, `base-300`, etc.), never raw hex.
- **Typography** — DM Serif for page titles/logo, DM Sans for UI body, JetBrains Mono for all technical/code surfaces.
- **Components** — button variants, card styles, badge usage, elevation rules, and the layout shell.
- **Do's and Don'ts** — what to avoid (bold display text, shadow on inline cards, hardcoded colors).

When adding or editing UI: check `DESIGN.md` for the right token, component pattern, or elevation level first.

### Conventions

- `package web` for layout/components (`web/*.templ`); `package pages` for pages (`web/pages/*.templ`)
- JS files are ESM modules (`import`/`export`); no bundler
- All frontend deps via CDN — never add `package.json`
- `*_templ.go` files are auto-generated — never edit manually
- `mise run generate` after editing any `.templ` file; `mise run format` before committing
- daisyUI classes: `btn`, `input`, `badge`, `card`, `alert`, `toggle`, `collapse` — see https://daisyui.com/components/

### Adding a new page

1. `web/pages/mypage.templ` — package `pages`, use `@web.PageHeader`, `@web.FormField`, etc.
2. `web/static/js/pages/mypage.js` — ESM, `Alpine.data('mypagePage', () => ({ ... }))`
3. Wire up handler + route in `internal/admin/` (see `internal/admin/CLAUDE.md`)
4. Add nav link in `web/navbar.templ` → `navItems` slice
5. `mise run generate`

### Alpine patterns

```javascript
// API calls
import { api } from '/static/js/api.js';
const data = await api('GET', '/api/things');

// Toast notifications
this.$store.toast.show('Saved');
this.$store.toast.show(err.message, 'error');
```

### URL as state

Pages with filters, pagination, view modes, or tabs must encode that state in the URL — not only in Alpine data. This makes pages shareable, bookmarkable, and browser-back-compatible.

**What belongs in the URL:** search queries, filters, pagination, view mode, selected tab.  
**What does not:** unsaved form input, tokens, transient hover/focus state.

```javascript
init() {
  const p = new URLSearchParams(window.location.search);
  this.status = p.get('status') || 'all';   // read; omit param when it equals the default
  this.page   = parseInt(p.get('page')) || 1;
},
pushState() {
  const p = new URLSearchParams();
  if (this.status !== 'all') p.set('status', this.status);
  if (this.page   !== 1)     p.set('page',   this.page);
  const qs = p.toString();
  history.pushState({}, '', qs ? `?${qs}` : location.pathname);
},
```

- Omit parameters that equal the default value
- Use `pushState` for distinct steps; `replaceState` for incremental refinements (e.g. debounced search)
- Debounce frequent updates to avoid flooding browser history
- Parameter names must be self-documenting (`status=active`, not `s=1`)
- Listen for `popstate` to restore state on browser back/forward
