# Plan: Unify Plugin System

## Overview

Anna has three overlapping plugin concepts: JS plugins (`settings["plugins"]`),
runtime plugin bindings (`settings["runtime_plugins"]`), and bundled subprocess
plugins (tools + channels). Each has its own storage, CLI, and lifecycle. This
consolidation replaces all three with a single subprocess runtime plugin model.

### Goals

- One plugin type: subprocess runtime plugins with `plugin.json` manifests
- One DB table (`settings_plugins`) tracking all plugin state
- One CLI surface: `anna plugin list/add/remove/enable/disable/config`
- Remove JS plugin system and hook plumbing entirely
- Remove `RuntimePluginBindings` indirection
- Ship built-in tool/channel manifests as embedded data extracted to
  `$ANNA_HOME/plugins/` at startup

### Success Criteria

- [ ] `anna plugin list` shows all tools + channels with enabled status
- [ ] `anna plugin enable/disable <id>` toggles plugin state
- [ ] `anna plugin config <id> key=val` sets plugin config (e.g. channel tokens)
- [ ] `anna plugin add <path>` installs a new plugin from directory
- [ ] `anna plugin remove <id>` removes a user-installed plugin
- [ ] `anna serve` starts channels based on `settings_plugins` enabled + config
- [ ] Existing channel configs (tokens) survive the migration
- [ ] `mise run test` passes with `-race`
- [ ] `mise run lint` passes clean
- [ ] No JS plugin code remains in the codebase

### Out of Scope

- Provider plugins or memory plugins
- Replacing the CLI channel with a plugin
- Third-party plugin marketplace or remote install
- Hook system re-implementation via subprocess plugins
- Admin panel UI changes (API backend only)

## Technical Approach

Replace all plugin storage with a single `settings_plugins` table. All plugins
are discovered from `$ANNA_HOME/plugins/` via `pluginhost.Discover`. Built-in
tool/channel manifests are embedded in the binary and extracted at startup (same
pattern as `internal/embedded/` for tool binaries like `rg`, `fd`). The DB
tracks user state (enabled/config) per plugin.

### Schema

```sql
CREATE TABLE settings_plugins (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,
    name       TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    config     TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
```

Plugin IDs follow the existing `kind/name` convention: `tool/read`,
`channel/telegram`, etc.

### Components

- **Embedded plugin manifests**: `plugin.json` files for all 9 built-in plugins
  embedded in the binary and extracted to `$ANNA_HOME/plugins/`
- **Plugin DB store**: CRUD on `settings_plugins` via sqlc
- **Unified CLI**: `anna plugin list/add/remove/enable/disable/config`
- **Simplified tool registry**: Direct `"tool/<name>"` catalog lookup, no binding
  indirection
- **Simplified channel loading**: Read enabled channels from `settings_plugins`
  instead of `settings_channels`

## Implementation Phases

### Phase 1: Embed + Extract Plugin Manifests

Create actual `plugin.json` files for all built-in tools and channels. Embed
them in the binary and extract to `$ANNA_HOME/plugins/` at startup.

1. Create `internal/embedded/plugins/tool/{read,bash,edit,write,webfetch}/plugin.json`
   with manifests matching current programmatic definitions
2. Create `internal/embedded/plugins/channel/{telegram,qq,feishu,weixin}/plugin.json`
3. Create `internal/embedded/plugins.go` — embed FS + `EnsurePlugins(annaHome)`
   extraction function (files: `internal/embedded/plugins.go`)
4. Wire `EnsurePlugins` into startup paths in `cmd/anna/commands.go` and
   `cmd/anna-plugin/channel.go`
5. Simplify `internal/agent/tool/plugin_runtime.go` — remove
   `BuiltinToolDefinitions()` and `BuiltinToolPlugin()`; keep `BuiltinToolNames()`
6. Simplify `internal/channel/plugin_runtime.go` — remove
   `BuiltinChannelDefinitions()` and `BuiltinChannelDefinition()`; keep
   `BuiltinChannelNames()`
7. Update `internal/agent/tool/tool.go` `LoadCatalog()` — remove
   `catalog.Merge(BuiltinToolDefinitions(...))`, discover from
   `config.PluginsPath()` only
8. Update `cmd/anna/channel_plugins.go` `loadRuntimeChannelCatalog()` — same
   simplification
9. Add test for manifest extraction and catalog discovery

### Phase 2: DB Table + Store + Migration

Add `settings_plugins` table and implement store methods. Migrate existing
channel data from `settings_channels`.

1. Create `internal/db/schemas/tables/settings_plugins.sql`
2. Generate migration via `mise run db:diff -- unify-plugins`
3. Create `internal/db/queries/settings_plugins.sql` — sqlc queries: GetPlugin,
   ListPlugins, ListPluginsByKind, ListEnabledPlugins, UpsertPlugin,
   UpdatePluginEnabled, UpdatePluginConfig, DeletePlugin
4. Run `mise run generate` for sqlc codegen
5. Create `internal/config/plugin.go` — `Plugin` type and bundled name constants
6. Add plugin CRUD methods to `internal/config/store.go` Store interface
7. Implement plugin methods in `internal/config/dbstore.go`
8. Update `SeedDefaults()` to migrate `settings_channels` rows into
   `settings_plugins` and seed all built-in plugins (idempotent)
9. Update `Snapshot()` to load from `settings_plugins`
10. Update `internal/config/snapshot.go` — replace `Plugins []PluginConfig` +
    `RuntimePlugins RuntimePluginBindings` with `Plugins []Plugin`
11. Add store tests for plugin CRUD

### Phase 3: Remove JS Plugins + Hooks + RuntimePluginBindings

Delete the JS plugin system, all hook plumbing, and the binding indirection.

1. Delete `internal/plugin/` package entirely
2. Delete `internal/config/runtime_plugins.go`
3. Delete `cmd/anna/plugin_runtime_cli.go`
4. Remove hook plumbing from engine:
   `internal/agent/engine/types.go` (PluginHookRunner),
   `internal/agent/engine/tool_execution.go` (hook calls),
   `internal/agent/engine/engine.go` (PluginHooks wiring)
5. Remove hook plumbing from runner:
   `internal/agent/runner/gorunner.go` (PluginHooks, RuntimePlugins),
   `internal/agent/factory.go` (pluginHooks param, RuntimePlugins)
6. Remove hook plumbing from pool:
   `internal/agent/pool_manager.go` (WithPluginHooksPM, pluginHooks),
   `internal/agent/pool.go` (pluginHooks, session hook calls),
   `internal/agent/pool_options.go` (WithPluginHooks)
7. Remove from agent tool: `internal/agent/tool/agent.go` (PluginHooks)
8. Remove from startup: `cmd/anna/commands.go` (pluginmgr import, pluginMgr,
   LoadAll, Registry, AdaptTool)
9. Remove from channel plugin: `cmd/anna-plugin/channel.go` (plugin manager)
10. Remove `PluginConfig` from `internal/config/config.go`
11. Verify build compiles clean

### Phase 4: Simplify Tool + Channel Loading

Update tool registry and channel gateway to use the unified plugin model.

1. Collapse `NewRegistry` + `NewRegistryWithBindings` into single
   `NewRegistry(workDir, userDataDir)` that looks up `"tool/<name>"` in catalog
   directly (files: `internal/agent/tool/tool.go`)
2. Update `cmd/anna/gateway.go` — channel startup reads enabled channels from
   `store.ListPluginsByKind(ctx, "channel")`, loads config from `Plugin.Config`
3. Simplify `cmd/anna/channel_plugins.go` —
   `resolveChannelPluginDefinition(catalog, name)` constructs
   `"channel/"+name` directly
4. Update `internal/channel/config.go` — `LoadConfig[T]` reads from
   `settings_plugins` instead of `settings_channels`
5. Remove old `enabledChannels()` and related helpers from
   `cmd/anna/plugin_runtime_cli.go` (already deleted) and any remaining
   `settings_channels` reads

### Phase 5: Rewrite Plugin CLI

Unified CLI replacing both `plugin.go` and `plugin_runtime_cli.go`.

1. Rewrite `cmd/anna/plugin.go` with subcommands:
   - `list` — queries `store.ListPlugins()`, shows ID/KIND/ENABLED table
   - `add <path>` — validates plugin.json, copies to `$ANNA_HOME/plugins/`,
     upserts DB row
   - `remove <id>` — deletes from DB + filesystem
   - `enable <id>` — `store.SetPluginEnabled(id, true)`
   - `disable <id>` — `store.SetPluginEnabled(id, false)`
   - `config <id> [key=val]` — view or merge config JSON
2. Remove all old `loadPlugins`/`savePlugins`/`parsePluginConfig`/`pluginName`
   helpers
3. Add CLI integration tests

### Phase 6: Cleanup + Tests + Docs

Final cleanup pass.

1. Delete dead test files:
   `internal/agent/tool/runtime_bindings_test.go`,
   `cmd/anna/channel_plugins_test.go` (if binding-dependent)
2. Update `internal/config/dbstore_test.go` — remove RuntimePlugins/PluginConfig
   tests, add Plugin CRUD tests
3. Update `cmd/anna/plugin_runtime_test.go` — remove RuntimePlugins usage
4. Update admin panel channel handlers to read/write `settings_plugins`
5. Update docs: `docs/content/docs/features/plugin-system.md` (+.zh.md, +.ja.md)
6. Update `README.md` plugin section
7. Update `internal/agent/runner/builtin/anna/SKILL.md`
8. Run full verification: `mise run format && mise run lint && mise run test`

## Testing Strategy

- Unit tests for embedded plugin extraction and catalog discovery
- Unit tests for `settings_plugins` CRUD via dbstore
- Unit tests for `SeedDefaults()` migration from `settings_channels`
- Integration tests for `anna plugin list/enable/disable/config` CLI
- Integration tests for tool registry creation from catalog (no bindings)
- End-to-end: `anna serve` starts enabled channels, skips disabled
- Regression: existing channel tokens survive migration

## Risks

| Risk | Impact | Mitigation |
| ---- | ------ | ---------- |
| Migration loses channel config data | High | SeedDefaults reads settings_channels before inserting to settings_plugins; transaction safety |
| Circular imports between config and tool/channel packages | Medium | Keep bundled name lists in `internal/config/plugin.go`; manifests are on disk |
| Embedded manifest format drift from code expectations | Medium | Validate manifests at extraction time; test catalog discovery |
| Removing hooks breaks undiscovered consumers | Low | grep confirmed only JS plugin registry provides hooks; all wiring is nil-checked |

## Open Questions

None — all resolved during planning.

## Review Feedback

(Updated during plan review rounds)

## Final Status

(Updated after implementation completes)
