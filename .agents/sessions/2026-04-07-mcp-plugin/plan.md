# Plan: MCP Plugin Tool + Prompt Integration

## Overview

Add a new built-in Anna tool plugin, `mcp`, that manages multiple configured MCP servers, proxies tool discovery/execution across all supported transports, and exposes discovered MCP tools to the model through the system prompt. Admins configure servers in the admin UI; enabling the plugin starts all configured servers in the background, disabling it stops them. The model uses one generic `mcp` tool with `list`, `get`, and `exec` actions, but prompt rendering also injects discovered MCP tools under the Tools section so the model does not need to call `list` repeatedly.

### Goals

- Add a built-in `tool/mcp` plugin with admin-managed config stored as JSON in `settings_plugins.config`
- Support multiple MCP servers and all transports supported by the chosen MCP client library
- Supervise server lifecycle: start on plugin enable, stop on disable, restart on crash, suppress always-failing servers
- Expose a single Anna `mcp` tool with `list`, `get`, `exec`
- Generate canonical discovered tool IDs as `mcp__<servername>__<toolname>` with non-alphanumeric characters stripped/sanitized deterministically
- Inject discovered MCP tools into `internal/agent/runner/prompt.go` / `template/system_prompt.tmpl` when the plugin is enabled
- Explicitly instruct the model to call `mcp get` before `mcp exec`
- Add admin-only form editing for MCP server config, backed by JSON storage

### Success Criteria

- [ ] `tool/mcp` is seeded as a built-in tool plugin and appears in the Plugins admin page
- [ ] Admins can edit MCP server config from the admin UI and persist it to plugin JSON config
- [ ] Enabling `tool/mcp` starts all configured MCP servers; disabling it stops them
- [ ] Supervisor auto-restarts transient failures and suppresses permanently failing servers after bounded retries/backoff
- [ ] `mcp list` returns discovered tools with normalized `id`, `name`, `description`, and server metadata
- [ ] `mcp get` returns full schema/details for a discovered MCP tool
- [ ] `mcp exec` validates target resolution and returns normalized Anna-facing output
- [ ] Prompt rendering includes discovered MCP tools when enabled and contains a clear “get before exec” instruction
- [ ] Pool/plugin hot reload picks up enable/disable and config changes without requiring process restart for new sessions
- [ ] Tests cover ID sanitization, config decoding, supervisor behavior, tool discovery/execution normalization, admin API auth, and prompt injection
- [ ] Docs and builtin anna skill are updated to describe MCP plugin behavior and workflow

### Out of Scope

- Dynamically registering each MCP tool as a standalone Anna tool in `pkg/tools.Registry`
- Secret encryption/redaction beyond existing plugin JSON storage
- Per-user MCP configuration or non-admin editing
- Rich MCP health dashboards beyond basic plugin page status/config affordances
- Arbitrary third-party plugin installation; this remains a built-in Go plugin

## Technical Approach

Implement the MCP integration as a built-in tool plugin plus a long-lived MCP runtime manager owned by the process. The runtime manager reads `tool/mcp` config from `settings_plugins`, supervises all configured servers, caches discovered tool metadata, and provides a thread-safe lookup/execution API to the `mcp` tool and prompt builder. The prompt builder consumes the cached registry to advertise tools directly in the system prompt when the plugin is enabled.

### Key Design Decisions

- **One generic tool surface:** Keep a single tool named `mcp` with actions `list|get|exec`; discovered MCP tools are identifiers managed inside the plugin runtime, not independent Anna tools.
- **Prompt-cached discovery:** Avoid repeated `mcp list` calls by projecting discovered tools into the system prompt. `mcp list` still exists for runtime inspection/debugging and explicit model usage.
- **Mandatory `get` before `exec`:** Encode this in prompt text and keep `get` the authoritative source for full schemas.
- **JSON-backed plugin config:** Reuse `settings_plugins.config` with typed decode helpers, while exposing a form-based admin editor.
- **Shared runtime manager:** Build one process-wide manager instead of one manager per agent/session to avoid redundant MCP connections and subprocesses.
- **Official Go MCP library:** Use the official Go MCP library as the transport/client foundation, wrapping it behind Anna-local adapters where lifecycle and normalization behavior differ.
- **Transport abstraction first:** Model config and runtime around a transport enum + transport-specific settings so stdio/HTTP/SSE/streamable transports share the same supervision and discovery pipeline.
- **Runtime ID map:** Auto-generate sanitized canonical IDs and maintain a runtime map from canonical ID → original server/tool identity so prompt rendering and execution stay stable without depending on wire names.
- **Prompt shows only valid tools:** Render only healthy/discovered MCP tools that are currently resolvable through the runtime manager; suppressed/offline/invalid tools stay out of the prompt.
- **Fail-fast suppression:** Track consecutive failures and backoff windows per server; after the threshold is exceeded, mark the server suppressed until config change or plugin re-enable resets it.

### Components

- **`plugins/tools/mcp/`**: Tool plugin registration and `mcp` tool implementation
- **`internal/mcp/`**: Runtime manager, config models, transport adapters, server supervisor, discovery cache, ID sanitization, normalized result mapping
- **`internal/admin/plugins.go` + UI**: MCP config read/update endpoints and admin form integration on the Plugins page
- **`internal/agent/runner/prompt.go` + `template/system_prompt.tmpl`**: MCP prompt inventory injection and “get before exec” guidance
- **`internal/agent` / startup wiring**: Runtime manager lifecycle, hot reload on plugin enable/disable/config changes
- **Docs + builtin skill**: User-facing description of MCP plugin setup and execution workflow

### Proposed Config Shape

Persist under plugin ID `tool/mcp`:

```json
{
  "servers": [
    {
      "name": "github",
      "enabled": true,
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_TOKEN": "..."
      },
      "url": "",
      "headers": {},
      "timeout_seconds": 30
    }
  ]
}
```

Transport-specific fields will be optional, with validation based on `transport`.

### Runtime Data Model

- **Server spec:** persisted config fields (name, enabled, transport, settings)
- **Server state:** runtime-only health/supervision fields (status, PID/session/client, last error, restart count, suppressed-until, discovered tools hash)
- **Tool cache entry:** normalized ID, original server name, original tool name, description, schema snapshot, annotations, last refreshed time
- **ID resolution map:** canonical sanitized ID → `{serverName, toolName}` plus collision bookkeeping for deterministic uniqueness

## Implementation Phases

### Phase 1: Discovery + Runtime Skeleton

1. Add `mcp` to built-in tool plugin seeding and blank-import/plugin registration plumbing (files: `internal/config/plugin.go`, plugin registration import location, `plugins/tools/mcp/...`)
2. Introduce `internal/mcp/config.go` with typed config structs, validation, transport enum/constants, and JSON decode helpers from `config.Plugin.Config`
3. Introduce `internal/mcp/ids.go` with deterministic sanitization and canonical ID generation/parsing for `mcp__<server>__<tool>`, plus runtime collision-safe ID mapping helpers
4. Introduce `internal/mcp/manager.go` interface + skeleton implementation for runtime lifecycle, cache access, and exec proxying
5. Add unit tests for config decode/validation and ID sanitization/collision handling assumptions

### Phase 2: Server Lifecycle + Discovery Cache

1. Implement per-server supervisor state machine in `internal/mcp/supervisor.go`
2. Add transport adapters/client bootstrap for all supported MCP transports via the official Go MCP library wrapper
3. Implement enable/start, disable/stop, auto-restart, bounded exponential backoff, and suppression of always-failing servers
4. Implement tool discovery refresh and cache snapshot APIs (`ListTools`, `GetTool`, server health snapshot)
5. Hook lifecycle into app startup/admin toggle flow so plugin enable/disable starts/stops manager-managed servers (likely via new admin server lifecycle hooks plus startup bootstrap)
6. Add tests for lifecycle transitions, suppression, and cache refresh behavior using fake transport clients

### Phase 3: `mcp` Tool Execution Surface

1. Implement `plugins/tools/mcp/tool.go` with `list|get|exec` schema and execution routing into the runtime manager
2. Normalize `list` output to lightweight metadata only
3. Normalize `get` output to include full input/output schema, annotations, and server/tool metadata
4. Normalize `exec` output into stable Anna-facing result structure (`ok`, `id`, `server`, `tool`, `content`, `structured`, `is_error`, `meta` or equivalent)
5. Add error handling for unknown/suppressed/offline servers, unresolved IDs, invalid args, and missing preconditions
6. Add tool tests for action validation and result normalization

### Phase 4: Admin API + Plugins UI

1. Extend admin plugin API with MCP config update endpoint(s) using `store.SetPluginConfig` and admin-only auth (`internal/admin/plugins.go`, `server.go`)
2. Add MCP-specific config panel/form to Plugins page, backed by existing plugin list and plugin config JSON
3. Support editing multiple servers with transport-specific fields, JSON preview/raw fallback if needed, and save feedback
4. Ensure config changes trigger runtime reconciliation/hot reload of MCP manager and pool tool reload where needed
5. Add admin tests for authz and config persistence; add minimal UI behavior tests if coverage exists in current patterns

### Phase 5: Prompt Integration

1. Extend `internal/agent/runner/prompt.go` prompt data model to carry MCP tool inventory and plugin-enabled state
2. Add a prompt-facing MCP inventory adapter that snapshots runtime manager tool cache without blocking model startup on slow discovery
3. Update `internal/agent/runner/template/system_prompt.tmpl` to render only valid/discovered MCP tools under Tools when plugin enabled
4. Add explicit instruction text: use prompt-listed MCP tools for discovery, but always call `mcp get` before `mcp exec`
5. Ensure fallback behavior when MCP is enabled but servers are offline/suppressed (prompt should degrade gracefully)
6. Add tests for prompt rendering with/without MCP plugin and tool inventory

### Phase 6: Wiring, Docs, and Verification

1. Wire manager construction into process startup so the admin server and tool plugin share the same runtime manager instance
2. Update hot-reload paths for plugin enable/disable/config changes to reconcile manager state and reload plugin tools for new sessions
3. Update docs: plugin system doc and a focused MCP plugin doc under docs/content/docs/
4. Update `internal/agent/runner/builtin/anna/SKILL.md` with MCP setup and `get`-before-`exec` workflow
5. Run `mise run format`, `mise run lint`, `mise run test`

## Testing Strategy

- Unit tests for config validation per transport
- Unit tests for tool ID sanitization/parsing and deterministic collisions behavior
- Unit tests for manager cache snapshots and server state transitions
- Supervisor tests for restart backoff and suppression of always-failing servers
- Tool tests for `list|get|exec` request validation and normalized responses
- Admin API tests for admin-only config mutation and persistence
- Prompt tests for MCP inventory rendering and mandatory instruction text
- Integration tests with fake MCP clients/transports to exercise discovery + exec end-to-end

## Risks

| Risk | Impact | Mitigation |
| ---- | ------ | ---------- |
| MCP transport library capability mismatch vs “all transports” requirement | High | Choose/verify library support up front; wrap transports behind internal adapter so unsupported variants are isolated clearly |
| Shared runtime manager introduces concurrency issues around cache and lifecycle | High | Centralize state behind mutexes, use snapshot copies, race-test with fake clients |
| Prompt inventory can become stale vs live server state | Medium | Refresh cache on connect/restart/config change and render last-known-good inventory with timestamps/status |
| Tool ID sanitization can collide for different server/tool names | Medium | Add deterministic collision suffixing or reject duplicates with explicit errors during discovery |
| Hot reload may update admin/runtime state but not existing sessions | Medium | Target new-session correctness, document existing-session behavior, reload pool factories for plugin tool changes |
| UI complexity for multi-transport forms grows quickly | Medium | Start with concise transport-specific sections plus raw JSON preview/edit fallback |

## Open Questions

- On ID sanitization collisions, should the runtime append a deterministic suffix, or reject conflicting tools from prompt exposure while still surfacing the error in admin/runtime diagnostics?

## Review Feedback

- Initial plan drafted after inspecting current plugin registry, admin plugin toggle flow, pool hot reload behavior, and prompt template structure.
- Requirements refined: use the official Go MCP library, render only valid MCP tools in prompt, and keep a runtime canonical-ID map rather than deriving IDs ad hoc at call time.

## Final Status

- Implemented the MCP plugin end-to-end on branch `feat/mcp-plugin`.
- Added built-in `tool/mcp` plugin seeding, shared MCP runtime management, official Go MCP SDK transport bootstrap, server supervision with restart/backoff/suppression, normalized `mcp` tool actions (`list|get|exec`), admin MCP config editing, prompt integration for valid MCP tools, updated docs, and builtin anna skill sync.
- Verification completed with `mise run format`, `mise run lint`, and `mise run test`.
- Notable implementation decision: `http` currently reuses the official streamable HTTP client transport, which matches modern MCP HTTP behavior in the official SDK.
