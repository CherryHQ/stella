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

## Local workspace ownership

Phase 1 supports one replica and one trusted POSIX `STELLA_HOME`. PostgreSQL owner rows are identity and authorization authority; deterministic paths under `STELLA_HOME` are layout and byte authority. `internal/home.WorkspaceManager` is the only production component that creates typed roots. It creates a missing root only after confirming its live user, group, and Agent owners, and rejects symlinks, non-directories, unsafe IDs, and replacement of the trusted root. A user and group with the same raw ID use distinct paths.

A user or group run receives its Principal and Agent Home attachments plus read-only shared Skill roots. A user-less run receives only those read-only shared Skill roots and no Principal or Agent Home. Group Agent Home Skill materialization has no user or `user_agent` scope: it does not turn group data into a user's `user_agent` Skill.

Explicit destructive user, group, or Agent deletion fences local execution before deleting the database owner. Files and inodes are retained, but a later `WorkspaceView` fails because the durable owner is gone. Any filesystem entry at `agents/{id}` reserves that global Agent ID. These guarantees are bounded by the trusted host and are single-replica only. Multi-replica, Kubernetes, and S3 authority require a future redesign rather than reuse of a registry that does not exist.

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

## Builtin Skill bundle and projection

`resources.Registry` is the sole authority for release-owned builtins. It produces the immutable content-addressed bundle installed at `$STELLA_HOME/bundles/<revision>` for native `local` and `none` execution. Isolating execution projects that exact bundle read-only at `/opt/stella/skills/builtin`; `/opt` is an execution coordinate, not another authority, and bundle executable helper modes must survive the projection.

Project Skills remain ordinary files in durable Agent/project working trees. PostgreSQL remains the authority for mutable `system`, `system_agent`, `user`, and `user_agent` records; their execution materializations are derived caches. The Home filesystem authority cutover is planned and not active.

The Docker sandbox image bakes and labels the exact revision. It has no host-builtin fallback. Docker provider preflight rejects a binary/image revision mismatch, preventing the runner session from starting. Use `stellad system-bundle --help` for operator command syntax. Rebuild the development image with `mise run sandbox:docker:build`; rebuild every custom sandbox image from the matching Stella revision.

Before upgrading, operators must use the old working binary to import each custom Skill root under legacy `$STELLA_HOME/.agents/skills` as a global (`system`) Skill through **Settings → Skills** on older releases or **Admin Console → Deployment resources → Global Skills** on newer releases. They must back up, verify, and remove other residual paths. Startup reports every blocking path and exits without mutation. Current-manifest paths are inert even if their contents or modes differ; every other Skill root or residual path blocks startup.

## Agent Skill policy

The stored scope vocabulary is `system`, `system_agent`, `user`, `user_agent`, and `project`, plus contextual `builtin`. Release `builtin:<name>` is immutable. Administrator-installed `system:<name>` and Agent-bound `system_agent:<name>` are distinct mutable identities.

Resolution selects one winner before policy: `project > user_agent > user > system_agent > system > builtin`. Disabling that winner never exposes a lower same-name Skill. Policy defaults to enabled, is shared per Agent, and is independent of content-edit authorization and `disable_model_invocation`. An admitted turn keeps its snapshot; the next turn sees a successful commit. Legacy non-empty arrays are diagnostic but all-enabled; dangling disabled references have no execution effect and need explicit cleanup.

## Adding a New Backend

Every new sandbox backend requires changes in all of the following locations — missing any one causes a runtime error:

| Step | File                                | What to do                                                                                                       |
| ---- | ----------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| 1    | `internal/config/sandbox.go`        | Add `SandboxBackend<Name> = "<name>"` constant                                                                   |
| 2    | `internal/config/sandbox_env.go`    | Accept the name in `ActiveSandboxBackend`'s `STELLA_SANDBOX_BACKEND` switch                                      |
| 3    | `plugins/sandbox/<name>/session.go` | Implement `sandbox.Factory` and `sandbox.Session`                                                                |
| 4    | `internal/agent/sandbox/session.go` | Add a `case config.SandboxBackend<Name>:` branch in `createSessionForBackend` and implement the factory function |
| 5    | Docs                                | Update the [Sandbox guide](/docs/guides/sandbox) and this file                                                   |

## Related Docs

- [Sandbox guide](/docs/guides/sandbox) — choosing and configuring backends
- [Architecture](/docs/development/architecture)
