# Tasks: Unified Plugin Host

## Phase 1: Shared Plugin Platform Contracts

- [x] 1.1: Add `pkg/plugins` package with plugin, host, capability, config, runtime, and prompt-inventory interfaces/types
- [x] 1.2: Define narrow build contexts for tool/provider/hook/channel/memory/runtime capabilities
- [x] 1.3: Add unit tests for shared plugin registration/runtime helper types

## Phase 2: Internal Host Implementation

- [x] 2.1: Add `internal/pluginhost` catalog loader and host implementation
- [x] 2.2: Add capability indexing by plugin ID and capability identity with duplicate detection
- [x] 2.3: Add config service adapter backed by existing `config.Store`
- [x] 2.4: Add plugin-state compatibility helpers that treat plugin ID as canonical without DB schema changes
- [x] 2.5: Add runtime host with `Apply/Stop/Snapshot` orchestration
- [x] 2.6: Add unit tests for host registration, duplicate detection, plugin-state compatibility, runtime apply sequencing, and runtime lookup

## Phase 3: Compatibility Adapters

- [x] 3.1: Adapt existing tool registry into host capability registrations
- [x] 3.2: Adapt existing provider registry into host capability registrations
- [x] 3.3: Adapt existing hook registry into host capability registrations
- [x] 3.4: Adapt existing memory registry into host capability registrations
- [x] 3.5: Add integration tests proving existing non-MCP plugins still load/build correctly

## Phase 4: App and Admin Wiring

- [x] 4.1: Create plugin host during app setup and thread it through runtime startup
- [x] 4.2: Replace MCP-specific startup lifecycle bootstrapping with generic runtime host application
- [x] 4.3: Add generic plugin config validate/save path in admin while preserving current legacy MCP route compatibility
- [x] 4.4: Add generic plugin status lookup path in admin while preserving current MCP UI behavior
- [x] 4.5: Define and wire the reload/reapply matrix for tool, hook, provider, and runtime capability changes
- [x] 4.6: Add tests for generic config/status host-backed behavior and reload/reapply triggering

## Phase 5: MCP Migration

- [x] 5.1: Register MCP config capability
- [x] 5.2: Register MCP runtime capability
- [x] 5.3: Register MCP tool capability
- [x] 5.4: Register MCP status capability
- [x] 5.5: Register MCP prompt inventory capability
- [x] 5.6: Add typed MCP runtime lookup helper inside plugin package
- [x] 5.7: Remove MCP external dependence on global manager access where host-backed lookup is available
- [x] 5.8: Replace MCP-specific admin/gateway wiring with host-backed behavior
- [x] 5.9: Add MCP host-backed integration tests for config, runtime, tool exec, status, prompt inventory, and legacy admin route compatibility

## Phase 6: Cleanup and Stabilization

- [x] 6.1: Delete `SetMCPLifecycle` and related MCP-only admin/runtime plumbing
- [x] 6.2: Remove external `DefaultManager()` usage outside MCP plugin internals
- [x] 6.3: Verify built-in plugin seeding/state lookup still behaves correctly after host adoption
- [x] 6.4: Tighten comments/docs around MCP as the first advanced host-backed plugin
- [x] 6.5: Update session notes with migration outcomes and remaining follow-up work

## Phase 7: Verification and Docs

- [x] 7.1: Run `mise run format`
- [x] 7.2: Run `mise run lint`
- [x] 7.3: Run `mise run test`
- [x] 7.4: Update plugin-system docs if needed
- [x] 7.5: Update builtin anna skill if needed
- [x] 7.6: Record reflect/channel follow-up work without implementing it

## Phase 8: Reflect Host Migration Slice

- [x] 8.1: Register reflect with host-backed config, runtime, and status capabilities
- [x] 8.2: Replace bespoke gateway reflect lifecycle wiring with host-backed `Apply`
- [x] 8.3: Reuse existing standalone `reflect` persistence row without schema changes
- [x] 8.4: Route admin reflect config/status/toggle behavior through generic host plumbing
- [x] 8.5: Add focused tests for reflect runtime reapply/disable behavior and admin integration
- [x] 8.6: Update docs and session notes for the reflect slice
