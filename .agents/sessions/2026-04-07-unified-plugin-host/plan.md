# Plan: Unified Plugin Host

## Overview

Refactor Anna's plugin architecture from kind-specific registries into a unified plugin host model where a single plugin package owns config and lifecycle and can register multiple capabilities. The first implementation slice will introduce the shared host/platform APIs, add an internal host/runtime orchestration layer, adapt existing tool/provider/hook/memory registries, and migrate MCP as the first reference plugin.

### Goals

- Introduce a stable shared plugin platform API under `pkg/plugins`
- Introduce an internal host/runtime orchestration layer under `internal/pluginhost`
- Preserve current behavior while shifting ownership from kind-specific wiring to plugin-owned capability registrations
- Migrate MCP to the new host model with config, runtime, tool, status, and prompt inventory capabilities
- Leave the codebase ready for follow-up migrations such as `reflect`

### Success Criteria

- [ ] `pkg/plugins` defines plugin registration, host interfaces, capability registrations, config service, runtime lookup, and shared runtime/status types
- [ ] `internal/pluginhost` can load built-in plugins, collect capability registrations, and expose host services without import cycles
- [ ] Existing tool/provider/hook/memory plugins still work through compatibility adapters during migration
- [ ] Built-in plugin seeding and plugin state lookup continue to work without DB schema changes and use plugin ID as the canonical ownership key
- [ ] MCP registers config, runtime, tool, status, and prompt inventory capabilities through the new host
- [ ] Admin config save and plugin status fetch use generic host-backed config/status plumbing for MCP while preserving current MCP UI behavior
- [ ] Gateway/runtime startup no longer relies on MCP-specific lifecycle hooks
- [ ] Prompt integration no longer reaches into MCP global singleton state from outside the plugin package
- [ ] Plugin enable/disable and config changes trigger the correct reload/reapply behavior for affected subsystems (runtime, tools, hooks, providers)
- [ ] Current MCP behavior remains unchanged: config persistence, runtime supervision, tool execution, prompt inventory, and admin status UX
- [ ] Tests cover host registration, runtime apply semantics, generic config/status flow, plugin state compatibility, and MCP integration through the new host

### Out of Scope

- Database schema changes to `settings_plugins`
- Aggressive plugin package path flattening
- Channel migration to the unified host
- Reflect migration to the unified host
- Generic schema-driven admin UI rendering
- Third-party plugin loading or sandboxing

## Technical Approach

Introduce two new layers:

- **`pkg/plugins`** as the stable plugin-facing contract surface
- **`internal/pluginhost`** as the host implementation for plugin catalog loading, capability indexing, config/status dispatch, and runtime orchestration

The design follows the v2 architecture review:

- plugin identity and capability identity stay distinct
- plugins own config and lifecycle
- capabilities remain explicit subsystem registrations
- runtime lifecycle uses declarative `Apply(ctx, desired PluginState)` semantics
- prompt contribution remains narrow and inventory-based
- DB schema and package layout stay stable in the first slice

### Components

- **`pkg/plugins/`**: plugin interface, host interfaces, capability registration structs, config/runtime lookup interfaces, shared types
- **`internal/pluginhost/`**: plugin catalog loader, host implementation, config service adapter, runtime manager, capability indexes, compatibility adapters
- **App wiring**: host creation in startup/setup paths and generic runtime apply on boot/config changes
- **Admin integration**: generic plugin config validation/save and generic plugin status lookup
- **MCP migration layer**: host-backed MCP registrations replacing MCP-specific admin/gateway/global-manager wiring
- **Prompt integration**: host-backed prompt inventory lookup for MCP discovered tools

## Implementation Phases

### Phase 1: Shared Plugin Platform Contracts

1. Add `pkg/plugins` package with plugin, host, capability, config, runtime, and prompt-inventory interfaces/types (files: `pkg/plugins/*.go`)
2. Define narrow build contexts for tool/provider/hook/channel/memory/runtime capabilities (files: `pkg/plugins/*.go`)
3. Add unit tests for shared plugin registration/runtime helper types (files: `pkg/plugins/*_test.go`)

### Phase 2: Internal Host Implementation

1. Add `internal/pluginhost` catalog loader and host implementation (files: `internal/pluginhost/*.go`)
2. Add capability indexing by plugin ID and capability identity, with duplicate detection rules that fail fast for conflicting registrations (files: `internal/pluginhost/*.go`)
3. Add config service adapter backed by existing `config.Store`, including plugin-state lookup helpers that treat plugin ID as canonical while remaining compatible with current `settings_plugins` rows (files: `internal/pluginhost/config_service.go` or similar)
4. Add runtime host with `Apply/Stop/Snapshot` orchestration and explicit desired-state application flow for enable/disable/config changes (files: `internal/pluginhost/runtime*.go`)
5. Add unit tests for host registration, duplicate detection, plugin-state compatibility, runtime apply sequencing, and runtime lookup (files: `internal/pluginhost/*_test.go`)

### Phase 3: Compatibility Adapters

1. Adapt existing tool registry into host capability registrations (files: `internal/pluginhost/adapter_tools.go`, related plugin registry touchpoints if needed)
2. Adapt existing provider registry into host capability registrations (files: `internal/pluginhost/adapter_providers.go`)
3. Adapt existing hook registry into host capability registrations (files: `internal/pluginhost/adapter_hooks.go`)
4. Adapt existing memory registry into host capability registrations (files: `internal/pluginhost/adapter_memory.go`)
5. Add integration tests proving existing non-MCP plugins still load/build correctly (files: `internal/pluginhost/*_test.go`, existing runtime tests if needed)

### Phase 4: App and Admin Wiring

1. Create plugin host during app setup and thread it through runtime startup (files: `cmd/anna/commands.go`, related setup structs)
2. Replace MCP-specific startup lifecycle bootstrapping with generic runtime host application (files: `cmd/anna/gateway.go`, host wiring files)
3. Add generic plugin config validate/save path in admin, with legacy route compatibility preserved where current UI expects kind/name addressing (files: `internal/admin/plugins.go`, `internal/admin/server.go`)
4. Add generic plugin status lookup path in admin, preserving current MCP page behavior while moving lookup ownership into the host (files: `internal/admin/plugins.go`, `internal/admin/server.go`)
5. Define and wire a clear reload/reapply matrix for plugin enable/config changes:
   - runtime host apply for runtime capabilities
   - `ReloadPluginTools` for tool-capability changes
   - `ReloadPluginHooks` for hook-capability changes
   - `ReloadPluginProviders` for provider-capability changes
   (files: `cmd/anna/commands.go`, `cmd/anna/gateway.go`, `internal/admin/plugins.go`, `internal/agent/pool_manager.go` as needed)
6. Add tests for generic config/status host-backed behavior and reload/reapply triggering (files: `internal/admin/server_test.go`, new host tests if needed)

### Phase 5: MCP Migration

1. Register MCP config capability (files: `plugins/tools/mcp/*` or a new MCP registration layer there)
2. Register MCP runtime capability (files: `plugins/tools/mcp/*`, MCP runtime wrapper code)
3. Register MCP tool capability (files: `plugins/tools/mcp/tool.go`, new host registration layer)
4. Register MCP status capability (files: `plugins/tools/mcp/*`)
5. Register MCP prompt inventory capability (files: `plugins/tools/mcp/*`, prompt integration touchpoints)
6. Add typed MCP runtime lookup helper inside the plugin package and stop external code from depending on `internal/mcp.DefaultManager()` (files: `plugins/tools/mcp/*`, prompt/startup integration points)
7. Replace MCP-specific admin/gateway wiring with host-backed behavior (files: `internal/admin/*`, `cmd/anna/gateway.go`, MCP integration points)
8. Keep the physical package path stable in this slice unless the host migration becomes materially cleaner with a package move; if moved, do it as a contained follow-up inside this phase rather than a prerequisite
9. Add MCP host-backed integration tests for config, runtime, tool exec, status, prompt inventory, and legacy admin route compatibility (files: `plugins/tools/mcp/*_test.go`, `internal/admin/server_test.go`, prompt tests)

### Phase 6: Cleanup and Stabilization

1. Delete `SetMCPLifecycle` and related MCP-only admin/runtime plumbing (files: `internal/admin/server.go`, `cmd/anna/gateway.go`, related callsites)
2. Remove external `DefaultManager()` usage outside MCP plugin internals (files: MCP integration points, prompt code, startup code)
3. Tighten comments/docs around MCP as the first advanced host-backed plugin (files: docs and inline package docs)
4. Update session notes with migration outcomes and remaining follow-up work (files: `.agents/sessions/...`)

### Phase 7: Verification and Docs

1. Run `mise run format`
2. Run `mise run lint`
3. Run `mise run test`
4. Update plugin-system docs if needed (files: `docs/content/docs/features/plugin-system.md` and related docs)
5. Update builtin anna skill if needed (files: `internal/agent/runner/builtin/anna/SKILL.md`)
6. Record reflect/channel follow-up work without implementing it

## Testing Strategy

- Unit tests for `pkg/plugins` registration and helper behavior
- Unit tests for `internal/pluginhost` capability indexing, duplicate detection, runtime apply semantics, and runtime lookup
- Integration tests proving current non-MCP plugins still load/build through compatibility adapters
- Generic admin config update tests validating through host-backed config capability
- Generic admin status tests resolving through host-backed status capability
- MCP integration tests for startup apply, config reconcile, status fetch, tool execution, and prompt inventory
- Regression tests ensuring MCP shutdown, transport/header behavior, and deterministic IDs stay unchanged

## Risks

| Risk | Impact | Mitigation |
| ---- | ------ | ---------- |
| Import cycles between plugin packages and host implementation | High | Keep plugin-facing contracts in `pkg/plugins`; host implementation in `internal/pluginhost`; plugins import only `pkg/...` |
| Compatibility adapter layer becomes too magical | High | Keep adapters explicit and temporary; add focused registration-flow tests per capability type |
| Runtime apply semantics are misunderstood | High | Centralize lifecycle policy in runtime host; document `Apply` contract clearly; use MCP as reference implementation |
| MCP migration accidentally changes working behavior | High | Add integration coverage before deleting old wiring; migrate behind compatibility shims first |
| Prompt integration regresses due to runtime lookup timing | Medium | Keep prompt contribution snapshot-based; ensure startup order applies runtimes before pool creation |
| Admin routes/UI drift during backend refactor | Medium | Preserve route behavior where possible; back current MCP UI with generic host-backed logic |
| DB semantics become muddled during transition | Medium | Keep DB schema unchanged initially; treat plugin ID as canonical in host logic |

## Assumptions

- **Runtime snapshot shape:** v1 uses a small host-level snapshot envelope only where the host needs stable orchestration metadata; plugin status payloads exposed to admin remain plugin-defined `any` so MCP can preserve its current status shape without premature schema design.
- **Compatibility adapter placement:** adapters live in `internal/pluginhost` in the first slice; existing registries should stay as thin as possible and avoid learning host internals unless a minimal helper is clearly simpler.
- **MCP package path:** MCP remains in `plugins/tools/mcp` for the first slice; package flattening is explicitly deferred unless a concrete implementation blocker appears.
- **Admin route compatibility:** host-backed logic will support current legacy kind/name MCP routes during migration; any plugin-ID-only route cleanup is deferred until after behavior migration is stable.

## Review Feedback

- The earlier draft was tightened to preserve the distinction between plugin identity and capability identity
- Runtime lifecycle was changed to declarative `Apply()` semantics
- Admin concerns were split into config and status instead of one broad admin abstraction
- Prompt contribution was narrowed to structured inventory only
- Database and package-layout cleanup were deferred until after behavior migration proves out

## Final Status

Not started.

Intended outcome after implementation:

- Anna has a unified plugin host with narrow typed APIs
- MCP becomes the first fully host-backed advanced plugin
- Existing plugins continue to work through compatibility adapters
- Built-in plugin state seeding and current admin behavior remain compatible during the transition
- Reflect has a clean second-step migration target
