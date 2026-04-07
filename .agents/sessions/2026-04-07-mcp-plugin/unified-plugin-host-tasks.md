# Tasks: Unified Plugin Host for Anna

## Phase 1: Shared plugin platform contracts

- [ ] 1.1: Add `pkg/plugins` package with plugin, host, capability, config, runtime, and prompt-inventory interfaces/types
- [ ] 1.2: Define narrow build contexts for tool/provider/hook/channel/memory/runtime capabilities
- [ ] 1.3: Add unit tests for shared plugin registration/runtime helper types

## Phase 2: Internal host implementation

- [ ] 2.1: Add `internal/pluginhost` catalog loader and host implementation
- [ ] 2.2: Add capability indexing by plugin ID and capability identity
- [ ] 2.3: Add config service adapter backed by existing `config.Store`
- [ ] 2.4: Add runtime host with `Apply/Stop/Snapshot` orchestration
- [ ] 2.5: Add unit tests for host registration, duplicate detection, runtime apply sequencing, and runtime lookup

## Phase 3: Compatibility adapters

- [ ] 3.1: Adapt existing tool registry into host capability registrations
- [ ] 3.2: Adapt existing provider registry into host capability registrations
- [ ] 3.3: Adapt existing hook registry into host capability registrations
- [ ] 3.4: Adapt existing memory registry into host capability registrations
- [ ] 3.5: Add integration tests proving existing non-MCP plugins still load/build correctly

## Phase 4: App/admin wiring

- [ ] 4.1: Create plugin host during app setup and thread it through runtime startup
- [ ] 4.2: Replace MCP-specific startup lifecycle bootstrapping with generic runtime host application
- [ ] 4.3: Add generic plugin config validate/save path in admin
- [ ] 4.4: Add generic plugin status lookup path in admin
- [ ] 4.5: Add tests for generic config/status host-backed behavior

## Phase 5: MCP migration

- [ ] 5.1: Register MCP config capability
- [ ] 5.2: Register MCP runtime capability
- [ ] 5.3: Register MCP tool capability
- [ ] 5.4: Register MCP status capability
- [ ] 5.5: Register MCP prompt inventory capability
- [ ] 5.6: Add typed MCP runtime lookup helper inside plugin package
- [ ] 5.7: Replace MCP-specific admin/gateway wiring with host-backed behavior
- [ ] 5.8: Add MCP host-backed integration tests for config, runtime, tool exec, status, and prompt inventory

## Phase 6: Cleanup and stabilization

- [ ] 6.1: Delete `SetMCPLifecycle` and related MCP-only admin/runtime plumbing
- [ ] 6.2: Remove external `DefaultManager()` usage outside MCP plugin internals
- [ ] 6.3: Tighten comments/docs around MCP as the first advanced host-backed plugin
- [ ] 6.4: Update session notes with migration outcomes and remaining follow-up work

## Phase 7: Verification and docs

- [ ] 7.1: Run `mise run format`
- [ ] 7.2: Run `mise run lint`
- [ ] 7.3: Run `mise run test`
- [ ] 7.4: Update plugin-system docs if needed
- [ ] 7.5: Update builtin anna skill if needed
- [ ] 7.6: Record reflect/channel follow-up work without implementing it
