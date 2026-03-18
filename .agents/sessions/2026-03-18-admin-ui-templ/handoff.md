# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: Foundation — templ + daisyUI shell + static serving

**Status:** complete

**Tasks completed:**
- 1.1: Added `github.com/a-h/templ` v0.3.1001 to go.mod; added `"github:a-h/templ" = "latest"` to mise.toml tools
- 1.2: Updated `mise run generate` to run `templ generate && sqlc generate`
- 1.3: Created `layout.templ` — full HTML shell with daisyUI 5 CSS, themes.css, Tailwind 4 browser runtime, Alpine.js 3 ESM from esm.sh, custom terra theme via `[data-theme="terra"]` CSS custom properties, page-script injection via `<meta name="page-script">` + dynamic import, toast overlay, footer
- 1.4: Created `navbar.templ` — `<a>` links for 7 pages with active highlighting, theme switcher dropdown (terra + 30+ built-in daisyUI themes), status indicator, mobile responsive select
- 1.5: Created 7 placeholder page templ files in `ui/pages/` (providers, agents, channels, users, sessions, scheduler, settings)
- 1.6: Created `api.js` ESM fetch wrapper extracted from app.js
- 1.7: Created `stores/toast.js` — Alpine store with show(msg, type) and auto-dismiss
- 1.8: Created `stores/theme.js` — Alpine store with localStorage persistence and data-theme attribute
- 1.9: Rewrote `embed.go` — `//go:embed ui/static` directive, fs.Sub + http.StripPrefix serving for `/static/*`
- 1.10: Created `render.go` — 7 page handler functions + `renderPage()` helper that sets Content-Type and calls `ui.Layout().Render()`
- 1.11: Updated `server.go` — `GET /{$}` redirects to `/providers`, 7 `GET /{page}` routes, `GET /static/` serves embedded JS; all API routes unchanged
- 1.12: Updated `server_test.go` — TestRootRedirect (302 to /providers), TestPageRoutes (7 pages return 200 + text/html), TestUnknownPathReturns404
- 1.13: Added `exclude-files: ".*_templ\\.go$"` to `.golangci.yml` for templ generated code
- 1.14: Verified `templ generate && go build ./...` succeeds; `golangci-lint run` returns 0 issues
- 1.15: Verified `GET /nonexistent` returns 404 (tested in TestUnknownPathReturns404)

**Files changed:**
- `go.mod` / `go.sum` — added templ dependency
- `mise.toml` — added templ tool, updated generate task
- `.golangci.yml` — added templ generated file exclusion
- `internal/admin/ui/layout.templ` — new: HTML shell component
- `internal/admin/ui/navbar.templ` — new: navigation bar component
- `internal/admin/ui/pages/*.templ` — new: 7 placeholder page components
- `internal/admin/ui/static/js/api.js` — new: ESM fetch wrapper
- `internal/admin/ui/static/js/stores/toast.js` — new: Alpine toast store
- `internal/admin/ui/static/js/stores/theme.js` — new: Alpine theme store
- `internal/admin/embed.go` — rewritten: `//go:embed ui/static` + staticHandler()
- `internal/admin/render.go` — new: page handler functions
- `internal/admin/server.go` — updated: page routes, redirect, static serving
- `internal/admin/server_test.go` — updated: added page route + redirect + 404 tests

**Commits:**
- `3f1ed40` — 🔧 chore: add templ dependency and tooling
- `b5516f1` — ✨ feat: add templ + daisyUI admin UI foundation (Phase 1)

**Decisions & context for next phase:**
- Page templ files are in `package pages` under `internal/admin/ui/pages/`; layout/navbar are in `package ui` under `internal/admin/ui/`
- Page script injection uses `<meta name="page-script" content="...">` tag in `<head>`, read by the Alpine init `<script type="module">` via `document.querySelector('meta[name="page-script"]')` + dynamic `import()`. For placeholder pages, pageScript is `""` and the meta tag is omitted.
- The `AlpineInit()` templ component renders the ESM init script as a separate templ component called from Layout — this keeps the script block clean
- Theme switcher calls `$store.theme.set('name')` which sets `data-theme` on `<html>` and persists to localStorage key `anna-theme`
- Toast uses `$store.toast.show(msg, type)` — Alpine store with visibility flag and auto-dismiss timeout
- Old `serveUI`, `assembleHTML`, `assembledUI`, `includeRE` code was fully removed from embed.go — replaced with clean `//go:embed ui/static` + `staticHandler()`. Old `ui/index.html`, `ui/sections/`, `ui/js/` still exist on disk but are no longer embedded or served — deletion deferred to Phase 8
- The `GET /{$}` pattern matches only exact root `/`, so unknown paths like `/nonexistent` fall through to Go's default 404
- render.go handlers pass empty `pageScript` for now; Phase 2+ will set real paths like `"/static/js/pages/settings.js"`

## Phase 2: Shared components + Settings page

**Status:** complete

**Tasks completed:**
- 2.1: Created `components.templ` with four shared daisyUI components: `PageHeader(title, subtitle)`, `EmptyState(message)`, `Badge(text, variant)` with variant helper, and `FormField(label)` using `{ children... }` slot
- 2.2: Replaced settings.templ placeholder with full settings page — uses `x-data="settingsPage()"`, imports `ui.PageHeader`, iterates `settingsKeys` with `x-for`, textarea (`textarea textarea-bordered`) + save button (`btn btn-primary btn-sm`) per key
- 2.3: Created `settings.js` ESM module — exports `register(Alpine)` that registers `Alpine.data('settingsPage', ...)` with `loadSettings()` (fetches each key via `api('GET', '/api/settings/' + key)`) and `saveSetting(key)` (parses JSON, calls `api('PUT', ...)`, shows toast)
- 2.4: Updated `render.go` — `pageSettings` handler now passes `"/static/js/pages/settings.js"` as `pageScript`
- 2.5: Verified: `templ generate && go build ./...` succeeds, `go test -race ./internal/admin/...` passes, `mise run lint` reports 0 issues

**Files changed:**
- `internal/admin/ui/components.templ` — new: shared daisyUI components (PageHeader, EmptyState, Badge, FormField)
- `internal/admin/ui/pages/settings.templ` — rewritten: full settings editor replacing placeholder
- `internal/admin/ui/static/js/pages/settings.js` — new: Alpine.data ESM module for settings
- `internal/admin/render.go` — updated: settings handler passes pageScript path

**Commits:**
- `e579077` — shared daisyUI components
- `7419863` — settings page implementation with templ + Alpine.js ESM

**Decisions & context for next phase:**
- `PageHeader` takes two string params (title, subtitle) — subtitle renders conditionally when non-empty
- `Badge` uses a `badgeVariantClass()` helper function for mapping variant string to daisyUI class
- `FormField` uses `{ children... }` templ slot pattern for wrapping arbitrary input content
- Settings page JS uses `$store.toast.show()` for success/error feedback — same pattern for all future pages
- The `pages/` directory under `ui/static/js/` now exists for page-specific ESM modules
- Pattern established: each page's `.js` exports `register(Alpine)` which calls `Alpine.data('xyzPage', () => ({...}))`

## Phase 3: Providers page

**Status:** complete

**Tasks completed:**
- 3.1: Replaced providers.templ placeholder with full providers page — uses `x-data="providersPage()"`, imports `ui.PageHeader`, `ui.EmptyState`, `ui.FormField`; select dropdown for adding providers, provider cards with API key (password input) + base URL fields, save/delete buttons, fetch models with loading spinner, collapsible model list with badges, inline confirm dialog for delete
- 3.2: Created `providers.js` ESM module — exports `register(Alpine)` registering `Alpine.data('providersPage', ...)` with `providerDefaults` (anthropic, openai, openai-response), computed `addableProviders`, CRUD methods (`loadProviders`, `addProvider`, `saveProvider`, `doDeleteProvider`), `fetchModels` with loading state, `confirmDelete` pattern
- 3.3: Updated `render.go` — `pageProviders` handler now passes `"/static/js/pages/providers.js"` as `pageScript`
- 3.4: Verified: `templ generate && go build ./...` succeeds, `go test -race ./internal/admin/...` passes, `mise run lint` reports 0 issues

**Files changed:**
- `internal/admin/ui/pages/providers.templ` — rewritten: full providers page replacing placeholder
- `internal/admin/ui/static/js/pages/providers.js` — new: Alpine.data ESM module for providers
- `internal/admin/render.go` — updated: providers handler passes pageScript path

**Commits:**
- `ece5740` — providers page with templ + daisyUI (Phase 3)

**Decisions & context for next phase:**
- Confirm dialog is implemented inline within the providers page `x-data` scope (confirmMsg, confirmAction, confirmDelete method) — not shared via layout. Future pages needing delete confirmation should use the same inline pattern
- `providerDefaults` is a module-level constant in providers.js — maps provider ID to `{ base_url, name }` for anthropic, openai, openai-response
- Model list uses `badge badge-ghost badge-sm` for each model name — visual distinction from plain text
- Fetch models button shows `loading loading-spinner loading-xs` spinner during fetch — daisyUI loading component
- After delete, `newProviderType` is refreshed from `addableProviders` so the deleted provider reappears in the add dropdown
- The `providerModels` object is keyed by provider ID — same structure as old app.js, used by agents page for model combo dropdowns (Phase 4 will need access to this data, but agents page loads its own provider list independently)

## Phase 4: Agents page

**Status:** complete

**Tasks completed:**
- 4.1: Created `utils.js` ESM module with `formatTime()` (relative time formatting), `allProviderModels()` (deduplicated provider/model list builder), and `filteredProviderModels()` (search filter helper)
- 4.2: Replaced agents.templ placeholder with full agents page — uses `x-data="agentsPage()"`, imports `ui.PageHeader`, `ui.EmptyState`, `ui.FormField`; add/edit form with ID, name, three model combo dropdowns (default, strong, fast) with autocomplete, workspace, system prompt textarea, enabled toggle (daisyUI `toggle`), save/cancel; agent list with name, ID badge, enabled status badge, model display, edit/delete buttons on hover; inline confirm dialog
- 4.3: Created `agents.js` ESM module — exports `register(Alpine)` registering `Alpine.data('agentsPage', ...)` with agent CRUD methods, provider+model loading (fetches providers then models in parallel for autocomplete), `allModels()` and `filteredModels()` helpers that delegate to utils.js, inline confirm dialog pattern
- 4.4: Updated `render.go` — `pageAgents` handler now passes `"/static/js/pages/agents.js"` as `pageScript`
- 4.5: Verified: `templ generate && go build ./...` succeeds, `go test -race ./internal/admin/...` passes, `mise run lint` reports 0 issues

**Files changed:**
- `internal/admin/ui/static/js/utils.js` — new: shared ESM utilities (formatTime, model helpers)
- `internal/admin/ui/pages/agents.templ` — rewritten: full agents page replacing placeholder
- `internal/admin/ui/static/js/pages/agents.js` — new: Alpine.data ESM module for agents
- `internal/admin/render.go` — updated: agents handler passes pageScript path

**Commits:**
- `543594d` — agents page with templ + daisyUI (Phase 4)

**Decisions & context for next phase:**
- `utils.js` exports `formatTime`, `allProviderModels`, `filteredProviderModels` — reusable by sessions page (Phase 7) and any future page needing relative time or model lists
- Model combo dropdowns use nested `x-data="{ open: false, search: '' }"` scopes inside the parent `agentsPage()` scope — the nested scope accesses parent methods (`allModels()`, `filteredModels()`, `form`) via Alpine's scope chain
- The `modelComboField` templ component is a helper within `pages/agents.templ` — renders a labeled input with autocomplete dropdown for a given form field name
- Agents page independently loads providers + fetches their models on init (does not share state with providers page since they are separate routes/page loads)
- Confirm dialog pattern is identical to providers page: inline `confirmMsg`, `confirmAction`, `confirmDelete()` method
- Agent enabled status uses `badge-success` (on) / `badge-ghost` (off) daisyUI badges
- Edit/delete buttons use `opacity-0 group-hover:opacity-100` for clean hover reveal

## Phase 5: Channels + Users pages

**Status:** complete

**Tasks completed:**
- 5.1: Replaced channels.templ placeholder with full channels page — uses `x-data="channelsPage()"`, imports `ui.PageHeader`, `ui.FormField`; three platform blocks (Telegram, QQ, Feishu) each with enable toggle (`toggle toggle-primary`), conditional config form (`x-show`), platform-specific fields, save button; reusable `channelBlock` templ helper component with `{ children... }` slot for platform enable/disable + content reveal pattern
- 5.2: Created `channels.js` ESM module — exports `register(Alpine)` registering `Alpine.data('channelsPage', ...)` with `channelData` object (telegram/qq/feishu configs), `loadChannels()` (parses JSON config per platform), `saveChannel(platform)` (serializes config, calls PUT API), `parseAllowedIds(str, numeric)` and `formatAllowedIds(arr)` helpers for comma-separated ID handling (Telegram uses numeric IDs, QQ/Feishu use string IDs)
- 5.3: Replaced users.templ placeholder with full users page — uses `x-data="usersPage()"`, imports `ui.PageHeader`, `ui.EmptyState`; user list with platform badge, name, external_id; default agent dropdown per user with save button; collapsible memory section with memory list (agent_id, updated_at, textarea, save/delete buttons), add memory form (agent dropdown filtering already-assigned agents, textarea, add/cancel), empty state; inline confirm dialog for memory deletion
- 5.4: Created `users.js` ESM module — exports `register(Alpine)` registering `Alpine.data('usersPage', ...)` with user + agent loading (parallel init), memory CRUD (`loadUserMemories`, `saveUserMemory`, `doDeleteUserMemory`, `addUserMemory`), `toggleUserMemory` for expand/collapse, `saveUserDefaultAgent`, inline confirm dialog pattern
- 5.5: Updated `render.go` — `pageChannels` passes `"/static/js/pages/channels.js"`, `pageUsers` passes `"/static/js/pages/users.js"` as `pageScript`
- 5.6: Verified: `templ generate && go build ./...` succeeds, `go test -race ./internal/admin/...` passes, `mise run lint` reports 0 issues

**Files changed:**
- `internal/admin/ui/pages/channels.templ` — rewritten: full channels page replacing placeholder
- `internal/admin/ui/static/js/pages/channels.js` — new: Alpine.data ESM module for channels
- `internal/admin/ui/pages/users.templ` — rewritten: full users page replacing placeholder
- `internal/admin/ui/static/js/pages/users.js` — new: Alpine.data ESM module for users
- `internal/admin/render.go` — updated: channels and users handlers pass pageScript paths

**Commits:**
- `23adfed` — channels page with templ + daisyUI (Phase 5)
- `8806a43` — users page with templ + daisyUI (Phase 5)
- `51d6436` — wire channels and users page scripts in render.go

**Decisions & context for next phase:**
- Channels page uses a reusable `channelBlock(name, platform)` templ helper with `{ children... }` slot — renders the enable toggle header and conditional content reveal; platform-specific fields are passed as children
- Channel status uses `badge-success` (on) / `badge-ghost` (off) daisyUI badges, same pattern as agents page
- `parseAllowedIds` has a `numeric` boolean parameter — Telegram uses numeric IDs (`map(Number)`), QQ/Feishu use string IDs
- `channelData` is a nested object with per-platform configs; `saveChannel(platform)` destructures to separate `enabled` from the config JSON before calling the API
- Users page loads agents list independently (for default agent dropdown and memory agent dropdown) — same approach as agents page loading providers independently
- Memory section uses the `toggleUserMemory(u)` pattern to lazy-load memories on first expand
- Add memory form filters out agents that already have a memory for that user — `agents.filter(a => !(userMemories[u.id] || []).some(m => m.agent_id === a.id))`
- Confirm dialog pattern is identical to agents/providers pages: inline `confirmMsg`, `confirmAction`, `confirmDelete()` method — used for memory deletion

## Phase 6: Scheduler page

**Status:** complete

**Tasks completed:**
- 6.1: Replaced scheduler.templ placeholder with full scheduler page — uses `x-data="schedulerPage()"`, imports `ui.PageHeader`, `ui.EmptyState`, `ui.FormField`; job creation/edit form with name input, session mode dropdown (`select select-bordered`), schedule type radio (`radio radio-primary`) for cron vs interval with conditional inputs, agent dropdown (loaded from API), message textarea (`textarea textarea-bordered`), enabled toggle (`toggle toggle-primary`); job list with name, status badge (`badge-success`/`badge-ghost`), cron/every display, session_mode and agent_id badges; hover actions for toggle/edit/delete; inline confirm dialog for delete; empty state
- 6.2: Created `scheduler.js` ESM module — exports `register(Alpine)` registering `Alpine.data('schedulerPage', ...)` with `loadJobs()`, `loadAgents()` (parallel init), `resetJobForm()`, `editJob()`, `saveJob()` (create/update), `toggleJob()`, `doDeleteJob()`, `confirmDelete()` pattern
- 6.3: Updated `render.go` — `pageScheduler` handler now passes `"/static/js/pages/scheduler.js"` as `pageScript`
- 6.4: Verified: `templ generate && go build ./...` succeeds, `go test -race ./internal/admin/...` passes, `mise run lint` reports 0 issues

**Files changed:**
- `internal/admin/ui/pages/scheduler.templ` — rewritten: full scheduler page replacing placeholder
- `internal/admin/ui/static/js/pages/scheduler.js` — new: Alpine.data ESM module for scheduler
- `internal/admin/render.go` — updated: scheduler handler passes pageScript path

**Commits:**
- `e3c8ad8` — scheduler page with templ + daisyUI (Phase 6)

**Decisions & context for next phase:**
- Scheduler page loads agents independently (for agent dropdown) — same pattern as other pages
- Job form uses `schedule_type` field ('cron' or 'every') to conditionally show either cron expression or interval duration input — the non-active field is cleared when building the API payload
- `saveJob()` builds payload from `jobForm`, sending only the active schedule field (cron or every), clearing the other
- `toggleJob()` sends full job data with `enabled` flipped — same approach as old app.js
- Confirm dialog pattern is identical to all other pages: inline `confirmMsg`, `confirmAction`, `confirmDelete()` method
- Job list items show session_mode and agent_id as `badge-ghost badge-xs` badges below the message preview

## Phase 7: Sessions page

**Status:** complete

**Tasks completed:**
- 7.1: Replaced sessions.templ placeholder with full sessions page — two-mode UI (list vs detail) controlled by Alpine `x-show` on `sessionDetail`. List mode shows clickable session rows with title, channel, agent_id, archived badge, relative time. Detail mode shows session metadata header, collapsible tools section (loaded from `/api/tools`), collapsible system prompt with token estimate, and full message transcript.
- 7.2: Created `sessions.js` ESM module — exports `register(Alpine)` registering `Alpine.data('sessionsPage', ...)` with `loadSessions()`, `openSession()` (fetches detail + messages + system prompt in parallel), `loadTools()`, `backToList()`, `formatTime()` delegating to utils.js. Most logic-heavy page JS with parallel API calls and multiple view states.
- 7.3: Updated `render.go` — `pageSessions` handler now passes `"/static/js/pages/sessions.js"` as `pageScript`
- 7.4: Verified: `templ generate && go build ./...` succeeds, `go test -race ./internal/admin/...` passes, `mise run lint` reports 0 issues

**Files changed:**
- `internal/admin/ui/pages/sessions.templ` — rewritten: full sessions page replacing placeholder
- `internal/admin/ui/static/js/pages/sessions.js` — new: Alpine.data ESM module for sessions
- `internal/admin/render.go` — updated: sessions handler passes pageScript path

**Commits:**
- `001964a` — sessions page with templ + daisyUI (Phase 7)

**Decisions & context for next phase:**
- Sessions page is the most complex page with two Alpine-driven modes (list vs detail) — no server-side routing between them, just `x-show` toggling on `sessionDetail`
- Message rendering handles three roles: user (simple text), assistant (blocks: thinking/text/tool_call), tool (results with success/error styling)
- Thinking blocks support redaction detection (`block.redacted`) and show token estimate for long content
- Tool call blocks show tool name + last 6 chars of call ID, with collapsible JSON arguments
- Tool result blocks use daisyUI `border-error`/`border-success` for error/success styling, with collapsible content and token/char count
- Tools section loads from `/api/tools` on init (parallel with sessions), displays by category badge
- System prompt section shows token estimate (`chars / 4`) and is independently collapsible via `showSystemPrompt` flag
- `backToList()` resets all detail state including `showTools` and `showSystemPrompt`
- `openSession()` uses `encodeURIComponent` for session IDs that may contain special characters
- daisyUI semantic colors used throughout: `text-primary` for Anna label, `text-secondary` for metadata, `text-success`/`text-error` for tool results, `badge-ghost` for category and archived badges

## Phase 8: Cleanup + polish (FINAL)

**Status:** complete

**Tasks completed:**
- 8.1: Deleted old SPA UI files — `ui/index.html`, `ui/sections/*.html` (7 files), `ui/js/app.js`, and the now-empty `sections/` and `js/` directories. Total: 1244 lines removed.
- 8.2: Verified embed.go is already clean — the old `{@include}` assembly code (`assembleHTML`, `includeRE`, `assembledUI`, `assembleOnce`, old `serveUI`) was fully removed in Phase 1. No changes needed.
- 8.3: Ran `mise run format` and `mise run lint` — 0 issues found. No fixes needed.
- 8.4: Verified `templ generate && go build ./...` succeeds. All 7 page routes confirmed via test suite (TestPageRoutes).
- 8.5: Ran `go test -race ./internal/admin/...` — all tests pass (TestListProviders, TestCreateProvider, TestListAgents, TestCreateAgent, TestRootRedirect, TestPageRoutes, TestUnknownPathReturns404, TestCORSPreflight).
- 8.6: Checked documentation — README.md, docs/content/docs/, and builtin anna skill all reference the admin panel generically ("admin panel", "anna onboard"). No old hash-based SPA URLs (like `/#providers`) found anywhere. No documentation updates needed.

**Files changed:**
- `internal/admin/ui/index.html` — deleted
- `internal/admin/ui/sections/*.html` (7 files) — deleted
- `internal/admin/ui/js/app.js` — deleted

**Commits:**
- `5c18028` — delete old SPA UI files (Phase 8)

**Migration complete.** All 8 phases are done. The admin UI is now fully server-rendered via templ components with daisyUI 5 styling, Alpine.js 3 ESM modules for interactivity, and clean server routes (`/providers`, `/agents`, etc.) replacing the old SPA tab-switching pattern.
