# Handoff

## Phase 1: Embed + Extract Plugin Manifests

**Status:** complete

**Tasks completed:**
- 1.1: Created plugin.json manifests for 5 built-in tools (read, bash, edit, write, webfetch) under `internal/embedded/plugins/tool/`
- 1.2: Created plugin.json manifests for 4 built-in channels (telegram, qq, feishu, weixin) under `internal/embedded/plugins/channel/`
- 1.3: Created `internal/embedded/plugins.go` with `EnsurePlugins()` that extracts embedded manifests to `$ANNA_HOME/plugins/bundled/`; added `//go:embed plugins/*` in `embed.go`
- 1.4: Wired `EnsurePlugins` into `NewRegistryWithBindings()` and `loadRuntimeChannelCatalog()`
- 1.5: Simplified `internal/agent/tool/plugin_runtime.go` — removed `BuiltinToolDefinitions()`, `BuiltinToolPlugin()`, `toolSpecFrom()`; added `BuiltinToolRuntime()` (returns just the Go runtime, no Definition) and `augmentDefinition()` (injects --work-dir/metadata at runtime)
- 1.6: Simplified `internal/channel/plugin_runtime.go` — removed `BuiltinChannelDefinitions()` and `BuiltinChannelDefinition()`; kept `BuiltinChannelNames()`
- 1.7: Updated `LoadCatalog()` — removed `catalog.Merge(BuiltinToolDefinitions(...))` call
- 1.8: Updated `loadRuntimeChannelCatalog()` — removed `catalog.Merge(BuiltinChannelDefinitions(...))`; updated `resolveChannelPluginDefinition()` to accept workDir/userDataDir and inject runtime metadata
- 1.9: Added `internal/embedded/plugins_test.go` — verifies all 9 manifests extract correctly and `pluginhost.Discover()` loads them into a valid catalog

**Files changed:**
- `internal/embedded/plugins/tool/{read,bash,edit,write,webfetch}/plugin.json` — new static manifests
- `internal/embedded/plugins/channel/{telegram,qq,feishu,weixin}/plugin.json` — new static manifests
- `internal/embedded/embed.go` — added `//go:embed plugins/*` and `pluginsFS`
- `internal/embedded/plugins.go` — new file with `EnsurePlugins()` extraction logic
- `internal/embedded/plugins_test.go` — new test for extraction + discovery
- `internal/agent/tool/plugin_runtime.go` — replaced `BuiltinToolDefinitions`/`BuiltinToolPlugin` with `BuiltinToolRuntime` + `augmentDefinition`
- `internal/agent/tool/tool.go` — wired `EnsurePlugins`, removed `Merge(BuiltinToolDefinitions)`, added `augmentDefinition` calls
- `internal/channel/plugin_runtime.go` — removed all definition functions, kept only `BuiltinChannelNames()`
- `cmd/anna/channel_plugins.go` — removed `Merge(BuiltinChannelDefinitions)`, added runtime augmentation in `resolveChannelPluginDefinition`
- `cmd/anna/channel_plugins_test.go` — updated for new `resolveChannelPluginDefinition` signature
- `cmd/anna/gateway.go` — updated `resolveChannelPluginDefinition` call with workDir/userDataDir
- `cmd/anna-plugin/main.go` — uses `BuiltinToolRuntime` + inline Definition construction
- `cmd/anna-plugin/channel.go` — inline Definition construction instead of `BuiltinChannelDefinition`

**Commits:**
- `10ca24f2` — add plugin.json manifests for builtin tools and channels
- `468ef286` — add EnsurePlugins() to extract embedded plugin manifests
- `019dc53e` — wire EnsurePlugins into tool and channel catalog loading
- `9358df42` — remove programmatic builtin definitions, use disk manifests
- `6f5beec2` — remove BuiltinChannelDefinitions, rely on disk manifests
- `e8c0076d` — test: verify plugin manifest extraction and catalog discovery
- `07ef723e` — fix: inject runtime args into disk-loaded plugin definitions

**Decisions & context for next phase:**
- `EnsurePlugins` does NOT use `sync.Once` (unlike `EnsureTools`). Manifests are small JSON files so always-overwrite is fine and avoids issues when `ANNA_HOME` changes between test runs.
- Static manifests have `args: ["tool", "<name>"]` or `["channel", "<name>"]`. Runtime-specific flags (`--work-dir`, `--user-data-dir`) and `metadata` are injected by `augmentDefinition()` / `resolveChannelPluginDefinition()` when creating subprocess wrappers.
- `cmd/anna-plugin` (subprocess entry point) constructs its own Definition inline for the protocol handshake — it doesn't load from disk since it IS the plugin runtime.
- `BuiltinToolNames()` and `BuiltinChannelNames()` are still needed for iterating known slots and collision detection.
- `LoadCatalog()` still accepts `(workDir, userDataDir string)` params even though they're unused — callers pass them but the function only does `pluginhost.Discover()`. A future cleanup could simplify the signature.

## Phase 2: DB Table + Store + Migration

**Status:** complete

**Tasks completed:**
- 2.1: Created `internal/db/schemas/tables/settings_plugins.sql` with id, kind, name, enabled, config, timestamps
- 2.2: Added import to `internal/db/schemas/main.sql`, generated migration `20260404092535_unify-plugins.sql`
- 2.3: Created `internal/db/queries/settings_plugins.sql` with 9 queries (Get, List, ListByKind, ListEnabled, Upsert, Seed, UpdateEnabled, UpdateConfig, Delete)
- 2.4: Ran sqlc codegen — generated `internal/db/sqlc/settings_plugins.sql.go`
- 2.5: Created `internal/config/plugin.go` with Plugin type, kind constants, PluginID(), BuiltinPluginIDs()
- 2.6: Added 8 Plugin CRUD methods to Store interface in `internal/config/store.go`
- 2.7: Implemented all Plugin methods in DBStore with pluginFromDB() helper, JSON marshal/unmarshal for config
- 2.8: Updated SeedDefaults() with seedPlugins(): migrates settings_channels on first run, seeds all 9 built-ins with INSERT OR IGNORE
- 2.9: Updated Snapshot() to load plugins from settings_plugins table, removed legacy plugins/runtime_plugins setting loads
- 2.10: Replaced `Plugins []PluginConfig` + `RuntimePlugins RuntimePluginBindings` with `Plugins []Plugin` in Snapshot struct
- 2.11: Added 4 tests: TestPluginCRUD, TestPluginSeedDefaults, TestPluginSeedDefaultsIdempotent, TestPluginSeedMigratesChannels; updated TestSnapshot for new Plugin type

**Files changed:**
- `internal/db/schemas/tables/settings_plugins.sql` — new schema
- `internal/db/schemas/main.sql` — added settings_plugins import
- `internal/db/migrations/20260404092535_unify-plugins.sql` — generated migration
- `internal/db/migrations/atlas.sum` — updated checksum
- `internal/db/queries/settings_plugins.sql` — new queries
- `internal/db/sqlc/settings_plugins.sql.go` — generated Go code
- `internal/db/sqlc/models.go` — generated SettingsPlugin model
- `internal/config/plugin.go` — new Plugin domain type + constants
- `internal/config/store.go` — added Plugin CRUD to Store interface
- `internal/config/dbstore.go` — Plugin methods, pluginFromDB, seedPlugins, updated Snapshot
- `internal/config/snapshot.go` — replaced PluginConfig/RuntimePluginBindings with []Plugin
- `internal/config/dbstore_test.go` — 4 new tests + updated TestSnapshot

**Commits:**
- `df7fc308` — add settings_plugins schema table
- `e14c15e2` — generate unify-plugins migration
- `c8c4ad3b` — add sqlc queries for settings_plugins
- `f1def9ba` — add Plugin domain type and builtin ID constants
- `7c3860de` — add Plugin CRUD methods to Store interface
- `295741b5` — implement Plugin CRUD methods in DBStore
- `6bf5ca2e` — seed built-in plugins and migrate channels in SeedDefaults
- `b6771d85` — load plugins from settings_plugins in Snapshot
- `c4743f52` — add plugin CRUD, seed, and channel migration tests

**Decisions & context for next phase:**
- `PluginConfig` type in `config.go` is now orphaned (no longer used by Snapshot). Phase 3 should remove it or repurpose it.
- `RuntimePluginBindings` in `runtime_plugins.go` is still referenced by `internal/agent/factory.go:75` via `snap.RuntimePlugins`. Phase 3 must replace this with plugin-table-based binding resolution.
- `internal/agent/pool_manager_test.go` has a `mockStore` that does not implement the new Plugin methods — Phase 3 must add stub implementations.
- The `builtinToolNames` / `builtinChannelNames` slices are duplicated between `config/plugin.go` and `agent/tool/plugin_runtime.go` / `channel/plugin_runtime.go`. Phase 3 should consolidate to a single source of truth in `config/plugin.go`.
- `SeedPlugin` uses `INSERT OR IGNORE`, so user-modified rows (enabled=0, custom config) are preserved across restarts.
- Channel migration only runs when `settings_plugins` is empty — it is a one-time data migration, not a continuous sync.

## Phase 3: Delete JS Plugin System & Hook Plumbing

**Status:** complete

**Tasks completed:**
- 3.1: Deleted `internal/plugin/` package entirely (11 files: manager, registry, runtime, hostapi, adapt, convert, types + tests)
- 3.2: Deleted `internal/config/runtime_plugins.go` and `runtime_plugins_test.go` (RuntimePluginBindings type, Load/Save functions)
- 3.3: Deleted `cmd/anna/plugin_runtime_cli.go` (anna plugin runtime list/bind CLI)
- 3.4: Removed hook plumbing from engine — deleted PluginHookRunner interface, BeforeToolCallEvent, AfterToolCallEvent, SessionEvent structs; removed PluginHooks from LoopConfig and ToolCallbacks; removed before/after hook invocations from ExecuteToolCalls
- 3.5: Removed hook plumbing from runner — removed PluginHooks and RuntimePlugins from GoRunnerConfig/GoRunner; simplified tool registry creation to use NewRegistry (default bindings); removed PluginHooks from LoopConfig in Chat()
- 3.6: Removed hook plumbing from pool — deleted WithPluginHooksPM, WithPluginHooks, pluginHooks field; removed all 3 hook call sites (session_start, 2x session_end)
- 3.7: Removed PluginHooks from AgentConfig and subagent LoopConfig in agent tool
- 3.8: Removed JS plugin manager from startup — removed pluginmgr import/usage, plugin loading, plugin tools wiring, WithPluginHooksPM from setup(); updated modelSwitcher signature
- 3.9: Removed JS plugin manager from channel subprocess (cmd/anna-plugin/channel.go)
- 3.10: Deleted PluginConfig struct from config.go; replaced NewRegistryWithBindings with simplified NewRegistry (direct plugin ID lookup); simplified resolveChannelPluginDefinition to not use bindings; deleted runtime_bindings_test.go; rewrote plugin.go CLI to list from settings_plugins table; added Plugin interface stubs to mockStore
- 3.11: Verified build compiles clean — `mise run lint` (0 issues) and `mise run test` (all pass with -race)

**Files deleted:**
- `internal/plugin/` — all 11 files (JS plugin system)
- `internal/config/runtime_plugins.go` — RuntimePluginBindings type
- `internal/config/runtime_plugins_test.go` — its tests
- `cmd/anna/plugin_runtime_cli.go` — runtime plugin CLI
- `internal/agent/tool/runtime_bindings_test.go` — binding override tests

**Files changed:**
- `internal/agent/engine/types.go` — removed PluginHookRunner, event structs, PluginHooks from LoopConfig
- `internal/agent/engine/tool_execution.go` — removed PluginHooks from ToolCallbacks, removed hook invocations
- `internal/agent/engine/engine.go` — removed PluginHooks from ToolCallbacks initialization
- `internal/agent/runner/gorunner.go` — removed PluginHooks, RuntimePlugins from config/struct; use NewRegistry
- `internal/agent/factory.go` — removed pluginHooks param from NewRunnerFactory, removed RuntimePlugins
- `internal/agent/pool_manager.go` — removed WithPluginHooksPM, pluginHooks field, updated NewRunnerFactory call
- `internal/agent/pool.go` — removed pluginHooks field, removed 3 hook call sites
- `internal/agent/pool_options.go` — removed WithPluginHooks
- `internal/agent/pool_manager_test.go` — added Plugin interface stubs to mockStore
- `internal/agent/tool/agent.go` — removed PluginHooks from AgentConfig and LoopConfig
- `internal/agent/tool/tool.go` — replaced NewRegistryWithBindings with simplified NewRegistry (direct ID lookup)
- `internal/config/config.go` — removed PluginConfig struct
- `cmd/anna/commands.go` — removed pluginmgr import, plugin loading, plugin tools, pluginMgr from setupResult
- `cmd/anna/chat.go` — removed pluginMgr.Close(), updated modelSwitcher call
- `cmd/anna/gateway.go` — removed pluginMgr.Close(), updated modelSwitcher and resolveChannelPluginDefinition calls
- `cmd/anna/plugin.go` — rewrote to list plugins from settings_plugins table (removed JS plugin add/remove/list)
- `cmd/anna/channel_plugins.go` — simplified resolveChannelPluginDefinition (no bindings param)
- `cmd/anna/channel_plugins_test.go` — updated tests for simplified signature
- `cmd/anna/commands_test.go` — updated NewRunnerFactory calls (2 args instead of 3)
- `cmd/anna-plugin/channel.go` — removed pluginmgr import and usage

**Commits:**
- `dad03f3f` — delete JS plugin system, runtime bindings, and runtime CLI
- `5d6c6218` — remove hook plumbing from engine package
- `73cd97b6` — remove hook plumbing from runner and factory
- `0f4daae5` — remove hook plumbing from pool and pool manager
- `1dbf035f` — remove PluginHooks from agent tool config
- `cf5c1baf` — remove JS plugin manager from startup and model switcher
- `1c4378b6` — remove JS plugin manager from channel subprocess
- `e701a9f5` — remove PluginConfig type, RuntimePluginBindings, simplify tool registry
- `a1e88549` — resolve compile errors from Phase 3 deletions

**Decisions & context for next phase:**
- `NewRegistry()` now does direct plugin ID lookup (`tool/<name>`) instead of going through RuntimePluginBindings. This is the same behavior as default bindings but without the indirection layer.
- `resolveChannelPluginDefinition()` now takes `(catalog, name, workDir, userDataDir)` without bindings — constructs ID as `channel/<name>`.
- The `plugin.go` CLI now shows plugins from the `settings_plugins` table. Phase 5 will rewrite the full plugin CLI.
- `PluginConfig` (legacy JS plugin config) is fully removed. The new `Plugin` type from Phase 2 is the sole plugin representation.
- No hook mechanism exists after this phase. If hooks are needed later, they should be re-implemented as part of the new plugin protocol.

## Phase 4: Tool Registry & Channel Gateway Use Unified Plugin Model

**Status:** complete

**Tasks completed:**
- 4.1: Simplified `LoadCatalog()` signature — removed unused `workDir, userDataDir` params. `NewRegistry()` still takes these for `augmentDefinition()` at runtime.
- 4.2: Updated `gateway.go` — replaced hardcoded platform iteration (telegram/qq/feishu/weixin) with dynamic `store.ListPluginsByKind(ctx, "channel")` loop. Each enabled channel plugin is validated via `channel.HasValidConfig()` and notify flag checked via `channel.IsNotifyEnabled()`.
- 4.3: Deleted `loadRuntimeChannelCatalog()` from `channel_plugins.go` — replaced with shared `agenttool.LoadCatalog()` in gateway.go. `resolveChannelPluginDefinition()` kept as-is (clean after Phase 3).
- 4.4: Updated `LoadConfig[T]` in `internal/channel/config.go` — reads from `settings_plugins` via `store.GetPlugin("channel/<name>")` instead of `store.GetChannel()`. Uses JSON roundtrip (marshal map -> unmarshal typed config).
- 4.5: Added `HasValidConfig()` and `IsNotifyEnabled()` helpers to `internal/channel/config.go` — encapsulate per-platform credential checks. Remaining `settings_channels` reads (weixin bot cursor, admin panel) are out of scope (Phase 6).

**Files changed:**
- `internal/agent/tool/tool.go` — `LoadCatalog()` takes no params; internal call updated
- `cmd/anna/channel_plugins.go` — deleted `loadRuntimeChannelCatalog()`, removed unused imports
- `cmd/anna/gateway.go` — uses `agenttool.LoadCatalog()`, dynamic `ListPluginsByKind` loop
- `internal/channel/config.go` — `LoadConfig[T]` reads from `settings_plugins`; added `HasValidConfig()`, `IsNotifyEnabled()`

**Commits:**
- `12123764` — refactor: channel startup reads from settings_plugins instead of settings_channels
- `747ce3ac` — chore: gofmt alignment and go mod tidy cleanup

**Decisions & context for next phase:**
- `LoadConfig[T]` now reads from `settings_plugins` everywhere (both gateway.go and cmd/anna-plugin/channel.go). The subprocess channel runtime also benefits.
- `settings_channels` is still read by weixin bot internals (cursor persist/clear) and admin panel CRUD. Phase 6 should migrate the admin panel to use `settings_plugins` and remove channel methods from Store.
- `HasValidConfig`/`IsNotifyEnabled` use switch on platform name — when third-party channel plugins are added, these helpers should be generalized (e.g., via manifest-declared required fields).
- `go mod tidy` removed `fastschema/qjs` and `tetratelabs/wazero` — leftover deps from the deleted JS plugin system.
