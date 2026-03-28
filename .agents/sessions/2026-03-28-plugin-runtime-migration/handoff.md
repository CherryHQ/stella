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
- Fixed the helper fallback path to preserve the executable suffix from the
  current binary, so Windows installs resolve `anna-plugin.exe` correctly.

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

## Phase 3: Channel Plugin Migration

**Status:** complete

**Tasks completed:**

- Added a channel subprocess protocol contract for start, stop, notify, and
  inbound event semantics in `pluginapi` and `pluginhost`.
- Added a restartable host-side channel adapter that supervises plugin
  processes and retries after crashes instead of taking down the daemon.
- Replaced the hard-coded Telegram/QQ/Feishu/Weixin constructors in the main
  gateway with catalog-driven loading and bundled channel plugin definitions.
- Added a built-in `anna-plugin channel <name>` helper path that boots each
  migrated channel as a subprocess plugin while preserving the existing DB
  config and auth wiring.
- Added focused restart coverage for the channel plugin adapter.

**Files changed:**

- `internal/pluginapi/types.go` — channel plugin wire types and capabilities
- `internal/pluginhost/server.go` — channel protocol server loop
- `internal/pluginhost/channeladapter.go` — restartable host adapter
- `internal/pluginhost/catalog.go` — catalog merge support for bundled defs
- `internal/channel/config.go` — shared channel config JSON types
- `internal/channel/plugin_runtime.go` — bundled channel manifest definitions
- `cmd/anna-plugin/channel.go` — channel plugin bootstrapper
- `cmd/anna-plugin/main.go` — channel helper command registration
- `cmd/anna/channel_plugins.go` — catalog-driven channel loading helpers
- `cmd/anna/gateway.go` — removed hard-coded channel construction
- `internal/pluginhost/channeladapter_test.go` — channel restart coverage

**Commits:**

- `61e7c05` — `✨ feat: add channel plugin runtime contract`
- `cee64f2` — `✨ feat: load channels from plugin catalog`
- `6fd8499` — `🧪 test: cover channel plugin restart handling`

**Decisions & context for next phase:**

- Bundled first-party channels are now launched through the same subprocess
  helper path as future third-party channel plugins.
- Channel config JSON remains unchanged and is still read from the DB.
- The host now treats a channel process crash as a restartable failure instead
  of a daemon-level failure.
- Phase 4 can now focus on plugin binding config, JS compatibility, operator
  visibility, and docs.

### Fixes

- Switched admin/channel link-code storage from per-process in-memory state to
  a shared DB-backed store, preserving account-linking flows after the channel
  subprocess migration.
- Updated both the host gateway and channel helper runtime to use the shared
  link-code store so `/api/auth/profile/link-code` and `/link <code>` operate
  across process boundaries.
- Added regression coverage that opens two DB handles against the same Anna DB
  and verifies one store instance can generate a code that another instance
  consumes.

## Phase 4: Integration, Compatibility, and Docs

**Status:** complete

**Tasks completed:**

- Added a separate `runtime_plugins` setting so subprocess tool and channel
  bindings can change independently from the legacy JS plugin list.
- Routed the Go runner and channel gateway through runtime binding resolution,
  which now makes built-in tool/channel slots replaceable by plugin ID.
- Added `anna plugin runtime list` and `anna plugin runtime bind` to expose
  effective bindings, enabled channel state, and log location to operators.
- Preserved JS plugin compatibility by keeping `settings.plugins` untouched and
  documenting the new boundary between JS and subprocess plugins.
- Added binding coverage for tool replacement and channel binding resolution,
  then ran the full Go test suite and release config validation.

**Files changed:**

- `internal/config/runtime_plugins.go` — runtime binding setting types and
  load/save helpers
- `internal/config/snapshot.go` — runtime binding snapshot field
- `internal/config/dbstore.go` — snapshot loading for `runtime_plugins`
- `internal/agent/tool/tool.go` — runtime tool catalog loading and binding
  resolution
- `internal/agent/tool/plugin_runtime.go` — bundled tool definition helpers
- `internal/agent/factory.go` — runner config now carries runtime bindings
- `internal/agent/runner/gorunner.go` — tool registry now respects runtime
  bindings
- `internal/channel/plugin_runtime.go` — bundled channel definition helpers
- `cmd/anna/channel_plugins.go` — channel binding resolution helper
- `cmd/anna/gateway.go` — gateway now resolves channel plugins via bindings
- `cmd/anna/plugin_runtime_cli.go` — runtime plugin binding and status CLI
- `cmd/anna/plugin.go` — JS plugin command wording kept distinct from runtime
  plugin management
- `internal/agent/tool/runtime_bindings_test.go` — replacement-path coverage
- `cmd/anna/channel_plugins_test.go` — channel binding coverage
- `internal/config/dbstore_test.go` — JS/runtime setting compatibility coverage
- `README.md` — runtime plugin CLI and feature summary updates
- `docs/content/docs/features/plugin-system.md` — JS vs subprocess plugin docs
- `docs/content/docs/getting-started/configuration.md` — `runtime_plugins`
  setting and plugin directory layout

**Commits:**

- `c393ba8` — `✨ feat: add runtime plugin bindings`
- `61a5c76` — `✨ feat: add runtime plugin runtime CLI`
- `1a73cce` — `🧪 test: cover runtime plugin bindings`

**Decisions & context for next phase:**

- The migration slice is complete for tools and channels.
- JS plugins remain the extension path for hooks and lightweight custom tools.
- Runtime bindings are now the control point for swapping subprocess-backed
  tool and channel implementations.
- Providers and memory remain deferred to later plugin phases.

### Verification

- `mise x -- go test ./...`
- `mise run release:check`
