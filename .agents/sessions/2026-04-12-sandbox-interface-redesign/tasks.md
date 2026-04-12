# Tasks: Long-term sandbox interface redesign

## Phase 1: Finalize architecture, policy model, and enforcement scope
- [x] Write the design spec for `sandbox.Policy`, `sandbox.Session`, and `sandbox.Host`
- [x] Inventory built-in tool filesystem/process/network access paths
- [x] Inventory plugin tool filesystem/process/network access paths
- [x] Inventory MCP-related local execution and transport paths
- [x] Classify each path as mediated / to-be-mediated / explicit exception
- [x] Define unsupported backend/policy behavior and relaxed-mode creation rules
- [x] Define required transport classes for first-cut plugin/MCP support
- [x] Define compatibility/deprecation plan for current execution APIs
- [x] Define observability requirements and emitted events/logs
- [x] Produce policy compatibility matrix
- [x] Produce exceptions register
- [x] Produce rollout/deprecation notes

## Phase 2: Introduce top-level sandbox types in code
- [x] Add `internal/sandbox/policy.go`
- [x] Add `internal/sandbox/session.go`
- [x] Add `internal/sandbox/factory.go`
- [x] Add explicit relaxed/local implementation
- [x] Add boxsh-backed implementation behind abstract interfaces
- [x] Add contract tests for shared session/host behavior
- [x] Add policy compatibility tests for fail-closed behavior

## Phase 3: Refactor runner and execution-time contexts onto the abstraction
- [x] Replace runner boxsh-leaking seams with session-based factory usage
- [x] Remove build-time `BuildContext.Backend` leakage
- [x] Introduce execution-time host injection
- [x] Define concurrency semantics
- [x] Define shared state visibility semantics
- [x] Define cancellation semantics
- [x] Define liveness-loss handling
- [x] Define cleanup-on-close guarantees
- [x] Add runner and lifecycle integration tests

## Phase 4: Unify core tools and remove duplicate adapter path
- [ ] Create parity matrix for `bash/read/write/edit`
- [ ] Refactor `bash` to host-based implementation
- [ ] Refactor `read` to host-based implementation
- [ ] Refactor `write` to host-based implementation
- [ ] Refactor `edit` to host-based implementation
- [ ] Consolidate normalization/output shaping into shared tool logic
- [ ] Remove `internal/sandbox/boxshclient/tool_adapters.go`
- [ ] Remove duplicate adapter tests after parity passes

## Phase 5: Mediate non-core execution paths and reduce bypasses
- [ ] Migrate plugin paths onto host-mediated file/network/process services
- [ ] Migrate MCP/local helper paths onto host-mediated surfaces where required
- [ ] Add static checks / tests / lint rules for forbidden direct bypasses
- [ ] Update exceptions register with owner, reason, and closure plan
- [ ] Add observability for relaxed mode, denials, unsupported backend, and exceptions

## Phase 6: Cleanup and verification
- [ ] Remove obsolete boxsh-specific abstraction leaks
- [ ] Remove speculative leftover layers from migration
- [ ] Simplify tests around abstract contract and backends
- [ ] Update sandbox architecture docs
- [ ] Update backend addition rules docs
- [ ] Update compatibility/fallback/relaxed mode docs
- [ ] Update plugin/tool integration docs
