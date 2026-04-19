# Plan: Add Docker as a sandbox backend

## Problem

Anna currently supports two local sandbox backends:

- **boxsh** — overlay-filesystem sandbox, Linux/macOS only, depends on a managed
  `boxsh` binary. It is the isolation backend of record.
- **local** — relaxed, advisory-only. No actual isolation. Used when boxsh is
  unavailable or when a caller explicitly opts into relaxed mode.

Gaps this plan addresses:

1. **Windows has no isolation option.** Today Windows forcibly degrades to
   `local`, which is advisory only. Docker Desktop on Windows would give
   Windows users a real isolation backend.
2. **No way to provide a custom image / OS.** boxsh mirrors the host
   filesystem. Some workflows want a clean Linux userspace (e.g. running CI-like
   jobs or agents that need a specific toolchain). There is no way to pin that
   today.
3. **Network whitelist is unsupported on boxsh.** boxsh 2.0.1 rejects
   `whitelist` mode; docker (via `--network` + user-defined bridge + optional
   proxy / nftables) is a reasonable future-path for fine-grained egress
   control.

Adding `docker` as a third backend alongside `boxsh` and `local` closes gap 1 and
2 immediately, and gives us a platform for gap 3 later.

## How we got here

Explored to ground this plan:

- `pkg/sandbox/session.go` — `Session` and `Host` interfaces (the contract).
- `pkg/sandbox/factory.go` — `Factory` interface and `Registry`.
- `pkg/sandbox/policy.go` — `Policy`, `FilesystemPolicy`, `NetworkPolicy`,
  `ProcessPolicy`, `PolicyCompatibilityError`.
- `pkg/sandbox/observe.go` — session logging helpers (`LogSessionCreated`,
  `LogSessionClosed`, `LogRelaxedMode`, `LogPolicyDenied`).
- `internal/sandbox/factory.go` — `DefaultRegistry()` wires `boxsh` and `local`
  factories; this is the single registration point.
- `internal/sandbox/contract_test.go` — `TestSessionContract` runs the shared
  contract against each factory. Docker must pass the same suite.
- `internal/config/sandbox.go` — `SandboxConfig` enumerates `auto`, `boxsh`,
  `local`. Validation whitelists backend names.
- `internal/agent/runner/sandbox_backend.go` — runner-side session creation:
  `sessionRegistry` map + `resolveSessionBackendName` (auto-select logic).
- `plugins/sandbox/boxsh/` — reference plugin layout:
  - `session.go` — `Factory`/`Session`/`Host` impl
  - `config.go` — backend-local config types
  - `preflight.go` — early validation
  - `trace.go` — OTel span helpers
  - `boxshclient/` — low-level subprocess client & session-dir management
- `plugins/sandbox/local/session.go` — simpler reference for the contract.
- `docs/content/docs/core/sandbox-backend-abstraction.md` — "Backend Addition
  Rules" (add-only; implement `Factory`/`Session`/`Host`; register;
  contract-test; fail closed when policy unsupported).
- `docs/content/docs/getting-started/configuration.md`,
  `docs/content/docs/getting-started/deployment.md` — end-user surface to
  update.

## Design decisions

### D1. Container-per-session, not container-per-call

**Decision:** One long-lived docker container per `sandbox.Session`. `Close()`
stops and removes it. All `Host` operations are routed to this container.

**Alternatives ruled out:**
- *Container-per-call*: start a container for each `Exec`/`ReadFile`. Simple but
  startup latency (100–500ms) kills `read`/`stat` performance and breaks
  `StartProcess` which needs a persistent PID.
- *Docker API via REST*: equivalent semantics; adds a Go dependency
  (`github.com/docker/docker/client`). Shelling out to `docker` is simpler,
  matches boxsh's subprocess pattern, and avoids version coupling with the
  Docker Engine library. Chosen for phase 1; revisit if we need streaming exec
  primitives that shelling out can't deliver.

**Tradeoffs accepted:**
- One-time container warm-up on session start (~1–3s for pull-miss, ~200ms
  warm). Runner sessions already pay a comparable cost for boxsh startup, so
  this is in-band.
- Session lifetime is the container lifetime. Crash recovery requires an
  "orphan cleanup" analogous to boxsh's `CleanupOrphanedSessions` — we list
  containers with our label prefix and remove stale ones on startup.

### D2. Bind-mount the workspace; do filesystem ops from the host

**Decision:** Bind-mount `WorkspaceRoot` at a fixed in-container path
(`/workspace`) with `:rw`, and each `ReadOnlyPath` at `/workspace-readonly/<i>`
with `:ro`. `ReadFile`/`WriteFile`/`EditFile`/`Stat`/`ListDir`/`MkdirAll`/
`Remove`/`Rename`/`CreateTemp` operate on the host paths directly (same
strategy the `local` backend uses, minus the escape check flip).

**Alternatives ruled out:**
- *Route every FS op through `docker cp` / `docker exec cat / tee`*: stronger
  UID-mapping story but adds ~10–50ms per op and complicates atomicity
  (`docker cp` is not transactional; a failed write leaves partial state).
  The container already enforces the process boundary — the FS boundary is
  enforced by the mount set.
- *Use a volume instead of bind mount*: volumes are Docker-managed and don't
  let the host see writes. That breaks `Sync()` semantics (writes inside the
  sandbox must be visible on the host).

**Tradeoffs accepted:**
- UID mismatch: container processes may write as root (uid 0), leaving
  root-owned files on the host. Mitigation: pass `--user "$(id -u):$(id -g)"`
  by default. Loses `sudo`-like abilities inside the container, but that is an
  acceptable default — user can override via plugin config if needed.
- No copy-on-write isolation (unlike boxsh's overlay). A runaway script can
  damage the workspace. This is equivalent to the `local` backend's blast
  radius, so it is not a regression for users currently on `local`; and it's a
  regression from boxsh for users explicitly switching. Document this
  tradeoff in backend docs.

### D3. Exec via `docker exec -i`

**Decision:** `Host.Exec` runs `docker exec -i --workdir <cwd> --env ...
<container> /bin/sh -c <command>`. Env and cwd are passed as flags, not
encoded into the shell string (avoids the quoting gymnastics boxsh has to
do). Use `/bin/sh` rather than `bash` so the minimal default image (see D6)
just works; users who want bash-specific features can ship `bash` in a custom
image and point their script's shebang at it.

**Host↔container path translation:** Every caller-supplied path that crosses
into `docker exec` or into a mount flag must be rewritten from host form to
in-container form:

- `WorkspaceRoot` (host) ↔ `/workspace` (container, `:rw`).
- `ReadOnlyPaths[i]` (host) ↔ `/workspace-readonly/<i>` (container, `:ro`).
- `opts.Cwd` / `ProcessRequest.Cwd`: arrive as host paths; `docker exec
  --workdir` requires the in-container path, so translate before invoking
  exec. If the host cwd is outside the known mount set, reject with a typed
  error rather than passing it through verbatim.

`dockerHost.ResolvePath` keeps returning host paths (so host-side FS ops keep
working as per D2); a separate `toContainerPath(host string) (string, error)`
helper inside `dockerclient` handles the rewrite and is the single place
where the mapping lives. The factory populates the mount table at session
start so the mapping is deterministic.

**Tradeoffs accepted:**
- `/bin/sh` on minimal images is typically `dash` or BusyBox `ash`, not bash.
  Scripts that rely on bashisms (`[[ ]]`, arrays, `set -o pipefail` in some
  variants) must explicitly use `bash` — either by using a custom image or by
  invoking `bash -c` inside the command. Document this.

### D3a. Container entrypoint / keep-alive

**Decision:** `dockerSession` starts the container with
`--entrypoint /bin/sh -c 'tail -f /dev/null'` (portable no-op, works in
BusyBox/alpine and full distros). The container has a single long-running PID
1 that consumes no CPU and stays up until `docker stop`. All work goes
through `docker exec` against this PID 1's container.

**Alternatives ruled out:**
- *`sleep infinity`*: not all `sleep` impls accept `infinity` (BusyBox older
  than 1.30 rejects it).
- *`docker create` + `docker start` with no entrypoint*: the container would
  exit immediately if the image's default CMD is short-lived (e.g. `sh`).
- *Custom "anna-sandbox-init" image with a proper PID 1 supervisor*: adds a
  CI image-publishing surface (see D6) for zero current benefit.

**Tradeoffs accepted:**
- `tail -f /dev/null` is a PID-1 anti-pattern in production (no signal
  forwarding), but we don't run child services under it — every user command
  is its own `docker exec`, so signals are handled by `docker exec`'s own
  process tree.

### D4. `StartProcess` and `HTTPRequest` / `OpenHTTPStream`

**Decision — phase 1:**
- `StartProcess` → implemented via `docker exec -i` with stdin/stdout/stderr
  pipes returned as `ProcessHandle`. (boxsh fails closed today; docker is
  well-suited to this via persistent `docker exec` attach.)
- `HTTPRequest` / `OpenHTTPStream` → **fail closed**, matching boxsh. Transport
  mediation is a cross-backend concern; wiring it here would create a second
  implementation before we've stabilized the interface. Revisit when boxsh
  grows it.

**Alternatives ruled out:**
- Implement HTTP using a host-side `http.Client`: breaks the isolation
  boundary — the container thinks it has no network but we'd make requests
  from the host. Fails the "fail-closed over silent downgrade" principle.

### D5. Network policy mapping

**Decision:**

| Policy mode   | Docker flag                                                         |
| ------------- | ------------------------------------------------------------------- |
| `disabled`    | `--network none`                                                    |
| `allow_all`   | default bridge network (no flag)                                    |
| `whitelist`   | **fail closed** in phase 1 (`PolicyCompatibilityError`, `RelaxedWouldHelp=true`) |

`whitelist` requires either a custom bridge with iptables rules or a sidecar
egress proxy. Not in phase 1 — matches boxsh's current behavior so we don't
set a stronger contract than boxsh delivers.

### D6. Default image & overridability

**Decision:** Default image is configurable via `sandbox.docker.image` in
`config.SandboxConfig` (new field). Default value: a known-small,
widely-available image — `alpine:3.20`. Users can override per-config.

**Alternatives ruled out:**
- *Bake a custom "anna-sandbox" image*: adds a CI publishing surface
  (registry, tags, signing) that doesn't exist today. Use an off-the-shelf
  image; revisit once we have repeat use cases requiring specific tools.
- *Hardcode image*: blocks the "custom toolchain" use case that is one of the
  motivators for this backend.

**Tradeoffs:** `alpine` uses musl libc, which can surprise users whose scripts
assume glibc. Document this and make the override discoverable.

### D7. `auto` selection rules

**Decision:** `auto` selection order stays:
`boxsh` (if platform supports it) → `local`.

Docker is **not** included in `auto` selection. It is opt-in via
`sandbox.backend: docker`.

**Why:** docker requires a running daemon and an image pull — preflight cost
we shouldn't impose silently. Users who want docker will pick it explicitly.
This matches "explicit denial over silent downgrade" in the abstraction doc.

**Future:** could add `auto` → `docker` on Windows (where boxsh is
unavailable and `local` is advisory only). Defer — keep phase 1 strictly
opt-in and revisit after real usage.

### D8. Plugin layout

**Decision:** Mirror `plugins/sandbox/boxsh/`:

```
plugins/sandbox/docker/
  session.go       Factory, Session, Host impl
  config.go        DockerConfig + Validate
  preflight.go     docker daemon reachable, image exists or pullable
  trace.go         OTel tracer + helpers (copy of boxsh/trace.go)
  dockerclient/
    client.go        shell-out to `docker` CLI + JSON parsing
    container.go     container lifecycle (create, start, stop, rm)
    exec.go          `docker exec` wrappers for Host.Exec / StartProcess
    labels.go        label constants for orphan cleanup
    orphan.go        CleanupOrphanedContainers(annaHome)
```

Keeps the per-backend subpackage pattern already established; runner code
never imports `dockerclient`.

## What changes where

### New files

- `plugins/sandbox/docker/session.go` — `dockerFactory`, `dockerSession`,
  `dockerHost`. Session creates a container on `CreateSession`, removes it on
  `Close`.
- `plugins/sandbox/docker/config.go` — `DockerConfig{Image, User, ExtraMounts,
  Network: NetworkConfig}` + `Validate()`.
- `plugins/sandbox/docker/preflight.go` — check `docker version` succeeds,
  image is present locally (or `DockerConfig.AllowPull` will pull it).
- `plugins/sandbox/docker/trace.go` — OTel tracer `sandbox.docker`.
- `plugins/sandbox/docker/dockerclient/*.go` — subprocess client,
  container lifecycle, exec, orphan cleanup.
- `plugins/sandbox/docker/session_test.go`,
  `plugins/sandbox/docker/dockerclient/*_test.go` — unit tests.
- `plugins/sandbox/docker/preflight_test.go`.

### Modified files

- `internal/config/sandbox.go`:
  - Add `SandboxBackendDocker = "docker"`.
  - Add `Docker SandboxDockerConfig` field on `SandboxConfig`.
  - Accept `docker` in `Validate`.
  - New `SandboxDockerConfig{Image, User, AllowPull, ExtraMounts}` with
    `Validate()`. `AllowPull` (default `false`) gates preflight from running
    `docker pull`; `ExtraMounts []string` lets users add extra bind mounts
    beyond the managed workspace / read-only set (validated as
    `host:container[:ro]` tuples).
- `internal/sandbox/factory.go`:
  - Import `dockerplugin "github.com/vaayne/anna/plugins/sandbox/docker"`.
  - Register `dockerplugin.NewFactory()` in `DefaultRegistry()`. Always
    registered (docker daemon discovery happens at session-create time via
    `Supported()` / `Available()`).
- `internal/agent/runner/sandbox_backend.go`:
  - Add `createDockerSession` mirroring `createBoxshSession`.
  - Add entry in `sessionRegistry`.
  - Add `cleanupOrphanedDockerContainers(annaHome)` (thin wrapper around
    `dockerclient.CleanupOrphanedContainers`), guarded by a `sync.Once`
    matching the boxsh path.
  - `resolveSessionBackendName` keeps current behavior for `auto` (boxsh→local);
    no change.
- `internal/agent/runner/sandbox_backend_test.go` — coverage for the new
  factory wiring and for the explicit `docker` selection path.
- `internal/agent/runner/gorunner.go`:
  - `prepareSandbox` currently early-returns on `local` and unconditionally
    runs `cleanupOrphanedBoxshSessions` + `boxshsandbox.Preflight` for
    everything else. Restructure into a `switch cfg.Sandbox.BackendName()`:
    `local` → return; `boxsh` → existing path; `docker` → call
    `cleanupOrphanedDockerContainers` then a new `dockerpreflight.Preflight`.
    Default case keeps the boxsh path for `auto`-resolved backends to stay
    compatible with the existing resolver. (Without this fix a
    `docker`-configured runner misfires boxsh preflight and fails on Windows
    where docker is the only viable backend.)
- `internal/sandbox/contract_test.go`:
  - Add `TestSessionContract/DockerFactory` — skip if `docker version` fails
    or if CI lacks daemon access.
- `internal/sandbox/policy_compat_test.go` — add docker policy-compat cases
  (disabled / allow_all supported; whitelist fails closed with
  `RelaxedWouldHelp=true`).

### Documentation

- `docs/content/docs/core/sandbox-backend-abstraction.md` — list `docker` as a
  registered backend; note it's opt-in and does not participate in `auto`.
- `docs/content/docs/getting-started/configuration.md` (+ `.zh.md`, `.ja.md`)
  — add a `docker` backend section: config keys, default image, required
  daemon, known tradeoffs (UID, FS isolation).
- `docs/content/docs/getting-started/deployment.md` (+ `.zh.md`, `.ja.md`) —
  note docker option for Windows / containerized-agent deployments.
- `README.md` — mention docker as a backend option (1 line).
- `internal/agent/runner/builtin/anna/` — if any agent preset references the
  sandbox backend list, keep in sync (per repo CLAUDE.md).

## Migration / implementation order

Phase order is chosen so the compiler enforces correctness at each step, and
so we never have a half-wired registry.

1. **Config first.** Introduce `SandboxBackendDocker` + validation so both
   plugin code and tests can reference the constant without chicken-and-egg.
2. **Low-level `dockerclient` package.** Pure subprocess / JSON code, no
   sandbox types. Testable in isolation with a fake `docker` binary in
   `$PATH`.
3. **Plugin `Factory` + `Session` + `Host`.** Consume `dockerclient`;
   implement the `pkg/sandbox` contract. Add unit tests that run only if
   `docker` is available (skip otherwise, matching boxsh).
4. **Registry + runner wiring.** Register in `internal/sandbox` and wire
   `createDockerSession` in the runner. After this step, the backend is
   end-to-end reachable via config.
5. **Contract & policy-compat tests.** Extend shared test suites. CI skips
   them when docker is unavailable.
6. **Docs + changelog.** Last, because the user-facing surface (config keys,
   defaults) stabilizes in earlier phases.
7. **Release notes entry + `docs/content/docs/changelog.mdx`.**

## Tasks

### Phase 1: Config ✅

- [x] Add `SandboxBackendDocker = "docker"` to `internal/config/sandbox.go`.
- [x] Add `SandboxDockerConfig{Image, User, AllowPull, ExtraMounts}` +
      `Validate()`; embed as `Docker SandboxDockerConfig` on `SandboxConfig`.
      `Validate` must reject malformed `ExtraMounts` entries and reject
      absolute-outside-project paths when `AllowEscapes=false`.
- [x] Accept `docker` in `SandboxConfig.Validate()`; reject unknown
      `sandbox.docker.*` combinations.
- [x] Extend `internal/config/config_test.go` with docker cases.

### Phase 2: `dockerclient` low-level package ✅

- [x] `plugins/sandbox/docker/dockerclient/client.go` — `Client` struct;
      `docker version` preflight; binary-path resolution (PATH lookup; error
      if missing).
- [x] `plugins/sandbox/docker/dockerclient/container.go` — `CreateContainer`,
      `StartContainer`, `StopContainer`, `RemoveContainer`; tag created
      containers with `anna.sandbox.session_id` label.
- [x] `plugins/sandbox/docker/dockerclient/exec.go` — `Exec` (blocking,
      returns stdout/stderr/exit) and `StartExec` (streaming, returns piped
      handles for `StartProcess`).
- [x] `plugins/sandbox/docker/dockerclient/labels.go` — label constants
      (`anna.sandbox.session_id`, `anna.sandbox.anna_home`).
- [x] `plugins/sandbox/docker/dockerclient/orphan.go` —
      `CleanupOrphanedContainers(annaHome)` lists and force-removes
      containers whose `anna.sandbox.anna_home` matches but whose session is
      dead.
- [x] Unit tests using a shim `docker` binary on `PATH` (shell script
      echoing canned JSON). No real daemon required for unit coverage.

### Phase 3: Plugin `Factory` / `Session` / `Host` ✅

- [x] `plugins/sandbox/docker/trace.go` — tracer + `recordError` helper (copy
      boxsh pattern).
- [x] `plugins/sandbox/docker/config.go` — re-export `NetworkConfig` from
      `pkg/sandbox` or define local wrapper + `Validate`.
- [x] `plugins/sandbox/docker/preflight.go` — `Preflight(cfg)` returning
      `error` if daemon unreachable or image absent and `AllowPull=false`.
- [x] `plugins/sandbox/docker/session.go` — `dockerFactory` (`Name="docker"`,
      `Available()` checks daemon reachability, `Supported()` fails closed on
      `whitelist`), `dockerSession` (container lifecycle, `watchContainer`
      goroutine analogous to boxsh's `watchBackend`), `dockerHost`
      (mediated FS ops on host paths, `Exec` via `docker exec`, `StartProcess`
      via streaming exec, HTTP fails closed).
- [x] `dockerHost.ResolvePath` — translates logical `WorkingDir`-relative
      paths to host paths under `WorkspaceRoot` (same as `local`); translates
      in-container mount points back to host paths where relevant.
- [x] `dockerclient.toContainerPath(hostPath) (string, error)` — the single
      host→container path mapper used by `Exec` and `StartProcess`. Built
      from the session's mount table (`WorkspaceRoot → /workspace`,
      `ReadOnlyPaths[i] → /workspace-readonly/<i>`, plus any configured
      `ExtraMounts`). Returns a typed error if the host path isn't covered
      by any mount — callers fail closed rather than silently running with a
      bogus `--workdir`.
- [x] `dockerHost.Exec` / `dockerHost.StartProcess` — apply `toContainerPath`
      to `opts.Cwd` / `ProcessRequest.Cwd` before passing to
      `docker exec --workdir`, and translate any path-shaped values in
      `opts.Env` / `ProcessRequest.Env` that refer to workspace locations.
- [x] Unit tests for `dockerHost` path resolution and writability checks.
- [x] Unit tests for `dockerSession` lifecycle — skip if docker absent.

### Phase 4: Registry + runner wiring ✅

- [x] `internal/sandbox/factory.go` — register `dockerplugin.NewFactory()` in
      `DefaultRegistry()`. Always registered (platform gating is done by
      `Available()`).
- [x] `internal/agent/runner/sandbox_backend.go` — add `createDockerSession`;
      add to `sessionRegistry`; add `cleanupOrphanedDockerContainers`
      (`sync.Once`-guarded wrapper around `dockerclient.CleanupOrphanedContainers`).
      Do **not** change `resolveSessionBackendName` (docker stays opt-in, per D7).
- [x] `internal/agent/runner/gorunner.go` — refactor `prepareSandbox` into a
      `switch` on `cfg.Sandbox.BackendName()`: `local` → return; `docker` →
      `cleanupOrphanedDockerContainers` + `dockerpreflight.Preflight`;
      `boxsh` (and default) → existing boxsh path. _Required: without this
      change a docker-configured runner will misfire the boxsh preflight._
- [x] `internal/agent/runner/sandbox_backend_test.go` — add cases for
      explicit `docker` selection and for error when daemon missing.
- [x] `internal/agent/runner/gorunner_test.go` — test that `prepareSandbox`
      dispatches to the docker path (preflight is invoked, boxsh preflight
      is not) when backend is `docker`.

### Phase 5: Contract & policy-compat tests ✅

- [x] `internal/sandbox/contract_test.go` — add `DockerFactory` subtest;
      skip with `t.Skip` when docker is unreachable (same pattern as the
      existing `BoxshFactory` skip).
- [x] `internal/sandbox/policy_compat_test.go` — cases for
      `disabled`/`allow_all` pass, `whitelist` fails with
      `PolicyCompatibilityError{RelaxedWouldHelp: true}`, `whitelist +
      Relaxed=true` passes with a `logRelaxedMode` warning.
- [x] `mise run test` green locally. Reserve race for CI per repo CLAUDE.md.

### Phase 6: Documentation ✅

- [x] `docs/content/docs/core/sandbox-backend-abstraction.md` — add docker
      entry under "Current Architecture" and "Backend Addition Rules"
      example.
- [x] `docs/content/docs/getting-started/configuration.md` + `.zh.md` /
      `.ja.md` — docker config example; UID-mapping note; default image note.
- [x] `docs/content/docs/getting-started/deployment.md` + `.zh.md` /
      `.ja.md` — when to prefer docker over boxsh/local.
- [x] `docs/content/docs/changelog.mdx` — entry for docker backend.
- [x] `README.md` — 1-line mention of docker backend.
- [x] `internal/agent/runner/builtin/anna/` — sync if any preset lists
      backends.

### Phase 7: Release prep

<!-- Only after code + tests + docs land. Matches release workflow at
     .agents/skills/release/SKILL.md. -->

- [ ] Commit pending UI changes (`agents.templ`, `agents_templ.go`, `agents.js`).
- [ ] Fix open bugs from code review (see `handoff.md` blockers section).
- [ ] `mise run format` + `mise run test` green.
- [ ] `mise run release:check`.
- [ ] Draft commit: `✨ feat: add docker as sandbox backend`.
