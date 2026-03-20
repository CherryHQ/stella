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
