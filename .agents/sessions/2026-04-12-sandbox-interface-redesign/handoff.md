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

## Phase 3: Refactor runner and execution-time contexts onto the abstraction

**Status:** complete

**Tasks completed:**

- 3.1: Replaced the runner's backend resolution path with `runnerSession` creation through `resolveSession`, keeping only a deprecated legacy backend shim for compatibility tests
- 3.2: Removed build-time `BuildContext.Backend` leakage and updated runner/plugin build wiring to use sandbox-agnostic build context
- 3.3: Introduced execution-time host injection for boxsh-backed core tools via host-backed runner tools and a host-backed plugin sandbox runtime
- 3.4: Defined and tested runner/session lifecycle semantics for noop, local, and boxsh paths: liveness, done-channel behavior, backend death propagation, and cleanup on close
- 3.5: Added runner/core-tool/runtime integration coverage for host-backed execution and session lifecycle handling

**Files changed:**

- `internal/agent/runner/gorunner.go` — runner now owns `runnerSession` instead of the old backend seam and builds tools from session-aware context
- `internal/agent/runner/sandbox_backend.go` — new `runnerSession`, session factories, noop/local/boxsh resolution, lifecycle helpers, and deprecated compatibility shim
- `internal/agent/runner/coretools_builder.go` — host-backed core tool path for boxsh sessions without build-time backend leakage
- `internal/agent/runner/coretools_builder_test.go` — session/host core tool builder coverage
- `internal/agent/runner/sandbox_backend_test.go` — runner session lifecycle coverage
- `internal/agent/runner/gorunner_test.go` — runner integration coverage updated for session-based sandboxing
- `plugins/tools/registry.go` — `BuildContext.Backend` removed from plugin build-time context
- `plugins/tools/sandbox_runtime.go` — host-backed plugin sandbox runtime added; non-boxsh/noop paths remain fail-closed
- `plugins/tools/sandbox_runtime_test.go` — plugin sandbox runtime host-path coverage
- `internal/sandbox/factory.go` — global registry access used by the runner session factories
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/tasks.md` — Phase 3 tasks marked complete

### Fixes
- Preserved fail-closed unsupported-platform behavior by keeping `auto` on non-boxsh platforms mapped to `noop`, not relaxed/local execution.
- Kept plugin-facing sandbox runtime disabled for non-boxsh sessions so relaxed/local mode does not appear sandboxed to plugin tools.
- Reintroduced plugin sandbox runtime capability for boxsh through a host-backed runtime rather than re-exposing `boxshclient` at build time.
- Moved boxsh core tools onto a host-backed execution surface while preserving PATH prefixing for managed tools and current normalized bash output.
- Kept local/noop core tools on the delegate path until full Phase 4 parity work lands, avoiding a premature behavior regression.
- Fixed host-backed `read` / `edit` semantics by doing line-based pagination and uniqueness checks at the tool layer instead of relying on raw host offsets.
- Preserved runner semantics for disabled sandbox sessions: no subprocess-backed liveness dependency, safe nil handling, and close stability.
- Added a guarded `ANNA_HOME` handoff during boxsh session construction so session-based creation still resolves the requested managed boxsh binary in tests and custom runner setups.

**Validation:**

- `mise run format`
- `go test ./internal/agent/runner ./plugins/tools ./internal/sandbox`
- `go test ./...`

**Decisions & context for Phase 4:**

- Phase 3 is approved with `runnerSession` as the runner-owned lifecycle wrapper around `sandbox.Session`.
- Build-time plugin/tool context is now sandbox-agnostic; backend identity is no longer passed through `BuildContext`.
- Execution-time mediation now flows through `sandbox.Host` for the boxsh-backed core tool path and plugin sandbox runtime.
- Local/relaxed execution remains explicitly non-sandboxed at the plugin runtime layer and still uses delegate/native core tools until Phase 4 parity is complete.
- Phase 4 should finish the host-based parity work for `bash/read/write/edit` and then remove the duplicate adapter path entirely.

## Phase 4: Unify core tools and remove duplicate adapter path

**Status:** complete

**Tasks completed:**

- 4.1: Added a Phase 4 parity matrix for `bash/read/write/edit`
- 4.2: Unified `bash` on the host-backed implementation path
- 4.3: Unified `read` on the host-backed implementation path
- 4.4: Unified `write` on the host-backed implementation path
- 4.5: Unified `edit` on the host-backed implementation path
- 4.6: Consolidated normalization and output shaping into shared host-backed tool logic in `internal/sandbox/tools.go`
- 4.7: Removed `internal/sandbox/boxshclient/tool_adapters.go`
- 4.8: Removed duplicate adapter tests and replaced them with local/boxsh host-tool coverage

**Files changed:**

- `internal/sandbox/tools.go` — new shared host-backed core tool implementations and pagination/edit helpers
- `internal/sandbox/tools_test.go` — local parity coverage for write/read/edit/bash behavior
- `internal/sandbox/tools_boxsh_integration_test.go` — boxsh integration coverage for working-dir, shared-state, pagination, and edit preflight behavior
- `internal/agent/runner/coretools_builder.go` — runner now always builds sandbox core tools from shared host-backed implementations when a session host exists
- `internal/agent/runner/coretools_builder_test.go` — updated runner core-tool builder coverage for unified host-backed tools
- `internal/sandbox/local_session.go` — aligned local host write/exec behavior with unified core-tool expectations without changing the abstract host read contract
- `internal/sandbox/boxsh_session.go` — fixed boxsh read pagination offset calculation and delegated edits back to backend-native `client.Edit`
- `internal/sandbox/boxshclient/coretools_helpers_test.go` — backend-level helpers for non-tool adapter integration tests
- `internal/sandbox/boxshclient/network_policy_test.go` — migrated off deleted adapters onto backend/client assertions
- `internal/sandbox/boxshclient/isolation_test.go` — migrated off deleted adapters onto backend/client assertions
- `internal/sandbox/boxshclient/integration_cow_test.go` — migrated off deleted adapters onto backend/client assertions
- `internal/sandbox/boxshclient/tool_adapters.go` — removed
- `internal/sandbox/boxshclient/tool_adapters_test.go` — removed
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/core-tools-parity-matrix.md` — new Phase 4 parity matrix
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/tasks.md` — Phase 4 tasks marked complete

### Fixes
- Removed the duplicated boxsh adapter layer entirely and moved core-tool behavior into a single shared host-backed implementation.
- Preserved managed-tool PATH prefixing for `bash` while keeping the implementation backend-agnostic at the runner/build layer.
- Restored legacy `read` semantics: binary detection, line-based pagination hints, and correct continuation behavior for long first lines.
- Restored legacy `write` semantics by ensuring parent directories are created before writes through the shared tool path.
- Restored legacy `edit` semantics by enforcing missing/non-unique match failures in the shared tool path.
- Prevented the boxsh large-file data-loss regression by delegating `boxshHost.EditFile` back to `client.Edit(...)` instead of read-modify-write on truncated content.
- Fixed boxsh continuation offsets to advance by lines actually returned in the current page rather than total file line count.
- Added boxsh integration coverage for multi-page `read` continuation and `edit` preflight across paginated content.
- Replaced adapter-specific backend tests with equivalent backend/client/session assertions so adapter removal did not reduce isolation/network/COW coverage.

**Validation:**

- `mise run format`
- `go test ./internal/sandbox ./internal/sandbox/boxshclient ./internal/agent/runner`
- `go test ./...`

**Decisions & context for Phase 5:**

- Core tool execution is now unified on `sandbox.Host`; the old boxsh-only adapter path is gone.
- Boxsh-specific behavior still exists only inside `internal/sandbox`, not in runner/build-time seams.
- Noop sessions still use native/delegate tools because they intentionally have no sandbox session host.
- Phase 5 should now focus on non-core execution surfaces: plugin paths, MCP/local helper mediation, bypass detection, exceptions register updates, and observability.

## Phase 5: Mediate non-core execution paths and reduce bypasses

**Status:** complete

**Tasks completed:**

- [x] 5.1: Migrated non-core plugin/runtime-adjacent filesystem reads for skills, agent presets, and prompt context onto host-first mediation
- [x] 5.3: Added a static guard test for migrated files to prevent direct `os/exec/net/http` bypass regressions
- [x] 5.4: Updated the exceptions register to narrow EX-003 through EX-005 from active runtime bypasses to explicit nil-host fallbacks
- [x] 5.5: Added sandbox observability logs for session lifecycle, relaxed mode, unsupported backend selection, and key fail-closed denials
- [x] 5.2: Moved MCP stdio transport process spawning onto `sandbox.Host.StartProcess`; remote HTTP/SSE dialing remains an explicit exception

**Files changed:**

- `pkg/plugins/context.go` — added execution-time `sandbox.Host` injection for tool builders
- `plugins/tools/registry.go` — extended tool build context with the execution-time host
- `internal/pluginhost/builders.go` — now passes the host into plugin tool builders
- `internal/agent/runner/gorunner.go` — moved prompt construction after session resolution and passed the session host into tools/preset loading
- `internal/agent/runner/sandbox_backend.go` — exposed runner session host and mounted common skill/agent readonly paths for boxsh sessions
- `internal/agent/runner/prompt.go` — prompt context loading now prefers host-mediated file access
- `internal/agent/runner/prompt_host.go` — new host-aware prompt file helpers with explicit nil-host fallback
- `plugins/tools/skills/{plugin.go,tool.go,catalog.go,manage.go,remove_lib.go,install.go,hostfs.go}` — skills tool and catalog now use host-first mediation for read/list/write/remove paths
- `plugins/tools/agent/{preset_loader.go,hostfs.go}` — agent preset discovery now uses host-first mediation
- `cmd/anna/skills.go` — updated CLI callers to the new host-aware signatures
- `plugins/reflect/{conversation_review.go,expiry.go}` — updated in-process skill callers to the new host-aware signatures
- `internal/sandbox/observe.go` — new structured sandbox observability helpers
- `internal/sandbox/{factory.go,local_session.go,boxsh_session.go}` — hooked unsupported/relaxed/lifecycle/denial logs into runtime paths
- `internal/sandbox/bypass_guard_test.go` — new static regression guard for migrated files
- `plugins/tools/mcp/{session.go,manager.go,supervisor.go,plugin.go,session_test.go,tool_test.go}` — MCP stdio transport now uses host-mediated process spawning and runtime host injection
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/tasks.md` — Phase 5 task status updated
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/exceptions-register.md` — narrowed EX-003 through EX-005 and added EX-009 for remaining remote MCP transport dialing

### Fixes
- Stopped plugin tool builders from reading process cwd directly by threading the runner workdir and session host through the plugin build context.
- Moved AGENTS.md prompt-context loading to execution time so it can run under the same sandbox host as the rest of the tool surface.
- Preserved non-sandboxed callers by centralizing explicit nil-host fallbacks in small helper files instead of leaving direct filesystem reads spread across runtime code.
- Extended boxsh session readonly mounts to include common `.agents` skill/agent directories and builtin skill cache so host-mediated discovery still works under strict sessions.
- Added a coarse but reviewable bypass guard that fails tests if the migrated files regress to direct `os/exec/net/http` use.
- Added structured logs for the Phase 5 observability envelope without reintroducing backend leakage above `internal/sandbox`.

**Validation:**

- `mise run format`
- `mise run test`

**Decisions & context for next phase slice:**

- Skills, preset discovery, prompt context, and MCP stdio process spawning are now host-first; remaining direct access in those areas is explicitly limited to nil-host fallback paths or remote MCP transport dialing.
- Remote MCP HTTP/SSE transport remains the main explicit exception because the managed runtime is still process-wide rather than session-scoped.
- The new bypass guard only covers the files migrated in this slice; Phase 6 should extend that guard or replace it with a broader lint rule and decide whether remote MCP transport becomes mediated or remains a documented separate trust boundary.
