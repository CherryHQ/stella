# Plan: boxsh sandbox backend

## Overview

Replace Anna's current path-guard/cwd-scoped local tool behavior with a mandatory `boxsh`-backed sandbox for Linux and macOS, while leaving Windows on the existing behavior.

The feature covers three connected concerns:

1. **Binary management** — Anna must acquire and expose a prebuilt `boxsh` binary the same way it already manages helper binaries such as `rtk`.
2. **Sandboxed core tools** — `bash`, `read`, `write`, and `edit` must execute through a shared long-lived `boxsh --rpc` subprocess instead of touching the host filesystem directly.
3. **Policy enforcement** — each agent may only access its own workspace boundary, with network disabled by default and configurable to `disabled`, `allow_all`, or `whitelist`.

### Goals

- Make `boxsh` the required sandbox backend on Linux and macOS.
- Restrict each agent to its own workspace boundary; no access to other agents' workspaces or arbitrary host paths.
- Ensure `bash`, `read`, `write`, and `edit` all see the same copy-on-write session view.
- Ship `boxsh` as a prebuilt managed binary, not a source-built dependency.
- Add network policy configuration with a secure default (`disabled`).
- Keep Windows working by explicitly skipping `boxsh` there.

### Success Criteria

- [ ] On Linux and macOS, Anna starts with a valid `boxsh` binary available through the existing managed-binary path.
- [ ] On Linux and macOS, core tools execute through `boxsh` rather than direct host I/O.
- [ ] An agent can only read/write within its own session COW view rooted at its own workspace boundary.
- [ ] Cross-agent workspace access is blocked by construction.
- [ ] Network policy defaults to disabled and supports `allow_all` and `whitelist` modes.
- [ ] Windows continues using the current backend without regressions.
- [ ] Tests cover binary bootstrap, tool execution, workspace isolation, and network policy behavior.

### Out of Scope

- Sandboxing the whole Anna server process.
- Reworking plugin execution outside the core tool boundary.
- Making macOS isolation equivalent to Linux.
- Changing user-facing tool names or LLM-facing tool affordances beyond backend behavior.
- Adding a backend-selection toggle or per-agent backend config.

## Technical Approach

Anna will keep its existing runner architecture and tool names, but on Linux/macOS the implementation behind core tools will change from direct Go execution to a shared `boxsh` RPC client.

### Key design decisions

- **Platform split**
  - Linux/macOS: require `boxsh`.
  - Windows: keep the current path-guard/cwd-scoped implementation.
- **Workspace scope**
  - Phase 1 sandbox root is derived from session type, not hard-coded blindly to `UserDataDir`.
  - For user-scoped sessions (`UserID > 0`): source workspace (`SRC`) is `UserDataDir`.
  - For non-user/system sessions: source workspace (`SRC`) is the agent workspace root from `snap.Workspace`.
  - Phase 1 destination workspace (`DST`) is an ephemeral per-session upperdir managed by Anna.
  - Relative paths and tool cwd must resolve inside that selected sandbox root.
- **Single sandbox view**
  - `bash`, `read`, `write`, and `edit` all use the same long-lived `boxsh` process so they share one filesystem view.
  - This requires one shared backend object per runner instance, not four independently constructed sandbox sessions.
- **Network policy**
  - New per-agent sandbox setting for network mode: `disabled` (default), `allow_all`, `whitelist`.
  - Linux maps directly to boxsh namespace flags/binds.
  - macOS uses the strongest available boxsh policy path, with behavior documented separately.
- **Failure behavior**
  - Linux/macOS: missing/invalid `boxsh` is a startup/runtime error for the sandboxed tool path.
  - Windows: never tries to use `boxsh`.

### Components

- **Embedded binary management**
  - Extend `scripts/download-tools.sh` to fetch `boxsh` release binaries.
  - Embed compressed binaries under `internal/embedded/binaries/`.
  - Reuse `internal/embedded/embedded.go` extraction flow.
  - On Linux/macOS, resolve only the managed embedded binary in normal operation; do not rely on implicit PATH fallback.
- **boxsh client**
  - New `internal/sandbox/boxshclient/` package.
  - Starts/stops `boxsh --rpc`, handles JSON-RPC, lifecycle, and path/session setup.
  - Exposes health so runner liveness can fail fast when the sandbox process dies.
- **Sandbox session management**
  - New helper(s) to create per-runner/per-session upperdir, derive binds, and clean up ephemeral state.
- **Core tool backend wiring**
  - Update core tool construction so Linux/macOS tools call the boxsh client.
  - Ensure all four core tools are built around one shared backend instance per runner.
  - Keep Windows on the existing Go tool implementations.
- **Config plumbing**
  - Extend agent config with sandbox network config.
  - Load via existing `settings_agents` store and `config.Snapshot` path.
- **Startup preflight**
  - Validate binary availability, selected platform support, sandbox root shape, required COW/filesystem capability, and selected network mode before a sandboxed runner starts.
  - Fail closed on Linux/macOS when guarantees cannot be met.
- **Tests/docs**
  - Update tests for managed binary expectations, platform behavior, and sandbox semantics.
  - Update docs to describe platform guarantees and network policy.

### Assumptions

- The phase-1 workspace boundary is selected by session type: `UserDataDir` for user sessions and `snap.Workspace` for non-user/system sessions.
- Ephemeral session upperdirs are sufficient for the first rollout.
- Network whitelist semantics can be expressed as an explicit allowlist of hosts/CIDRs/domains in config; exact boxsh argument mapping will be decided during implementation but must not block phase 1.
- macOS ships in the same milestone, but with explicitly weaker guarantees documented after testing.

## Implementation Phases

### Phase 1: Managed boxsh binary and config foundation

1. Extend `scripts/download-tools.sh` to fetch `boxsh` for supported Linux/macOS targets and add the compressed binary artifact to `internal/embedded/binaries/` packaging flow.  
   Files: `scripts/download-tools.sh`, `internal/embedded/binaries/*`, `internal/embedded/embedded.go`, related tests.
2. Add binary-resolution helpers/tests so Anna resolves the extracted managed `boxsh` binary on Linux/macOS and fails closed if it is missing or invalid.  
   Files: new helper in `internal/sandbox/...` or `internal/embedded/...`, tests.
3. Add startup preflight checks for Linux/macOS that validate managed binary presence, platform support, filesystem/COW prerequisites, and selected network mode.  
   Files: new helper in `internal/sandbox/...`, runner integration points, tests.
4. Add per-agent sandbox network policy config types and snapshot loading.  
   Files: `internal/config/config.go`, `internal/config/store.go`, `internal/config/snapshot.go`, `internal/config/dbstore.go`, DB schema/migrations, config tests.
5. Document the required Linux/macOS behavior and Windows skip path in exploration/architecture docs.  
   Files: `docs/boxsh-sandbox-exploration.md`, relevant docs pages.

### Phase 2: boxsh RPC client and session model

1. Implement `internal/sandbox/boxshclient/` with:
   - process startup/shutdown
   - JSON-RPC request/response handling
   - health/handshake
   - tool methods for `Exec`, `Read`, `Write`, `Edit`  
   Files: new package under `internal/sandbox/boxshclient/`, tests.
2. Implement a shared backend object/factory so one runner can construct `bash`, `read`, `write`, and `edit` adapters around the same boxsh session instead of independent per-tool sessions.  
   Files: `internal/sandbox/boxshclient/*`, `internal/agent/runner/gorunner.go`, tool construction helpers.
3. Implement session workspace helpers that:
   - derive `SRC` from `UserDataDir` for user sessions and `snap.Workspace` for non-user/system sessions
   - set cwd and relative-path resolution inside the chosen sandbox root
   - create ephemeral per-session `DST`
   - compute `boxsh` args/binds/network flags
   - enforce cleanup rules  
   Files: new package under `internal/sandbox/` or `internal/agent/runner/`, tests.
4. Define response normalization so Anna preserves compatible tool outputs and error semantics.  
   Files: `internal/sandbox/boxshclient/*`, possibly shared helper tests.

### Phase 3: Core tool integration on Linux/macOS

1. Introduce boxsh-backed tool implementations/adapters for `bash`, `read`, `write`, and `edit`.  
   Files: `plugins/tools/bash/bash.go`, `plugins/tools/read/read.go`, `plugins/tools/write/write.go`, `plugins/tools/edit/edit.go`, or new shared backend files.
2. Update core tool construction in `internal/agent/runner/gorunner.go` and related plumbing so Linux/macOS use boxsh-backed tools while Windows retains current behavior.  
   Files: `internal/agent/runner/gorunner.go`, `plugins/tools/registry.go`, possibly `internal/agent/factory.go`.
3. Propagate boxsh backend health into runner liveness so a dead sandbox process invalidates the runner promptly instead of leaving a wedged cached runner alive.  
   Files: `internal/agent/runner/gorunner.go`, runner lifecycle helpers, tests.
4. Ensure runner/tool lifecycle closes the shared boxsh process and cleans up ephemeral upperdirs correctly.  
   Files: `internal/agent/runner/gorunner.go`, new lifecycle helpers, tests.
5. Remove or reduce reliance on the existing path-guard wrapper for Linux/macOS core tools where it becomes redundant, while preserving Windows behavior.  
   Files: `plugins/tools/sandbox/sandbox.go`, relevant tool tests.

### Phase 4: Policy enforcement, isolation tests, and docs

1. Add integration tests proving cross-workspace access is blocked by construction and that all four tools share the same COW view.  
   Files: tool tests, runner tests, new integration tests.
2. Add network policy tests for:
   - disabled default
   - allow-all mode
   - whitelist mode behavior and validation  
   Files: config tests, integration tests.
3. Verify Linux/macOS platform-specific behavior and document the guarantees precisely.  
   Files: docs under `docs/content/docs/`, runner/tool docs, builtin agent docs if needed.
4. Update README/docs for the new sandbox model and managed-binary requirement.  
   Files: `README.md`, `docs/content/docs/...`, `internal/agent/runner/builtin/anna/...` if user-facing behavior changes.

## Testing Strategy

- **Config tests**
  - agent config loads sandbox network settings from `settings_agents.sandbox`
  - invalid network modes / malformed whitelist values fail validation
- **Embedded binary tests**
  - `boxsh` is extracted alongside existing binaries
  - managed-bin resolution finds `boxsh` correctly on supported platforms
  - Linux/macOS do not silently fall back to arbitrary PATH binaries in normal operation
- **boxsh client unit tests**
  - process startup/handshake
  - request/response parsing
  - timeout/error propagation
  - cleanup on close/crash
  - cached-runner invalidation when the shared boxsh process dies
- **Tool integration tests**
  - `bash`, `read`, `write`, `edit` all operate against the same COW session view
  - edits/writes do not affect the underlying source workspace directly
- **Isolation tests**
  - one agent cannot access another agent's sandbox root (`UserDataDir` for user sessions, agent workspace root for non-user sessions)
  - absolute-path access outside the allowed workspace is rejected/hidden
- **Network policy tests**
  - default disabled blocks outbound access
  - allow-all permits outbound access where platform supports it
  - whitelist mode only permits configured destinations
- **Regression tests**
  - Windows still uses current backend behavior
  - existing non-sandboxed subsystems are unaffected

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| boxsh binary packaging adds release/build complexity | High | Reuse current embedded-binary workflow and keep boxsh version pinned/tested |
| startup preconditions fail late (missing binary, unsupported FS/platform, invalid network mode) | High | Add explicit Linux/macOS preflight and fail closed before sandboxed runners start |
| macOS behavior is weaker or inconsistent vs Linux | High | Treat as supported-but-weaker, add explicit platform tests/docs, avoid Linux-equivalence claims |
| Session COW model changes semantics vs direct host writes | High | Make source/destination mapping explicit, add end-to-end tests, document behavior clearly |
| Long-lived subprocess lifecycle leaks processes/temp dirs | Medium | Centralize lifecycle in `boxshclient`, attach cleanup to runner/tool close, test crash/timeout paths |
| Network whitelist mapping to boxsh is more limited than desired | Medium | Lock phase-1 config shape early, document any platform-specific limitations, validate config strictly |
| Existing path-guard tests assume direct host filesystem behavior | Medium | Split tests by platform/backend expectations and update assertions deliberately |

## Open Questions

- None. Remaining unknowns are treated as implementation assumptions in this plan.

## Review Feedback

Reviewer round 1:

- Clarified sandbox root semantics for user vs non-user sessions.
- Added requirement for a shared backend object so all four tools share one boxsh session.
- Removed implicit PATH fallback from normal Linux/macOS behavior.
- Added startup preflight/fail-closed validation.
- Added runner liveness propagation for dead boxsh subprocesses.
- Tightened the test matrix to cover cached-runner invalidation after shared boxsh death.

## Final Status

Phase 1 complete. Phase 2 not started.
