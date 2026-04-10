# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: Shared plugin platform contracts

**Status:** not started

**Tasks planned:**

- 1.1: Add `pkg/plugins` package with plugin, host, capability, config, runtime, and prompt-inventory interfaces/types
- 1.2: Define narrow build contexts for tool/provider/hook/channel/memory/runtime capabilities
- 1.3: Add unit tests for shared plugin registration/runtime helper types

**Planned files:**

- `pkg/plugins/*.go`
- tests under `pkg/plugins/*_test.go`

**Decisions & context for next phase:**

- v2 design keeps plugin identity and capability identity separate
- runtime contract should use declarative `Apply()` semantics, not `Start/Reconcile/Stop`
- prompt contribution should stay inventory-only in the first slice
