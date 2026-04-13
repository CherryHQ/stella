---
title: Sandbox Backend Abstraction
---

## Status

Implemented. Anna's local execution boundary is owned by `internal/sandbox`.

## Purpose

The sandbox abstraction exists so runner code, plugin wiring, and tool execution do not depend on concrete backend types such as `boxsh`.

The top-level model is:

- `sandbox.Policy` — immutable backend-agnostic execution policy
- `sandbox.Session` — per-run execution boundary and lifecycle owner
- `sandbox.Host` — mediated filesystem / process / network surface exposed to execution paths

Backend identity stays inside `internal/sandbox`.

## Current Architecture

### Session ownership

The runner creates a `sandbox.Session` for each run and keeps ownership of its lifecycle.

- `boxsh` is used on Linux and macOS when configured and supported
- `local` is used only for explicit relaxed execution paths
- unsupported policy/backend combinations fail closed by default

### Execution-time mediation

All local execution paths that must obey sandbox policy are expected to use `sandbox.Host`:

- core tools: `bash`, `read`, `write`, `edit`
- plugin/runtime-adjacent filesystem access for skills, agent presets, and prompt context
- MCP stdio process spawning through `Host.StartProcess`

Build-time plugin registration remains sandbox-agnostic. Execution-time contexts receive the host.

### Relaxed local sessions

Some non-runner code paths still need local filesystem access without an already-injected host, such as prompt rendering or metadata discovery outside an active agent run.

Those paths must not bypass the abstraction directly. They create an explicit relaxed local sandbox session and use its `Host` instead.

### Explicit exception boundary

Remote MCP HTTP/SSE/StreamableHTTP transport is currently treated as a separate trust boundary.

- local stdio transport is sandbox-mediated
- remote transport dialing is **not** currently mediated by `sandbox.Host`
- this exception is tracked explicitly as `EX-009` and logged as `sandbox.exception_path`

## Backend Addition Rules

A new sandbox backend should be mostly add-only:

1. implement the `sandbox.Factory`, `sandbox.Session`, and `sandbox.Host` contracts
2. register the factory in `internal/sandbox`
3. pass contract and policy-compatibility tests
4. avoid leaking backend-specific types into runner, plugin host, or tool code

If a backend cannot honor a policy, it should fail closed with a policy compatibility error.

## Compatibility Rules

### What remains stable above `internal/sandbox`

Code above the sandbox boundary should depend on:

- session lifecycle
- host-mediated operations
- generic plugin sandbox runtime behavior

It should not depend on:

- `boxshclient`
- backend-specific process/session types
- implicit direct `os` / `exec` / `net/http` fallback paths for sandboxed execution surfaces

### Fail-closed behavior

Anna prefers explicit denial over silent downgrade:

- unsupported backends fail closed
- unsupported policies fail closed
- direct non-mediated plugin exec remains fail closed
- boxsh transport operations not yet supported by the host surface remain fail closed

## Verification

The abstraction is covered by:

- session/host contract tests
- policy compatibility tests
- core tool parity tests
- backend-specific boxsh integration tests
- static bypass regression guards for migrated runtime paths

## Related Docs

- [Architecture](/docs/core/architecture)
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/design-spec.md`
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/exceptions-register.md`
