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
