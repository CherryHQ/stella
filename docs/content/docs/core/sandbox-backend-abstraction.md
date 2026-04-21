---
title: Sandbox Backend Abstraction
---

## Status

Implemented. Anna's local execution boundary is described by `pkg/sandbox` contracts, with runner-facing registry wiring in `internal/sandbox`.

## Purpose

The sandbox abstraction exists so runner code, plugin wiring, and tool execution do not depend on concrete backend types such as `boxsh`.

The top-level model is:

- `pkg/sandbox.Policy` — immutable backend-agnostic execution policy
- `pkg/sandbox.Session` — per-run execution boundary and lifecycle owner
- `pkg/sandbox.Host` — mediated filesystem / process / network surface used inside the runner boundary
- `pkg/plugins.ToolRuntime` — plugin-facing file and process capability surface derived from the active runner session

Backend identity stays inside the runner and sandbox packages. Plugin packages do not import `internal/sandbox`.

## Configuration

The active sandbox backend is a global admin setting configured on the **Plugins** page (`/plugins`). Only one backend can be active at a time. Enabling a backend disables the others.

Per-agent configuration is limited to network policy (mode and allowlist). Each agent independently controls whether its sandbox allows outbound network access and which hosts are reachable.

## Current Architecture

### Session ownership

The runner creates a `sandbox.Session` for each run and keeps ownership of its lifecycle.

- `auto` selects `boxsh` on Linux and macOS when configured and supported
- `auto` fails closed on unsupported platforms — `docker` must be explicitly configured
- explicit `boxsh` fails closed when the platform or policy cannot be supported
- `docker` is an opt-in Docker-backed backend; it is never auto-selected and honors policies strictly, with the docker daemon contacted at session-create time
- unsupported policy/backend combinations fail closed by default

### Backend resolution

The active backend is resolved at snapshot time: `config.ActiveSandboxBackend(plugins)` finds the enabled `sandbox/*` plugin and injects its name into the runner config. Per-agent sandbox config carries only the network policy; the backend field is ignored on agents and always sourced from the global plugin state.

### Execution-time mediation

All local execution paths that must obey sandbox policy are mediated through the active runner session:

- core tools (`bash`, `read`, `write`, `edit`) use the runner-owned `sandbox.Host`
- plugin tools receive `ToolContext.Runtime`, a `pkg/plugins.ToolRuntime` adapter over the active `sandbox.Host`
- skills and agent preset loading use `ToolRuntime` when running inside an agent session
- MCP stdio process spawning uses `ToolRuntime.StartProcess`

Build-time plugin registration remains sandbox-agnostic. Execution-time tool contexts receive runtime capabilities, not sandbox internals.

### Non-runner filesystem access

Some code paths need local filesystem access without an already-injected runtime, such as prompt rendering or metadata discovery outside an active agent run.

Prompt rendering falls back to direct `os.*` calls when it has no runner host. Skills and agent preset discovery use `pkg/plugins.NewLocalToolRuntime(...)` when called outside an active runner. These are intentional non-runner paths, not fallbacks for sandboxed tool execution.

### Explicit exception boundary

Remote MCP HTTP/SSE/StreamableHTTP transport is currently treated as a separate trust boundary.

- local stdio transport is runtime-mediated through the active runner session
- remote transport dialing is **not** currently mediated by `ToolRuntime`
- this exception is tracked explicitly as `EX-009` and logged as `runtime.exception_path`

## Backend Addition Rules

A new sandbox backend should be mostly add-only:

1. implement the `pkg/sandbox.Factory`, `pkg/sandbox.Session`, and `pkg/sandbox.Host` contracts
2. register the factory in `internal/sandbox`
3. pass contract and policy-compatibility tests
4. add a runner adapter when plugins need a new `ToolRuntime` capability
5. avoid leaking backend-specific types into runner, plugin host, or tool code

If a backend cannot honor a policy, it should fail closed with a policy compatibility error.

The docker backend in `plugins/sandbox/docker/` is the most recent example of an add-only backend — it demonstrates how to integrate via `pkg/sandbox.Factory` without touching existing boxsh paths.

## Compatibility Rules

### What remains stable above `internal/sandbox`

Code above the sandbox boundary should depend on:

- session lifecycle
- host-mediated operations
- generic plugin `ToolRuntime` behavior

It should not depend on:

- `boxshclient`
- backend-specific process/session types
- `internal/sandbox` from plugin packages
- implicit direct `os` / `exec` / `net/http` fallback paths for sandboxed execution surfaces

### Fail-closed behavior

Anna prefers explicit denial over silent downgrade:

- unsupported backends fail closed
- unsupported policies fail closed
- direct non-mediated plugin exec remains fail closed
- current `boxsh` builds may reject `whitelist` network mode; that fails closed instead of silently widening access
- docker backend rejects `whitelist` network mode (same posture as boxsh); fail closed rather than silently widening access
- remote MCP HTTP/SSE/StreamableHTTP remains an explicit exception, not an implicit sandbox bypass

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
