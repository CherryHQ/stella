# Handoff

## Phase 0: Planning

**Status:** complete

**Tasks completed:**

- Planned the subprocess plugin runtime migration with tools and channels as
  the first scope.
- Converted discovery questions into explicit implementation assumptions.
- Saved the approved session plan, task checklist, and handoff log.

**Files changed:**

- `.agents/sessions/2026-03-28-plugin-runtime-migration/plan.md` — approved
  implementation plan
- `.agents/sessions/2026-03-28-plugin-runtime-migration/tasks.md` — phase
  checklist
- `.agents/sessions/2026-03-28-plugin-runtime-migration/handoff.md` — session
  handoff log initialized

**Commits:**

- None yet

**Decisions & context for next phase:**

- Tools and network channels are the first migration target.
- Providers and memory are explicitly deferred.
- `cli` remains builtin in this milestone.
- JS plugins remain supported during the migration.

## Phase 1: Runtime Foundation

**Status:** complete

**Tasks completed:**

- 1.1: Added a new `pluginapi` package with a versioned manifest schema,
  protocol envelopes, handshake payloads, and capability constants.
- 1.2: Added a new `pluginhost` package with manifest loading, catalog
  discovery, line-framed stdio protocol helpers, a plugin client, and a
  restart-capable supervisor.
- 1.3: Added Anna home plugin path helpers and a builtin entrypoint token
  (`@anna`) so first-party plugins can run as subprocesses of the current
  binary in later phases.
- 1.4: Added focused tests for manifest validation, duplicate discovery,
  handshake/health, and supervisor restart behavior.

**Files changed:**

- `internal/pluginapi/types.go` — protocol and manifest types
- `internal/pluginhost/manifest.go` — manifest loading and validation
- `internal/pluginhost/catalog.go` — filesystem discovery and catalog lookup
- `internal/pluginhost/protocol.go` — line-framed envelope IO helpers
- `internal/pluginhost/client.go` — subprocess lifecycle and request/response
  client
- `internal/pluginhost/supervisor.go` — restart-capable host supervisor
- `internal/pluginhost/catalog_test.go` — catalog discovery tests
- `internal/pluginhost/manifest_test.go` — manifest validation tests
- `internal/pluginhost/client_test.go` — handshake and restart tests
- `internal/config/plugins.go` — plugin path helpers under `ANNA_HOME`

**Commits:**

- `99c7d80` — `✨ feat: scaffold subprocess plugin runtime`

**Decisions & context for next phase:**

- The runtime is intentionally generic and not yet wired into runner or channel
  startup paths.
- First-party plugin packaging should avoid a second deployment story; the
  `@anna` builtin entrypoint is the preferred bridge for Phase 2.
- Tool migration should start by defining the tool RPC contract and adapter
  before converting the concrete tools.

### Fixes

- Removed the hidden `anna plugin runtime ...` CLI entrypoint and moved the
  builtin tool runtime behind an internal env-driven boot path in `cmd/anna`,
  so the subprocess bridge is no longer exposed as a normal command surface.
- Changed request cancellation in `internal/pluginhost/client.go` to terminate
  the current plugin process instead of leaking a blocked response reader
  goroutine.
- Added `Close()` forwarding to the sandbox wrapper so subprocess-backed file
  tools actually shut down when the runner closes.
- Added `internal/agent/tool/plugin_entrypoint_test.go` so tool package tests
  use a real built `anna` binary as the builtin plugin entrypoint instead of
  recursively re-running the Go test binary.
- Tightened the internal runtime gate to verify a host-provided token from an
  inherited file descriptor instead of trusting env vars alone.
- Bounded `Client.Close()` shutdown requests with a timeout so an unresponsive
  plugin cannot block runner teardown indefinitely.
- Removed the tool runtime from the main `anna` binary entirely and moved the
  subprocess bridge into the dedicated `cmd/anna-plugin` helper binary.
- Updated the test harnesses and command build task so plugin-backed tools now
  use `anna-plugin` as the builtin entrypoint instead of the main binary.
- Updated GoReleaser packaging so release archives include both `anna` and
  `anna-plugin`, keeping the helper binary present in shipped artifacts.

## Phase 2: Tool Plugin Migration

**Status:** complete

**Tasks completed:**

- Added a subprocess tool RPC bridge that serves built-in tools from the
  current `anna` binary via the `@anna` builtin entrypoint.
- Routed the built-in tool registry through subprocess-backed tool adapters
  while preserving the existing sandbox and working-directory behavior.
- Migrated `read`, `bash`, `edit`, `write`, and `webfetch` onto the bundled
  subprocess plugin path.
- Added integration coverage that runs the real `anna` binary and verifies the
  migrated tools still behave equivalently, including sandbox rejection.

**Files changed:**

- `internal/pluginapi/types.go` — added tool-specific manifest and RPC types
- `internal/pluginhost/server.go` — single-tool subprocess protocol server
- `internal/pluginhost/manifest.go` — builtin entrypoint and tool validation
- `internal/pluginhost/client.go` — builtin entrypoint launch bridge
- `internal/agent/tool/plugin_runtime.go` — bundled built-in tool manifest and
  runtime helper
- `cmd/anna/plugin.go` — hidden `plugin runtime tool` entrypoint
- `internal/agent/plugin_entrypoint_test.go` — test helper for built binary
- `internal/agent/runner/plugin_entrypoint_test.go` — test helper for runner
  package
- `internal/agent/tool/plugin_tool.go` — subprocess-backed tool adapter
- `internal/agent/tool/tool.go` — registry now wires built-in tools through the
  plugin adapter
- `internal/agent/runner/gorunner.go` — closes plugin-backed tools on shutdown
- `cmd/anna/plugin_runtime_test.go` — end-to-end tool migration coverage
- `internal/pluginhost/catalog_test.go` — tool manifest validation updates
- `internal/pluginhost/client_test.go` — builtin tool handshake test updates

**Commits:**

- `47c34a9` — `✨ feat: add subprocess tool runtime bridge`
- `ded0fff` — `✨ feat: run built-in tools through plugins`
- `da97d2d` — `🧪 test: cover plugin-backed tool migration`

**Decisions & context for next phase:**

- The tool path is now fully going through the subprocess protocol for the
  built-in tool set.
- `@anna` is the preferred bridge for first-party plugin packaging; there is no
  second packaging format.
- JS plugins remain untouched and continue to work alongside the new runtime.
- Phase 3 can now focus on channel plugin contracts and channel loading.
