---
title: Sandbox Backend Abstraction
---

## Status

Implemented. Docker is the only sandbox backend. Anna's local execution boundary is described by `pkg/sandbox` contracts, with runner-facing registry wiring in `internal/sandbox`.

## Purpose

The sandbox abstraction exists so runner code, plugin wiring, and tool execution do not depend on concrete backend types. All sandboxed execution runs through a Docker container. Docker is required; Anna fails closed when the Docker daemon is unavailable at session-create time.

The top-level model is:

- `pkg/sandbox.Policy` — immutable backend-agnostic execution policy (filesystem root, working dir, network mode, env, timeout)
- `pkg/sandbox.Session` — per-run execution boundary and lifecycle owner; combines lifecycle and host-access into one interface
- Runner-owned file I/O — the runner uses `os.*` with `Session.ResolvePath` to read and write files; there is no `ReadFile`/`WriteFile` on `Session`

Backend identity stays inside the runner and sandbox packages. Plugin packages do not import `internal/sandbox`.

## Session Interface

`pkg/sandbox.Session` exposes 8 methods:

| Method                                                     | Description                                                       |
| ---------------------------------------------------------- | ----------------------------------------------------------------- |
| `Policy() Policy`                                          | Returns the immutable policy the session was created with         |
| `Exec(ctx, command, ExecOptions) (ExecResult, error)`      | Run a command and wait for its result                             |
| `StartProcess(ctx, ProcessRequest) (ProcessHandle, error)` | Spawn a long-lived process with stdio handles                     |
| `ResolvePath(path string) (string, error)`                 | Translate a sandbox-relative path to a host path for `os.*` calls |
| `WorkingDir() string`                                      | Returns the logical working directory inside the sandbox          |
| `Close() error`                                            | Tear down the session and release resources                       |
| `Alive() bool`                                             | Reports whether the session is still active                       |
| `Done() <-chan struct{}`                                   | Channel closed when the session terminates                        |

File I/O (`read`, `write`, `edit`) is runner-owned: the runner calls `ResolvePath` to obtain the host path and then uses `os.ReadFile` / `os.WriteFile` / `os.MkdirAll` directly. `Session` carries no file read/write methods.

## Docker Requirement

Docker is the only sandbox backend. The Docker daemon must be running and reachable. Anna contacts the Docker daemon at session-create time and fails closed if it is unavailable:

- missing or unreachable Docker daemon → session creation fails, runner does not start
- unsupported policy → `PolicyCompatibilityError`, runner does not start
- no silent downgrade path exists

All platforms (Linux, macOS, Windows) use the same Docker-backed session. There is no `auto`, `boxsh`, or `Relaxed` mode.

## Configuration

Per-agent sandbox configuration is limited to network policy (mode and allowlist). Each agent independently controls whether its sandbox allows outbound network access and which hosts are reachable.

Network modes supported by the Docker backend:

| Mode        | Description                          |
| ----------- | ------------------------------------ |
| `disabled`  | No outbound network access (default) |
| `allow_all` | Unrestricted outbound access         |

The `whitelist` mode is removed. Anna validates the configured mode at session-create time and fails closed if the backend cannot enforce it.

## Current Architecture

### Session ownership

The runner creates a `sandbox.Session` for each run and keeps ownership of its lifecycle. Runner construction fails closed when no active sandbox session is available.

### Backend resolution

The Docker backend self-registers in `internal/sandbox` via `init()`. `pkg/sandbox.Registry.CreateSession` iterates registered factories in registration order and uses the first available factory that supports the policy. With only one factory registered, this always selects Docker or fails.

### Execution-time mediation

All local execution paths that must obey sandbox policy are mediated through the active runner session:

- core tools (`bash`) use `Session.Exec` through the runner-owned session
- `read`, `write`, `edit` use `Session.ResolvePath` then `os.*` for file I/O
- plugin tools receive `ToolContext.Runtime`, a `pkg/plugins.ToolRuntime` adapter over the active session
- skills and agent preset loading use `ToolRuntime` when running inside an agent session
- MCP stdio process spawning uses `Session.StartProcess`

### stdio-MCP benefit

`Session.StartProcess` is fully supported by the Docker backend on every run. This means MCP stdio transports work uniformly across all platforms without requiring platform-specific subprocess handling.

### Non-runner filesystem access

Some code paths need local filesystem access without an already-injected runtime, such as prompt rendering or metadata discovery outside an active agent run.

Prompt rendering falls back to direct `os.*` calls when it has no runner session. Skills and agent preset discovery use `pkg/plugins.NewLocalToolRuntime(...)` when called outside an active runner. These are intentional non-runner paths, not fallbacks for sandboxed tool execution.

### Explicit exception boundary

Remote MCP HTTP/SSE/StreamableHTTP transport is currently treated as a separate trust boundary.

- local stdio transport is runtime-mediated through the active runner session via `Session.StartProcess`
- remote transport dialing is **not** currently mediated by `ToolRuntime`
- this exception is tracked explicitly as `EX-009` and logged as `runtime.exception_path`

## Fail-Closed Behavior

Anna prefers explicit denial over silent downgrade:

- Docker unavailable at session-create time → runner fails to start
- unsupported policies → `PolicyCompatibilityError`, runner fails to start
- direct non-mediated plugin exec → fail closed
- remote MCP HTTP/SSE/StreamableHTTP → explicit exception, not an implicit sandbox bypass

## Verification

The abstraction is covered by:

- session/host contract tests
- policy compatibility tests
- core tool parity tests
- Docker backend integration tests
- static bypass regression guards for migrated runtime paths

## Related Docs

- [Architecture](/docs/core/architecture)
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/design-spec.md`
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/exceptions-register.md`
