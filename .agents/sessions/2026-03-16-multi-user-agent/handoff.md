# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: DB Consolidation & New Schema — Complete

### Commits (on `feat/multi-user-agent` branch)

1. `888b8f5` — Task 1.1: Extract `ConfigureDB` from `OpenDB` for shared DB connections
2. `7aa439e` — Task 1.2: Add `NewEngineFromDB` for shared DB connections in memory engine
3. `8b2e579` — Task 1.3: Add 7 new schema files (settings, providers, agents, channels, users, chat_agents, user_agent_memory)
4. `da6eb71` — Task 1.4: Add agent_id/user_id columns to conversations table
5. `320f878` — Task 1.5: Add agent_id/user_id columns to scheduler_jobs table
6. `2d9112d` — Task 1.6: Generate Atlas migration + sqlc codegen
7. `2d2777c` — Task 1.7: Add sqlc queries for all new tables + regenerate sqlc

### Files Changed

**Modified:**
- `internal/db/database.go` — Added `ConfigureDB(db *sql.DB) error`; `OpenDB` now delegates to it
- `internal/memory/engine.go` — Added `NewEngineFromDB(db, summarizer, opts)`, `ownsDB` flag, conditional `Close()`
- `internal/db/schemas/main.sql` — Added imports for all 7 new table schemas
- `internal/db/schemas/tables/conversations.sql` — Added `agent_id TEXT`, `user_id INTEGER`
- `internal/db/schemas/tables/scheduler_jobs.sql` — Added `agent_id TEXT`, `user_id INTEGER`
- `internal/db/migrations/atlas.sum` — Updated checksum
- `internal/db/sqlc/` — All generated files regenerated with new models and queries

**New:**
- `internal/db/schemas/tables/{settings,providers,agents,channels,users,chat_agents,user_agent_memory}.sql`
- `internal/db/migrations/20260316075129_multi_user_agent.sql`
- `internal/db/queries/{settings,providers,agents,channels,users,chat_agents,user_agent_memory}.sql`
- `internal/db/sqlc/{agents,channels,chat_agents,providers,settings,user_agent_memory,users}.sql.go`

### Key Functions/Types Added

- `db.ConfigureDB(db *sql.DB) error` — enables WAL + foreign keys on existing connection
- `memory.NewEngineFromDB(db *sql.DB, summarizer, opts...) Engine` — shared DB constructor
- sqlc query functions: `CreateProvider`, `GetAgent`, `ListEnabledAgents`, `UpsertUser`, `GetChatAgent`, `UpsertUserAgentMemory`, `GetSetting`, `UpsertSetting`, etc.

### Test Results

- All tests pass except `TestIntegrationPoolWithGoRunner` (pre-existing, requires API key)
- 0 lint issues, code compiles cleanly

### Notes for Phase 2

- `ConfigureDB` is ready for the shared DB pattern — call `db.OpenDB()` once, pass `*sql.DB` to both config store and `memory.NewEngineFromDB()`
- The `ownsDB` flag on memory engine ensures `Close()` is safe when sharing connections
- All new sqlc query functions are in `internal/db/sqlc/` for config store implementation
- `conversations` and `scheduler_jobs` have nullable `agent_id`/`user_id` — existing rows remain valid

## Phase 2: Config Store & Bootstrap — Complete

### Commits (on `feat/multi-user-agent` branch)

1. `0a4867e` — Task 2.1: Define Store interface and domain types (Provider, Agent, Channel, User)
2. `909196c` — Task 2.2: Implement DBStore with sqlc queries and env var fallbacks
3. `045558b` — Task 2.3: Add SeedDefaults for empty DB bootstrap
4. `3e655fe` — Task 2.4: Rewrite anna onboard to use DB-backed config store
5. `1133b63` — Task 2.5: Add model resolution methods to Snapshot type
6. `4b4e1d7` — Task 2.6: Remove old YAML config loading and update all callers

### Files Changed

**New:**
- `internal/config/store.go` — Store interface with typed CRUD methods for all entities
- `internal/config/dbstore.go` — DBStore implementation using sqlc queries, env var fallbacks, SeedDefaults
- `internal/config/snapshot.go` — Snapshot type with ResolveModelID/ResolveModel/ResolveModelTier

**Modified:**
- `internal/config/config.go` — Stripped to only sub-types (RunnerConfig, CompactionConfig, SchedulerConfig, HeartbeatConfig, PluginConfig, ProviderConfig)
- `internal/config/paths.go` — Removed Config method receivers (StatePath, SkillsPath, etc.), added DBPath()
- `internal/config/model.go` — Removed Config.ResolveModel* methods, renamed modelConfigToType to ModelConfigToAI
- `internal/config/config_test.go` — Replaced YAML/env loading tests with Snapshot and path tests
- `cmd/anna/onboard.go` — Rewritten to open DB, create Store, seed defaults; config API uses Store
- `cmd/anna/commands.go` — setup() uses DB+Store+Snapshot instead of config.Load(); setupResult stores snap+store
- `cmd/anna/chat.go` — Uses s.snap instead of s.cfg
- `cmd/anna/gateway.go` — Channel configs loaded from DB JSON blobs via loadChannelConfig generic helper
- `cmd/anna/models.go` — All subcommands use openStore()+defaultSnapshot(); removed config.ProviderConfig dependency
- `cmd/anna/skills.go` — Uses Store for workspace path resolution
- `cmd/anna/plugin.go` — Plugin list stored in settings table JSON instead of config.yaml
- `cmd/anna/commands_test.go` — Adapted to use Snapshot instead of Config

**Deleted:**
- `internal/config/state.go` — SaveModelSelection, applyState (no more state.yaml)
- `internal/config/channels.go` — ChannelsConfig, TelegramConfig, QQConfig, FeishuConfig types

### Key Types/Functions

- `config.Store` interface — typed CRUD for providers, agents, channels, users, chat_agents, user_agent_memory, settings
- `config.DBStore` — implementation with `NewDBStore(db)`, `SeedDefaults(ctx)`, `Snapshot(ctx, agentID)`
- `config.Snapshot` — read-only config with `ResolveModelID(tier)`, `ResolveModel()`, `ResolveModelTier(tier)`
- `config.Provider`, `config.Agent`, `config.Channel`, `config.User` — lean domain types
- `config.DBPath()` — returns `ANNA_HOME/anna.db`
- `openStore()` / `defaultSnapshot()` helpers in cmd/anna for CLI commands

### Design Decisions

- `DBStore.SeedDefaults()` is idempotent: only seeds if providers/agents tables are empty
- Default anna soul is embedded as a Go constant (from template/soul.md content)
- Env var fallbacks (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`) apply in `GetProvider()` and `Snapshot()`
- `MaxOpenConns(1)` set in `NewDBStore()` for SQLite concurrency safety
- Channel configs are stored as JSON blobs in the `channels` table, deserialized with generic `loadChannelConfig[T]`
- `models set` command updates the default agent's model in the DB (no more state.yaml)
- Plugin list stored in `settings` table under key `"plugins"` as JSON array
- Old `config.ProviderConfig` kept but no longer used by callers (can be removed later if unused)

### Test Results

- `internal/config/` — All tests pass (path, scheduler, heartbeat, snapshot, model resolution)
- `cmd/anna/` — All tests pass (runner factory, help, gateway no-services)
- Pre-existing integration test failures (require API key) unchanged

### Notes for Phase 3

- `setup()` in commands.go currently creates a single Pool for the first enabled agent — Phase 3 will replace this with PoolManager
- `setupResult` now holds both `snap *config.Snapshot` and `store config.Store`
- `modelSwitcher()` now takes both `snap` and `store` to look up provider credentials on switch
- The memory engine is created via `NewEngineFromDB(db)` sharing the same `*sql.DB` as the config store
- Channel configs in gateway.go use generic `loadChannelConfig[T]` for JSON deserialization
- `config.ProviderConfig` type is kept for model listing workflows but may be removable
