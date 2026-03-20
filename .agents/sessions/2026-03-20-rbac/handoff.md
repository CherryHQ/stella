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

## Phase 4: User Profile + Channel Linking

**Status:** Complete
**Date:** 2026-03-20
**Commits:** 4 commits on `main`

### What was done

1. **LinkCodeStore** (`internal/auth/linkcode.go`):
   - In-memory `sync.Map`-based store for 6-char alphanumeric link codes
   - `Generate(userID, platform) string` — creates code with 5-min TTL
   - `Consume(code) (userID, platform, ok)` — single-use consumption with expiry check
   - `IsLinkCode(s) bool` — quick format check (6 alphanumeric chars)
   - Codes are uppercase hex from `crypto/rand`

2. **Profile page** (`internal/admin/ui/pages/profile.templ`, `internal/admin/ui/static/js/pages/profile.js`):
   - Password change form (current password, new password, confirm)
   - Linked identities list with unlink button per identity
   - Link code generation buttons for Telegram, QQ, Feishu
   - Shows generated code with platform-specific instructions
   - Alpine.js component with `api()` helper for all operations

3. **Profile API handlers** (`internal/admin/profile.go`):
   - `GET /api/auth/profile/identities` — list linked identities for current user
   - `PUT /api/auth/profile/password` — change password (verify current, validate min 8 chars, max 72)
   - `POST /api/auth/profile/link-code` — generate link code for platform (telegram/qq/feishu)
   - `DELETE /api/auth/profile/identities/{id}` — unlink identity (ownership verification)

4. **Routes and navigation** (`internal/admin/server.go`, `internal/admin/render.go`, `internal/admin/ui/navbar.templ`):
   - `GET /profile` page route (accessible to all authenticated users)
   - Profile API routes under `/api/auth/profile/`
   - `LinkCodes()` accessor on Server for channel handlers
   - Username in navbar is now a clickable link to `/profile`
   - `LinkCodeStore` created once in `New()` and stored on Server

5. **Channel link code interception** (`internal/channel/linkcode.go`, telegram/qq/feishu handlers):
   - Shared `TryLinkCode()` function: checks code format, consumes, verifies platform match, creates `auth_identity`
   - `WithAuth(authStore, linkCodes)` BotOption added to telegram, qq, feishu
   - Each handler intercepts 6-char alphanumeric messages before command processing
   - Platform mismatch detection (code for telegram sent to qq returns error)
   - Already-linked accounts detected and reported

6. **Auth-aware identity resolution** (`internal/channel/identity.go`, `internal/channel/resolved.go`):
   - New `ResolvedIdentity` type with `AuthUserID` and `Roles` fields
   - `ResolveUserWithAuth()`: looks up `auth_identities` first, falls back to `settings_users`
   - Auto-migration: when `settings_users` record exists but no `auth_identity`, creates `auth_user` (username=`{platform}_{externalID}`, random password, `user` role) and links identity
   - `ResolveWithAuth()`: full auth-aware resolution path
   - `ResolvedChat` extended with `AuthUserID` and `Roles` fields
   - Each channel bot uses `ResolveWithAuth` when `authStore` is configured, falls back to legacy `Resolve`

7. **Tests**:
   - `internal/auth/linkcode_test.go` — generate, consume, single-use, case-insensitive, uniqueness, IsLinkCode, multiple platforms
   - `internal/admin/profile_test.go` — list identities (empty/with link), change password (success/wrong/short), generate link code (valid/invalid platform), unlink identity (own/other user), profile page route
   - `internal/channel/identity_test.go` — auto-migration, linked identity lookup, idempotency, TryLinkCode (success/wrong platform/invalid code/non-code text)
   - All tests pass with `-race`

### Notes for next phases

- **Channel bots need `WithAuth` option**: Callers that create channel bots (in `cmd/anna/gateway.go`) must pass `WithAuth(authStore, linkCodes)` to enable link code interception and auth-aware identity resolution. Without it, bots fall back to legacy behavior.
- **`LinkCodes()` accessor on Server**: The admin Server exposes its `LinkCodeStore` via `LinkCodes()` so that `gateway.go` can pass it to channel bots.
- **`ResolvedChat` extended**: Now carries `AuthUserID` and `Roles` — Phase 5 can use these for agent access enforcement.
- **Auto-migration username format**: `{platform}_{externalID}` (e.g., `telegram_12345`). Auto-migrated users get a random password and the `user` role. They can set a real password via admin UI later.
- **Backward compatibility preserved**: All existing code paths work without auth. `Resolve()` still works as before. `ResolveWithAuth()` is only called when `authStore` is non-nil.
- **settings_users still used**: Even in the auth-aware path, `store.UpsertUser()` is called for backward compat (sessions, memories still reference `settings_users.id`). The `config.User` record is still the primary user object in `ResolvedChat`.

## Phase 5: Agent Scoping + Access Enforcement

**Status:** Complete
**Date:** 2026-03-20
**Commits:** 4 commits on `main`

### What was done

1. **Scope field on Agent** (`internal/config/store.go`, `internal/config/dbstore.go`):
   - Added `Scope string` field to `config.Agent` struct
   - Added `AgentScopeSystem` and `AgentScopeRestricted` constants
   - Updated `agentFromDB` helper to map the scope column (defaults to `"system"`)
   - Updated `CreateAgent` and `UpdateAgent` to include scope in DB writes
   - Updated sqlc queries in `internal/db/queries/settings_agents.sql`
   - Updated `SeedDefaults` to set scope on the default anna agent

2. **Agent user assignment API** (`internal/admin/agents.go`, `internal/admin/server.go`):
   - `GET /api/agents/{id}/users` — list users assigned to an agent (admin-only, returns id + username)
   - `POST /api/agents/{id}/users` — assign user to agent (admin-only, body: `{"user_id": N}`)
   - `DELETE /api/agents/{id}/users/{userId}` — unassign user from agent (admin-only)
   - Routes registered in `server.go` behind `adminOnlyMiddleware`

3. **Policy engine integration in admin API** (`internal/admin/agents.go`):
   - `listAgents`: non-admin users get filtered results — only system-scoped + assigned agents
   - `getAgent`: non-admin users get 403 for restricted agents they are not assigned to
   - `filterAccessibleAgents()` and `canAccessAgent()` helpers build `AccessRequest` and call `engine.Can()`
   - Subject includes `AgentIDs` loaded from `ListUserAgentIDs`, resource includes `scope` attr

4. **Admin UI for agent management** (`internal/admin/ui/pages/agents.templ`, `agents.js`):
   - Scope dropdown in agent form (system / restricted)
   - "restricted" badge on agent list items
   - User assignment modal: shows assigned users, add/remove buttons, user dropdown
   - "users" button appears on restricted agents (admin only)
   - Add/edit/delete buttons only visible to admins (`isAdmin` loaded via `/api/auth/me`)

5. **Channel-side agent access enforcement** (`internal/channel/identity.go`, `internal/channel/resolved.go`):
   - New `ResolveAgentWithAuth()` function checks agent access via policy engine
   - DM default agent: checks access, returns `ErrAgentAccessDenied` if denied
   - Group chat agent: checks access, returns `ErrAgentAccessDenied` if denied
   - Fallback path: iterates enabled agents, returns first one user can access
   - `resolveWithUser()` uses `ResolveAgentWithAuth` when auth store + engine available
   - Error message: "you don't have access to this agent, contact an admin"

6. **Channel bot wiring** (`internal/channel/telegram/telegram.go`, `qq/qq.go`, `feishu/feishu.go`, `cmd/anna/gateway.go`):
   - Added `engine *auth.PolicyEngine` field to all three Bot structs
   - Updated `WithAuth()` signatures to accept `(authStore, engine, linkCodes)`
   - Updated all `ResolveWithAuth` calls to pass engine
   - `gateway.go`: auth store + engine created before bot initialization, shared across bots + admin panel
   - Link code store created in gateway for channel bots

7. **Tests**:
   - `internal/admin/agents_test.go` — scope in create/get/update, invalid scope, user assignment CRUD, non-admin denied for assignment API, non-admin sees only accessible agents, non-admin get access check
   - `internal/channel/access_test.go` — system agent allowed, restricted denied, restricted allowed when assigned, admin accesses all, fallback filtering, group chat denied
   - All tests pass with `-race`

### Notes for next phases

- **`WithAuth` signature changed**: Now takes 3 args: `(authStore, engine, linkCodes)` instead of 2. Gateway.go already updated.
- **`ResolveWithAuth` signature changed**: Now takes `engine *auth.PolicyEngine` as 5th parameter.
- **Backward compatibility preserved**: When `authStore` or `engine` is nil, the legacy `ResolveAgent` is used (no access checks). The `Resolve()` path (no auth) is unchanged.
- **Agent scope values**: `"system"` (default, all users) and `"restricted"` (only assigned users). Stored in `settings_agents.scope` column.
- **Policy evaluation**: Uses built-in policies `system:user-system-agents` (scope == "system") and `system:user-assigned-agents` (agent_id in subject.agent_ids). Admin full access via `system:admin-full-access`.
- **Error handling in channels**: `ErrAgentAccessDenied` is propagated through `resolve()` and surfaces to users as "Error: you don't have access to this agent, contact an admin" in all channel bots.
