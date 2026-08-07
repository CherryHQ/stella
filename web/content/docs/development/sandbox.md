---
title: Sandbox Backend Abstraction
---

> This section is for developers contributing to Stella. For choosing and configuring a sandbox backend, see the [Sandbox guide](/docs/guides/sandbox).

## Core Model

The sandbox abstraction exists so runner code, plugin wiring, and tool execution do not depend on concrete backend types. Execution always runs through the active backend selected by the runner.

- `pkg/sandbox.Policy` — immutable backend-agnostic execution policy (logical working directory, typed Homes, network mode, env, timeout)
- `pkg/sandbox.Session` — per-run process-execution boundary and lifecycle owner; it exposes no host paths
- `pkg/sandbox.Filesystem` — provider-neutral persistent file-operation boundary, obtained separately from a `FilesystemSession`

Backend identity stays inside the runner and runner-facing sandbox packages. Plugin packages do not import `internal/agent/sandbox`.

## Session Interface

`pkg/sandbox.Session` exposes 7 methods:

| Method                                                     | Description                                               |
| ---------------------------------------------------------- | --------------------------------------------------------- |
| `Policy() Policy`                                          | Returns the immutable policy the session was created with |
| `Exec(ctx, command, ExecOptions) (ExecResult, error)`      | Run a command and wait for its result                     |
| `StartProcess(ctx, ProcessRequest) (ProcessHandle, error)` | Spawn a long-lived process with stdio handles             |
| `WorkingDir() string`                                      | Returns the logical working directory inside the sandbox  |
| `Close() error`                                            | Tear down the session and release resources               |
| `Alive() bool`                                             | Reports whether the session is still active               |
| `Done() <-chan struct{}`                                   | Channel closed when the session terminates                |

`Session` carries no file read/write methods or physical filesystem coordinates. File consumers use the separate `FilesystemSession.Filesystem()` capability.

## Provider filesystem boundary

`pkg/sandbox.Filesystem` is the provider-neutral boundary for persistent file operations. Callers use only canonical sandbox paths under `/workspace`, `/user`, or `/tmp`; the interface never exposes a host path. It supports bounded streaming reads, streaming writes and uploads, stat/list, mkdir, remove, and rename.

`local` and unsafe-gated `none` implement the boundary directly with root-contained filesystem operations. Docker runs one `stella-fs` helper process per operation inside the sandbox container. The protocol has strict request and response framing, preserves stable `io/fs` errors across the process boundary, and rejects malformed or oversized read responses.

An interrupted mutation returns `sandbox.ErrOutcomeUnknown`. Callers must report that state and must not retry automatically because the first operation may have completed. Docker preflight also requires the image's filesystem-helper revision to match the running `stellad` binary.

`read`, `write`, and `edit`, plus active prompt context and Skill paths, use the injected `Filesystem`. A missing `FilesystemSession`, an error opening it, or a nil `Filesystem` fails closed; active runtime paths never fall back to direct host I/O.

Physical sources, mounts, and coordinate resolution are immutable provider-private `hostlayout.Layout` construction configuration. Providers may translate physical coordinates internally, but those coordinates never cross the `pkg/sandbox` runtime contract.

## Typed Home registry and attachments

Phase 1 gives persistent Homes typed identity separate from a machine path. The registry records an immutable Store ID and opaque locator for each user or group Principal Home, per-principal Agent Home, and narrow system or system-Agent Skill root. `sandbox.HomeAttachment` is the stable contract for compute-facing consumers. `internal/home.WorkspaceView` temporarily carries local root projections for migrated current consumers until Phase 2. A user and a group with the same raw ID are distinct principals.

A user or group run receives its Principal and Agent Home attachments plus read-only shared Skill roots. A user-less run receives only those read-only shared Skill roots and no Principal or Agent Home. Group Agent Home Skill materialization has no user or `user_agent` scope: it does not turn group data into a user's `user_agent` Skill.

Explicit destructive owner deletion tombstones and fences Homes before a shared River purge worker removes bytes. This fencing is single-replica only. Phase 3 must add cross-replica SessionSandbox fencing; it is not implemented now.

## Current Architecture

### Session ownership

The runner creates a `sandbox.Session` for each run and keeps ownership of its lifecycle. Runner construction fails closed when no active sandbox session is available.

### Backend resolution

The runner resolves the active backend from plugin state and dispatches to a backend-specific factory. Built-in factories currently support `docker`, `local`, and `none`.

### Execution-time mediation

All local execution paths that must obey sandbox policy are mediated through the active runner session:

- core tools (`bash`) use `Session.Exec` through the runner-owned session
- `read`, `write`, `edit` obtain a short-lived `Filesystem` from `FilesystemSession` for each operation
- plugin tools receive `ToolContext.Runtime`, a `pkg/plugins.ToolRuntime` adapter over the active session
- active prompt context and Skill paths use the injected `Filesystem`
- MCP stdio process spawning uses `Session.StartProcess`

### stdio-MCP benefit

`Session.StartProcess` is supported by both built-in backends. Docker gives stdio MCP servers a dedicated container process namespace; the local backend starts them directly on the host OS.

### Non-runner filesystem access

Some code paths need local filesystem access without an already-injected runtime, such as prompt rendering or project metadata discovery outside an active agent run.

Prompt rendering falls back to direct `os.*` calls only when it has no injected runner Host. Skills and agent preset discovery use `pkg/plugins.NewLocalToolRuntime(...)` when called outside an active runner. These are intentional non-runner paths, not fallbacks for sandboxed tool execution.

### Explicit exception boundary

Remote MCP HTTP/SSE/StreamableHTTP transport is currently treated as a separate trust boundary.

- local stdio transport is runtime-mediated through the active runner session via `Session.StartProcess`
- remote transport dialing is **not** currently mediated by `ToolRuntime`
- this exception is tracked explicitly as `EX-009` and logged as `runtime.exception_path`

## Fail-Closed Behavior

Stella prefers explicit denial over silent downgrade:

- Docker unavailable at session-create time → runner fails to start
- unsupported policies → `PolicyCompatibilityError`, runner fails to start
- missing, failing, or nil active `Filesystem` → file consumers fail closed without host-I/O fallback
- direct non-mediated plugin exec → fail closed
- remote MCP HTTP/SSE/StreamableHTTP → explicit exception, not an implicit sandbox bypass
- On supported POSIX hosts, daemon Vision Xberg consumes one validated owned image-byte snapshot through `0700`/`0600` temporary staging with no host input path; it fails closed on Windows, while agent document parsing continues to use the sandbox Xberg CLI.

## Verification

The abstraction is covered by:

- session/host contract tests
- shared local, `none`, and Docker filesystem conformance tests
- strict helper protocol and cancellation tests
- a real-image Docker filesystem conformance test
- Docker binary/image helper-revision preflight tests
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

Project Skills remain ordinary files in durable Agent/project working trees. Typed Home filesystems are the authority for mutable `system`, `system_agent`, `user`, and `user_agent` content. PostgreSQL keeps Home identity inventory, Agent Skill policy, logical Reflect usage and pair activity, and migration/audit/backup compatibility; it does not keep mutable Skill bytes, current state, or changelog writes. There is no PostgreSQL fallback, mirror, dual read/write path, or restore-on-miss.

The Docker sandbox image bakes and labels the exact revision. It has no host-builtin fallback. Docker provider preflight rejects a binary/image revision mismatch, preventing the runner session from starting. Use `stellad system-bundle --help` for operator command syntax. Rebuild the development image with `mise run sandbox:docker:build`; rebuild every custom sandbox image from the matching Stella revision.

On startup, production verifies the strict Skill Home authority marker and rejects residual legacy PostgreSQL current state. Migrate a legacy deployment offline: enter maintenance mode, stop every legacy Skill writer, make and verify a PostgreSQL backup, run the dry run, resolve every unsupported-item or collision report, then run the real migration before starting the new server. Both dry run and migration require all three confirmation flags; use `stellad storage migrate-skills --help` for syntax. The migration is idempotent and no-replace, verifies digests, preserves canonical metadata, and writes migrated legacy PostgreSQL files as `0644`; it does not guess extensions or invent an executable bit. It archives deprecated/changelog data in a hidden Home migration archive, migrates logical Reflect usage, and never deletes source PostgreSQL rows or backups. A completed-marker rerun verifies only.

## Agent Skill policy

The stored scope vocabulary is `system`, `system_agent`, `user`, `user_agent`, and `project`, plus contextual `builtin`. Release builtins are immutable; managed Skills are mutable. Public canonical IDs are URL-safe stable resource identifiers. Clients must treat their encoding as an implementation detail and must not parse them or derive filesystem paths from them.

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
