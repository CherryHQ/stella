---
title: Sandbox Backend Abstraction
---

## Status

Implemented. Docker is the recommended sandbox backend. A local backend is also available for Docker-free environments; Linux keeps OS-level hardening, while macOS currently runs local commands directly on the host without additional sandboxing. A `none` backend is also available for fully trusted workloads — it runs the agent directly on the host with the current user's permissions and no isolation of any kind. Anna's execution boundary is described by `pkg/sandbox` contracts, with runner-facing registry wiring in `internal/sandbox`.

## Purpose

The sandbox abstraction exists so runner code, plugin wiring, and tool execution do not depend on concrete backend types. Execution always runs through the active backend selected by the runner. Docker provides the strongest isolation; the local backend is a fallback for environments where Docker is unavailable or undesirable; the none backend skips all isolation for fully trusted single-user workloads.

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

## Backends

### Docker (recommended)

Docker provides full container-level process, filesystem, and network isolation. The Docker daemon must be running and reachable. Anna contacts it at session-create time and fails closed if it is unavailable:

- missing or unreachable Docker daemon → session creation fails, runner does not start
- unsupported policy → `PolicyCompatibilityError`, runner does not start
- no silent downgrade path exists

All platforms (Linux, macOS, Windows) support the Docker backend. There is no `auto` or `Relaxed` mode.

### Local (Docker-free)

The local backend runs commands directly on the host OS. It is intended for environments where Docker is unavailable (CI without Docker, embedded deployments, developer machines that prefer not to run a daemon).

**This backend does not provide container-level isolation.** It applies OS-level hardening layers instead:

| Layer                          | Platform | Mechanism                                                                                                                                                                                                                                      |
| ------------------------------ | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Process group kill + rlimits   | All Unix | `SIGKILL` on process group; `RLIMIT_FSIZE`, `RLIMIT_NOFILE`, `RLIMIT_CPU` via `prlimit(2)`                                                                                                                                                     |
| Filesystem + network isolation | Linux    | `bwrap` (required) — minimal usable Linux root with `/workspace` read-write, `/tmp`/`/var/tmp`/`/dev/shm` writable tmpfs, selected runtime/tool directories and DNS resolver config read-only; `--unshare-net` when network mode is `disabled` |
| No additional local isolation  | macOS    | Commands run directly on the host OS; filesystem and network policy are not enforced                                                                                                                                                           |

The local backend uses a **fail-closed** strategy on Linux: `bwrap` (bubblewrap) is mandatory. Session creation fails with an actionable error if bwrap is absent or non-functional (e.g. inside Docker without `--privileged`). There is no fallback to `unshare`-only or unconfined execution. Local sandbox processes do not inherit the full host environment; Anna injects only runner-managed session variables plus a small locale/terminal/proxy allowlist. On macOS, no extra sandboxing tool is currently applied.

#### Installing dependencies

**Linux — bubblewrap (required):**

```bash
# Debian / Ubuntu
apt install bubblewrap

# Fedora / RHEL
dnf install bubblewrap

# Arch
pacman -S bubblewrap
```

bwrap must be functional, not just installed. Inside Docker containers without `--privileged`, the kernel seccomp profile typically blocks namespace creation even when bwrap is present — use the Docker backend in that environment instead.

**macOS:**
No additional dependency is required. The current local backend runs commands directly on the host OS without applying a macOS-specific sandbox.

**Windows:** Not supported. Use the Docker backend.

#### Path presentation

On Linux the agent always sees its workspace at `/workspace` regardless of the real host path (bwrap bind-mounts it). This mirrors Docker's bind-mount behaviour. On macOS the agent sees the real host path.

### None (host execution)

The none backend runs the agent directly on the host OS with the current user's permissions. It imposes no isolation of any kind — no filesystem confinement, no network restrictions, no process group kill, and no rlimits. The agent inherits the full host environment merged with any runner-injected session variables.

**Use only for fully trusted agents in single-user local deployments.** This backend is not safe for untrusted agents or multi-user environments.

- No external dependencies — works on all platforms
- `ResolvePath` resolves relative paths against the working directory; absolute paths pass through unchanged
- Network policy is always `allow_all`; the configured per-agent network mode is ignored
- Session creation never fails due to missing tooling

## Configuration

Per-agent sandbox configuration is limited to network policy (mode and allowlist). Each agent independently controls whether its sandbox allows outbound network access and which hosts are reachable.

Network modes supported by Docker and by the Linux local backend:

| Mode        | Description                          |
| ----------- | ------------------------------------ |
| `disabled`  | No outbound network access (default) |
| `allow_all` | Unrestricted outbound access         |

The `whitelist` mode is removed. Docker and the Linux local backend validate the configured mode at session-create time and fail closed if the backend cannot enforce it. The macOS local backend currently ignores network policy and runs with host network access.

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

## Adding a New Backend

Every new sandbox backend requires changes in all of the following locations — missing any one causes a runtime error:

| Step | File                                           | What to do                                                                                                     |
| ---- | ---------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| 1    | `internal/config/sandbox.go`                   | Add `SandboxBackend<Name> = "<name>"` constant                                                                 |
| 2    | `internal/config/plugin.go`                    | Append name to `builtinSandboxNames` so the DB row is seeded                                                   |
| 3    | `plugins/sandbox/<name>/session.go`            | Implement `sandbox.Factory` and `sandbox.Session`                                                              |
| 4    | `plugins/sandbox/plugin.go`                    | Add entry to the `backends` slice in `init()` to register `AdminVisible` plugin metadata                       |
| 5    | `internal/sandbox/factory.go`                  | Call `mustRegisterFactory(r, <name>plugin.NewFactory(), true)` in `DefaultRegistry()`                          |
| 6    | `internal/agent/sandbox_backend.go`            | Add `config.SandboxBackend<Name>: create<Name>Session` to `sessionRegistry` and implement the factory function |
| 7    | `internal/admin/ui/static/js/pages/plugins.js` | Add `"sandbox/<name>"` to `validBackends` and a `sandboxMeta` entry with features/limitations                  |
| 8    | Docs                                           | Update this file and `sandbox-backend-abstraction.zh.md`                                                       |

## Related Docs

- [Architecture](/docs/core/architecture)
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/design-spec.md`
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/exceptions-register.md`
