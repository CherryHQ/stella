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

## Phase 3: Multi-Agent Core — Complete

### Commits (on `feat/multi-user-agent` branch)

1. `046b756` — Task 3.1: Add per-agent workspace setup
2. `a059f6c` — Task 3.3: Add agentID field to Pool with WithAgentID option
3. `eb6035a` — Task 3.4: Extract runner factory to agent package
4. `0738301` — Task 3.5: Document per-agent workspace param in skills tool
5. `6182bd7` — Task 3.2: Add PoolManager for multi-agent pool management
6. `3b80e8a` — Task 3.6: Update setup() to use PoolManager

### Files Changed

**New:**
- `internal/agent/workspace.go` — `SetupWorkspace(agentID, basePath)` creates `workspaces/{agentID}/skills/`
- `internal/agent/workspace_test.go` — Tests for workspace setup (idempotent, empty ID)
- `internal/agent/factory.go` — `NewRunnerFactory(snap, extraTools, pluginHooks)` creates per-agent runner factory
- `internal/agent/pool_manager.go` — `PoolManager` maps agentID to Pool, with StartAll/Get/Close/DefaultPool
- `internal/agent/pool_manager_test.go` — Tests for PoolManager lifecycle (start, get, close, no agents)

**Modified:**
- `internal/agent/pool.go` — Added `agentID` field, `AgentID()` getter, logger enriched with agent_id
- `internal/agent/pool_options.go` — Added `WithAgentID(id string) PoolOption`
- `internal/skills/tool.go` — Documented that workspace param should be per-agent
- `cmd/anna/commands.go` — `setup()` uses PoolManager; `setupResult` has `poolManager` + `pool` (backward compat); removed local `newRunnerFactory`; removed `skills` import
- `cmd/anna/commands_test.go` — Updated to use `agent.NewRunnerFactory` instead of local function

### Key Types/Functions

- `agent.SetupWorkspace(agentID, basePath) (string, error)` — creates per-agent workspace dirs
- `agent.NewRunnerFactory(snap, extraTools, pluginHooks) (runner.NewRunnerFunc, error)` — moved from cmd/anna
- `agent.PoolManager` — manages map[agentID]*Pool; `NewPoolManager(store, mem, opts...)`, `Get(agentID)`, `StartAll(ctx)`, `Close()`, `DefaultPool()`
- `agent.PoolManagerOption` — `WithIdleTimeoutPM`, `WithCompactionPM`, `WithSharedExtraTools`, `WithExtraToolsFactory`, `WithPluginHooksPM`
- `agent.ExtraToolsFactory` — callback type for per-agent tool injection
- `agent.WithAgentID(id) PoolOption` — sets agent ID on a Pool
- `Pool.AgentID() string` — returns the agent ID

### Design Decisions

- Skills tool is per-agent: PoolManager creates it in `startAgent()` with the agent's workspace path
- Shared tools (scheduler, memory retrieval, plugins) are passed via `WithSharedExtraTools` and shared across all agent pools
- `setupResult` keeps both `poolManager` and `pool` (default agent's pool) for backward compat — existing CLI/channel code that references `s.pool` still works
- Pool.Close() on a shared memory engine is safe because `ownsDB=false` makes Close() a no-op
- PoolManager.startAgent overrides `snap.Workspace` with the per-agent workspace path from SetupWorkspace
- Task order: 3.3 done before 3.2 since PoolManager depends on Pool having agentID

### Test Results

- All unit tests pass (including new workspace, pool, pool_manager, factory tests)
- Pre-existing integration test failures unchanged (require API key)
- 0 lint issues

### Notes for Phase 4

- `setupResult.poolManager` is available for Phase 4 channel code to call `poolManager.Get(agentID)` for agent routing
- `s.pool` still points to default agent's pool — Phase 4/6 can gradually migrate callers to use poolManager.Get()
- Chat/gateway code still calls `s.pool.Close()` which only closes the default pool; Phase 6 should switch to `s.poolManager.Close()`
- The scheduler currently routes all jobs through the default pool — Phase 4 task 4.11 should route via poolManager using job's agent_id
- `agent.ExtraToolsFactory` is available but not currently used — Phase 4 can use it to inject per-agent user_memory tools
