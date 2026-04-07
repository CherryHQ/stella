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
- `plugins/tools/mcp` is the best current reference for a multi-capability unit, but it still depends on `internal/mcp`.
- `plugins/channels/telegram` has started moving ownership into the package, but it still imports `internal/channel` types and runtime helpers.
- Core tools are still separate because `plugins/tools/registry.go` owns the required-tool boot path used by the Go runner.
- `cmd/anna/plugins_imports.go` is still the blank-import bootstrap for built-ins. That is acceptable for repo-scoped plugins.

## Implementation Plan

### Phase 1: Finish host unification

1. Remove the remaining builder fallback dependence on legacy registries where possible.
2. Decide whether required tools should also be expressed through `pkg/plugins.ToolRegistration` or remain intentionally separate.
3. Make new contribution work land in host registrations first, with legacy registries only as compatibility input.

### Phase 2: Normalize identity and persistence

1. Normalize mixed IDs deliberately:
   - `mcp`
   - `channel/telegram`
   - `reflect`
2. Decide and document one compatibility rule for persisted rows:
   - keep existing `settings_plugins` table temporarily
   - treat it as plugin state storage during migration
3. Remove built-in plugin name lists as a source of truth where the host can infer or register them.

### Phase 3: Make config schema-driven

1. Add a real schema type under `pkg/plugins` for host-owned config handling.
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
  - `mcp`
  - `channel/telegram`
  - `reflect`
  This must be normalized deliberately during migration.
- Required tools are still built through `plugintools.BuildCore(...)`. That path was not changed in this slice.
- `Go init()` blank-import registration is still acceptable for repo-level built-ins, but it should not remain the only discovery logic in the design language.

## Next Steps

1. Decide whether core tools should be lifted into `pkg/plugins.ToolRegistration` or intentionally remain outside the plugin host.
2. Audit `plugins/tools/mcp` and `plugins/channels/telegram` for `internal/...` imports and pick the next concrete extraction into `pkg/...`.
3. Normalize persisted plugin IDs and host discovery rules so built-ins and persisted rows use one deliberate identity model.
4. Introduce a schema-backed config contract under `pkg/plugins` instead of per-plugin ad hoc validation only.
5. Remove legacy ID aliasing so pluginhost uses one canonical identity model without compatibility shims.
6. Update this `handoff.md` after every meaningful step with:
   - what changed
   - what is now safe to remove
   - what the next agent should do next

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
  - `RegisterLegacyID(...)`, `resolvePluginID(...)`, and `configService` alias inference once `mcp` and any other persisted rows are made canonical
