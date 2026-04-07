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

## Phase 3: `mcp` Tool Execution Surface

**Status:** complete

**Tasks completed:**

- 3.1: Completed the `mcp` tool surface with the stable `list|get|exec` action contract
- 3.2: Normalized `list` output to sorted lightweight metadata (`id`, `name`, `description`, `server_name`)
- 3.3: Kept `get` on the full normalized cached tool detail payload for schema introspection
- 3.4: Reused the live manager execution path to return normalized MCP exec results through the tool
- 3.5: Added tool tests for list/get/exec and validation/error paths

**Files changed:**

- `plugins/tools/mcp/tool.go` — sorted list normalization and stable proxy behavior
- `plugins/tools/mcp/tool_test.go` — list/get/exec contract tests and validation tests
- `.agents/sessions/2026-04-07-mcp-plugin/tasks.md` — checked off Phase 3 tasks

**Commits:**

- `HEAD` — `✨ feat: finalize MCP proxy tool actions`

**Decisions & context for next phase:**

- The `mcp` tool now relies on the shared manager for both discovery and execution, so admin config updates only need to reconcile the manager and reload plugin tools for new sessions
- `get` returns the cached normalized tool detail; prompt guidance in Phase 5 should explicitly tell the model to call `get` before every `exec`
- The admin page still lacks MCP form editing and config persistence APIs, which is the main remaining product-facing gap before prompt integration

## Phase 4: Admin API + Plugins UI

**Status:** complete

**Tasks completed:**

- 4.1: Added an admin-only MCP plugin config update API with validation against typed MCP config rules
- 4.2: Added an MCP server editor to the Plugins page under the `mcp` tool plugin row
- 4.3: Supported multiple server entries with transport-specific fields (`stdio`, `sse`, `streamable_http`, `http`)
- 4.4: Reconciled the shared MCP runtime after config saves so background sessions update without restart
- 4.5: Added admin tests for MCP config save success and invalid-config rejection

**Files changed:**

- `internal/admin/server.go` — added MCP config route
- `internal/admin/plugins.go` — added config update handler and MCP reconcile trigger
- `internal/admin/ui/pages/plugins.templ` — added MCP server editor form
- `internal/admin/ui/static/js/pages/plugins.js` — added MCP config editing, parsing, and save logic
- `internal/admin/server_test.go` — added MCP config API tests
- `.agents/sessions/2026-04-07-mcp-plugin/tasks.md` — checked off Phase 4 tasks

**Commits:**

- `HEAD` — `✨ feat: add MCP admin config editor`

**Decisions & context for next phase:**

- The UI stores `args`, `env`, and `headers` through structured form fields but still uses JSON textareas for nested key/value and array data, which is a good compromise for now
- Config saves already trigger runtime reconciliation, so prompt integration can assume the manager reflects the latest persisted MCP configuration
- The remaining core feature gap is prompt exposure of valid MCP tools plus explicit `get`-before-`exec` instructions

## Phase 5: Prompt Integration

**Status:** complete

**Tasks completed:**

- 5.1: Extended prompt data with MCP tool inventory
- 5.2: Loaded prompt MCP inventory from the shared runtime manager snapshot
- 5.3: Rendered only valid MCP tools in the system prompt tools section
- 5.4: Added explicit prompt guidance requiring `mcp get` before `mcp exec`
- 5.5: Added prompt rendering tests for MCP-enabled and MCP-disabled cases

**Files changed:**

- `internal/agent/runner/prompt.go` — attached valid MCP tools to prompt data
- `internal/agent/runner/template/system_prompt.tmpl` — rendered MCP prompt inventory + usage guidance
- `internal/agent/runner/prompt_mcp_test.go` — prompt integration tests
- `.agents/sessions/2026-04-07-mcp-plugin/tasks.md` — checked off Phase 5 tasks

**Commits:**

- `HEAD` — `📝 docs: wire MCP prompt inventory`

**Decisions & context for next phase:**

- Prompt rendering reads only `ValidTools()` from the shared manager, so offline/suppressed servers do not leak into the prompt
- The MCP prompt section is intentionally lightweight; the authoritative schema still comes from `mcp get`
- Remaining work is docs/skill sync and final verification

## Phase 6: Wiring, Docs, and Verification

**Status:** complete

**Tasks completed:**

- 6.1: Shared one MCP runtime manager across setup, admin lifecycle, and the `mcp` tool plugin
- 6.2: Hooked runtime hot reload for plugin enable/disable and config saves
- 6.3: Updated plugin system and architecture docs (en/zh/ja) for the MCP plugin
- 6.4: Updated builtin anna skill documentation for MCP behavior and the `get`-before-`exec` rule
- 6.5: Ran `mise run format`, `mise run lint`, and `mise run test`

**Files changed:**

- `docs/content/docs/features/plugin-system.md`
- `docs/content/docs/features/plugin-system.zh.md`
- `docs/content/docs/features/plugin-system.ja.md`
- `docs/content/docs/core/architecture.md`
- `docs/content/docs/core/architecture.zh.md`
- `docs/content/docs/core/architecture.ja.md`
- `internal/agent/runner/builtin/anna/SKILL.md`
- `.agents/sessions/2026-04-07-mcp-plugin/plan.md`
- `.agents/sessions/2026-04-07-mcp-plugin/tasks.md`

**Commits:**

- `HEAD` — `📝 docs: wire MCP prompt inventory`

**Decisions & context for next phase:**

- Full verification passed; the branch is ready for review
- `http` transport remains implemented via the official streamable HTTP client transport, which should be documented if naming clarity becomes an issue later

## Phase 7: MCP Admin UX Polish

**Status:** complete

**Tasks completed:**

- 7.1: Re-inspected the MCP plugins page, admin plugin endpoints, runtime status model, and existing session docs before changing UI behavior
- 7.2: Added a lightweight admin MCP status endpoint backed by the shared runtime manager so the UI can show running/backoff/suppressed/discovered server state
- 7.3: Refactored the MCP editor into clearer summary + per-server cards with better hierarchy, transport-specific copy, and runtime detail panels
- 7.4: Replaced JSON-first args/env/headers inputs with structured argument rows and key/value editors while preserving the existing persisted config shape
- 7.5: Added client-side validation, duplicate-name detection, and explicit dirty/saving/saved feedback so save behavior is easier to understand
- 7.6: Updated plugin docs/session notes and reran generate/format/lint/test

**Files changed:**

- `internal/admin/server.go` — added MCP status hook plumbing and a plugin-status route
- `internal/admin/plugins.go` — added MCP plugin status handler
- `cmd/anna/gateway.go` — wired admin MCP status snapshots from the shared runtime manager
- `internal/admin/ui/pages/plugins.templ` — redesigned MCP editor layout and runtime/status presentation
- `internal/admin/ui/pages/plugins_templ.go` — regenerated templ output
- `internal/admin/ui/static/js/pages/plugins.js` — added structured MCP form state, validation, save-state handling, and status loading
- `internal/admin/server_test.go` — added MCP plugin status endpoint coverage
- `docs/content/docs/features/plugin-system.md`
- `docs/content/docs/features/plugin-system.zh.md`
- `docs/content/docs/features/plugin-system.ja.md`
- `.agents/sessions/2026-04-07-mcp-plugin/plan.md`
- `.agents/sessions/2026-04-07-mcp-plugin/tasks.md`

**Commits:**

- `HEAD` — pending final polish commit

**Decisions & tradeoffs:**

- Kept config compatibility by translating structured UI rows back into the exact same `servers[].args/env/headers` JSON shape already consumed by `internal/mcp.DecodeConfig`
- Added a separate status endpoint instead of changing the persisted plugin payload shape, which keeps storage/API compatibility cleaner and avoids mixing runtime state into config data
- Chose lightweight client-side validation over introducing new backend-only schema rules, so UX improves without changing the MCP runtime/tool contract
