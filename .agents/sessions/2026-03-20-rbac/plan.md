# Plan: RBAC + ABAC Permission System for Multi-Agent Multi-User

## Overview

Add a hybrid RBAC + ABAC permission system to anna, enabling multi-user access with fine-grained authorization. This includes: unified user identity with channel linking, role-based access control, attribute-based policies, admin UI authentication, agent scoping, per-user data isolation, and per-user-per-agent skill installation.

### Goals

- Unified user identity: single account links to multiple channel identities (Telegram, QQ, Feishu, CLI)
- RBAC: extensible role system (admin, user initially) with role-based permission grants
- ABAC: policy engine evaluating subject/action/resource/context for fine-grained control
- Admin UI auth: username/password login, session-based, with route-level enforcement
- Agent scoping: system-level vs restricted agents, user-agent assignments
- Per-user data isolation within agent workspaces
- Per-user-per-agent skill installation
- API-level enforcement on all admin endpoints
- Channel-side enforcement: deny access to unassigned agents

### Success Criteria

- [ ] Users can register, log in, and manage their profile in admin UI
- [ ] First registered user becomes admin; subsequent default to user role
- [ ] Admin can manage roles, assign agents to users, see all data
- [ ] Regular users only see their assigned agents, own sessions, own skills
- [ ] Admin-only sections (providers, scheduler, channels, settings) are gated
- [ ] Policy engine correctly evaluates RBAC + ABAC policies with deny-overrides
- [ ] Channel identities (TG/QQ/Feishu) link to system users via code-based flow
- [ ] Skills install per (user_id, agent_id), isolated from other users
- [ ] User workspace data isolated under `workspaces/{agent_id}/users/{user_id}/`
- [ ] All existing functionality continues to work (backward compatible)
- [ ] Tests with -race, >80% coverage on new packages

### Out of Scope

- OAuth / SSO external providers (future enhancement)
- Fine-grained per-field permissions (e.g., can edit agent name but not model)
- Audit logging / activity trail
- API keys / token-based auth for programmatic access
- Admin UI for custom policy creation (policies managed via DB/seed for now)
- Multi-tenancy / organization-level isolation
- 2FA / TOTP

## Technical Approach

### Architecture

```
┌─────────────────────────────────────────────┐
│           Policy Decision Point (PDP)       │
│  internal/auth/engine.go                    │
│  Evaluates policies: deny-overrides         │
│  Input: Subject, Action, Resource, Context  │
│  Output: Allow / Deny                       │
├─────────────────────────────────────────────┤
│           Policy Store                      │
│  internal/auth/store.go                     │
│  DB-backed: auth_policies table             │
│  Built-in defaults seeded on bootstrap      │
└──────────┬──────────────────┬───────────────┘
           │                  │
┌──────────┴───────┐  ┌──────┴──────────────┐
│  PEP: Admin UI   │  │  PEP: Channel       │
│  Middleware       │  │  identity.go        │
│  (HTTP auth +    │  │  (agent access       │
│   route guard)   │  │   check)            │
└──────────────────┘  └─────────────────────┘
```

### Components

- **`internal/auth/`** (NEW): Core auth package — types, policy engine, session management, password hashing
  - `types.go` — AuthUser, Role, Policy, AccessRequest, Subject, Resource, Action types
  - `engine.go` — PolicyEngine: loads policies, evaluates deny-overrides
  - `session.go` — HTTP session management (cookie-based, DB-backed sessions)
  - `password.go` — bcrypt password hashing/verification
  - `store.go` — AuthStore interface for DB operations on auth tables
- **`internal/auth/authdb/`** (NEW): SQLite implementation of AuthStore
- **DB schema** (`internal/db/schemas/tables/`): New tables — auth_users, auth_roles, auth_user_roles, auth_identities, auth_policies, auth_user_agents, auth_sessions
- **DB queries** (`internal/db/queries/`): sqlc queries for all auth tables
- **`internal/admin/`** (MODIFY): Add auth middleware, login page, route guards, profile page with channel linking
- **`internal/channel/identity.go`** (MODIFY): Resolve channel identity → system user, check agent access
- **`internal/agent/workspace.go`** (MODIFY): Per-user workspace directories
- **`internal/skills/tool.go`** (MODIFY): Per-user skill paths
- **`internal/config/store.go`** (MODIFY): Add agent scope field to Agent struct only. Auth methods stay in separate `AuthStore` interface — do NOT expand `config.Store` with auth methods

### Data Model

```sql
-- System users (first-class identity, login credentials)
CREATE TABLE auth_users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    is_active     INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Roles (extensible, seeded with admin + user)
CREATE TABLE auth_roles (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    is_system   INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- User ↔ Role assignments (many-to-many)
CREATE TABLE auth_user_roles (
    user_id INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    role_id TEXT NOT NULL REFERENCES auth_roles(id) ON DELETE CASCADE,
    PRIMARY KEY(user_id, role_id)
);

-- Linked channel identities (TG/QQ/Feishu/CLI → system user)
CREATE TABLE auth_identities (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    platform    TEXT NOT NULL,
    external_id TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    linked_at   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(platform, external_id)
);

-- ABAC policies (JSON conditions for fine-grained control)
CREATE TABLE auth_policies (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    effect     TEXT NOT NULL CHECK(effect IN ('allow', 'deny')),
    subjects   TEXT NOT NULL DEFAULT '{}',   -- JSON: {"roles":["admin"]}
    actions    TEXT NOT NULL DEFAULT '[]',   -- JSON: ["read","write"]
    resources  TEXT NOT NULL DEFAULT '[]',   -- JSON: ["agent","provider"]
    conditions TEXT NOT NULL DEFAULT '{}',   -- JSON: ABAC conditions
    priority   INTEGER NOT NULL DEFAULT 0,
    is_system  INTEGER NOT NULL DEFAULT 0,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- User ↔ Agent access (which users can use which restricted agents)
-- No per-agent role column for now — access is binary (has access or not).
-- If per-agent roles are needed later, add a role column via migration.
CREATE TABLE auth_user_agents (
    user_id  INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES settings_agents(id) ON DELETE CASCADE,
    PRIMARY KEY(user_id, agent_id)
);

-- HTTP sessions (cookie-based login sessions)
CREATE TABLE auth_sessions (
    id         TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
```

Additionally modify existing tables:
- `settings_agents`: add `scope TEXT NOT NULL DEFAULT 'system'` column

### Policy Engine Design

**Evaluation algorithm** (deny-overrides):
1. Collect all enabled policies
2. Filter to policies matching: subject roles/attrs, action, resource type
3. Evaluate conditions (ABAC) for each matching policy
4. If ANY matching policy has effect=deny → DENY
5. If at least one matching policy has effect=allow → ALLOW
6. No match → DENY (default deny)

**Condition expressions** (kept simple, not a full expression language):
```json
{
  "resource.owner_id": {"eq": "subject.id"},
  "resource.scope": {"eq": "system"},
  "resource.agent_id": {"in": "subject.agent_ids"}
}
```

Operators: `eq`, `neq`, `in`, `not_in`, `contains`. Values can reference subject/resource attributes or be literals.

### Seeded Roles

| ID | Name | Description | System? |
|----|------|-------------|---------|
| `"admin"` | Admin | Full system access | Yes |
| `"user"` | User | Standard user with scoped access | Yes |

These role IDs are referenced as string literals in policy conditions. They are seeded on bootstrap and cannot be deleted (`is_system=1`).

### Built-in Policies (seeded on bootstrap)

| ID | Effect | Roles | Actions | Resources | Conditions |
|----|--------|-------|---------|-----------|------------|
| `system:admin-full-access` | allow | admin | * | * | — |
| `system:user-system-agents` | allow | user | read,execute | agent | scope == 'system' |
| `system:user-assigned-agents` | allow | user | read,execute | agent | agent_id in subject.agent_ids |
| `system:user-own-sessions` | allow | user | read,write,create,delete | session | owner_id == subject.id |
| `system:user-own-data` | allow | user | read,write | user_data | owner_id == subject.id |
| `system:user-own-skills` | allow | user | read,write,create,delete | skill | owner_id == subject.id |
| `system:user-own-profile` | allow | user | read,write | user | resource.id == subject.id |
| `system:user-view-agents-list` | allow | user | read | agent_list | — (can see names of agents for UI nav) |

### Session Security

- **Session ID**: 32 bytes from `crypto/rand`, hex-encoded (64 chars)
- **Cookie attributes**: `HttpOnly=true`, `SameSite=Lax`, `Secure=true` (when not localhost), `Path=/`
- **Session expiry**: 7 days, extended on each authenticated request
- **Session regeneration**: New session ID on login (prevent fixation)
- **Cleanup**: Delete expired sessions on each validation check (lazy cleanup on read)

### Password Requirements

- Minimum 8 characters
- bcrypt cost=12
- Never logged, never returned in API responses

### Rate Limiting

- Login/register endpoints: in-memory rate limiter per IP, max 10 attempts per minute
- After 5 consecutive failed logins for a username: 30-second cooldown before next attempt
- Implementation: `sync.Map` with IP → attempt count + timestamp (simple, no external deps)

### CORS Hardening

- Replace `Access-Control-Allow-Origin: *` with explicit origin from config (default `http://localhost:8080`)
- Add `Access-Control-Allow-Credentials: true`
- Configurable via `settings` table key `admin.cors_origin`

### Auth Flow

**Registration & Login:**
1. `GET /login` → login page (templ)
2. `POST /api/auth/register` → validate password (min 8 chars), create auth_user, if first user assign admin role, else user role
3. `POST /api/auth/login` → rate-limit check → verify password → create auth_session (crypto/rand ID) → set HttpOnly cookie
4. `POST /api/auth/logout` → delete session, clear cookie
5. Middleware: extract session cookie → load user + roles (delete if expired) → inject into request context

**Channel Linking:**
1. User logged into admin UI → profile page → "Link Telegram" button
2. Backend generates a unique 6-char code, stores in-memory `sync.Map` with 5-min TTL
3. User sends code to anna Telegram bot
4. Bot handler: look up code → find auth_user → create auth_identity(user_id, telegram, tg_external_id)
5. Code is consumed (single use). If process restarts mid-linking, user just generates a new code — acceptable for MVP.

**Channel Identity Resolution (modified):**
1. Message arrives from Telegram with external_id
2. Look up `auth_identities` for (platform=telegram, external_id=X)
3. If found → resolve to auth_user → load roles + assigned agents → check agent access
4. If not found → **fallback**: check `settings_users` for backward compat (migration period)
5. If fallback found → auto-create `auth_user` + `auth_identity` from `settings_users` record (auto-migration)
6. If neither found → reject with "Please register and link your account" message

**Note on auto-migration (step 5):** When an existing `settings_users` record is found but no `auth_identity` exists, we auto-create an `auth_user` (username = `{platform}_{external_id}`, random password) and link the identity. This prevents breaking existing bot users. The auto-created user gets the `user` role and can later set a password via admin UI to gain full access.

### Workspace & Skills Isolation

**Per-user workspace:**
```
~/.anna/workspaces/{agent_id}/
├── users/
│   └── {user_id}/
│       ├── .agents/skills/    # user-installed skills
│       └── data/              # user files
└── shared/                    # agent-level shared data (system prompt, etc.)
```

**Skills tool modification:**
- `NewTool(annaHome, workspace, cwd)` → `NewTool(annaHome, workspace, cwd, userID)`
- Skills path: `workspaces/{agentID}/users/{userID}/.agents/skills/` instead of `workspaces/{agentID}/skills/`
- Load skills: merge agent-level builtin skills + user's installed skills

### Migration Strategy

- **`settings_users` table**: Preserved as-is. NOT dropped or renamed. Serves as fallback during migration.
- **Auto-migration on channel message**: When an unlinked `settings_users` record is encountered, auto-create `auth_user` + `auth_identity` (see Auth Flow above). This ensures zero downtime for existing bot users.
- **`ctx_conversations.user_id`**: Currently INTEGER with no FK constraint. After migration, new sessions use `auth_users.id`. Both ID spaces can coexist. The auto-migration path ensures the `auth_users.id` for auto-migrated users is deterministic (created once, reused thereafter).
- **`ctx_agent_memory.user_id`**: Has a hard FK constraint `REFERENCES settings_users(id)`. To resolve: the Atlas migration will change this FK to reference `auth_users(id)` instead. The auto-migration path (which creates `auth_users` records from `settings_users`) must run first, so we also add a data migration step: for each `settings_users` record, create a corresponding `auth_users` record and `auth_identity`, then update `ctx_agent_memory.user_id` to the new `auth_users.id`. This is handled in the Atlas migration SQL. Alternatively, if IDs happen to match (both are AUTOINCREMENT), we can simply change the FK target without data changes — but we should NOT rely on this; the migration must be explicit.
- **`settings_users` deprecation**: After all active users have been auto-migrated (or manually linked), `settings_users` can be dropped in a future release. Not in scope for this work.
- **First admin**: First user to register via admin UI `/login` page gets admin role. No auto-created admin on startup.

## Implementation Phases

### Phase 1: Auth Foundation — DB Schema + Auth Package

Core auth types, password hashing, and database schema.

1. Create auth DB schema files in `internal/db/schemas/tables/` (files: `auth_users.sql`, `auth_roles.sql`, `auth_user_roles.sql`, `auth_identities.sql`, `auth_policies.sql`, `auth_user_agents.sql`, `auth_sessions.sql`)
2. Add `scope` column to `settings_agents` table (file: `settings_agents.sql`)
3. Generate Atlas migration: `mise run atlas:diff -- add-auth-tables`
4. Create sqlc queries for all auth tables (files: `internal/db/queries/auth_*.sql`)
5. Run `mise run generate` to regenerate sqlc
6. Create `internal/auth/types.go` — AuthUser, Role, Policy, AccessRequest, Subject, Resource, Action
7. Create `internal/auth/password.go` — bcrypt hash/verify
8. Create `internal/auth/store.go` — AuthStore interface
9. Create `internal/auth/authdb/store.go` — SQLite AuthStore implementation using sqlc
10. Write tests for password and authdb store

### Phase 2: Policy Engine

The ABAC policy evaluation engine.

1. Create `internal/auth/engine.go` — PolicyEngine struct, `Can()` and `Must()` methods
2. Implement condition evaluator: parse JSON conditions, resolve subject/resource attributes, apply operators
3. Implement deny-overrides algorithm: collect matching policies → deny wins → at least one allow → default deny
4. Create `internal/auth/seed.go` — built-in default policies (the 8 system policies)
5. Integrate seed into bootstrap: call from `SeedDefaults()` or a new auth bootstrap function
6. Write comprehensive tests: policy matching, condition evaluation, deny-overrides, edge cases

### Phase 3: Admin UI Authentication

Login/register pages, session middleware, route guards, CORS hardening.

1. Create `internal/auth/session.go` — session management: `crypto/rand` ID generation, create/validate/delete, cookie helpers (`HttpOnly`, `SameSite=Lax`, `Secure` when not localhost), lazy expired-session cleanup on validation
2. Create `internal/auth/ratelimit.go` — in-memory rate limiter: per-IP attempt tracking via `sync.Map`, 10 req/min limit, 30s cooldown after 5 consecutive failures per username
3. Create login/register templ page: `internal/admin/ui/pages/login.templ`
4. Create login page JS: `internal/admin/ui/static/js/pages/login.js`
5. Add auth API handlers in `internal/admin/auth.go`: register (validate min 8 char password), login (rate-limit → verify → create session → set cookie), logout, get-current-user
6. Add auth middleware in `internal/admin/middleware.go`: extract session cookie → load user+roles (delete expired) → inject into request context → 401 if unauthenticated
7. Harden CORS in `server.go`: replace `*` origin with configurable origin (default localhost), add `Allow-Credentials: true`, read from `settings` table key `admin.cors_origin`
8. Apply auth middleware to all routes in `server.go`, exempt `/login`, `/static/`, `/api/auth/*`
9. Add route guard middleware: admin-only routes (providers, scheduler, channels, settings, users management) check role → 403 if not admin
10. Modify navbar to show/hide sections based on user role (pass role via templ context)
11. Modify root redirect: unauthenticated → `/login`, authenticated user → `/agents`, admin → `/providers`
12. Write tests for auth handlers, middleware, route guards, rate limiting, CORS

### Phase 4: User Profile + Channel Linking

Profile page for users to manage their account and link channel identities.

1. Create `internal/auth/linkcode.go` — in-memory `sync.Map` link code store with 5-min TTL (no DB table; restart clears codes, user regenerates — acceptable for MVP)
2. Create profile templ page: `internal/admin/ui/pages/profile.templ`
3. Create profile page JS: `internal/admin/ui/static/js/pages/profile.js`
4. Add profile API handlers: get profile, update password, generate link code, list identities, unlink identity
5. Add route + nav link for `/profile`
6. Modify channel handlers (Telegram/QQ/Feishu): intercept link-code messages, look up code, create auth_identity
7. Modify `internal/channel/identity.go`: resolve via `auth_identities` → `auth_users` instead of `settings_users`
8. Handle backward compat: if auth_identity not found, fall back to settings_users (migration period)
9. Write tests

### Phase 5: Agent Scoping + Access Enforcement

Agent scope field, user-agent assignments, enforcement at API + channel level.

1. Add `scope` field to `config.Agent` struct and admin UI agent form
2. Create user-agent assignment API: `POST/DELETE /api/agents/{id}/users/{userId}`
3. Create admin UI for agent user management (on agents page: manage assigned users)
4. Integrate policy engine into admin API middleware: check `engine.Must()` on each API route
5. Integrate policy engine into channel identity resolution: check agent access before routing
6. Return "permission denied" / "please link your account" on channel access failures
7. Modify `ResolveAgent()` to filter by user's accessible agents
8. Write tests

### Phase 6: Per-User Data + Skills Isolation

Per-user workspace directories and per-user-per-agent skill installation.

1. Modify `SetupWorkspace()` to create per-user directories: `workspaces/{agentID}/users/{userID}/`
2. Modify `SkillsTool` to accept `userID`, use per-user skill path
3. Modify skill load/list/install/remove to use `workspaces/{agentID}/users/{userID}/.agents/skills/`
4. Modify `internal/agent/runner/skill.go` `LoadSkills()`: add user-specific skills directory in the priority chain (project > **user** > workspace/agent-level > common > builtin)
5. Modify runner creation to pass user ID to SkillsTool and LoadSkills
6. Add sandbox enforcement for file tools:
   - Create `internal/auth/sandbox.go` — `ValidatePath(userDir, requestedPath) error`: resolves symlinks via `filepath.EvalSymlinks`, checks `filepath.Clean(resolved)` has `userDir` prefix
   - Modify file tools (read/write/edit) in `internal/agent/tool/`: before executing, call `ValidatePath` with user's workspace dir
   - Bash tool: set `CWD` to user's data dir, prepend `cd <userDir> &&` to commands (soft sandbox — defense in depth, not a security boundary for admin-level threats)
   - Admin users bypass sandbox (policy engine check: if user has admin role, skip path validation)
7. Migrate existing per-agent skills: keep in `workspaces/{agentID}/skills/` as "agent-level" shared skills, loaded for all users
8. Write tests

### Phase 7: Admin User Management

Admin pages for managing users, roles, and agent assignments. Note: the existing `/users` page continues to work during Phases 3-6 (it shows `settings_users` data). This phase replaces it with an auth-aware version showing `auth_users`.

1. Enhance `/users` page: show auth_users (not just settings_users), display roles, linked identities
2. Add role management: admin can promote/demote users (toggle admin role)
3. Add agent assignment management: admin can assign/unassign users to restricted agents
4. Add user detail view: see user's sessions, skills, linked identities
5. Cleanup: remove or repurpose old `settings_users` page functionality
6. Write tests

## Testing Strategy

- **Unit tests**: password hashing, condition evaluator, policy engine, session management
- **Integration tests**: auth store operations, policy evaluation against real DB
- **HTTP tests**: auth handlers (register/login/logout), middleware (auth check, route guard), API access control
- **Channel tests**: identity resolution with linked accounts, agent access enforcement
- **Workspace tests**: per-user directory creation, skill isolation
- **Migration test**: ensure existing data survives schema migration
- All tests run with `-race` flag
- Target >80% coverage on `internal/auth/` package

## Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Breaking existing channel identity resolution | High — all bot users lose access | Backward compat fallback to settings_users during migration period |
| Schema migration on existing databases | High — data loss | Atlas-generated migration, test on copy of real DB first |
| Session management security (cookie theft, fixation) | Medium | Secure cookie flags, session expiry, regenerate on login |
| Policy engine performance (evaluate on every request) | Low — small policy set | Load policies once at startup into memory. Since custom policy UI is out of scope, policies only change on restart. If policy editing is added later, use a version counter to trigger reload. |
| Complexity creep in condition evaluator | Medium | Keep operators minimal (eq, in, contains), no nested expressions |
| Per-user workspace disk usage | Low | Only create dirs on first use, not eagerly |
| Password storage security | High | bcrypt with cost=12, never log passwords |

## Open Questions

All resolved — converted to assumptions:

- **Auth mechanism**: Username + password with cookie sessions (can add OAuth later)
- **First user bootstrap**: First registered user auto-assigned admin role
- **Link code delivery**: In-memory `sync.Map` with 5-min TTL (restart clears codes, user regenerates)
- **Existing users migration**: Auto-migrated on first channel message (see Auth Flow auto-migration). `settings_users` preserved, deprecated in future release.
- **Policy caching**: Loaded once at startup. No runtime invalidation needed since custom policy UI is out of scope.

## Review Feedback

### Round 1 (reviewer subagent)

Issues addressed:
1. **CORS hardening** — Added to Phase 3 step 7: replace `*` origin, add credentials header, configurable via settings
2. **Session security** — Added "Session Security" section: crypto/rand IDs, HttpOnly/SameSite/Secure cookies, 7-day expiry, regeneration on login, lazy cleanup
3. **Rate limiting** — Added "Rate Limiting" section + Phase 3 step 2: per-IP + per-username throttling
4. **`settings_users` migration path** — Expanded Migration Strategy: auto-create auth_user+auth_identity on first channel message from unlinked user, `ctx_conversations.user_id` coexistence explained
5. **Breaking change for existing bot users** — Auto-migration path prevents breakage (see Auth Flow step 5)
6. **`role` column in `auth_user_agents`** — Removed. Access is binary for now.
7. **Session cleanup** — Lazy cleanup on validation (delete expired on read)
8. **Phase 6 sandbox enforcement** — Expanded into 4 concrete sub-steps: ValidatePath with symlink resolution, file tool integration, bash CWD, admin bypass
9. **`config.Store` interface** — Clarified: auth methods stay in separate `AuthStore`, NOT added to `config.Store`

Additional improvements from review:
- Added "Password Requirements" section (min 8 chars)
- Added "Seeded Roles" section with explicit role IDs
- Added "CORS Hardening" section
- Clarified Phase 7 ordering (existing `/users` page works during Phases 3-6)

### Round 2 (reviewer subagent)

All 9 Round 1 issues verified as resolved. Two new issues found:
1. **`ctx_agent_memory` FK constraint** (Medium) — `ctx_agent_memory.user_id REFERENCES settings_users(id)` conflicts with new auth_users identity. **Fixed**: Migration Strategy updated to change FK target + data migration in Atlas migration.
2. **`LoadSkills` in `runner/skill.go`** (Low) — Not mentioned in Phase 6. **Fixed**: Added as Phase 6 step 4, user-specific skills directory in priority chain.

## Final Status

All 7 phases complete. The RBAC + ABAC permission system is fully implemented:

- **Phase 1**: Auth DB schema (7 tables), sqlc queries, auth types, password hashing, AuthStore interface + SQLite implementation
- **Phase 2**: Policy engine with deny-overrides, condition evaluator (eq/neq/in/not_in/contains), 8 built-in policies, 2 system roles
- **Phase 3**: Admin UI authentication — login/register, session management, rate limiting, CORS hardening, route guards, role-based navbar
- **Phase 4**: User profile page, channel identity linking (link codes), auth-aware identity resolution with auto-migration fallback
- **Phase 5**: Agent scope field (system/restricted), user-agent assignments, policy engine enforcement in admin API + channel bots
- **Phase 6**: Per-user workspace directories, per-user skill installation, sandbox enforcement for file tools
- **Phase 7**: Admin user management page — auth users list with roles/identities, role management (promote/demote), agent assignment, user detail panel, activate/deactivate, legacy memory tab preserved
