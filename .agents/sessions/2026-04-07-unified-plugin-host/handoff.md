# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: Shared Plugin Platform Contracts

**Status:** complete

**Tasks completed:**

- Added `pkg/plugins` as the shared plugin-facing contract package
- Defined plugin catalog registration, `Host`/`RegistryHost`/`ServiceHost`, capability registration structs, runtime/config interfaces, and prompt inventory types
- Defined narrow build contexts for tool, provider, hook, channel, memory, and runtime capabilities
- Added focused unit tests for catalog registration, clone helpers, and config helper behavior
- Verified the new package with `go test ./pkg/plugins`

**Files changed:**

- `pkg/plugins/doc.go`
- `pkg/plugins/catalog.go`
- `pkg/plugins/host.go`
- `pkg/plugins/types.go`
- `pkg/plugins/context.go`
- `pkg/plugins/capabilities.go`
- `pkg/plugins/catalog_test.go`
- `pkg/plugins/types_test.go`
- `.agents/sessions/2026-04-07-unified-plugin-host/tasks.md`
- `.agents/sessions/2026-04-07-unified-plugin-host/handoff.md`

**Commits:**

- `1bc402e9` — `✨ feat: add shared plugin platform contracts`

**Decisions & context for next phase:**

- `pkg/plugins` intentionally depends only on shared `pkg/...` contracts plus stdlib to avoid import cycles before `internal/pluginhost` lands
- Runtime snapshotting is kept deliberately small: shared state/message/timestamp/metadata for host orchestration, while plugin-specific admin status remains plugin-defined
- Build contexts include only narrow host services plus subsystem-specific inputs; raw DB/admin/pool internals remain out of the public plugin contract surface
- Plugin catalog registration fails fast on invalid or duplicate plugin IDs so host loading can treat plugin registration mistakes as programmer errors
- Phase 2 can build `internal/pluginhost` against these contracts without touching DB schema or migrating MCP behavior yet

## Phase 2: Internal Host Implementation

**Status:** complete

**Tasks completed:**

- Added `internal/pluginhost` with host registration, config service aliasing, runtime orchestration, prompt inventory access, and host-backed build helpers
- Added plugin-state compatibility for canonical plugin IDs with legacy `settings_plugins` ownership rows, including `mcp -> tool/mcp`
- Added host tests for registration, alias lookup, runtime apply, and runtime lookup

**Files changed:**

- `internal/pluginhost/host.go`
- `internal/pluginhost/config_service.go`
- `internal/pluginhost/runtime.go`
- `internal/pluginhost/catalog.go`
- `internal/pluginhost/host_test.go`
- `internal/pluginhost/runtime_test.go`

**Commits:**

- pending final squash for later phases

**Decisions & context for next phase:**

- Host-facing plugin IDs are canonical, but persistence compatibility is preserved through alias-based config lookup/set
- Runtime orchestration is plugin-centric: the host owns creation and `Apply/Stop/Snapshot`, plugins implement behavior only

## Phase 3: Compatibility Adapters

**Status:** complete

**Tasks completed:**

- Exposed read-only registry accessors for legacy tool/provider/hook/memory registries
- Added host-side compatibility adapter loading for legacy registry-backed plugins
- Added host-backed build helpers so existing plugin loading paths can migrate incrementally without flattening package layout

**Files changed:**

- `plugins/tools/registry.go`
- `plugins/providers/registry.go`
- `plugins/hooks/registry.go`
- `plugins/memory/registry.go`
- `internal/pluginhost/adapters.go`
- `internal/pluginhost/builders.go`

**Commits:**

- pending final squash for later phases

**Decisions & context for next phase:**

- Compatibility adapters keep existing registries thin and explicit instead of teaching them host internals
- MCP is excluded from the legacy tool adapter because it is now host-backed directly

## Phase 4: App and Admin Wiring

**Status:** complete

**Tasks completed:**

- Created the plugin host during app setup and threaded it through pool/admin startup
- Replaced MCP-specific startup reconciliation with generic runtime host apply
- Switched admin plugin config/status flow to host-backed validation, persistence, status lookup, and runtime reapply while preserving legacy MCP route shapes
- Wired prompt inventory into runner system prompt construction through the pool manager

**Files changed:**

- `cmd/anna/commands.go`
- `cmd/anna/gateway.go`
- `internal/admin/plugins.go`
- `internal/admin/server.go`
- `internal/agent/factory.go`
- `internal/agent/pool_manager.go`
- `internal/agent/runner/prompt.go`
- `internal/agent/runner/prompt_mcp_test.go`

**Commits:**

- pending final squash for later phases

**Decisions & context for next phase:**

- Prompt integration now depends on host-provided structured inventory instead of the MCP global singleton
- Tool/hook/provider reload behavior remains explicit in admin; runtime-capable plugins reapply through the host

## Phase 5: MCP Migration

**Status:** complete

**Tasks completed:**

- Added host-backed MCP plugin registration for config, runtime, tool, status, and prompt inventory capabilities
- Added typed runtime lookup inside `plugins/tools/mcp`
- Kept the physical package path stable at `plugins/tools/mcp`
- Added host-backed MCP tests and updated existing MCP prompt/admin tests

**Files changed:**

- `plugins/tools/mcp/plugin.go`
- `plugins/tools/mcp/runtime_lookup.go`
- `plugins/tools/mcp/plugin_test.go`
- `plugins/tools/mcp/tool.go`
- `plugins/tools/mcp/tool_test.go`
- `internal/admin/server_test.go`

**Commits:**

- pending final squash for later phases

**Decisions & context for next phase:**

- MCP is now the first plugin that fully owns runtime/config/status/prompt behavior through the host
- Legacy admin routes remain stable even though backend ownership moved to canonical plugin ID `mcp`

## Phase 6: Cleanup and Stabilization

**Status:** complete

**Tasks completed:**

- Removed `SetMCPLifecycle` and related admin/gateway wiring
- Removed external MCP singleton dependence from prompt and startup wiring
- Verified built-in plugin seed/state compatibility through host aliasing and full test runs
- Documented MCP as the first advanced host-backed plugin

**Files changed:**

- `cmd/anna/gateway.go`
- `internal/admin/server.go`
- `docs/content/docs/features/plugin-system.md`
- `internal/agent/runner/builtin/anna/SKILL.md`

**Commits:**

- pending final squash for later phases

**Decisions & context for next phase:**

- Reflect and channels remain follow-up migration targets; this slice keeps their current wiring while the host foundation is now in place

## Phase 7: Verification and Docs

**Status:** complete

**Tasks completed:**

- Ran `mise run format`
- Ran `mise run lint`
- Ran `mise run test`
- Updated plugin system docs and builtin Anna skill text to reflect the unified host and MCP host-backed ownership

**Files changed:**

- `.agents/sessions/2026-04-07-unified-plugin-host/tasks.md`
- `.agents/sessions/2026-04-07-unified-plugin-host/handoff.md`
- `docs/content/docs/features/plugin-system.md`
- `internal/agent/runner/builtin/anna/SKILL.md`

**Commits:**

- pending final squash for later phases

**Decisions & context for next phase:**

- Follow-up work should migrate reflect into a host-backed runtime/status plugin and then revisit channels once runtime semantics have proven out under MCP
