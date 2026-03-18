# Tasks: Admin UI Migration to templ + daisyUI

## Phase 1: Foundation — templ + daisyUI shell + static serving

- [ ] 1.1 — Add templ to go.mod and mise.toml tools (`go.mod`, `mise.toml`)
- [ ] 1.2 — Update mise run generate to include templ generate (`mise.toml`)
- [ ] 1.3 — Create layout.templ with CDN links, terra theme, content slot, ESM script loader (`ui/layout.templ`)
- [ ] 1.4 — Create navbar.templ with page links, theme switcher, status indicator (`ui/navbar.templ`)
- [ ] 1.5 — Create 7 placeholder page templ files (`ui/pages/*.templ`)
- [ ] 1.6 — Create api.js ESM fetch wrapper (`ui/static/js/api.js`)
- [ ] 1.7 — Create toast.js Alpine store (`ui/static/js/stores/toast.js`)
- [ ] 1.8 — Create theme.js Alpine store (`ui/static/js/stores/theme.js`)
- [ ] 1.9 — Rewrite embed.go with `//go:embed ui/static` and static file serving (`embed.go`)
- [ ] 1.10 — Create render.go with page handler functions (`render.go`)
- [ ] 1.11 — Update server.go with page routes, redirect, static serving (`server.go`)
- [ ] 1.12 — Update server_test.go with page route tests (`server_test.go`)
- [ ] 1.13 — Check golangci-lint compatibility with templ generated code (`.golangci.yml`)
- [ ] 1.14 — Verify build compiles and shell renders in browser
- [ ] 1.15 — Verify unknown paths return 404

## Phase 2: Shared components + Settings page

- [ ] 2.1 — Create components.templ with FormField, EmptyState, Badge, PageHeader (`ui/components.templ`)
- [ ] 2.2 — Create settings.templ page (`ui/pages/settings.templ`)
- [ ] 2.3 — Create settings.js Alpine.data module (`ui/static/js/pages/settings.js`)
- [ ] 2.4 — Update render.go for settings page (`render.go`)
- [ ] 2.5 — Verify settings load/save end-to-end

## Phase 3: Providers page

- [ ] 3.1 — Create providers.templ page (`ui/pages/providers.templ`)
- [ ] 3.2 — Create providers.js Alpine.data module (`ui/static/js/pages/providers.js`)
- [ ] 3.3 — Update render.go for providers page (`render.go`)
- [ ] 3.4 — Verify provider CRUD, model fetching, model list

## Phase 4: Agents page

- [ ] 4.1 — Create utils.js with formatTime, model combo helpers (`ui/static/js/utils.js`)
- [ ] 4.2 — Create agents.templ page (`ui/pages/agents.templ`)
- [ ] 4.3 — Create agents.js Alpine.data module (`ui/static/js/pages/agents.js`)
- [ ] 4.4 — Update render.go for agents page (`render.go`)
- [ ] 4.5 — Verify agent CRUD, model dropdown

## Phase 5: Channels + Users pages

- [ ] 5.1 — Create channels.templ page (`ui/pages/channels.templ`)
- [ ] 5.2 — Create channels.js Alpine.data module (`ui/static/js/pages/channels.js`)
- [ ] 5.3 — Create users.templ page (`ui/pages/users.templ`)
- [ ] 5.4 — Create users.js Alpine.data module (`ui/static/js/pages/users.js`)
- [ ] 5.5 — Update render.go for channels and users pages (`render.go`)
- [ ] 5.6 — Verify channel save, user default agent, memory CRUD

## Phase 6: Scheduler page

- [ ] 6.1 — Create scheduler.templ page (`ui/pages/scheduler.templ`)
- [ ] 6.2 — Create scheduler.js Alpine.data module (`ui/static/js/pages/scheduler.js`)
- [ ] 6.3 — Update render.go for scheduler page (`render.go`)
- [ ] 6.4 — Verify job CRUD, toggle, schedule type switching

## Phase 7: Sessions page

- [ ] 7.1 — Create sessions.templ page (`ui/pages/sessions.templ`)
- [ ] 7.2 — Create sessions.js Alpine.data module (`ui/static/js/pages/sessions.js`)
- [ ] 7.3 — Update render.go for sessions page (`render.go`)
- [ ] 7.4 — Verify session list, detail view, message transcript, tools, system prompt

## Phase 8: Cleanup + polish

- [ ] 8.1 — Delete old UI files (`ui/index.html`, `ui/sections/`, `ui/js/`)
- [ ] 8.2 — Remove {@include} assembly code from embed.go (`embed.go`)
- [ ] 8.3 — Run format and lint, fix issues
- [ ] 8.4 — Final manual test of all 7 pages
- [ ] 8.5 — Verify server_test.go passes
- [ ] 8.6 — Update documentation (README, docs, builtin anna skill)
