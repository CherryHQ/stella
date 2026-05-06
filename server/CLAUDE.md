## HTTP Server

Handles page rendering, REST API, auth, and middleware. Web UI templates and static assets live in `web/` — see `web/CLAUDE.md` for frontend conventions.

### Architecture

```
Browser → GET /providers → Go router → templ renders HTML (from web/ package)
       → GET /api/*     → Go handler → JSON response
```

Each page handler in `render.go` wraps a `web/pages` component with `web.Layout()` and streams it to the response.

### Directory Structure

```
server/
├── server.go           # Server struct, initialization
├── routes.go           # Route registration
├── render.go           # Page handlers — thin wrappers calling web.Layout().Render()
├── middleware.go       # Auth & CORS middleware
├── response.go         # writeData/writeError helpers
├── http.go             # HTTP utilities
├── models.go           # GET /api/models — reads from models cache (no live API calls)
├── agents.go           # Agent CRUD API handlers
├── channels.go         # Channel management API
├── providers.go        # LLM provider API; includes updateModelsCache() on model fetch
├── scheduler.go        # Job scheduling API
├── sessions.go         # Chat session API
├── skills.go           # Skill management API
├── plugins.go          # Plugin management API
├── users.go            # User management API
├── auth.go             # Login/logout/registration handlers
├── oauth.go            # OAuth flow (GitHub, Lark, etc.)
├── vault.go            # Per-user encrypted secrets API
└── server_test.go      # API + page route tests
```

### API Conventions

- All API routes: `/api/*` with JSON `Content-Type`
- Response envelope: `{"data": ...}` on success, `{"error": "message"}` on failure
- Handler helpers: `writeData(w, status, data)`, `writeError(w, status, msg)`, `decodeJSON(r, &dst)`
- Models cache: `GET /api/models` reads `~/.anna/cache/models.json` (no provider API calls). Cache is updated when "Fetch models" is clicked on the providers page.

### Dev Workflow

```bash
mise run build      # Build binary (runs generate first)
anna --open         # Start admin panel at localhost:8080
```

### Adding a new page

See `web/CLAUDE.md` for the full workflow. Summary:

1. Create `web/pages/mypage.templ` + `web/static/js/pages/mypage.js`
2. Add handler in `render.go`: `func (s *Server) pageMypage(...) { renderPage(w, r, "mypage", "/static/js/pages/mypage.js", pages.MypagePage()) }`
3. Add route in `routes.go`: `s.mux.HandleFunc("GET /mypage", s.pageMypage)`
4. Run `mise run generate`
