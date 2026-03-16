# Tasks: Multi-User × Multi-Agent Support

## Phase 1: DB Consolidation & New Schema

- [x] 1.1 — Refactor `internal/db/database.go`: add ability for packages to share one `*sql.DB` connection
- [x] 1.2 — Refactor `internal/memory/engine.go`: add `NewEngineFromDB(db *sql.DB, ...)` constructor
- [x] 1.3 — Add new schema files: `settings.sql`, `providers.sql`, `agents.sql`, `channels.sql`, `users.sql`, `chat_agents.sql`, `user_agent_memory.sql` (`internal/db/schemas/tables/`)
- [x] 1.4 — Modify `conversations.sql`: add `agent_id TEXT`, `user_id INTEGER` columns
- [x] 1.5 — Modify `scheduler_jobs.sql`: add `agent_id TEXT`, `user_id INTEGER` columns
- [x] 1.6 — Update `main.sql` imports, generate Atlas migration, run sqlc codegen
- [x] 1.7 — Add sqlc queries for new tables: CRUD for providers, agents, channels, users, chat_agents, user_agent_memory, settings (`internal/db/queries/`)

## Phase 2: Config Store & Bootstrap

- [x] 2.1 — Define `Store` interface with typed read/write methods (`internal/config/store.go`)
- [x] 2.2 — Implement `DBStore` with sqlc queries + env var fallbacks (`internal/config/dbstore.go`)
- [x] 2.3 — Add `SeedDefaults()` for empty DB bootstrap, including default anna soul in system_prompt (`internal/config/dbstore.go`)
- [x] 2.4 — Rewrite `anna onboard`: create ANNA_HOME, open anna.db, seed defaults, start admin server (`cmd/anna/onboard.go`)
- [x] 2.5 — Add `Config` snapshot type for downstream consumption (`internal/config/config.go`)
- [x] 2.6 — Remove old config loading: YAML, state.yaml, env parsing, old channel types (`internal/config/`)

## Phase 3: Multi-Agent Core

- [ ] 3.1 — Create per-agent workspace setup: `SetupWorkspace(agentID, basePath)` — skills/ + logs only (`internal/agent/workspace.go`)
- [ ] 3.2 — Create `PoolManager`: map[agentID]*Pool, reads from config Store (`internal/agent/pool_manager.go`)
- [ ] 3.3 — Update `Pool` to store `agentID` (`internal/agent/pool.go`)
- [ ] 3.4 — Create per-agent runner factory (`internal/agent/factory.go`)
- [ ] 3.5 — Update skills tool to be per-agent (`internal/skills/`)
- [ ] 3.6 — Update `setup()` in commands.go: single *sql.DB, config Store, PoolManager (`cmd/anna/commands.go`)

## Phase 4: Multi-User, Agent Routing & User Memory

- [ ] 4.1 — Add user resolution: `ResolveUser(ctx, db, externalID, platform, name)` (`internal/channel/identity.go`)
- [ ] 4.2 — Add agent routing: `ResolveAgent(ctx, store, user, chatContext)` — DM→user default, group→chat_agents, fallback→first enabled (`internal/channel/identity.go`)
- [ ] 4.3 — Update session key construction: `BuildSessionKey(...)` helper (`internal/agent/session.go`)
- [ ] 4.4 — Update `Pool.CreateSession` / `ResolveSession` to store agent_id + user_id (`internal/agent/pool.go`)
- [ ] 4.5 — Update memory engine: pass agent_id + user_id to conversation inserts (`internal/memory/engine.go`)
- [ ] 4.6 — Add `user_agent_memory` read/write layer (`internal/memory/usermemory.go`)
- [ ] 4.7 — Add `user_memory` tool for agent to read/write per-user notes (`internal/memory/tool/usermemory.go`)
- [ ] 4.8 — Update system prompt builder: remove SOUL.md/USER.md/memories.md.tmpl, compose from DB (`internal/agent/runner/prompt.go`)
- [ ] 4.9 — Implement `/agent` command: list agents, set active agent for DM/group (`internal/channel/command.go`)
- [ ] 4.10 — Refactor Telegram channel: resolve user → resolve agent → PoolManager → chat (`internal/channel/telegram/`)
- [ ] 4.11 — Update scheduler: jobs use agent_id to route to Pool via PoolManager (`internal/scheduler/`)

## Phase 5: Web API & Admin UI

- [ ] 5.1 — Create `internal/admin/` package: server struct, middleware, route mounting (`internal/admin/server.go`)
- [ ] 5.2 — Provider APIs: CRUD + model listing (`internal/admin/providers.go`)
- [ ] 5.3 — Agent APIs: CRUD (`internal/admin/agents.go`)
- [ ] 5.4 — Channel APIs: `GET/PUT /api/channels/{platform}` (`internal/admin/channels.go`)
- [ ] 5.5 — User, session, settings APIs (`internal/admin/users.go`, `sessions.go`, `settings.go`)
- [ ] 5.6 — Scheduler job APIs with agent_id + user_id (`internal/admin/scheduler.go`)
- [ ] 5.7 — Build admin SPA: Alpine.js + Tailwind, tabs for all entities (`internal/admin/ui/index.html`)
- [ ] 5.8 — Wire admin server into `anna onboard` and `anna gateway` (`cmd/anna/`)

## Phase 6: Integration & Cleanup

- [ ] 6.1 — Rewrite `gateway.go`: PoolManager, single bot per platform, QQ/Feishu on default agent (`cmd/anna/gateway.go`)
- [ ] 6.2 — Update CLI chat: `--agent` flag, single Pool for selected agent (`cmd/anna/commands.go`)
- [ ] 6.3 — Update notification dispatcher: per-agent channels (`internal/channel/dispatcher.go`)
- [ ] 6.4 — Clean up dead code: old config types, state.yaml, SOUL.md/USER.md defaults, memories.md.tmpl (`internal/config/`, `cmd/anna/`, `internal/agent/runner/template/`)
- [ ] 6.5 — Update documentation: README, docs, builtin anna skill (`README.md`, `docs/`, `internal/agent/runner/builtin/anna/`)
- [ ] 6.6 — Add tests: config Store, PoolManager, admin API, session keys, user resolution, agent routing, user_memory tool (`*_test.go`)
