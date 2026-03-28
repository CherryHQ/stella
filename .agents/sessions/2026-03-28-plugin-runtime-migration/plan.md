# Plan: Plugin Runtime Migration

## Overview

Build a subprocess plugin runtime and migrate existing first-party tools and
network channels onto it first, while keeping the current JS plugin path and
hard-coded builtins temporarily for compatibility. The goal is to make tools
and channels replaceable without changing Anna's external behavior.

### Goals

- Introduce a versioned subprocess plugin host with discovery, manifest
  validation, supervision, and capability-based loading.
- Migrate built-in tools (`read`, `bash`, `edit`, `write`, `webfetch`) to
  bundled first-party subprocess plugins.
- Migrate network channels (Telegram, QQ, Feishu, Weixin) to bundled
  first-party subprocess plugins.
- Preserve DB-backed configuration and existing agent/session behavior during
  the migration.

### Success Criteria

- [ ] Anna can discover bundled plugins from a plugin catalog and load them by
      manifest.
- [ ] Tool execution can be routed through subprocess plugins without changing
      the engine contract.
- [ ] Network channels can start, stop, and notify through subprocess plugins
      instead of hard-coded constructors.
- [ ] Existing built-in tool behavior remains functionally equivalent from the
      agent's perspective.
- [ ] Existing channel config stored in `settings_channels` remains usable
      without manual migration.
- [ ] JS plugins remain operational during this migration slice.

### Out of Scope

- Providers and memory plugins.
- Replacing the CLI channel.
- Removing the current JS plugin system.
- Third-party install UX beyond the runtime/catalog support needed for bundled
  plugins.

## Technical Approach

Introduce a subprocess plugin subsystem with four core parts:

- `pluginapi`: manifest types, protocol envelopes, capability constants, and
  version negotiation.
- `pluginhost`: discovery, validation, process supervision, stdio transport,
  health checks, restart policy, and structured logging.
- adapters: host-side adapters that make subprocess plugins implement existing
  Anna interfaces for tools and channels.
- bundled first-party plugin binaries: first-party executables built from repo
  code and shipped as Anna-managed plugins.

Key design decisions:

- Use a single versioned stdio protocol for all plugin kinds.
- Keep canonical state in the host; plugins do not write directly to Anna DB
  internals.
- Load tools lazily and channels eagerly.
- Preserve current DB config as the source of truth, layering plugin binding on
  top during migration.
- Preserve current engine and session contracts so the migration stays mostly
  at the loading and adapter layer.

### Components

- **Plugin API**: protocol messages, manifest schema, capability model,
  handshake, and health endpoints.
- **Plugin Catalog**: bundled plugin discovery and binding resolution.
- **Tool Adapter**: exposes subprocess tool plugins as `tool.Tool`.
- **Channel Adapter**: exposes subprocess channel plugins as `channel.Channel`.
- **Bundled Plugin Binaries**: first-party executables for existing tools and
  channels.
- **Compatibility Layer**: preserves current settings loading and channel
  config shapes during the migration.

## Implementation Phases

### Phase 1: Runtime Foundation

1. Define plugin manifest, protocol, capability model, and supervisor package
   layout (files: `internal/pluginapi/*`, `internal/pluginhost/*`).
2. Implement subprocess lifecycle management, stdio framing, handshake, health,
   shutdown, restart policy, and log handling (files:
   `internal/pluginhost/*`).
3. Implement bundled plugin discovery and host-side registration APIs (files:
   `internal/pluginhost/catalog.go`, `internal/pluginhost/manifest.go`,
   `internal/config/*` as needed).
4. Add focused tests for protocol negotiation, invalid manifests, crashed
   processes, timeouts, and restart behavior (files:
   `internal/pluginhost/*_test.go`).

### Phase 2: Tool Plugin Migration

1. Introduce a subprocess tool adapter and plugin registry path that plugs into
   runner setup (files: `internal/pluginhost/tooladapter.go`,
   `internal/agent/tool/tool.go`, `internal/agent/runner/gorunner.go`,
   `cmd/anna/commands.go`).
2. Migrate first-party tools `read`, `bash`, `edit`, `write`, and `webfetch`
   into bundled subprocess plugins (files: `cmd/anna-plugin/*`,
   `internal/agent/tool/*`, plugin manifests under a bundled plugin dir).
3. Preserve sandboxing and working-directory semantics now enforced in the host
   tool registry.
4. Add integration tests proving equivalent tool definitions and execution
   behavior.

### Phase 3: Channel Plugin Migration

1. Define the channel plugin contract for start, stop, inbound message
   delivery, and notifications (files: `internal/pluginapi/*`,
   `internal/pluginhost/channeladapter.go`).
2. Replace hard-coded channel construction with catalog-driven loading (files:
   `cmd/anna/gateway.go`, `internal/pluginhost/*`).
3. Migrate Telegram, QQ, Feishu, and Weixin as bundled channel plugins while
   preserving existing config schemas and auth wiring (files:
   `internal/channel/*`, `cmd/anna-plugin/*`, bundled plugin manifests).
4. Add supervision, restart, and partial-failure handling so a crashed channel
   plugin does not terminate the daemon.

### Phase 4: Integration, Compatibility, and Docs

1. Add plugin binding config for bundled tools and channels without breaking
   current DB settings (files: `internal/config/*`, `cmd/anna/*`,
   admin/config surfaces as needed).
2. Keep JS plugins working and document migration boundaries clearly (files:
   `docs/*`, `README.md`).
3. Add operator-facing status and log surfaces for plugin processes (files:
   `internal/admin/*`, `internal/pluginhost/*` as needed).
4. Document the bundled plugin model and the migration path for later provider
   and memory work.

## Testing Strategy

- Unit tests for manifest parsing, protocol framing, handshake validation, and
  supervisor lifecycle.
- Integration tests for subprocess tool execution under success, timeout,
  stderr noise, invalid JSON, and crash scenarios.
- Integration tests for channel startup, inbound event bridging,
  notifications, and restart-after-crash behavior.
- Regression tests ensuring current channel configs still deserialize and
  function.
- End-to-end smoke tests covering daemon boot with bundled plugins only.

## Risks

| Risk | Impact | Mitigation |
| ---- | ------ | ---------- |
| Protocol scope grows too quickly | High | Keep v1 limited to tools and channels only; defer providers and memory |
| Tool sandbox behavior regresses | High | Preserve host-side sandbox enforcement and add regression coverage for each migrated tool |
| Channel plugin crash loops | High | Add restart backoff, health checks, and isolate failure per channel |
| Packaging bundled plugin executables becomes messy | Medium | Standardize manifest and output layout early and wire it into the build |
| Compatibility layer becomes permanent debt | Medium | Make the deprecation boundary explicit and follow up after tools/channels land |

## Open Questions

- None. Earlier scope questions were converted into explicit assumptions.

## Review Feedback

- Self-review: the plan addresses the approved scope, keeps phases ordered
  around runtime first, and defers providers/memory to avoid overloading v1.

## Final Status

Implementation complete on branch `feat/plugin-runtime-migration`.

- Phase 1: runtime foundation — complete
- Phase 2: tool subprocess migration — complete
- Phase 3: channel subprocess migration — complete
- Phase 4: bindings, operator visibility, compatibility, and docs — complete
