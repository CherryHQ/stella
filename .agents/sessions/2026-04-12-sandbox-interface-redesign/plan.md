# Plan: Long-term sandbox interface redesign

## Overview
Redesign sandboxing so it becomes the single execution boundary for all tool execution, not an optional helper for a subset of tools.

The end state is:

- built-in tools
- plugin tools
- MCP-related execution paths
- future tool integrations

all run under a sandbox-managed session boundary whose policy covers:

- filesystem limits
- network limits
- process / OS limits
- future resource limits

Backend identity must not leak into tool code.

### Goals

- Define a durable top-level sandbox interface for all tool execution.
- Remove boxsh-specific leakage from runner and tool construction layers.
- Make future sandbox backends mostly add-only: implement interface + register factory.
- Preserve compatibility for built-in tools, plugin tools, and MCP-related paths during migration.
- Make bypasses, relaxed mode, and unsupported-policy behavior explicit and observable.

### Success Criteria

- A reviewed design spec defines interface, naming, package layout, ownership boundaries, lifecycle semantics, and policy model.
- Sandbox becomes the framework-owned execution boundary for all tool execution.
- A first-class immutable `sandbox.Policy` defines limits independently of any backend.
- Non-backend code no longer depends on `boxshclient`.
- Core tools converge to one implementation path; boxsh-specific tool adapters are removed.
- Built-in tools, plugin tools, and MCP-related paths have explicit mediation or explicit exceptions.
- Unsupported backend/policy combinations fail closed unless explicitly relaxed.
- Relaxed mode, denied operations, unsupported backends, and exception paths are observable.

### Out of Scope

- Implementing the refactor in this planning pass.
- Full remote trust modeling beyond clarifying the local sandbox boundary.
- Designing every future resource limit in detail beyond defining where policy belongs.

## Technical Approach

Use `internal/sandbox` as the top-level abstraction package.

### Top-level concepts

- **`sandbox.Policy`**  
  Immutable backend-agnostic session policy describing requested filesystem, network, process, and related limits.

- **`sandbox.Session`**  
  Per-agent / per-run sandbox boundary and lifecycle owner.

- **`sandbox.Host`**  
  The constrained host surface exposed to tool execution inside a session.

### Core contract rules

- Any **local** operation that must obey sandbox policy must go through `sandbox.Host`.
- All local execution paths are in scope by default; anything not mediated by `sandbox.Host` must be recorded in an explicit exceptions register.
- Tool registration/build-time code remains sandbox-agnostic.
- Sandbox handles are injected only into execution-time contexts.
- Unsupported policy requests fail closed by default.
- A local/unsandboxed session may only be created through explicit relaxed policy/config opt-in, never via implicit fallback.
- Session semantics are shared across backends:
  - defined concurrency behavior
  - defined cross-call state visibility
  - defined cancellation behavior
  - `Close()` guarantees cleanup
- Any known direct `os` / `exec` / `net` / `http` bypasses must be inventoried, mediated, or explicitly accepted as exceptions.

### Frozen planning assumptions

These are locked for planning so implementation can proceed without open design churn:

- Execution-time tool contexts receive **`sandbox.Host`** by default; `sandbox.Session` remains runner/infrastructure-owned unless lifecycle access is explicitly required.
- Remote MCP servers remain separate trust boundaries; sandbox guarantees cover the **local** side:
  - process spawning
  - local transport
  - local file access
  - outbound connections initiated locally
- Transport scope is a **Phase 1 validation item**:
  the inventory must enumerate required transport classes for plugins/MCP in the first cut, not assume plain HTTP is sufficient.
- Migration must include a compatibility/deprecation plan for existing build/execution APIs.
- Migration must include observability for:
  - policy denied
  - relaxed mode selected
  - backend unsupported
  - exception path used

### Components

- **`internal/sandbox/policy.go`**  
  Immutable backend-agnostic policy/session spec types

- **`internal/sandbox/session.go`**  
  `Session`, `Host`, request/result types, lifecycle contract comments

- **`internal/sandbox/factory.go`**  
  Backend resolution/creation, platform support, policy compatibility checks

- **`internal/sandbox/local_*.go`**  
  Local implementation for explicit relaxed mode

- **`internal/sandbox/boxsh_*.go`**  
  Boxsh-backed implementation hiding `boxshclient` behind `Policy` / `Session` / `Host`

- **Bypass inventory + exceptions register**  
  Direct `os` / `exec` / `net` / `http` usage outside host mediation

- **Runner integration**
- **Execution context integration**
- **Core tool unification**
- **Plugin/MCP mediation**
- **Observability hooks**

## Implementation Phases

### Phase 1: Finalize architecture, policy model, and enforcement scope
1. Write the design spec for `sandbox.Policy`, `sandbox.Session`, and `sandbox.Host`.
2. Inventory current filesystem/process/network access paths for built-in tools, plugin tools, and MCP-related execution.
3. Classify each path as:
   - mediated
   - to-be-mediated
   - explicit exception
4. Define unsupported backend/policy behavior and explicit relaxed-mode creation.
5. Define transport classes required in the first cut for plugin/MCP execution.
6. Define compatibility/deprecation plan for existing execution APIs.
7. Define observability requirements and emitted events/logs.
8. Produce:
   - policy compatibility matrix
   - exceptions register
   - rollout/deprecation notes

### Phase 2: Introduce top-level sandbox types in code
1. Add `internal/sandbox/policy.go`
2. Add `internal/sandbox/session.go`
3. Add `internal/sandbox/factory.go`
4. Add local and boxsh implementation adapters without exposing `boxshclient` upward

### Phase 3: Refactor runner and execution-time contexts onto the abstraction
1. Replace boxsh-leaking runner seams with session-based factory usage
2. Remove build-time boxsh leakage such as `BuildContext.Backend`
3. Introduce execution-time host injection
4. Specify and test:
   - concurrency
   - shared state visibility
   - cancellation
   - liveness loss
   - cleanup on close

### Phase 4: Unify core tools and remove duplicate adapter path
1. Create parity matrix for `bash/read/write/edit`, covering:
   - cwd handling
   - path normalization
   - text/binary behavior
   - truncation/temp-file rules
   - exit-code mapping
   - error shaping
2. Refactor core tools to one host-based implementation path
3. Remove `internal/sandbox/boxshclient/tool_adapters.go` only after parity passes
4. Consolidate normalization/output shaping into shared tool logic

### Phase 5: Mediate non-core execution paths and reduce bypasses
1. Migrate plugin/MCP/helper paths onto host-mediated file/network/process services
2. Add static checks/tests/lint rules for forbidden direct host bypasses
3. Update exceptions register with owner, reason, and closure plan
4. Add observability coverage for relaxed mode, exceptions, and denials

### Phase 6: Cleanup and verification
1. Remove obsolete boxsh-specific abstraction leaks and speculative leftovers
2. Simplify tests around:
   - abstract contract
   - backend implementations
   - parity matrix
   - bypass checks
   - runner integration
3. Update docs for:
   - sandbox architecture
   - backend addition rules
   - compatibility behavior
   - fallback/relaxed mode behavior
   - plugin/tool integration expectations

## Testing Strategy

- Contract tests for `sandbox.Session` / `sandbox.Host`
- Policy compatibility tests proving fail-closed behavior
- Runner integration tests proving runner does not depend on concrete boxsh types
- Core-tool parity tests across relaxed/local and boxsh sessions
- Backend-specific boxsh integration tests:
  - filesystem isolation
  - COW/session behavior
  - network mode behavior
  - lifecycle/aliveness
- Bypass-detection tests or lint checks for forbidden direct filesystem/process/network usage
- Plugin/MCP-focused host-mediation tests
- Observability tests for denied/relaxed/unsupported/exception cases

## Risks

| Risk | Impact | Mitigation |
| ---- | ------ | ---------- |
| Interface remains too boxsh-shaped | High | Keep interface limited to policy + session lifecycle + host-mediated operations |
| “All tool execution” is unrealistic without explicit bypass accounting | High | Inventory paths first; maintain explicit exceptions register |
| Session semantics drift across backends | High | Define contract early and enforce with backend contract tests |
| MCP/plugin transport needs exceed first-cut host surface | High | Make transport inventory a Phase 1 validation gate |
| Migration breaks plugin/internal API consumers | Medium | Add explicit compatibility/deprecation plan before deleting old seams |
| Relaxed mode erodes guarantees silently | High | Fail closed by default and add observability for relaxed/exception paths |
| Migration leaves dual paths indefinitely | High | Make adapter removal and parity completion explicit success gates |

## Open Questions

None. Remaining unknowns were converted into explicit assumptions or Phase 1 validation gates.

## Review Feedback

Integrated reviewer feedback: explicit `Policy` type, bypass inventory, execution-time-only injection, fail-closed semantics, parity matrix before adapter removal, transport-scope validation, compatibility/deprecation planning, and observability requirements.

## Final Status

Planning draft only.
