# Handoff

## Goal

Use `extension-design.md` as the target architecture and prepare the repository for implementation of the final repo-scoped plugin system:

- one ownership unit with multiple contributions per plugin
- `plugins/...` can remain the ownership boundary
- `pkg/...` as the only stable import surface for plugin packages
- `internal/pluginhost` remains orchestration only
- no plugin package importing `internal/...` in the final state

This handoff file is also the running implementation log for future sessions.

## Progress

- Reviewed the final design in `extension-design.md` and treated it as the authoritative target.
- Inspected the current extension-related surfaces:
  - `pkg/plugins/*`
  - `internal/pluginhost/*`
  - `plugins/channels/telegram/*`
  - `plugins/tools/mcp/*`
  - `cmd/anna/plugins_imports.go`
- Identified the main migration seams:
  - the current model is already close to “one ownership unit, many capabilities”, but naming and packaging are still `plugin`-centric
  - builders are still split between host-native behavior and legacy registries
  - some repo extensions still import `internal/...`
  - persistence and discovery still assume plugin IDs and plugin rows
- Implemented the first real migration slice without renaming packages:
  - `internal/pluginhost.RegisterLegacyCapabilities` now registers actual tool/hook/provider/memory build functions into the host instead of metadata-only placeholders
  - `internal/pluginhost.BuildEnabledTools` now builds from host registrations only
  - `internal/pluginhost.BuildEnabledHooks` now builds from host registrations only
  - `internal/pluginhost.BuildProvider` now prefers host registrations and falls back to the legacy registry only if the host was not primed
  - `internal/pluginhost.BuildMemory` now prefers host registrations and falls back to the legacy registry only if the host was not primed
  - `pkg/plugins.ToolRegistration` gained `Required` so the host can distinguish optional tool plugins from core tools
  - `pkg/plugins.MemoryContext` gained `DB`, `AnnaHome`, and `SummarizerFn` so memory builds can move through the host contract
- Added focused tests that verify legacy tool/hook/provider/memory registrations are buildable through `internal/pluginhost`.
- Ran `go test ./...` successfully after the changes.
- Removed the remaining provider and memory builder fallback to legacy registries:
  - `internal/pluginhost.BuildProvider(...)` now requires a host registration
  - `internal/pluginhost.BuildMemory(...)` now requires a host registration
- Added regression tests that prove unprimed hosts do not silently fall back to legacy provider/memory registries.
- Removed the remaining MCP legacy ID alias path:
  - MCP plugin registration is now canonical as `tool/mcp`
  - `internal/pluginhost` no longer translates plugin IDs through `RegisterLegacyID`, `resolvePluginID`, or config alias inference
  - backend callers now use canonical plugin IDs directly
- Introduced the first schema-driven config slice:
  - `pkg/plugins.ConfigRegistration` now carries schema data
  - `internal/pluginhost` can return config schemas by plugin ID
  - admin now exposes plugin config schema through a dedicated backend endpoint
  - concrete schemas are registered for MCP and Telegram
- Extracted the Telegram plugin config type into `pkg/channel`, so Telegram plugin code no longer depends on an app-private config struct for its persisted config shape.
- Extracted shared channel plugin config decode/clone helpers into `pkg/channel`, so Telegram config handling no longer imports `internal/channel` just to parse or redact persisted config maps.
- Extracted the generic managed channel bot runtime into `pkg/channelruntime`, so Telegram runtime wiring no longer depends on `internal/channel`.
- Extracted the MCP plugin config model and decoder into `pkg/mcp`, so MCP plugin config handling no longer depends on an app-private config package.
- Extracted MCP tool/status/result data types into `pkg/mcp`, so plugin-facing MCP data no longer depends on app-private type definitions.
- Moved the MCP manager/session/supervisor/canonical-ID runtime into `pkg/mcp`, so MCP plugin runtime and tool code no longer depend on `internal/mcp`.
- Moved the generated SQLC package into `pkg/db/sqlc`, so the memory plugins no longer depend on `internal/db/sqlc`.
- Switched production provider construction to `pluginhost`, so runner/admin/models/reflect no longer build providers through `plugins/providers` directly.
- Switched production core-tool construction to `pluginhost`, so runner/pool/model-switcher no longer build required tools through `plugintools.BuildCore(...)` directly.
- Converted the remaining built-in tools, hooks, providers, and memory implementations to register directly in `pkg/plugins`, so `LoadDefaultCatalog()` now discovers the whole built-in repo plugin set without any legacy mirroring step.
- Removed the legacy host adapter layer and deleted the dead root provider/memory registries; the remaining root `plugins/tools` and `plugins/hooks` packages are now just narrow shared helper surfaces, not parallel registration systems.
- Removed the remaining admin-side pluginhost compatibility branches:
  - admin plugin config/status endpoints now require a plugin host
  - admin server construction now panics if `pluginHost` is nil

## Key Decisions

- V clarified that package and concept names can stay `plugin`-centric for now. Do not spend migration budget on `extension` renames unless there is a concrete payoff.
- Scope is repo-only. Do not design for external/out-of-process extensions now.
- Keep the orchestration layer internal. `internal/pluginhost` remains the right package for app-private composition.
- The important migration is behavioral, not nominal:
  - one host
  - one ownership unit per plugin
  - multiple contributions per plugin
  - host-driven contribution discovery
- The core architectural rule is still strict: plugin packages should import only `pkg/...`; if a plugin needs a reusable helper, move that helper out of `internal/...`.
- Replace split builder paths before attempting bigger packaging or persistence refactors.
- No fallback code and no compatibility code. If a path is obsolete, remove it instead of translating through it.
- The schema phase is backend-first. Expose schema data through host/admin contracts first; UI rendering can consume it later.
- Admin plugin operations are now host-required, not dual-pathed through direct store mutations.

## Files Changed

- `extension-design.md` — rewritten into the final repo-scoped extension-system design.
- `handoff.md` — created as the implementation handoff and running log file.
- `internal/pluginhost/builders.go` — enabled tools/hooks/providers/memory and required core tools all resolve directly through host registrations.
- `pkg/plugins/capabilities.go` — `ToolRegistration` now carries `Required`.
- `pkg/plugins/context.go` — `MemoryContext` now carries the inputs needed to construct memory providers through host-owned registrations.

## Current State

The migration target from `extension-design.md` is now implemented for the in-repo plugin system:

- `pkg/plugins` already models “one owner, many capabilities”, but the API surface still uses plugin terminology and plugin-specific state types.
- `internal/pluginhost` already does the right class of work:
  - catalog loading
  - registration validation
  - runtime orchestration
  - discovery
  - config/state bridging
- `internal/pluginhost` is now the single contribution source for built-in tools, hooks, providers, channels, runtimes, and memory through `LoadDefaultCatalog()` alone.
- MCP now uses the same canonical plugin ID in runtime registration, persistence, and backend callers: `tool/mcp`.
- Config schemas now exist as host-readable data for the plugins that have been wired so far, instead of living only as Go validation callbacks.
- Telegram config is now a public package contract in `pkg/channel`, not an app-private type in `internal/channel`.
- Shared channel config decode/clone helpers are now public package contracts in `pkg/channel`, and the old `internal/channel` wrapper layer has been removed.
- Shared managed channel runtime orchestration now lives in `pkg/channelruntime`, which is the stable package boundary for code that depends on both `pkg/channel` and `pkg/plugins`.
- MCP config is now a public package contract in `pkg/mcp`, not an app-private type in `internal/mcp`.
- MCP tool metadata, execution result shape, and server status are now public package contracts in `pkg/mcp`.
- MCP runtime behavior now also lives in `pkg/mcp`: manager lifecycle, MCP session dialing, server supervision, and canonical tool ID handling are no longer app-private.
- Shared database query contracts now live in `pkg/db/sqlc`, not `internal/db/sqlc`.
- Production provider construction now flows through `internal/pluginhost`:
  - runner provider registries
  - admin provider validation/model fetch
  - CLI model cache refresh
  - reflect review provider setup
- Production memory construction now flows through `internal/pluginhost` without any registry adapter step.
- Production core and optional tool construction both flow through `internal/pluginhost`.
- The old split registries are gone as runtime/discovery systems:
  - `plugins/providers`
  - `plugins/memory`
  - the old registration logic from `plugins/tools`
  - the old registration logic from `plugins/hooks`
- Admin plugin config/status/toggle flows now assume one plugin host exists, matching the actual application wiring.
- `plugins/tools/mcp` production code now imports only `pkg/...`.
- `plugins/channels/telegram` production code now imports only `pkg/...`; remaining `internal/channel` imports in that package are test-only.
- `plugins/memory/lcm` and `plugins/memory/simple` production code now import `pkg/db/sqlc`; there are no remaining production `plugins/...` imports of `internal/...`.
- `cmd/anna/plugins_imports.go` is still the blank-import bootstrap for built-ins. That is acceptable for repo-scoped plugins.

## Implementation Plan

### Phase 1: Finish host unification

1. Remove the remaining builder fallback dependence on legacy registries where possible.
2. Decide whether required tools should also be expressed through `pkg/plugins.ToolRegistration` or remain intentionally separate.
3. Make new contribution work land in host registrations first, with legacy registries only as compatibility input.

### Phase 2: Normalize identity and persistence

1. Normalize mixed IDs deliberately:
   - `tool/mcp`
   - `channel/telegram`
   - `reflect`
2. Decide and document one compatibility rule for persisted rows:
   - keep existing `settings_plugins` table temporarily
   - treat it as plugin state storage during migration
3. Remove built-in plugin name lists as a source of truth where the host can infer or register them.

### Phase 3: Make config schema-driven

1. Expand schema coverage under `pkg/plugins` so host-owned config handling does not depend on bespoke knowledge.
2. Migrate current per-plugin config registrations so the host can render/edit config consistently.
3. Preserve typed decode helpers inside plugins, but make the host contract schema-driven.
4. Replace ad hoc admin branching with host-driven config/status behavior where possible.

### Phase 4: Eliminate plugin imports of `internal/...`

1. Audit every current `plugins/...` package for `internal/...` imports.
2. For each import, decide:
   - move reusable logic to `pkg/...`
   - duplicate locally if the abstraction is not yet stable
   - keep remaining app-only orchestration in `internal/...`
3. Highest-priority migrations:
   - `plugins/channels/telegram`
   - `plugins/tools/mcp`
   - any remaining channel/runtime slices
4. The final rule is strict: plugin packages must import only `pkg/...`.

### Phase 5: Verification and cleanup

1. Remove split builders that still depend on:
   - `plugins/tools/registry.go`
   - `plugins/hooks/registry.go`
   - `plugins/providers/registry.go`
   - `plugins/memory/registry.go`
2. Make `internal/pluginhost` the single source of contribution discovery.
3. Ensure adding a new tool/provider/hook/memory implementation only requires:
   - new `plugins/{kind}/{name}` package
   - contribution registration there
   - one blank import in bootstrap
4. Run format, lint, and tests.
5. Verify the following invariants:
   - all repo plugins load through one host
   - all repo plugins use the same manifest/config/status/lifecycle model
   - no plugin package imports `internal/...`
   - adding a new plugin of an existing type only requires one package plus one blank import

## Blockers / Gotchas

- The worktree warning from the earlier planning session was stale. In this session the relevant tracked changes were only the host unification edits listed above plus untracked design/handoff files.
- The current code still has mixed identity shapes:
  - `tool/mcp`
  - `channel/telegram`
  - `reflect`
  This must be normalized deliberately during migration.
- Schema coverage is still partial. MCP and Telegram are wired; other managed plugins still rely on validate/redact callbacks without schema data.
- Telegram production code no longer depends on `internal/channel`; runtime orchestration now uses `pkg/channelruntime` plus the public notification registry contract in `pkg/plugins`.
- The migration goal is complete:
  - production `plugins/...` packages import only `pkg/...`
  - all built-in tool/hook/provider/memory contributions resolve through `internal/pluginhost`
  - no provider/memory fallback path remains
  - no legacy registration mirroring step remains
- Admin construction now hard-requires `pluginhost`; the old nil-host compatibility path is gone.
- `Go init()` blank-import registration is still acceptable for repo-level built-ins, but it should not remain the only discovery logic in the design language.

## Next Steps

The migration is done, but the cleanup is not. The next mandate is to clean the remaining app-private orchestration packages until they are small, obvious, and boring:

1. Clean `internal/reflect` first.
   - Split review orchestration, config/runtime wiring, provider setup, and persistence-facing logic into narrower units.
   - Keep only app-private composition in `internal/reflect`; move reusable data/contracts out if they are stable.
2. Clean `internal/channel` next.
   - Separate channel lifecycle, notification dispatch, identity/linking, CLI wiring, and host-backed runtime adapters.
   - Reduce the package so it is no longer a broad “everything channel-related” bucket.
3. Clean adjacent orchestration packages after that:
   - `internal/admin`
   - `internal/scheduler`
   - `cmd/anna`
4. Revisit the blank-import bootstrap only after the internal cleanup work stops uncovering structural changes.

Rules for the cleanup phase:

- No fallback code.
- No compatibility layers just to preserve old shapes.
- Prefer splitting large packages into smaller internal packages over adding more files to the same broad package.
- If a helper is stable and reused across package boundaries, move it to `pkg/...`; otherwise keep it internal and local.
- Each cleanup slice should be a coherent phase with tests green and its own commit.

Suggested cleanup order after the current schema/admin work:

1. `internal/reflect`
2. `internal/channel`
3. scheduler/notification plumbing
4. `internal/admin`
5. `cmd/anna`

Success condition for this phase:

- fewer cross-package responsibilities hidden inside broad `internal/...` packages
- smaller package APIs
- less wiring logic mixed with domain behavior
- no new architecture debt introduced while cleaning code shape

### 2026-04-08 — channel config helper extraction

- Moved generic channel plugin config decode/clone helpers from `internal/channel` into `pkg/channel`.
- Updated Telegram config handling to use `pkg/channel.DecodePluginConfig(...)` and `pkg/channel.CloneConfigMap(...)` directly.
- Updated the internal QQ, Feishu, and Weixin managed runtime config helpers to use the same public helper functions, so there is only one channel config decode path.
- Removed the dead `internal/channel` wrapper exports and deleted `internal/channel/plugin_runtime_config.go`.
- Ran focused tests for `pkg/channel`, `internal/channel`, `plugins/channels/telegram`, `internal/admin`, and `cmd/anna`, then ran `go test ./...` successfully.

### 2026-04-08 — Telegram runtime extraction

- Moved the generic managed channel bot runtime out of `internal/channel` into the new public package `pkg/channelruntime`.
- Updated Telegram managed runtime wiring to use `pkg/channelruntime` plus the public `pkg/plugins.NotificationRegistry` interface instead of `internal/channel.Dispatcher`.
- Updated internal QQ, Feishu, and Weixin managed runtimes to use the same public runtime helper, then removed `internal/channel/bot_runtime.go`.
- Updated downstream tests and host wiring to the new Telegram runtime dependency shape.
- Verified that `plugins/channels/telegram` no longer imports `internal/channel` in production code.
- Ran focused tests for `pkg/channel`, `pkg/channelruntime`, `internal/channel`, `plugins/channels/telegram`, `internal/pluginhost`, and `cmd/anna`, then ran `go test ./...` successfully.

### 2026-04-08 — MCP runtime extraction

- Moved the MCP manager runtime from `internal/mcp` into `pkg/mcp`, including session dialing, supervisor lifecycle, and canonical tool ID handling.
- Updated `plugins/tools/mcp` to construct and consume `pkg/mcp.Manager` directly for both the managed runtime and the proxy tool.
- Updated CLI and admin test callers to use `pkg/mcp.PluginID` and `pkg/mcp` transport/status types directly.
- Removed the remaining `internal/mcp` package files after moving their live behavior and tests into `pkg/mcp`.

### 2026-04-08 — Core tool host routing

- Added `internal/pluginhost.BuildCoreTools(...)` so required tool construction now goes through host registrations just like optional tools.
- Updated `internal/pluginhost.RegisterLegacyCapabilities(...)` to mirror required legacy tool registrations into the host instead of skipping them.
- Added explicit `CoreToolsBuilder` injection to the Go runner, runner factory, pool manager, setup path, and model switcher so production runner creation no longer calls `plugintools.BuildCore(...)`.
- Updated runner/agent/CLI tests to provide explicit core-tool builders and verified the focused suites plus `go test ./...`.
- Verified that `plugins/tools/mcp` no longer imports `internal/mcp` in production code.
- Ran focused tests for `pkg/mcp`, `plugins/tools/mcp`, `cmd/anna`, and `internal/admin`, then ran `go test ./...` successfully.

### 2026-04-08 — SQLC package extraction

- Moved the generated SQLC package from `internal/db/sqlc` to `pkg/db/sqlc`.
- Updated the memory plugins and all internal callers to use the new public import path.
- Updated `sqlc.yaml` so regeneration now targets `pkg/db/sqlc`.
- Verified that there are no remaining production `plugins/...` imports of `internal/...`.
- Ran focused tests for `plugins/memory/...`, `internal/config`, `internal/db`, `internal/admin`, `internal/reflect`, and `internal/scheduler`, then ran `go test ./...` successfully.

### 2026-04-08 — provider host unification

- Added host-side provider builders for both single adapters and one-provider registries.
- Switched the Go runner factory path to require an explicit provider-registry builder and wired production callers to `pluginhost`.
- Switched admin provider model fetch, CLI provider model cache refresh, and reflect review provider setup to build through `pluginhost` instead of `plugins/providers`.
- Added host-side provider type discovery so admin provider-type listing no longer reads the legacy provider registry directly.
- Verified that production code outside `internal/pluginhost/adapters.go` no longer calls `pluginproviders.Build(...)` or `pluginproviders.BuildRegistry(...)`.
- Ran focused tests for `internal/agent/runner`, `internal/agent`, `internal/admin`, `internal/reflect`, `cmd/anna`, and `internal/pluginhost`, then ran `go test ./...` successfully.

### 2026-04-08 — final built-in catalog unification

- Converted the remaining built-in tool, hook, provider, and memory packages to register themselves directly with `pkg/plugins`.
- Removed `internal/pluginhost.RegisterLegacyCapabilities(...)` and deleted the host adapter layer entirely.
- Removed the dead root provider and memory registry packages, and reduced the root tool/hook packages to the narrow helper APIs still used in production.
- Updated production setup and model-host construction so `LoadDefaultCatalog()` is the only built-in discovery step.
- Rewired tests that used the deleted registries to use direct constructors instead.
- Verified the cleanup with focused suites, invariant searches, and `go test ./...`.

### 2026-04-08 — metadata and schema normalization

- Added explicit metadata for the remaining managed built-ins that were still relying on inferred rows:
  - `tool/mcp`
  - `channel/qq`
  - `channel/feishu`
  - `channel/weixin`
  - `reflect`
- Extended the managed-runtime registration helper so host-backed plugins can register config schema together with config/status/runtime behavior.
- Added concrete config schemas for QQ, Feishu, Weixin, and Reflect.
- Added regression coverage proving host-backed managed plugins now surface metadata and schema through `pluginhost`, and that MCP now exposes explicit metadata from the default catalog.
- Verified with focused tests for `internal/pluginhost`, `internal/admin`, `internal/channel`, `internal/reflect`, and `plugins/tools/mcp`.

### 2026-04-08 — admin plugin discovery normalization

- Changed `GET /api/plugins` to read from `pluginhost.ListAdminVisiblePlugins(...)` instead of the raw store list.
- Kept the response flat for the existing UI, but now include normalized metadata fields:
  - `display_name`
  - `managed`
  - `admin_visible`
  - `has_config`
  - `has_status`
  - `capabilities`
  - `supports_notifications`
  - `persisted`
  - `persisted_id`
- Plugin configs returned by the list endpoint now flow through `pluginhost.RedactConfig(...)`, so channel/tool secrets are not leaked through the plugin list payload.
- Added admin regression coverage for metadata exposure and config redaction in the plugin list endpoint.
- Verified with focused tests for `internal/admin` and `internal/pluginhost`.

### 2026-04-08 — plugin description normalization

- Added `Description` to `pkg/plugins.PluginMeta` so plugin discovery can carry user-facing summaries without a second frontend-side lookup table.
- Filled in explicit descriptions for the built-in tools, hooks, providers, memory implementations, Telegram, MCP, and the host-backed QQ, Feishu, Weixin, and Reflect registrations.
- Extended the admin plugin list payload to include `description` directly from host discovery metadata.
- Added admin regression coverage proving the plugin list endpoint now returns normalized descriptions alongside redacted config.

### 2026-04-08 — admin plugin metadata UI normalization

- Updated the admin plugins page to render host-backed metadata directly:
  - `display_name` for labels
  - `id` for the canonical identifier
  - `description` for user-facing summaries
  - metadata badges for managed/config/status/notification capability
- Removed the hardcoded plugin description map from the frontend JavaScript.
- Added schema-backed config field summaries to plugin rows by loading `/api/plugin-config-schema/{kind}/{name}` for plugins that expose config.
- Kept the specialized MCP editor intact, but moved its section guard to the canonical plugin ID instead of name matching.
- Verified the combined backend and admin UI normalization with `go test ./...`.

### 2026-04-08 — raw plugin config endpoint

- Added `GET /api/plugin-config/{kind}/{name}` as an admin-only raw config read path backed by `pluginhost.Config().Get(...)`.
- Kept `/api/plugins` redacted for discovery/list rendering, while making config editors able to fetch non-redacted values on demand.
- Added admin regression coverage proving raw plugin config reads return the stored config instead of the redacted plugin-list view.

### 2026-04-08 — schema-driven plugin config editor

- Extended the admin plugins page with a generic schema-driven config editor for current non-MCP configurable plugins.
- The page now:
  - loads raw config only when an editor is opened
  - maps schema fields to enums, booleans, scalar inputs, or JSON textareas for object/array fields
  - saves through `PUT /api/plugin-config/{kind}/{name}` using the host-owned validation/apply path
- Kept the existing MCP editor as the specialized path for `tool/mcp`; generic schema editing now covers the channel plugins and Reflect.
- Verified the combined backend and UI behavior with another full `go test ./...` pass.

### 2026-04-08 — reflect service responsibility split

- Started the post-migration cleanup mandate in `internal/reflect`.
- Split the broad `service.go` file by responsibility:
  - `loop.go` now owns review loop and agent-cycle orchestration
  - `candidates.go` now owns unreviewed-session selection and ordering
  - `conversation_review.go` now owns single-conversation review execution, provider setup, reviewer construction, and notification dispatch
- Left package behavior unchanged while shrinking the main service file down to dependency/config ownership.
- Verified with focused tests for `internal/reflect`, `internal/pluginhost`, `internal/admin`, and `cmd/anna`, then ran `go test ./...`.

### 2026-04-08 — reflect runtime/config split

- Continued the `internal/reflect` cleanup by separating plugin config/schema concerns from managed runtime lifecycle.
- Added:
  - `plugin_config.go` for plugin constants, schema/defaults, decode helpers, and config redaction
  - `managed_runtime.go` for runtime dependency wiring, lifecycle, and runtime snapshots
- Removed the old mixed `plugin_runtime.go` shape.
- Verified with focused tests for `internal/reflect`, `internal/pluginhost`, `internal/admin`, and `cmd/anna`, then ran `go test ./...`.

## Session Log

### 2026-04-08 — planning session

- Read `extension-design.md` and confirmed it is now scoped to a repo-level extension system.
- Inspected current host and extension surfaces:
  - `pkg/plugins/*`
  - `internal/pluginhost/*`
  - `plugins/channels/telegram/*`
  - `plugins/tools/mcp/*`
  - `cmd/anna/plugins_imports.go`
- Confirmed the main architectural gaps:
  - extension packages still import `internal/...`
  - contribution loading still mixes host-native and legacy registry paths
  - naming and persistence are still plugin-centric
- Wrote the phased migration plan in this file.
- Did not modify implementation code in this session.

### 2026-04-08 — host unification slice

- V clarified that the repo can keep `plugin` naming. Migration effort should go into behavior, not renames.
- Updated `internal/pluginhost.RegisterLegacyCapabilities(...)` so legacy tool/hook/provider/memory registries now feed real build functions into host registrations.
- Updated host builders so optional tools and hooks now build strictly from host registrations after setup.
- Updated provider and memory builders so they prefer host registrations and only fall back to legacy registries when the host has not been primed.
- Extended `pkg/plugins` contracts just enough to support that:
  - `ToolRegistration.Required`
  - `MemoryContext.DB`
  - `MemoryContext.AnnaHome`
  - `MemoryContext.SummarizerFn`
- Added `internal/pluginhost/legacy_builders_test.go` covering legacy tool/hook/provider/memory construction through the host.
- Verified the entire repository with `go test ./...`.
- Safe to remove next:
  - any dead assumptions that host registrations are metadata-only for legacy contributions

### 2026-04-08 — no-fallback provider and memory builds

- Removed the remaining provider and memory fallback path from `internal/pluginhost/builders.go`.
- `BuildProvider(...)` now resolves only through host registrations and returns `providers.ErrProviderNotFound` when the host has not been primed.
- `BuildMemory(...)` now resolves only through host registrations and returns `nil` when the host has not been primed.
- Added regression tests proving the host no longer silently consults legacy provider/memory registries.
- Verified the entire repository with `go test ./...`.
- Safe to remove next:
  - remaining mixed-ID assumptions after MCP identity is made canonical

### 2026-04-08 — canonical MCP plugin identity

- Changed the self-registered MCP plugin ID from `mcp` to `tool/mcp` so it matches persisted state and backend routes.
- Removed `RegisterLegacyID(...)`, `resolvePluginID(...)`, and config alias inference from `internal/pluginhost`.
- Updated CLI and backend callers to use canonical plugin IDs directly instead of relying on host translation.
- Left the admin UI unchanged because it already addresses MCP as `tool/mcp`.
- Verified the entire repository with `go test ./...`.
- Safe to remove next:
  - remaining code paths that still rely on mixed standalone IDs like `reflect`

### 2026-04-08 — schema-backed plugin config, first slice

- Extended `pkg/plugins.ConfigRegistration` with schema data and a defensive schema clone helper.
- Added `pluginhost.ConfigSchema(pluginID)` so the host can expose config shape as data.
- Added `GET /api/plugin-config-schema/{kind}/{name}` in admin as the first backend read path for schema-driven config.
- Registered concrete schemas for:
  - `tool/mcp`
  - `channel/telegram`
- Added regression tests for:
  - deep-copying schema definitions
  - host schema lookup by plugin ID
  - admin schema endpoints for MCP and Telegram
- Verification is in progress for the full repository test suite.
- Safe to remove next:
  - plugin-specific schema knowledge in backend handlers once remaining plugins are described through `ConfigRegistration.Schema`

### 2026-04-08 — first pkg/channel extraction for Telegram

- Added `pkg/channel.TelegramConfig` as the public persisted config contract for Telegram.
- Replaced the app-private `internal/channel.TelegramConfig` definition with a type alias to the new public contract.
- Updated Telegram plugin code and tests to use `pkg/channel.TelegramConfig` where only the config shape is needed.
- This does not remove Telegram's runtime dependency on `internal/channel`; it only moves the persisted config type to a stable public package.
- Verification is in progress for the full repository test suite.
- Safe to remove next:
  - the next Telegram-facing config/runtime helper in `internal/channel` that can become a stable `pkg/channel` contract

### 2026-04-08 — first pkg/mcp extraction

- Added `pkg/mcp` with the MCP config model, server config model, transport constants, timeout default, and config decoder.
- Replaced the old `internal/mcp/config.go` implementation with public-type aliases and a thin wrapper to the new package.
- Updated the MCP plugin to use `pkg/mcp` for config validation and schema constants.
- This does not remove the runtime dependency on `internal/mcp.Manager`; it only moves the config model/decoder into a stable public package.
- Verification is in progress for the full repository test suite.
- Safe to remove next:
  - the next MCP-facing type in `internal/mcp` that is pure data rather than runtime orchestration

### 2026-04-08 — pkg/mcp data types

- Added `pkg/mcp.ToolInfo`, `pkg/mcp.ExecResult`, and `pkg/mcp.ServerStatus`.
- Replaced the old `internal/mcp` definitions with public-type aliases so manager internals stay intact while plugin-facing code can depend on `pkg/mcp`.
- Updated the MCP plugin package to use `pkg/mcp` data types where only data shapes are needed.
- This does not remove the runtime dependency on `internal/mcp.Manager`; it only moves pure data contracts out to `pkg/mcp`.
- Verification is in progress for the full repository test suite.
- Safe to remove next:
  - admin/pluginhost branches that still pretend plugin host setup is optional for plugin config/status operations

### 2026-04-08 — admin pluginhost is mandatory

- Removed the remaining nil-`pluginHost` branches from admin plugin config/status/toggle/schema endpoints.
- `internal/admin.New(...)` now panics if `pluginHost` is nil, making the invariant explicit instead of silently supporting a compatibility path that the app no longer uses.
- Added a regression test covering the constructor invariant.
- Verification is in progress for the full repository test suite.
- Safe to remove next:
  - any remaining code that treats plugin operations as valid without `pluginhost`
