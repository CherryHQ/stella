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
- Extracted the MCP plugin config model and decoder into `pkg/mcp`, so MCP plugin config handling no longer depends on an app-private config package.
- Extracted MCP tool/status/result data types into `pkg/mcp`, so plugin-facing MCP data no longer depends on app-private type definitions.
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
- `internal/pluginhost/adapters.go` — legacy tool/hook/provider/memory registries now register real host build functions instead of host-visible placeholders only.
- `internal/pluginhost/builders.go` — enabled tools/hooks/providers/memory now resolve through host registrations first; optional legacy fallback remains only when the host was not primed.
- `pkg/plugins/capabilities.go` — `ToolRegistration` now carries `Required`.
- `pkg/plugins/context.go` — `MemoryContext` now carries the inputs needed to construct memory providers through host-owned registrations.
- `internal/pluginhost/legacy_builders_test.go` — added regression tests covering host-driven builds for legacy tool/hook/provider/memory registrations.

## Current State

The repository already has a useful base, but it is still transitional:

- `pkg/plugins` already models “one owner, many capabilities”, but the API surface still uses plugin terminology and plugin-specific state types.
- `internal/pluginhost` already does the right class of work:
  - catalog loading
  - registration validation
  - runtime orchestration
  - discovery
  - config/state bridging
- `internal/pluginhost` is now the single contribution source for optional tools, hooks, providers, and memory once `RegisterLegacyCapabilities(...)` has been called during setup.
- MCP now uses the same canonical plugin ID in runtime registration, persistence, and backend callers: `tool/mcp`.
- Config schemas now exist as host-readable data for the plugins that have been wired so far, instead of living only as Go validation callbacks.
- Telegram config is now a public package contract in `pkg/channel`, not an app-private type in `internal/channel`.
- Shared channel config decode/clone helpers are now public package contracts in `pkg/channel`, and the old `internal/channel` wrapper layer has been removed.
- MCP config is now a public package contract in `pkg/mcp`, not an app-private type in `internal/mcp`.
- MCP tool metadata, execution result shape, and server status are now public package contracts in `pkg/mcp`.
- Admin plugin config/status/toggle flows now assume one plugin host exists, matching the actual application wiring.
- `plugins/tools/mcp` is the best current reference for a multi-capability unit, but it still depends on `internal/mcp`.
- `plugins/channels/telegram` has started moving ownership into the package, but it still imports `internal/channel` types and runtime helpers.
- `plugins/channels/telegram/config.go` now imports only `pkg/channel`; the remaining `internal/channel` imports are runtime-oriented.
- Core tools are still separate because `plugins/tools/registry.go` owns the required-tool boot path used by the Go runner.
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
- Required tools are still built through `plugintools.BuildCore(...)`. That path was not changed in this slice.
- Schema coverage is still partial. MCP and Telegram are wired; other managed plugins still rely on validate/redact callbacks without schema data.
- Telegram still depends on `internal/channel` for runtime orchestration and dispatcher integration; only the config type moved out in this slice.
- Telegram still depends on `internal/channel` for runtime orchestration and dispatcher integration; config map decode/redaction helpers have already been moved out.
- MCP still depends on `internal/mcp` for the manager/runtime implementation and status types; only the config model and decoder moved out in this slice.
- MCP still depends on `internal/mcp` for the manager/runtime implementation; the extracted `pkg/mcp` types are data contracts only.
- Admin construction now hard-requires `pluginhost`; the old nil-host compatibility path is gone.
- `Go init()` blank-import registration is still acceptable for repo-level built-ins, but it should not remain the only discovery logic in the design language.

## Next Steps

1. Decide whether core tools should be lifted into `pkg/plugins.ToolRegistration` or intentionally remain outside the plugin host.
2. Audit `plugins/tools/mcp` and `plugins/channels/telegram` for `internal/...` imports and pick the next concrete extraction into `pkg/...`.
3. Normalize persisted plugin IDs and host discovery rules so built-ins and persisted rows use one deliberate identity model.
4. Expand schema coverage to the remaining managed plugins and start removing ad hoc admin/plugin-specific config logic where the schema is now sufficient.
5. Continue removing mixed-ID special casing so pluginhost uses one canonical identity model without compatibility shims.
6. Continue extracting reusable channel/runtime contracts from `internal/channel` into `pkg/channel`, starting with the next Telegram-facing type or helper that materially shrinks plugin imports.
7. Continue extracting reusable MCP contracts from `internal/mcp` into `pkg/mcp`, starting with the next pure-data type that does not pull runtime orchestration with it.
8. Audit the remaining plugin packages importing `internal/db/sqlc` and decide whether those storage contracts should move to `pkg/...` or remain app-private.
9. Continue extracting pure-data/plugin-facing types from `internal/channel` and `internal/mcp` where that meaningfully shrinks plugin imports.
10. Update this `handoff.md` after every meaningful step with:
   - what changed
   - what is now safe to remove
   - what the next agent should do next

### 2026-04-08 — channel config helper extraction

- Moved generic channel plugin config decode/clone helpers from `internal/channel` into `pkg/channel`.
- Updated Telegram config handling to use `pkg/channel.DecodePluginConfig(...)` and `pkg/channel.CloneConfigMap(...)` directly.
- Updated the internal QQ, Feishu, and Weixin managed runtime config helpers to use the same public helper functions, so there is only one channel config decode path.
- Removed the dead `internal/channel` wrapper exports and deleted `internal/channel/plugin_runtime_config.go`.
- Ran focused tests for `pkg/channel`, `internal/channel`, `plugins/channels/telegram`, `internal/admin`, and `cmd/anna`, then ran `go test ./...` successfully.

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
