# Tasks: Unify Plugin System

## Phase 1: Embed + Extract Plugin Manifests

- [x] 1.1: Create plugin.json manifests for 5 built-in tools
- [x] 1.2: Create plugin.json manifests for 4 built-in channels
- [x] 1.3: Create `internal/embedded/plugins.go` with embed FS + EnsurePlugins()
- [x] 1.4: Wire EnsurePlugins into startup paths
- [x] 1.5: Simplify `plugin_runtime.go` — remove BuiltinToolDefinitions/BuiltinToolPlugin
- [x] 1.6: Simplify channel `plugin_runtime.go` — remove BuiltinChannelDefinitions
- [x] 1.7: Update LoadCatalog() — discover from disk only, no merge
- [x] 1.8: Update loadRuntimeChannelCatalog() — same simplification
- [x] 1.9: Add test for manifest extraction + catalog discovery

## Phase 2: DB Table + Store + Migration

- [x] 2.1: Create `settings_plugins.sql` schema file
- [x] 2.2: Generate migration via `mise run db:diff`
- [x] 2.3: Create sqlc queries for settings_plugins
- [x] 2.4: Run `mise run generate` for codegen
- [x] 2.5: Create `internal/config/plugin.go` — Plugin type + constants
- [x] 2.6: Add Plugin CRUD to Store interface
- [x] 2.7: Implement plugin methods in DBStore
- [x] 2.8: Update SeedDefaults() — migrate settings_channels + seed built-ins
- [x] 2.9: Update Snapshot() — load from settings_plugins
- [x] 2.10: Update snapshot.go — replace PluginConfig + RuntimePlugins with Plugin
- [x] 2.11: Add store tests for plugin CRUD

## Phase 3: Remove JS Plugins + Hooks + RuntimePluginBindings

- [x] 3.1: Delete `internal/plugin/` package entirely
- [x] 3.2: Delete `internal/config/runtime_plugins.go`
- [x] 3.3: Delete `cmd/anna/plugin_runtime_cli.go`
- [x] 3.4: Remove hook plumbing from engine (types, tool_execution, engine)
- [x] 3.5: Remove hook plumbing from runner (gorunner, factory)
- [x] 3.6: Remove hook plumbing from pool (pool_manager, pool, pool_options)
- [x] 3.7: Remove PluginHooks from agent tool (agent.go)
- [x] 3.8: Remove pluginmgr from startup (commands.go)
- [x] 3.9: Remove plugin manager from channel plugin (channel.go)
- [x] 3.10: Remove PluginConfig from config.go
- [x] 3.11: Verify build compiles clean

## Phase 4: Simplify Tool + Channel Loading

- [x] 4.1: Collapse NewRegistry + NewRegistryWithBindings into single NewRegistry
- [x] 4.2: Update gateway.go — channel startup from settings_plugins
- [x] 4.3: Simplify resolveChannelPluginDefinition — no bindings
- [x] 4.4: Update LoadConfig[T] — read from settings_plugins
- [x] 4.5: Remove remaining settings_channels reads

## Phase 5: Rewrite Plugin CLI

- [x] 5.1: Rewrite plugin.go — list subcommand
- [x] 5.2: Add add/remove subcommands
- [x] 5.3: Add enable/disable subcommands
- [x] 5.4: Add config subcommand
- [x] 5.5: Remove old helpers (loadPlugins, savePlugins, etc.)

## Phase 6: Cleanup + Tests + Docs

- [x] 6.1: Delete dead test files
- [x] 6.2: Update dbstore_test.go — Plugin CRUD tests
- [x] 6.3: Update plugin_runtime_test.go
- [x] 6.4: Update admin panel channel handlers
- [x] 6.5: Update docs (plugin-system.md + translations)
- [x] 6.6: Update README.md + SKILL.md
- [x] 6.7: Run full verification: format + lint + test
