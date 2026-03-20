# Tasks: RBAC + ABAC Permission System

## Phase 1: Auth Foundation — DB Schema + Auth Package

- [x] 1.1 — Create auth DB schema files (`internal/db/schemas/tables/auth_users.sql`, `auth_roles.sql`, `auth_user_roles.sql`, `auth_identities.sql`, `auth_policies.sql`, `auth_user_agents.sql`, `auth_sessions.sql`)
- [x] 1.2 — Add `scope` column to `settings_agents` table (`internal/db/schemas/tables/settings_agents.sql`)
- [x] 1.3 — Generate Atlas migration (`mise run atlas:diff -- add-auth-tables`)
- [x] 1.4 — Create sqlc queries for all auth tables (`internal/db/queries/auth_*.sql`)
- [x] 1.5 — Run `mise run generate` to regenerate sqlc
- [x] 1.6 — Create `internal/auth/types.go`
- [x] 1.7 — Create `internal/auth/password.go`
- [x] 1.8 — Create `internal/auth/store.go` (AuthStore interface)
- [x] 1.9 — Create `internal/auth/authdb/store.go` (SQLite implementation)
- [x] 1.10 — Write tests for password and authdb store

## Phase 2: Policy Engine

- [ ] 2.1 — Create `internal/auth/engine.go` (PolicyEngine, Can, Must)
- [ ] 2.2 — Implement condition evaluator (JSON conditions, operators: eq, neq, in, not_in, contains)
- [ ] 2.3 — Implement deny-overrides algorithm
- [ ] 2.4 — Create `internal/auth/seed.go` (8 built-in policies + 2 roles)
- [ ] 2.5 — Integrate seed into bootstrap
- [ ] 2.6 — Write tests (policy matching, conditions, deny-overrides, edge cases)

## Phase 3: Admin UI Authentication

- [ ] 3.1 — Create `internal/auth/session.go` (crypto/rand IDs, cookies, lazy cleanup)
- [ ] 3.2 — Create `internal/auth/ratelimit.go` (per-IP + per-username throttling)
- [ ] 3.3 — Create login/register templ page (`internal/admin/ui/pages/login.templ`)
- [ ] 3.4 — Create login page JS (`internal/admin/ui/static/js/pages/login.js`)
- [ ] 3.5 — Add auth API handlers (`internal/admin/auth.go`)
- [ ] 3.6 — Add auth middleware (`internal/admin/middleware.go`)
- [ ] 3.7 — Harden CORS in `server.go`
- [ ] 3.8 — Apply auth middleware to routes, exempt login/static/auth
- [ ] 3.9 — Add admin-only route guard middleware
- [ ] 3.10 — Modify navbar for role-based visibility
- [ ] 3.11 — Modify root redirect (unauthenticated → login)
- [ ] 3.12 — Write tests

## Phase 4: User Profile + Channel Linking

- [ ] 4.1 — Create `internal/auth/linkcode.go` (in-memory sync.Map, 5-min TTL)
- [ ] 4.2 — Create profile templ page (`internal/admin/ui/pages/profile.templ`)
- [ ] 4.3 — Create profile page JS (`internal/admin/ui/static/js/pages/profile.js`)
- [ ] 4.4 — Add profile API handlers (get profile, update password, link code, identities)
- [ ] 4.5 — Add route + nav link for `/profile`
- [ ] 4.6 — Modify channel handlers to intercept link-code messages
- [ ] 4.7 — Modify `identity.go`: resolve via auth_identities with auto-migration fallback
- [ ] 4.8 — Write tests

## Phase 5: Agent Scoping + Access Enforcement

- [ ] 5.1 — Add `scope` field to `config.Agent` struct and agent form in admin UI
- [ ] 5.2 — Create user-agent assignment API (`POST/DELETE /api/agents/{id}/users/{userId}`)
- [ ] 5.3 — Create admin UI for agent user management
- [ ] 5.4 — Integrate policy engine into admin API middleware
- [ ] 5.5 — Integrate policy engine into channel identity resolution
- [ ] 5.6 — Return permission denied / link prompt on channel access failures
- [ ] 5.7 — Modify `ResolveAgent()` to filter by accessible agents
- [ ] 5.8 — Write tests

## Phase 6: Per-User Data + Skills Isolation

- [ ] 6.1 — Modify `SetupWorkspace()` for per-user directories
- [ ] 6.2 — Modify `SkillsTool` to accept `userID`, per-user skill path
- [ ] 6.3 — Modify skill load/list/install/remove for per-user paths
- [ ] 6.4 — Modify `LoadSkills()` in `runner/skill.go`: add user dir in priority chain
- [ ] 6.5 — Modify runner creation to pass user ID
- [ ] 6.6 — Add sandbox enforcement (`internal/auth/sandbox.go`, file tool integration)
- [ ] 6.7 — Migrate existing agent-level skills as shared
- [ ] 6.8 — Write tests

## Phase 7: Admin User Management

- [ ] 7.1 — Enhance `/users` page to show auth_users, roles, linked identities
- [ ] 7.2 — Add role management (admin promote/demote)
- [ ] 7.3 — Add agent assignment management
- [ ] 7.4 — Add user detail view (sessions, skills, identities)
- [ ] 7.5 — Cleanup old settings_users page functionality
- [ ] 7.6 — Write tests
