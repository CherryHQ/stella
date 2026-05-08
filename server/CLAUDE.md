## HTTP Server

Handles SPA delivery, REST API, auth, and middleware. The web UI is a React SPA — see `web/CLAUDE.md` for frontend conventions.

### Architecture

```
Browser → GET /agents   → Go router → pageApp → SPAPage (web/spa.templ) → React renders
       → GET /api/*     → Go handler → JSON response
       → GET /static/*  → static file handler
```

All page routes are handled by a single wildcard handler (`pageApp`). React Router owns client-side navigation. Go 1.22 ServeMux specificity ensures `/static/` and `/api/*` patterns always win over the `/{path...}` wildcard.

### Route registration (`routes.go`)

```
GET /static/    → static file handler  (most specific)
GET /{$}        → redirectRoot         (exact root — redirects to /providers or /agents)
GET /api/*      → apiserver.HandlerFromMux (all API routes from OpenAPI spec)
GET /{path...}  → pageApp             (wildcard — all page routes including /login)
```

`authMiddleware` (global, wraps the entire mux) exempts `/login`, `/static/`, and auth endpoints. All other page routes require a valid session; unauthenticated requests are redirected to `/login`.

### Directory Structure

```
server/
├── server.go           # Server struct, initialization
├── routes.go           # Route registration
├── render.go           # pageApp — only handler; renders web.SPAPage()
├── middleware.go       # Auth & CORS middleware
├── response.go         # writeData/writeError helpers
├── http.go             # HTTP utilities + redirectRoot
├── models.go           # GET /api/models
├── agents.go           # Agent CRUD API handlers
├── channels.go         # Channel management API
├── providers.go        # LLM provider API
├── scheduler.go        # Job scheduling API
├── sessions.go         # Chat session API
├── skills.go           # Skill management API
├── plugins.go          # Plugin management API
├── users.go            # User management API
├── auth.go             # Login/logout/registration + GetMe
├── oauth.go            # OAuth flow (GitHub, Lark, etc.)
├── vault.go            # Per-user encrypted secrets API
└── server_test.go      # API + page route tests
```

### API Conventions

- All API routes: `/api/*` with JSON `Content-Type`
- All `/api/*` routes are generated from the OpenAPI spec via `apiserver.HandlerFromMux(s, s.mux)` in `routes.go`. Do not hand-register API routes.
- To add a new API route: follow the spec-first workflow in `api/CLAUDE.md` (write spec → `mise run generate:api` → implement the generated `ServerInterface` method).
- Response envelope: `{"data": ...}` on success, `{"error": "message"}` on failure
- Handler helpers: `writeData(w, status, data)`, `writeError(w, status, msg)`, `decodeJSON(r, &dst)`
- `GET /api/auth/me` returns `{ id, username, role, is_admin }` (snake_case `is_admin`)

### Dev Workflow

```bash
mise run build      # Build binary (runs generate first)
anna --open         # Start admin panel at localhost:8080
```

### Adding a new page

No server changes needed for new pages — the wildcard already handles them. See `web/CLAUDE.md` for the full frontend workflow.
