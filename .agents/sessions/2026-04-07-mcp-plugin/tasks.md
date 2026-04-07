# Tasks: MCP Plugin Tool + Prompt Integration

## Phase 1: Discovery + Runtime Skeleton

- [x] 1.1: Seed built-in `tool/mcp` plugin entry and registration plumbing
- [x] 1.2: Add typed MCP config models + JSON decode/validation helpers
- [x] 1.3: Add MCP tool ID sanitization/parsing helpers and runtime canonical-ID mapping
- [x] 1.4: Add shared runtime manager interface/skeleton
- [x] 1.5: Add tests for config + ID helpers

## Phase 2: Server Lifecycle + Discovery Cache

- [x] 2.1: Implement per-server supervisor state
- [x] 2.2: Implement official Go MCP library transport client bootstrap adapters
- [x] 2.3: Implement start/stop/restart/suppression behavior
- [x] 2.4: Implement discovery cache + snapshot APIs
- [x] 2.5: Wire plugin enable/disable lifecycle hooks
- [x] 2.6: Add lifecycle/cache tests

## Phase 3: `mcp` Tool Execution Surface

- [x] 3.1: Implement `mcp` tool definition
- [x] 3.2: Implement `list` normalization
- [x] 3.3: Implement `get` normalization
- [x] 3.4: Implement `exec` normalization
- [x] 3.5: Add tool action/error-path tests

## Phase 4: Admin API + Plugins UI

- [x] 4.1: Add admin endpoint(s) for MCP plugin config updates
- [x] 4.2: Add MCP config form to Plugins page
- [x] 4.3: Support multi-server editing with transport-specific fields
- [x] 4.4: Trigger runtime reconciliation after config changes
- [x] 4.5: Add admin auth/persistence tests

## Phase 5: Prompt Integration

- [x] 5.1: Extend prompt data model for MCP inventory
- [x] 5.2: Add manager-backed MCP inventory snapshot adapter
- [x] 5.3: Render only valid MCP tools in system prompt template
- [x] 5.4: Add explicit `mcp get` before `mcp exec` instruction
- [x] 5.5: Add prompt rendering tests

## Phase 6: Wiring, Docs, and Verification

- [x] 6.1: Share runtime manager across startup/admin/tool layers
- [x] 6.2: Hook plugin hot reload for enable/disable/config changes
- [x] 6.3: Update docs for MCP plugin behavior
- [x] 6.4: Update builtin anna skill docs
- [x] 6.5: Run format + lint + test

## Phase 7: MCP Admin UX Polish

- [x] 7.1: Inspect current MCP admin UI/backend and capture polish scope
- [x] 7.2: Expose lightweight MCP runtime status to the admin UI
- [x] 7.3: Refactor the MCP editor into clearer per-server cards and transport-specific sections
- [x] 7.4: Replace raw JSON-first args/env/headers editing with structured editors that preserve config compatibility
- [x] 7.5: Add client-side validation plus dirty/saving/saved feedback
- [x] 7.6: Update docs/session notes and rerun generate + format + lint + test

## Phase 8: PR Review Fixes

- [x] 8.1: Guard nil `session.Wait()` shutdowns so clean remote disconnects do not panic the supervisor
- [x] 8.2: Propagate configured HTTP headers into remote MCP transports
- [x] 8.3: Make canonical MCP tool ID rebuilding deterministic across reconciles
- [x] 8.4: Add regression tests for shutdown, headers, and canonical ID stability
- [x] 8.5: Run format + lint + test
