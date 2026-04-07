# Plan: Unified Plugin Host for Anna

## Overview

Implement the unified plugin host described in `anna-unified-plugin-host-spec-v2.md` so Anna can move from kind-specific plugin registries to plugin-owned multi-capability registration.

The first implementation slice should prove the architecture without broad churn by:

- introducing shared plugin host/platform APIs
- adding an internal host/runtime orchestration layer
- adapting existing tool/provider/hook/memory registries into the new host
- migrating MCP as the first reference plugin
- deleting MCP-specific lifecycle/config/status special cases

This plan intentionally avoids database schema changes, broad package moves, and channel migration in the first slice.

### Goals

- Establish `pkg/plugins` as the stable plugin-facing API surface
- Establish `internal/pluginhost` as the runtime/config/status orchestration layer
- Preserve current behavior while shifting ownership from kind-specific wiring to plugin-owned registrations
- Make MCP fully fit the new host model with config/runtime/tool/status/prompt inventory capabilities
- Leave the codebase in a state where reflect can be the second migration target

### Success Criteria

- [ ] `pkg/plugins` defines plugin registration, host interfaces, capability registrations, config service, runtime lookup, and shared runtime/status types
- [ ] `internal/pluginhost` can load built-in plugins, collect capability registrations, and expose host services without import cycles
- [ ] Existing tool/provider/hook/memory plugins continue to work through compatibility adapters during migration
- [ ] MCP registers `ConfigRegistration`, `RuntimeRegistration`, `ToolRegistration`, `StatusRegistration`, and `PromptInventoryRegistration`
- [ ] Admin config save and status fetch paths use generic plugin config/status plumbing for MCP
- [ ] Gateway/runtime startup no longer uses MCP-specific lifecycle hooks
- [ ] Prompt integration no longer depends on global MCP singleton access from outside the plugin package
- [ ] Current MCP behavior remains unchanged: config persistence, runtime supervision, tool execution, prompt inventory, admin status UX
- [ ] Tests cover host registration, runtime apply semantics, generic config/status flow, and MCP integration through the new host
- [ ] Docs and builtin anna skill remain accurate if any user-visible plugin/config behavior changes

### Out of Scope

- Database schema changes to `settings_plugins`
- Flattening plugin package layout from `plugins/{kind}/...` to `plugins/...`
- Channel migration to the unified host
- Reflect migration to the unified host
- Generic schema-driven admin UI rendering
- Third-party plugin loading or sandboxing

## Technical Approach

Introduce a two-layer plugin platform:

- **`pkg/plugins`** for stable plugin-facing contracts
- **`internal/pluginhost`** for host implementation, capability indexing, runtime orchestration, config/status dispatch, and registry adapters

The host will distinguish:

- **plugin identity** for ownership/config/enablement/runtime
- **capability identity** for subsystem registration

The runtime contract will use a declarative `Apply(ctx, desired PluginState)` model instead of ad hoc start/reconcile callbacks.

MCP will be migrated first because it exercises the full design: config, runtime, tool, status, and prompt inventory.

### Key Design Decisions

- **Plugin remains the ownership boundary**: plugin state is keyed by plugin ID, even if a plugin registers multiple capabilities
- **Capability registration remains explicit**: tool/provider/hook/channel/memory/runtime/config/status/prompt inventory are separate registration types
- **Keep DB schema stable**: reuse the current `settings_plugins` shape and reinterpret it through the new host
- **Use adapters first**: old kind-specific registries continue working while the host becomes the new source of truth
- **Runtime lifecycle is declarative**: host applies desired plugin state and owns orchestration logic
- **Prompt contribution stays narrow**: plugins contribute structured tool inventory only; the core prompt renderer keeps prompt policy text
- **Package-path migration is deferred**: behavior shifts first, directory cleanup later

### Components

- **`pkg/plugins/`**: plugin interface, host interfaces, capability registration structs, config/runtime lookup interfaces, shared types
- **`internal/pluginhost/`**: plugin catalog loader, host implementation, config service adapter, runtime manager, capability indexes, compatibility adapters
- **`internal/config/` integration**: plugin-state loading through existing `settings_plugins` rows
- **`internal/admin/` integration**: generic plugin config validation/save and generic plugin status lookup
- **`cmd/anna/` integration**: plugin host creation during setup/startup and runtime apply on boot/config changes
- **MCP migration layer**: replace MCP-specific admin/gateway/global-manager wiring with host-backed registration
- **Prompt integration layer**: prompt builder reads MCP tool inventory through host-backed plugin capability instead of external singleton access

## Implementation Phases

### Phase 1: Define shared plugin platform contracts

1. Add `pkg/plugins/` with:
   - `Plugin`, `PluginFunc`, plugin catalog registration interfaces
   - `Host`, `RegistryHost`, `ServiceHost`
   - capability registration structs (`ToolRegistration`, `ProviderRegistration`, `HookRegistration`, `MemoryRegistration`, `ChannelRegistration`, `RuntimeRegistration`, `ConfigRegistration`, `StatusRegistration`, `PromptInventoryRegistration`)
   - `PluginState`, `ConfigService`, `RuntimeLookup`, `RuntimeHandle`, `ManagedRuntime`, `RuntimeSnapshot`
   - narrow capability build contexts
   (files: `pkg/plugins/*.go`)
2. Define ownership and validation rules in code comments so platform boundaries are explicit and testable
3. Add focused unit tests for registration data types and any helper logic in `pkg/plugins`

### Phase 2: Build `internal/pluginhost`

1. Add plugin host implementation that:
   - loads registered plugins from the plugin catalog
   - provides a `Host` implementation to plugin `Register()` calls
   - indexes capabilities by plugin ID and capability name
   (files: `internal/pluginhost/*.go`)
2. Add a `ConfigService` adapter backed by existing `internal/config.Store`
3. Add runtime orchestration using `ManagedRuntime.Apply/Stop/Snapshot`
4. Add runtime state tracking and snapshot/status lookup helpers
5. Add unit tests for host registration, duplicate detection, runtime apply sequencing, and runtime lookup

### Phase 3: Add compatibility adapters for existing registries

1. Adapt current tool/provider/hook/memory plugin registries so existing plugin packages can populate host capability registrations without immediate package moves
   (files likely under `internal/pluginhost/adapter_*.go`, plus small changes in `plugins/tools/registry.go`, `plugins/hooks/registry.go`, `plugins/providers/registry.go`, `plugins/memory/registry.go` if required)
2. Ensure current startup/build code can still build tools/providers/hooks/memory through the host-backed view
3. Keep channels untouched in this phase
4. Add integration tests confirming existing non-MCP plugins still load/build correctly through the compatibility layer

### Phase 4: Wire host creation into app setup and admin/runtime plumbing

1. Create the plugin host during `setup()` and carry it through the app runtime
   (files: `cmd/anna/commands.go`, possible setup/result structs)
2. Replace direct MCP runtime boot wiring with generic plugin runtime apply-on-startup behavior
3. Add generic plugin config save and generic plugin status lookup paths in admin
   (files: `internal/admin/plugins.go`, `internal/admin/server.go`)
4. Keep current admin UI endpoints stable where possible, but make them host-backed
5. Add tests for generic config validate/save and generic plugin status behavior

### Phase 5: Migrate MCP to the unified host

1. Register MCP as one plugin that contributes:
   - config capability
   - runtime capability
   - tool capability
   - status capability
   - prompt inventory capability
   (files: `plugins/tools/mcp/*` initially, or a small local registration layer there)
2. Add a host-backed typed runtime lookup helper inside the MCP package so MCP tool/prompt code no longer depends on external global manager access
3. Replace MCP-specific admin validation/status/reconcile logic with generic host-driven behavior
4. Replace MCP-specific gateway lifecycle hooks with generic runtime host application
5. Remove `DefaultManager()` usage from outside the MCP plugin package; keep temporary shim only if required internally during migration
6. Add integration tests covering MCP config apply, status fetch, tool execution, and prompt inventory via the host

### Phase 6: Clean up MCP special cases and stabilize the new platform

1. Delete MCP-specific lifecycle APIs and wiring:
   - `SetMCPLifecycle`
   - MCP-specific admin branches
   - MCP-specific gateway wiring
2. Reduce or remove external reliance on `internal/mcp/store.go` and global singleton patterns
3. Tighten docs/comments around the new plugin host and MCP as the reference advanced plugin
4. Update session artifacts with final architecture decisions and migration notes

### Phase 7: Verification, docs, and next-target preparation

1. Run full verification:
   - `mise run format`
   - `mise run lint`
   - `mise run test`
2. Update docs if behavior/API/admin routes changed:
   - `docs/content/docs/features/plugin-system.md`
   - related docs if needed
   - `internal/agent/runner/builtin/anna/SKILL.md`
3. Record follow-up work for reflect migration and later channel migration, but do not implement them in this slice

## Testing Strategy

### Unit Tests

- `pkg/plugins` registration and helper behavior
- `internal/pluginhost` capability indexing, duplicate detection, plugin ownership semantics
- runtime host apply/stop/snapshot behavior with fake runtimes
- config service adapter behavior with fake or test-backed stores

### Integration Tests

- existing tool/provider/hook/memory plugins still build through compatibility adapters
- generic plugin config update path validates through `ConfigRegistration`
- generic plugin status path resolves through `StatusRegistration`
- MCP runtime apply on startup/config change through the host
- MCP tool execution through host-backed runtime lookup
- prompt inventory for MCP through host-backed prompt contribution

### Regression Coverage

- MCP clean shutdown behavior remains correct
- MCP configured headers/transport behavior remains correct
- deterministic MCP canonical IDs remain unchanged
- plugin tool/hook/provider reload behavior remains correct after host adoption

## Risks

| Risk | Impact | Mitigation |
| ---- | ------ | ---------- |
| Import cycles between plugin packages and host implementation | High | Put plugin-facing contracts in `pkg/plugins`; keep host implementation in `internal/pluginhost`; plugins import only `pkg/...` |
| Compatibility adapter layer becomes too magical and hard to reason about | High | Keep adapters explicit, small, and temporary; add tests that prove registration flow per capability type |
| Runtime apply semantics are misunderstood and plugins implement inconsistent behavior | High | Centralize lifecycle policy in runtime host; document `Apply` contract clearly; use MCP as reference implementation |
| MCP migration accidentally changes working behavior | High | Migrate behavior behind compatibility shims first; add integration tests before deleting old wiring |
| Prompt integration regresses due to runtime lookup timing | Medium | Keep prompt contribution narrow and snapshot-based; ensure startup order applies runtimes before prompt-sensitive pool creation |
| Admin routes/UI drift during backend refactor | Medium | Preserve route shapes where possible in first slice; back existing MCP UI with generic host-backed behavior |
| Database semantics become muddled during transition | Medium | Keep DB schema unchanged initially; use plugin ID as canonical ownership key in host logic; defer schema cleanup |

## Open Questions

- [ ] Should `RuntimeSnapshot` be a small generic struct with `State/Healthy/Message/Details`, or remain plugin-defined `any` at the host boundary in v1?
- [ ] Should compatibility adapters live entirely in `internal/pluginhost`, or should existing kind-specific registries gain host-aware registration helpers?
- [ ] For MCP migration, is it worth introducing a new `plugins/mcp` package immediately, or should the first slice keep MCP in `plugins/tools/mcp` and change only the registration/runtime ownership model?
- [ ] Should plugin enable/disable and plugin config admin APIs switch immediately to `{id}`-only routes, or should the host support both legacy kind/name routes and canonical plugin-ID routes during migration?

## Review Feedback

- Deep review tightened the design versus the original draft:
  - plugin identity and capability identity must remain distinct
  - runtime lifecycle should be declarative via `Apply()`
  - admin should split into config and status instead of one broad admin blob
  - prompt contribution should remain structured and narrow
  - DB and package-layout cleanup should be deferred until after behavior migration proves out

## Final Status

Not started.

Intended result after implementation:

- Anna has a real unified plugin host
- MCP becomes the first fully host-backed advanced plugin
- future runtime plugins like reflect have a clean migration target
- existing plugin behavior remains stable through compatibility adapters
