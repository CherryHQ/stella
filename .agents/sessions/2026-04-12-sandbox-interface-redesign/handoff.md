# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: Finalize architecture, policy model, and enforcement scope

**Status:** complete ✅

**Tasks completed:**

- [x] 1.1: Wrote comprehensive design spec for `sandbox.Policy`, `sandbox.Session`, and `sandbox.Host`
- [x] 1.2: Inventoried built-in tool (bash/read/write/edit) filesystem/process/network access paths
- [x] 1.3: Inventoried plugin tool (webfetch, skills, agent, notify, mcp) access paths
- [x] 1.4: Inventoried MCP-related local execution and transport paths (stdio, SSE, HTTP)
- [x] 1.5: Classified all paths as mediated (4), to-be-mediated (18+), or explicit exception (8+)
- [x] 1.6: Defined unsupported backend/policy behavior with fail-closed semantics
- [x] 1.7: Defined relaxed-mode creation rules (explicit opt-in only, never implicit fallback)
- [x] 1.8: Defined required transport classes for plugin/MCP: stdio, SSE, StreamableHTTP, HTTP
- [x] 1.9: Defined compatibility/deprecation plan for current execution APIs
- [x] 1.10: Defined observability requirements with events, logs, metrics, and alerts
- [x] 1.11: Produced policy compatibility matrix (boxsh vs local backend)
- [x] 1.12: Produced exceptions register with 8 documented exceptions (EX-001 through EX-008)
- [x] 1.13: Produced rollout/deprecation notes with 6-phase migration plan

**Files changed:**

- `.agents/sessions/2026-04-12-sandbox-interface-redesign/plan.md` — approved implementation/design plan (no changes)
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/tasks.md` — updated Phase 1 tasks to completed
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/handoff.md` — appended Phase 1 completion section
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/design-spec.md` — **NEW**: Detailed design specification
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/execution-paths-inventory.md` — **NEW**: Complete inventory of all execution paths with classification
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/exceptions-register.md` — **NEW**: 8 documented exceptions with owners and closure plans
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/policy-compatibility-matrix.md` — **NEW**: Backend/policy compatibility matrix
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/rollout-deprecation-notes.md` — **NEW**: Rollout phases and deprecation timeline
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/observability-requirements.md` — **NEW**: Observability events, metrics, and alerts

**Commits:**

- `📋 docs: Phase 1 sandbox interface redesign design artifacts`
  - Add design-spec.md with Policy, Session, Host interfaces
  - Add execution-paths-inventory.md classifying all tool paths
  - Add exceptions-register.md with 8 documented exceptions
  - Add policy-compatibility-matrix.md (boxsh vs local)
  - Add rollout-deprecation-notes.md with 6-phase plan
  - Add observability-requirements.md with events/metrics
  - Update tasks.md marking Phase 1 complete

**Decisions & context for Phase 2:**

- `internal/sandbox` is the owning abstraction package
- Top-level concepts are `sandbox.Policy`, `sandbox.Session`, and `sandbox.Host`
- Sandbox is the execution boundary for all local tool execution paths by default
- Build-time contexts remain sandbox-agnostic; execution-time contexts receive `sandbox.Host`
- Unsupported backend/policy combinations fail closed unless explicit relaxed mode is chosen
- Remote MCP remains a separate trust boundary; local side remains under sandbox control
- **Transport validation complete**: Required classes are stdio, SSE, StreamableHTTP, HTTP (WebSocket deferred)
- **Exceptions register established**: 8 exceptions documented with owners and closure plans (EX-001 through EX-008)
- **Rollout plan approved**: 6 phases from types introduction to cleanup
- **Core finding**: Dual implementation path exists (boxsh adapters + native tools) - Phase 4 will unify

### Fixes
- Reconciled the canonical `Policy` contract across the design artifacts: added `Backend` and `Relaxed` to the spec and standardized `Factory.Supported(policy) error`.
- Added the missing first-cut `Host` request/result type appendix and clarified exec/HTTP semantics for Phase 2.
- Expanded the host transport contract to cover argv-based process spawning and streaming HTTP so MCP stdio/SSE/StreamableHTTP are represented explicitly.
- Removed the duplicate observer interface from the design spec and made `observability-requirements.md` the canonical observer contract.
- Aligned execution-path inventory classifications with the exceptions register, including reclassifying `notify` as to-be-mediated, adding the missing `skills/remove_lib.go` path, and updating summary counts.
- Corrected the canonical relaxed-mode example so explicit opt-in is shown in the `Policy` itself.
- Updated rollout notes to use `Host.StartProcess` for MCP stdio and normalized debug event names to the underscore convention.
- Expanded the `Host` filesystem contract with mkdir/remove/rename/temp primitives so skills migration scope matches the documented host surface.
- Reconciled relaxed whitelist behavior for the boxsh backend across the compatibility matrix and fail-closed examples in both the spec and matrix.
- Clarified that filesystem and HTTP metrics are emitted directly by `Host` implementations rather than requiring separate observer callbacks.

**Phase 2 Ready:**
- [x] Design artifacts reviewed and complete
- [x] All paths inventoried and classified
- [x] Exceptions documented
- [x] Rollout plan established
- [ ] Ready to implement types in `internal/sandbox/`


## Phase 2: Introduce top-level sandbox types in code

**Status:** complete

**Tasks completed:**

- 2.1: Added `internal/sandbox/policy.go` with immutable policy types, validation helpers, and compatibility errors
- 2.2: Added `internal/sandbox/session.go` with `Session`, `Host`, and first-cut request/result contracts
- 2.3: Added `internal/sandbox/factory.go` with backend registry, fail-closed selection, and relaxed session helper
- 2.4: Added explicit relaxed/local implementation in `internal/sandbox/local_session.go`
- 2.5: Added boxsh-backed implementation in `internal/sandbox/boxsh_session.go` behind abstract interfaces
- 2.6: Added contract tests for shared session/host behavior in `internal/sandbox/contract_test.go`
- 2.7: Added policy compatibility tests for fail-closed behavior in `internal/sandbox/policy_compat_test.go`

**Files changed:**

- `internal/sandbox/policy.go` — new policy model and compatibility error types
- `internal/sandbox/session.go` — new `Session` / `Host` interfaces and request/result contracts
- `internal/sandbox/factory.go` — new factory registry and backend selection logic
- `internal/sandbox/local_session.go` — explicit relaxed/local backend implementation
- `internal/sandbox/boxsh_session.go` — boxsh-backed session/host adapter
- `internal/sandbox/contract_test.go` — shared session/host contract tests
- `internal/sandbox/policy_compat_test.go` — fail-closed and compatibility tests
- `internal/sandbox/boxshclient/backend.go` — helper for managed boxsh path resolution
- `internal/sandbox/boxshclient/session.go` — exported session/path helpers reused by Phase 2 abstractions
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/tasks.md` — Phase 2 tasks marked complete

**Commits:**

- `6900c40` — `✨ feat: add phase 2 sandbox session abstractions`

### Fixes
- Tightened `localFactory` so explicit relaxed mode is required for all local sessions, matching the Phase 1 fail-closed contract.
- Changed boxsh `Host.StartProcess`, `Host.HTTPRequest`, and `Host.OpenHTTPStream` to fail closed until real transport mediation exists, instead of silently bypassing the sandbox.
- Made `boxshHost.ResolvePath` remap absolute paths into the boxsh session root so filesystem operations target the overlay view rather than the host workspace.
- Resolved boxsh host path-model drift by routing `ReadFile`, `WriteFile`, `CreateTemp`, and default `Exec` cwd through the same resolve/remap path logic.
- Added backend-liveness coverage so `Done()` closes when the boxsh backend becomes unavailable, not only on explicit `Close()`.
- Aligned boxsh host path semantics with the existing adapter model by accepting sandbox-absolute paths and read-only mounted paths through shared remap logic.
- Preserved the runner's sandbox-root vs cwd split by introducing `Filesystem.WorkspaceRoot` with backwards-compatible defaulting, so Phase 3 can mount one root while keeping a distinct logical working directory.
- Enforced Phase 2 boxsh cwd and read-only semantics by resolving relative paths from `WorkingDir` and fail-closing mutating operations against `ReadOnlyPaths`.
- Closed the remaining readonly-subtree bypass by enforcing `ensureWritable` for `WriteFile` / `EditFile` and adding a regression test for readonly subdirectories nested under `WorkspaceRoot`.
- Tightened Phase 2 fail-closed behavior further: mutating boxsh host operations now fail closed whenever `ReadOnlyPaths` overlap `WorkspaceRoot`, preventing symlink/alias escapes before Phase 4+ can add stronger mediation.
- Closed the remaining plugin-facing bypass by making `plugins/tools/sandbox_runtime.go` fail closed for direct exec in Phase 2 instead of bypassing the new session/host policy checks.

**Decisions & context for next phase:**

- Phase 2 compiles and passes `go test ./...`, `go test ./internal/sandbox ./internal/sandbox/boxshclient`, and `mise run format`
- `local` is implemented as explicit relaxed mode; strict unsupported policies fail closed via `PolicyCompatibilityError`
- `boxsh` is hidden behind `sandbox.Session` / `sandbox.Host`, but runner/build contexts still leak older boxsh-specific seams and must be refactored in Phase 3
- `Host` now includes the filesystem/process/network primitives required by the Phase 1 contract, including `StartProcess` and streaming HTTP
- The contract tests currently exercise `local` directly and only run `boxsh` when a managed binary is available in the environment
