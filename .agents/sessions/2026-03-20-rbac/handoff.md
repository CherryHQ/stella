# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: Auth Foundation — DB Schema + Auth Package

**Status:** Complete
**Date:** 2026-03-20
**Commits:** 4 commits on `main`

### What was done

1. **Schema files** (7 new + 1 modified):
   - `internal/db/schemas/tables/auth_users.sql` — system users with username, password_hash, is_active
   - `internal/db/schemas/tables/auth_roles.sql` — extensible roles (TEXT PK)
   - `internal/db/schemas/tables/auth_user_roles.sql` — user-role many-to-many with CASCADE
   - `internal/db/schemas/tables/auth_identities.sql` — linked channel identities with UNIQUE(platform, external_id)
   - `internal/db/schemas/tables/auth_policies.sql` — ABAC policies with CHECK(effect IN ('allow','deny'))
   - `internal/db/schemas/tables/auth_user_agents.sql` — user-agent binary access with CASCADE
   - `internal/db/schemas/tables/auth_sessions.sql` — HTTP sessions with CASCADE
   - `internal/db/schemas/tables/settings_agents.sql` — added `scope TEXT NOT NULL DEFAULT 'system'`
   - `internal/db/schemas/main.sql` — added atlas:import for all 7 auth tables

2. **Migration**: `internal/db/migrations/20260320104110_add-auth-tables.sql`
   - ALTER TABLE settings_agents ADD scope column
   - CREATE TABLE for all 7 auth tables with FKs, unique indices, and check constraints
   - `atlas.sum` updated and validated

3. **sqlc queries** (7 new files in `internal/db/queries/`):
   - `auth_users.sql` — CRUD + get by username + count
   - `auth_roles.sql` — CRUD + list
   - `auth_user_roles.sql` — assign (ON CONFLICT DO NOTHING) / remove / list roles for user / list users for role
   - `auth_identities.sql` — CRUD + get by platform+external_id + list by user
   - `auth_policies.sql` — CRUD + list enabled
   - `auth_user_agents.sql` — assign / remove / list agents for user / list users for agent
   - `auth_sessions.sql` — create / get / delete / delete expired / delete by user / update expiry

4. **Auth package** (`internal/auth/`):
   - `types.go` — AuthUser, Role, Policy, AccessRequest, Subject, Action (with 6 constants), Resource (with 10 ResourceType constants), Identity, Session, effect constants, role constants
   - `password.go` — HashPassword (bcrypt cost=12), CheckPassword
   - `store.go` — AuthStore interface with 26 methods (users, roles, identities, policies, user-agents, sessions)

5. **AuthStore implementation** (`internal/auth/authdb/`):
   - `store.go` — Full SQLite implementation using sqlc Queries, with time parsing helpers and DB-to-domain converters

6. **Tests**:
   - `internal/auth/password_test.go` — hash, verify, wrong password, empty hash, salt uniqueness
   - `internal/auth/authdb/store_test.go` — CRUD for all 6 entity types, idempotent assigns, unique constraints, cascade delete, interface satisfaction check
   - All tests pass with `-race`

### Notes for next phases

- **`ctx_agent_memory.user_id` FK**: Currently references `settings_users(id)`. This needs attention when handling data migration (not in scope for Phase 1). The FK target will need to change to `auth_users(id)` with a data migration step.
- **`settings_agents.scope`**: Column added to schema and migration. The existing sqlc-generated code now includes `Scope` in `SettingsAgent` model, but existing queries (CreateAgent, UpdateAgent) do not set it — it defaults to `'system'`. The `config.Agent` struct and `agentFromDB` helper do NOT yet map the scope field (deferred to Phase 5 task 5.1).
- **Pre-existing test failures**: Integration tests in `internal/agent/` and `internal/agent/runner/` fail due to missing API keys — these are not related to this phase's changes.

## Phase 2: Policy Engine

**Status:** Complete
**Date:** 2026-03-20
**Commits:** 4 commits on `main`

### What was done

1. **Policy Engine** (`internal/auth/engine.go`):
   - `PolicyEngine` struct holding sorted policies (by priority desc, then ID asc)
   - `NewEngine(ctx, store)` — loads all enabled policies from AuthStore at startup
   - `NewEngineFromPolicies(policies)` — constructor from pre-loaded policies (for testing)
   - `Can(ctx, req) bool` — deny-overrides evaluation
   - `Must(ctx, req) error` — returns `ErrAccessDenied` on denial
   - Policy matching: `matchSubjects` (role intersection, wildcard `*`), `matchActions`, `matchResources`

2. **Condition Evaluator** (`internal/auth/condition.go`):
   - Parses JSON conditions: `{"resource.owner_id": {"eq": "subject.id"}}`
   - Operators: `eq`, `neq`, `in`, `not_in`, `contains`
   - Attribute resolution: `subject.id`, `subject.roles`, `subject.agent_ids`, `resource.type`, `resource.id`, `resource.owner_id`, plus custom attrs via `Attrs` maps
   - Values can be attribute references (prefixed with `subject.` or `resource.`) or literals
   - All conditions AND'd together

3. **Seed** (`internal/auth/seed.go`):
   - `SeedRolesAndPolicies(ctx, store)` — idempotent seeding
   - 2 system roles: `admin`, `user` (both `is_system=true`)
   - 8 built-in policies matching the plan's table (admin full access, user system agents, user assigned agents, user own sessions/data/skills/profile, user view agents list)
   - Unique constraint violations are silently skipped for idempotency

4. **Bootstrap Integration** (`cmd/anna/commands.go`):
   - Added `auth.SeedRolesAndPolicies` call in `setup()` after `store.SeedDefaults`
   - Creates `authdb.Store` from the shared DB connection

5. **Tests**:
   - `internal/auth/condition_test.go` — 15 tests: all operators, attr refs, AND logic, invalid JSON, edge cases
   - `internal/auth/engine_test.go` — 16 tests: deny-overrides, default deny, allow matching, Must, priority ordering, multiple roles, conflicting policies, built-in policy scenarios
   - `internal/auth/seed_test.go` — 3 tests: seed correctness, idempotency (run twice), engine from seeded DB
   - All tests pass with `-race`

### Notes for next phases

- **PolicyEngine is read-only**: Policies loaded once at startup. No reload mechanism. If custom policy UI is added later, add a version counter + reload method.
- **`contains` operator**: Left side resolves to a JSON array string, right side is a scalar. Used for checking if a collection attribute contains a value.
- **`in` operator**: Left side is a scalar, right side resolves to a collection (attribute ref to JSON array or literal array).
- **Built-in policy for assigned agents**: Uses `{"resource.id":{"in":"subject.agent_ids"}}` — the caller must populate `Subject.AgentIDs` from `ListUserAgentIDs` before calling `Can`.

## Phase 3: Admin UI Authentication

**Status:** Complete
**Date:** 2026-03-20
**Commits:** 4 commits on `main`

### What was done

1. **Session management** (`internal/auth/session.go`):
   - `NewSessionID()` — 32 bytes from `crypto/rand`, hex-encoded (64 chars)
   - `SetSessionCookie()` — HttpOnly, SameSite=Lax, Secure when not localhost, Path=/, 7-day MaxAge
   - `ClearSessionCookie()`, `GetSessionCookie()` — cookie helpers
   - Cookie name: `anna_session`

2. **Rate limiting** (`internal/auth/ratelimit.go`):
   - In-memory rate limiter using `sync.Map`
   - Per-IP: max 10 attempts per minute with sliding window
   - Per-username: 30-second cooldown after 5 consecutive failures
   - `CheckIP`, `CheckUsername`, `RecordLoginFailure`, `RecordLoginSuccess`

3. **Login page** (`internal/admin/ui/pages/login.templ`, `internal/admin/ui/static/js/pages/login.js`):
   - Standalone page with `LoginLayout` (no navbar)
   - Login form + register toggle with Alpine.js component
   - Client-side password validation (match, min 8 chars)
   - POST to `/api/auth/login` or `/api/auth/register`, redirect to `/` on success

4. **Auth API handlers** (`internal/admin/auth.go`):
   - `POST /api/auth/register` — validate min 8 char password, hash (bcrypt cost=12), create user, first user gets admin role, set session cookie
   - `POST /api/auth/login` — rate-limit by IP + username, verify password, create DB session, set cookie
   - `POST /api/auth/logout` — delete session from DB, clear cookie
   - `GET /api/auth/me` — return current user info (id, username, roles, is_admin)

5. **Auth middleware** (`internal/admin/middleware.go`):
   - `authMiddleware` — extracts session cookie, loads session from DB (deletes if expired), loads user + roles, injects `AuthInfo` into context, extends session on each request
   - `adminOnlyMiddleware` — checks `IsAdmin`, returns 403 for API routes, redirects to `/agents` for pages
   - `UserFromContext(ctx)` — extracts `AuthInfo` from context
   - Exempt paths: `/login`, `/static/`, `/api/auth/login`, `/api/auth/register`, `/api/auth/logout`

6. **CORS hardening** (`internal/admin/server.go`):
   - Replaced `Access-Control-Allow-Origin: *` with configurable origin from settings key `admin.cors_origin`
   - Default: `http://localhost:8080`
   - Added `Access-Control-Allow-Credentials: true`

7. **Route guards** (`internal/admin/server.go`):
   - Admin-only pages: providers, channels, users, scheduler, settings
   - Admin-only APIs: providers/*, channels/*, users/*, settings/*, scheduler/*
   - Non-admin accessible: agents, sessions, models, tools

8. **Navbar** (`internal/admin/ui/navbar.templ`):
   - Role-based visibility: admin-only items hidden for regular users
   - Shows username + logout button
   - Logo links to `/providers` (admin) or `/agents` (user)

9. **Root redirect** (`internal/admin/server.go`):
   - Unauthenticated -> `/login`
   - Authenticated admin -> `/providers`
   - Authenticated user -> `/agents`

10. **Updated callers** (`cmd/anna/gateway.go`, `cmd/anna/onboard.go`):
    - `admin.New()` now accepts `auth.AuthStore` and `*auth.PolicyEngine`
    - Both callers create `authdb.Store` and `PolicyEngine` before creating admin server

11. **Tests**:
    - `internal/auth/session_test.go` — session ID generation, cookie set/get/clear, missing/empty cookie
    - `internal/auth/ratelimit_test.go` — IP limiting, username cooldown, success reset, below-threshold
    - `internal/admin/auth_test.go` — register, login, logout, /me, password validation, duplicate username, wrong password, first-user admin role, expired session
    - `internal/admin/server_test.go` — updated for auth-aware server: session cookies, admin/non-admin access control, unauthenticated redirects, CORS credentials header
    - All tests pass with `-race`

### Notes for next phases

- **`admin.New()` signature changed**: Now requires `auth.AuthStore` and `*auth.PolicyEngine` as parameters. All callers updated.
- **Layout signature changed**: `ui.Layout()` now takes `username string, isAdmin bool` parameters for navbar rendering.
- **Navbar signature changed**: `ui.Navbar()` now takes `activePage, username string, isAdmin bool`.
- **Auth middleware exempt paths**: Only `/login`, `/static/`, and three specific `/api/auth/` endpoints are exempt. The `/api/auth/me` endpoint goes through the middleware.
- **Session expiry extension**: Each authenticated request extends the session by 7 days (rolling expiry).
- **Lazy session cleanup**: Expired sessions are deleted on each middleware invocation via `DeleteExpiredSessions`.
- **CORS origin**: Reads from settings table key `admin.cors_origin`. Falls back to `http://localhost:8080`. Can be configured via the settings API.
