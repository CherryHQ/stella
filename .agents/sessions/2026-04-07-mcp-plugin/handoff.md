# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: Discovery + Runtime Skeleton

**Status:** complete

**Tasks completed:**

- 1.1: Seeded built-in `tool/mcp` plugin state and registration plumbing, with the plugin defaulting to disabled until fully configured
- 1.2: Added typed MCP config parsing/validation for multi-server JSON config with transport-aware validation and defaults
- 1.3: Added canonical tool ID sanitization and a runtime ID registry that maps canonical IDs back to original server/tool names
- 1.4: Added a shared `internal/mcp` manager skeleton with config state, tool cache, canonical ID resolution, and a stubbed exec path
- 1.5: Added unit tests for config decoding, transport validation, canonical IDs, collision suffixing, and manager registration

**Files changed:**

- `internal/config/plugin.go` — added `mcp` to built-in tool plugins
- `internal/config/dbstore.go` — seeded `tool/mcp` disabled by default
- `cmd/anna/plugins_imports.go` — added MCP plugin blank import
- `internal/admin/ui/static/js/pages/plugins.js` — added MCP plugin description
- `internal/mcp/config.go` — typed config parsing/validation helpers
- `internal/mcp/ids.go` — sanitization and canonical ID registry
- `internal/mcp/manager.go` — shared runtime manager skeleton
- `internal/mcp/config_test.go` — config tests
- `internal/mcp/ids_test.go` — ID mapping tests
- `internal/mcp/manager_test.go` — manager tests
- `plugins/tools/mcp/tool.go` — initial `mcp` tool registration and placeholder `list/get/exec` surface
- `.agents/sessions/2026-04-07-mcp-plugin/tasks.md` — checked off Phase 1 tasks

**Commits:**

- `HEAD` — `✨ feat: scaffold MCP plugin runtime`

**Decisions & context for next phase:**

- Canonical IDs are generated from stripped lowercase alphanumeric server/tool names; collisions currently receive deterministic numeric suffixes
- The runtime manager is intentionally minimal and not yet connected to real MCP transports or lifecycle hooks
- `mcp exec` is still a stub that returns a normalized not-implemented result; Phase 2/3 should replace it with official Go MCP transport-backed behavior

## Phase 2: Server Lifecycle + Discovery Cache

**Status:** complete

**Tasks completed:**

- 2.1: Implemented per-server runtime state with status snapshots, active session tracking, and per-server discovery cache ownership
- 2.2: Added official Go MCP SDK transport bootstrap for stdio, SSE, and streamable HTTP/HTTP transports
- 2.3: Implemented reconcile/start/stop/restart flow with bounded backoff and suppression for always-failing servers
- 2.4: Added tool discovery refresh from MCP `tools/list`, canonical cache rebuilds, and normalized exec result mapping from live MCP sessions
- 2.5: Wired MCP lifecycle into app startup and admin plugin toggling so enable/disable reconciles the shared manager
- 2.6: Added lifecycle/cache tests covering discovery, suppression, and normalized exec behavior with fake sessions

**Files changed:**

- `internal/mcp/manager.go` — manager reconcile logic, cache rebuilding, normalized exec result handling
- `internal/mcp/session.go` — official Go MCP SDK session + transport bootstrap adapter
- `internal/mcp/store.go` — load MCP plugin config/enabled state from plugin store
- `internal/mcp/supervisor.go` — supervisor loop, failure handling, tool discovery, status snapshots
- `internal/mcp/manager_test.go` — updated manager registration expectations
- `internal/mcp/supervisor_test.go` — lifecycle, suppression, exec normalization tests
- `cmd/anna/commands.go` — build and initialize shared MCP manager during setup
- `cmd/anna/gateway.go` — wire admin MCP lifecycle hooks into runtime reconcile logic
- `internal/admin/server.go` — added MCP lifecycle hook registration helpers
- `internal/admin/plugins.go` — start/stop MCP runtime on plugin toggle
- `go.mod` / `go.sum` — added official Go MCP SDK dependency
- `.agents/sessions/2026-04-07-mcp-plugin/tasks.md` — checked off Phase 2 tasks

**Commits:**

- `HEAD` — `✨ feat: add MCP server supervision`

**Decisions & context for next phase:**

- `TransportHTTP` currently reuses the official streamable HTTP client transport, which is the closest official SDK transport for modern HTTP MCP servers
- The manager now executes real MCP tool calls, so Phase 3 mainly needs to refine tool-level validation and response shaping rather than invent a new execution path
- Admin config mutation/reconcile on config edits is not wired yet; Phase 4 should trigger `reconcileMCP()` after config saves
- Prompt integration can now safely consume `ValidTools()` because discovery cache only contains currently connected server tools
