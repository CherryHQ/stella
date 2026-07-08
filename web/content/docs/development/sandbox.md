---
title: Sandbox Backend Abstraction
---

> This section is for developers contributing to Stella. For choosing and configuring a sandbox backend, see the [Sandbox guide](/docs/guides/sandbox).

## Core Model

The sandbox abstraction exists so runner code, plugin wiring, and tool execution do not depend on concrete backend types. Execution always runs through the active backend selected by the runner.

- `pkg/sandbox.Policy` — immutable backend-agnostic execution policy (filesystem root, working dir, network mode, env, timeout)
- `pkg/sandbox.Session` — per-run execution boundary and lifecycle owner; combines lifecycle and host-access into one interface
- Runner-owned file I/O — the runner uses `os.*` with `Session.ResolvePath` to read and write files; there is no `ReadFile`/`WriteFile` on `Session`

Backend identity stays inside the runner and runner-facing sandbox packages. Plugin packages do not import `internal/agent/sandbox`.

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

## Current Architecture

### Session ownership

The runner creates a `sandbox.Session` for each run and keeps ownership of its lifecycle. Runner construction fails closed when no active sandbox session is available.

### Backend resolution

The runner resolves the active backend from plugin state and dispatches to a backend-specific factory. Built-in factories currently support `docker`, `local`, and `none`.

### Execution-time mediation

All local execution paths that must obey sandbox policy are mediated through the active runner session:

- core tools (`bash`) use `Session.Exec` through the runner-owned session
- `read`, `write`, `edit` use `Session.ResolvePath` then `os.*` for file I/O
- plugin tools receive `ToolContext.Runtime`, a `pkg/plugins.ToolRuntime` adapter over the active session
- skills and agent preset loading use `ToolRuntime` when running inside an agent session
- MCP stdio process spawning uses `Session.StartProcess`

### stdio-MCP benefit

`Session.StartProcess` is supported by both built-in backends. Docker gives stdio MCP servers a dedicated container process namespace; the local backend starts them directly on the host OS.

### Non-runner filesystem access

Some code paths need local filesystem access without an already-injected runtime, such as prompt rendering or metadata discovery outside an active agent run.

Prompt rendering falls back to direct `os.*` calls when it has no runner session. Skills and agent preset discovery use `pkg/plugins.NewLocalToolRuntime(...)` when called outside an active runner. These are intentional non-runner paths, not fallbacks for sandboxed tool execution.

### Explicit exception boundary

Remote MCP HTTP/SSE/StreamableHTTP transport is currently treated as a separate trust boundary.

- local stdio transport is runtime-mediated through the active runner session via `Session.StartProcess`
- remote transport dialing is **not** currently mediated by `ToolRuntime`
- this exception is tracked explicitly as `EX-009` and logged as `runtime.exception_path`

## Fail-Closed Behavior

Stella prefers explicit denial over silent downgrade:

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

## Running the Docker Backend Locally

`mise run dev:docker` brings up the whole stack with one command, mirroring the production `docker-compose.yml`: `stellad` runs **inside a container** with the `docker` sandbox backend in **volume mode** (`STELLA_SANDBOX_BACKEND=docker`, `STELLA_DOCKER_SANDBOX_MODE=volume`, `STELLA_HOME_VOLUME=stella-data`), plus an `otel-lgtm` sidecar. It builds the local images (`docker:build` → `stella:latest`, `sandbox:docker:build` → `stella-sandbox:dev`), creates the named volumes if missing, and ensures `~/.stella-dev/.env` contains a dev vault key. It runs the same `docker-compose.yml` as prod, just exporting `STELLA_IMAGE=stella:latest` so it uses the local build instead of the released image.

The in-container Go server serves its baked-in embedded SPA at `localhost:25688` (see `web/embed.go`), and Grafana is available at `localhost:13413`.

Stop everything with `docker compose down`.

The sandbox image bakes its mise toolchain at `/opt/stella` via `stellad mise reconcile-builtins` (the same `resources/tools.yaml` reconcile the host runs), so docker and the Linux `local` backend present identical mise paths.

## Adding a New Backend

Every new sandbox backend requires changes in all of the following locations — missing any one causes a runtime error:

| Step | File                                                                                     | What to do                                                                                                       |
| ---- | ---------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| 1    | `internal/config/sandbox.go`                                                             | Add `SandboxBackend<Name> = "<name>"` constant                                                                   |
| 2    | `internal/config/plugin.go`                                                              | Append name to `builtinSandboxNames` so the DB row is seeded                                                     |
| 3    | `plugins/sandbox/<name>/session.go`                                                      | Implement `sandbox.Factory` and `sandbox.Session`                                                                |
| 4    | `plugins/sandbox/plugin.go`                                                              | Add entry to the `backends` slice in `init()` to register `AdminVisible` plugin metadata                         |
| 5    | `internal/agent/sandbox/session.go`                                                      | Add a `case config.SandboxBackend<Name>:` branch in `createSessionForBackend` and implement the factory function |
| 6    | `web/src/features/plugins/PluginsPage.tsx` and `web/src/features/plugins/pluginUtils.ts` | Add `"sandbox/<name>"` to `validSandboxBackends` and a `sandboxMeta` entry with features/limitations             |
| 7    | Docs                                                                                     | Update the [Sandbox guide](/docs/guides/sandbox) and this file                                                   |

## Related Docs

- [Sandbox guide](/docs/guides/sandbox) — choosing and configuring backends
- [Architecture](/docs/development/architecture)
