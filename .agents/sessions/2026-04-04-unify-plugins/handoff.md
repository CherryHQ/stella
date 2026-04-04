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
