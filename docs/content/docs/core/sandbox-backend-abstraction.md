# Sandbox Backend Abstraction (RFC Draft)

## Status

- **State**: Draft
- **Audience**: Runtime, plugin, and tooling maintainers
- **Goal**: Make sandboxing backend-agnostic so Anna can replace `boxsh` with other sandbox providers without rewriting tool/plugin integration.

## Problem

Today, the runtime has a mixed model:

- A generic plugin-facing sandbox interface exists (`SandboxRuntime` in `pkg/plugins`).
- Core runner lifecycle and context wiring still carry `boxsh`-specific backend types and checks.
- Optional plugin tool enforcement currently infers sandbox activation from the `boxsh` backend pointer.

This coupling makes future backend replacement expensive and error-prone.

## Non-Goals

- Replacing `boxsh` in this phase.
- Changing Windows behavior to require a sandbox backend.
- Defining provider-specific policy syntax for every future sandbox engine.

## Goals

1. **Single abstraction boundary** for sandbox execution and capability checks.
2. **Backend pluggability** (`boxsh` first, others later) with no plugin API churn.
3. **Fail-closed semantics** on platforms where sandboxing is configured as required.
4. **Consistent plugin ergonomics**: plugin authors use one host-provided sandbox surface.

## Proposed Architecture

### 1) Canonical sandbox contract

Promote the plugin-facing `SandboxRuntime` surface as the cross-layer contract:

- `Enabled() bool`
- `Exec(ctx, command, timeoutSeconds) (SandboxExecResult, error)`

Extend this surface incrementally (e.g. fs helpers, metadata, network capability introspection) only as needed by real plugins.

### 2) Runner-internal backend interface

Introduce a runner-internal backend lifecycle contract (conceptual shape):

- `Start(ctx) error`
- `Close() error`
- `Alive() bool`
- `Runtime() SandboxRuntime`
- `Metadata() SandboxMetadata` (provider name, capability flags)

`boxsh` becomes one implementation of this interface.

### 3) Backend factory and selection

Create a backend factory that resolves the configured backend:

- `boxsh` (Linux/macOS default for now)
- `none` (explicit disabled/no-sandbox mode)
- future providers (containerized, VM-backed, remote executor, etc.)

Platform checks happen in one place in the factory.

### 4) Remove boxsh-specific checks from tool registration

Replace checks like:

- `runtime.GOOS != "windows" && bc.Backend != nil`

with generic checks:

- `bc.Sandbox != nil && bc.Sandbox.Enabled()`

This keeps behavior stable while removing backend coupling.

### 5) Core tool integration

Refactor `CoreToolsBuilderWithBoxsh` to a backend-neutral wrapper:

- `CoreToolsBuilderWithSandbox`

Adapter logic should consume `SandboxRuntime`/backend capabilities instead of concrete `boxsh` types where possible.

### 6) Windows behavior

Windows continues to run without `boxsh`.

- Host supplies a disabled sandbox runtime (non-nil object).
- Plugins can safely call `Enabled()` and get deterministic `false`.
- Sandbox-requiring operations return explicit unavailability errors.

## Migration Plan

### Phase 1 ✅

- `SandboxRuntime` is threaded through tool contexts.
- Disabled runtime is provided when sandbox backend is unavailable.

### Phase 2 ✅

- Backend factory and runner-internal backend interface are implemented.
- Runner startup/shutdown uses backend abstraction.

### Phase 3 ✅

- `BuildContext.Backend` dependence was removed from plugin host checks.
- Plugin tool build path uses generic sandbox runtime context.

### Phase 4 ✅

- Core tool builder is backend-neutral (`CoreToolsBuilderWithSandbox`).
- `boxsh` adapters are isolated behind backend abstraction.

### Phase 5 ✅ (initial)

- Two backends (`boxsh`, `noop`) are selectable.
- Contract tests for backend/runtime behavior are in place; parity can be extended as new backends are added.

## Testing Strategy

1. **Contract tests** for sandbox runtime behavior (enabled/disabled/exec error model).
2. **Backend implementation tests** (`boxsh` adapter and future adapters).
3. **Integration tests** for tool registration/execution under:
   - sandbox enabled backend,
   - sandbox disabled backend,
   - Windows/no-boxsh path.
4. **Fail-closed tests** when backend is required but unavailable.

## Risks and Mitigations

- **Risk**: API churn for plugin authors.
  - **Mitigation**: Keep `SandboxRuntime` backward compatible; add methods conservatively.
- **Risk**: Divergent behavior across backends.
  - **Mitigation**: shared contract tests + capability metadata.
- **Risk**: subtle security regressions during migration.
  - **Mitigation**: fail-closed defaults and explicit capability checks.

## Open Questions

1. Should filesystem helper methods live on `SandboxRuntime` or a separate `SandboxFS` interface?
2. Do we need network policy introspection APIs in plugin context?
3. Should backend selection be per-agent, global, or both?
4. What is the minimum capability set a backend must provide to be considered "sandbox-enabled"?
