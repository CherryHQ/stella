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
- Extracted the generic managed channel bot runtime into a public package helper, so Telegram runtime wiring no longer depends on `internal/channel`.
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
- Extracted the shared skills library into `pkg/skills`, so prompt/review/CLI code no longer depends on `internal/skills` or runner-local skill catalog code.
- Moved the agent-facing `skills` tool out of `pkg/skills` and into `plugins/tools/skills`, so tool ownership now matches the plugin system instead of being manually wired by app code.
- Moved the reflect runtime package from `internal/reflect` to `plugins/reflect`, and replaced the hardcoded `pluginhost.RegisterReflect(...)` path with a narrow host service extension.
- Extracted QQ, Feishu, and Weixin persisted config types into `pkg/channel`.
- Moved QQ, Feishu, and Weixin runtime/config ownership from `internal/channel` + `internal/pluginhost` into:
  - `plugins/channels/qq`
  - `plugins/channels/feishu`
  - `plugins/channels/weixin`
- Removed the remaining static internal channel plugin registry; gateway/admin now derive channel runtime behavior from `pluginhost` registrations and canonical plugin IDs instead of `internal/channel/host_backed.go`.
- Started `internal/channel` cleanup by moving notification dispatch and the notify tool into `internal/notify`, so channel orchestration no longer owns scheduler/gateway notification routing.
- Continued `internal/channel` cleanup by splitting chat identity resolution, link-code handling, agent routing, and session-key resolution into separate files.
- Continued `internal/channel` cleanup by keeping store-backed channel config access in-package instead of a separate micro-package.
- Continued `internal/channel` cleanup by removing the remaining pkg-channel re-export shim from `internal/channel`.
- Continued `internal/channel` cleanup by keeping the shared slash-command handler in-package instead of a separate micro-package.
- Continued `internal/channel` cleanup by moving the terminal chat UI package into `internal/chatcli`, leaving `internal/channel` as the single app-private home for channel interaction flow.
- Simplified channel ownership boundaries further:
  - `internal/channel` no longer hardcodes per-channel readiness and notification rules
  - readiness now comes from plugin-owned channel registrations resolved through `internal/pluginhost`
- Unified managed bot-channel registration in `pkg/plugins`, so Telegram, QQ, Feishu, and Weixin no longer repeat the same metadata/config/runtime/status wiring shape in each plugin package.
- Simplified QQ, Feishu, and Weixin command flow:
  - shared slash-command parsing now lives in `pkg/channel`
  - shared text `/model` and `/agent` handling now lives in `pkg/channel`
  - Feishu keeps numbered model selection as a presentation difference, but uses the same shared command flow underneath
- Fixed the Telegram test seam after the managed-channel registration refactor by keeping its runtime factory late-bound; test overrides now still replace the runtime constructor instead of freezing the production constructor at init time.
- Removed `pkg/mcp` after re-checking the ownership boundary:
  - MCP config, IDs, manager, session, supervisor, and data types now live directly in `plugins/tools/mcp`
  - app/test callers now import `plugins/tools/mcp` directly for MCP-owned constants and types
  - there is no remaining fake “shared MCP package” layer
- Re-checked the design against `pi-mono` and tightened the target architecture:
  - extension-owned prompt behavior must be first-class, not app hardcode
  - declarative system-prompt sections are the immediate host capability
  - narrower run lifecycle hooks should follow for pi-style dynamic per-run behavior
  - `skills` should be implemented as one extension that contributes both a tool and prompt content
- Implemented the first extension-owned prompt contribution slice:
  - `pkg/plugins` now exposes `SystemPromptRegistration`, `SystemPromptContext`, and `SystemPromptSection`
  - `internal/pluginhost` now resolves enabled prompt sections through `SystemPromptSections(...)`
  - runner pool setup and admin session prompt rendering now both gather prompt sections through the host
  - `plugins/tools/skills` now owns the old hardcoded skills prompt block through a required prompt-section registration
  - `plugins/tools/mcp` now advertises prompt capability in metadata to reflect its prompt inventory contribution
- Implemented Phase A1 of capability expansion:
  - `pkg/plugins` now exposes `BeforeRunRegistration`, `BeforeRunContext`, and `BeforeRunResult`
  - `internal/pluginhost` now resolves enabled per-run lifecycle hooks through `BeforeRun(...)`
  - `internal/agent.Pool` now invokes the host-owned `BeforeRun` pipeline before each runner call
  - `internal/agent/runner` now supports a per-run system prompt override through context instead of mutating shared runner state
  - metadata validation now checks both prompt contributions and the new lifecycle capability instead of silently accepting undeclared holes
  - focused tests cover host lifecycle resolution and pool-level system prompt override behavior
- Implemented Phase B of capability expansion:
  - `pkg/plugins.ServiceHost` now exposes a generic `NotificationService`
  - `internal/pluginhost` now carries the app notification dispatcher as a host-owned service extension
  - reflect runtime/service wiring now uses the generic host notification service instead of a reflect-only notifier seam
  - managed channel notification registration remains a separate runtime service because it is channel lifecycle plumbing, not user-visible delivery
  - focused tests cover host notification service resolution through `pluginhost`
- Implemented Phase C of capability expansion:
  - `pkg/plugins.ServiceHost` now exposes a scoped plugin state store
  - plugin state is stored as host-owned JSON KV entries keyed by plugin, scope, and key
  - `internal/pluginstate` now provides the SQLite-backed implementation used by setup and tests
  - reflect watermarks now persist through the generic plugin state service instead of a dedicated `reflect_watermarks` table
  - reflect runtime no longer depends on database access just to store its watermark cursor
- Implemented Phase D of capability expansion:
  - `pkg/plugins.ServiceHost` now exposes a narrow auth directory service
  - plugins can resolve users and linked identities without importing `internal/auth` or DB-backed auth stores
  - `internal/pluginhost` now adapts the existing auth store into the public plugin auth service
  - `internal/notify` now depends on the public auth directory instead of `internal/auth`
  - focused tests cover host auth service resolution and user-notification routing through the new service
- Finished the remaining skills ownership move:
  - skill discovery, install/remove, patch/deprecate, validation, and builtin skill assets now live in `plugins/tools/skills`
  - `cmd/anna/skills.go` and `plugins/reflect/*` now import the skills plugin package directly
  - `pkg/skills` has been deleted instead of left behind as a public compatibility layer
- Started `internal/admin` cleanup by extracting route registration out of `server.go`.
- Continued `internal/admin` cleanup by moving channel lifecycle management out of `server.go`.
- Continued `internal/admin` cleanup by moving HTTP wrapper methods and JSON response helpers out of `server.go`.

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
- The next platform expansion should expose host capabilities, not `internal/...` package access.
- Capability order is:
  - run lifecycle hooks
  - notifications
  - plugin state storage
  - identity/auth
  - only then reconsider any DB-facing service

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
- Shared managed channel runtime orchestration now lives in `pkg/plugins`, which is the stable package boundary for managed runtime helpers that depend on both channel contracts and plugin lifecycle contracts.
- MCP now lives as one ownership unit inside `plugins/tools/mcp`: config, IDs, manager lifecycle, session dialing, server supervision, data types, runtime wiring, and proxy-tool behavior are all package-local there.
- `pkg/mcp` no longer exists; it was an intermediate migration stop, not the final design.
- Shared database query contracts now live in `pkg/db/sqlc`, not `internal/db/sqlc`.
- All skills ownership now lives in `plugins/tools/skills`: tool runtime, prompt contribution, discovery, validation, install/remove, patch/deprecate, and builtin skill assets.
- `pkg/skills` no longer exists; the earlier extraction there was an intermediate migration stop, not the final ownership boundary.
- Extension-owned prompt contribution now exists as a first-class host capability:
  - `pkg/plugins` exposes generic prompt-section contribution rather than a skills-specific API
  - runner/admin prompt builders gather prompt sections through `internal/pluginhost`
  - `plugins/tools/skills` owns both the `skills` tool and the skills prompt block
  - the remaining prompt-design follow-up is narrower dynamic run hooks, not more hardcoded prompt text
- Extension-owned per-run prompt mutation now exists as a first-class lifecycle capability:
  - `pkg/plugins` exposes a generic `BeforeRun` contract
  - `internal/pluginhost` chains enabled lifecycle registrations in deterministic plugin/name order
  - runner calls can override the effective system prompt for one request without mutating cached runner state
  - the platform can now support pi-style per-run prompt shaping without exposing app-private packages
- Extension-owned notification delivery now exists as a first-class host capability:
  - `pkg/plugins.ServiceHost` exposes `Notifications()`
  - plugins can notify all configured channels or a specific user without importing `internal/notify`
  - reflect now uses the generic host service instead of a plugin-specific notification contract
- Extension-owned durable state now exists as a first-class host capability:
  - `pkg/plugins.ServiceHost` exposes `StateStore()`
  - plugins can persist scoped JSON state without importing DB packages
  - reflect is the first migrated consumer, using session-scoped plugin state for review watermarks
- Extension-owned user and identity lookup now exists as a first-class host capability:
  - `pkg/plugins.ServiceHost` exposes `Auth()`
  - plugins can resolve users, roles, active status, and linked identities without importing auth internals
  - authorization policy evaluation is still intentionally out of scope for the public plugin surface
- Production provider construction now flows through `internal/pluginhost`:
  - runner provider registries
  - admin provider validation/model fetch
  - CLI model cache refresh
  - reflect review provider setup
- Production memory construction now flows through `internal/pluginhost` without any registry adapter step.
- Production core and optional tool construction both flow through `internal/pluginhost`.
- Reflect runtime/config/status now live in `plugins/reflect`, discovered through the default catalog plus host-provided reflect services.
- QQ, Feishu, and Weixin runtime/config/status now live in their plugin packages, discovered through the default catalog plus shared channel runtime services.
- There are no remaining plugin-owned runtime/config packages under `internal/...`.
- Notification dispatch, per-user notification routing, and the notify tool now live in `internal/notify` instead of `internal/channel`.
- Chat identity/linking, agent/session routing, shared channel commands, and store-backed channel config access now live together in `internal/channel`.
- Platform constants, model option aliases, and formatting helpers now come directly from `pkg/channel`; `internal/channel` no longer re-exports them.
- Shared slash-command parsing and shared text `/model` and `/agent` flows for QQ, Feishu, and Weixin now also live in `pkg/channel`.
- Channel readiness and notification-enabled checks are now plugin-owned registration behavior surfaced through `internal/pluginhost`, not an app-local map in `internal/channel`.
- The terminal chat UI now lives in `internal/chatcli`, not under `internal/channel`.
- `internal/admin/server.go` is no longer the only place that owns route registration; registration now lives in `internal/admin/routes.go`.
- `internal/admin/server.go` no longer owns channel start/stop lifecycle methods; those now live in `internal/admin/channel_lifecycle.go`.
- `internal/admin/server.go` no longer owns HTTP wrapper methods or JSON response helpers; those now live in `internal/admin/http.go` and `internal/admin/response.go`.
- The old split registries are gone as runtime/discovery systems:
  - `plugins/providers`
  - `plugins/memory`
  - the old registration logic from `plugins/tools`
  - the old registration logic from `plugins/hooks`
- Admin plugin config/status/toggle flows now assume one plugin host exists, matching the actual application wiring.
- `plugins/tools/mcp` now owns its full implementation directly instead of depending on a separate `pkg/mcp` package.
- `plugins/tools/skills` now owns the `skills` agent tool directly; `internal/agent` no longer manually appends or rewrites a `pkg/skills` tool instance.
- `plugins/channels/telegram` production code now imports only `pkg/...`; remaining `internal/channel` imports in that package are test-only.
- `plugins/memory/lcm` and `plugins/memory/simple` production code now import `pkg/db/sqlc`; there are no remaining production `plugins/...` imports of `internal/...`.
- `cmd/anna/plugins_imports.go` is still the blank-import bootstrap for built-ins. That is acceptable for repo-scoped plugins.

## Implementation Plan

### Next Capability Roadmap

1. Phase A: run lifecycle hooks
   - A1: `BeforeRun` prompt mutation
   - A2: tool lifecycle hooks if needed
   - A3: provider-request hooks only if the earlier slices prove insufficient
2. Phase B: notification capability
3. Phase C: plugin state storage
4. Phase D: identity/auth capability
5. Phase E: DB capability reassessment

The rule for all of these phases is the same:

- expose narrow services through `pkg/plugins`
- route them through `internal/pluginhost`
- do not let plugin packages import app-private packages directly

### Active Phase

Phase E: reassess whether any DB-facing plugin capability is still necessary now that notifications, state storage, and auth directory services exist.

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
- Schema coverage is still partial. MCP and all managed channels are wired; some non-channel managed plugins still rely on validate/redact callbacks without full schema data.
- Telegram production code no longer depends on `internal/channel`; runtime orchestration now uses managed runtime helpers from `pkg/plugins` plus the public notification registry contract there.
- The migration goal is complete:
  - production `plugins/...` packages import only `pkg/...`
  - all built-in tool/hook/provider/memory contributions resolve through `internal/pluginhost`
  - no provider/memory fallback path remains
  - no legacy registration mirroring step remains
- Admin construction now hard-requires `pluginhost`; the old nil-host compatibility path is gone.
- `Go init()` blank-import registration is still acceptable for repo-level built-ins, but it should not remain the only discovery logic in the design language.

## Next Steps

The plugin-ownership cleanup is done. The next mandate is ordinary app cleanup: make the remaining orchestration packages smaller, clearer, and less coupled.

Recommended order:

1. Clean `internal/channel`.
   - Keep it as one app-private interaction package; do not reintroduce the earlier micro-package split.
   - Leave only real cross-cutting helpers outside it:
     - `internal/notify`
     - `internal/chatcli`
   - Prefer pushing shared command and readiness policy down into `pkg/channel` or plugin registrations when the logic is truly plugin-owned.
   - Next cuts are lifecycle/coordinator shaping after the CLI move.
   - Keep using `pluginhost` discovery instead of reintroducing channel-specific registries or static lists.
2. Clean `internal/admin`.
   - Route registration is already split out of `server.go`.
   - Channel lifecycle extraction is already done.
   - HTTP/middleware/response helpers are already separated.
   - Next cuts are handler grouping inside the larger admin resource files.
3. Clean `cmd/anna`.
   - Split bootstrap/wiring responsibilities into narrower setup units.
4. Clean scheduler/notification plumbing after that.
5. Revisit the blank-import bootstrap only after the wiring packages stop moving.

Rules for the cleanup phase remain:

- No fallback code.
- No compatibility layers just to preserve old shapes.
- Prefer smaller internal packages over broad “misc” buckets.
- Move helpers to `pkg/...` only when they are genuinely stable and cross-package.
- Keep each cleanup slice coherent, tested, and committed separately.

Success condition now:

- no plugin-owned code under `internal/...`
- fewer cross-package responsibilities hidden inside broad app-private packages
- smaller package APIs
- less wiring logic mixed with domain behavior
- no new architecture debt introduced while cleaning code shape

## Extension Design Gap Analysis

Re-checked `extension-design.md` against the current codebase. The repo now satisfies the old migration invariants, but it still does **not** satisfy the design's ownership rule:

- no repo-owned plugin behavior should live under `internal/...`
- host discovery should not depend on extension-specific registration paths
- one ownership unit should carry all of an extension's contributions

Current mismatches:

1. `reflect` still lives in `internal/reflect`.
   - Runtime, config schema/defaults, reviewer logic, watermarking, and service orchestration are still owned by an internal package.
   - `internal/pluginhost/reflect.go` is an extension-specific host registration path.

2. `qq`, `feishu`, and `weixin` are split across `plugins/...` and `internal/...`.
   - Bot/channel implementations live in `plugins/channels/{qq,feishu,weixin}`.
   - Managed runtime wrappers, plugin IDs, config schema helpers, and decode/redact logic still live in `internal/channel/*_plugin_runtime.go`.
   - `internal/pluginhost/{qq,feishu,weixin}.go` is still hardcoded per-extension registration logic.

3. `internal/channel/host_backed.go` still hardcodes the host-backed channel plugin list.
   - That should be discoverable from registered extension metadata/contributions instead of a second static list.

4. The host contract is still plugin-centric rather than the final design's single-extension manifest/contribution model.
   - `pkg/plugins` still exposes per-kind registration structs.
   - IDs are still `kind/name` rather than the design's flat extension ID plus derived contribution IDs.
   - This is a larger model migration than the ownership cleanup above.

## Full Adoption Plan

To fully adopt the design goal that no repo-owned plugin behavior remains in `internal/...`, the work should proceed in this order:

### Phase A: Move repo-owned extensions out of `internal/...`

1. Move `reflect` ownership into `plugins/reflect`.
   - Keep only app-private composition helpers in internal packages.
   - If reusable helpers emerge, move them to `pkg/...` first, then move the extension package.

2. Move QQ/Feishu/Weixin plugin-owned runtime/config code out of `internal/channel`.
   - Create self-owned plugin registration/runtime files in:
     - `plugins/channels/qq`
     - `plugins/channels/feishu`
     - `plugins/channels/weixin`
   - Leave only shared channel orchestration helpers in `internal/...` or `pkg/...` depending on stability.

3. Delete extension-specific host registration entry points after those moves:
   - `internal/pluginhost/RegisterReflect`
   - `internal/pluginhost/RegisterQQ`
   - `internal/pluginhost/RegisterFeishu`
   - `internal/pluginhost/RegisterWeixin`

4. Remove `internal/channel/host_backed.go` as a source of truth.
   - Replace it with discovery derived from host metadata/contributions.

### Phase B: Finish host discovery cleanup

1. Ensure `LoadDefaultCatalog()` plus blank imports is the only repo-extension discovery path.
2. Remove any remaining extension-specific branching in admin/CLI/bootstrap that depends on hardcoded plugin IDs when the host can infer the same information.

### Phase C: Decide whether to do the literal model rewrite from the design doc

This is separate from the ownership cleanup above. It includes:

- moving from plugin-centric naming to extension-centric naming
- replacing `kind/name` IDs with flat extension IDs plus derived contribution IDs
- collapsing the public registration model toward one extension manifest + contribution set

This is a real second migration, not a cleanup detail. It should only start after Phase A and Phase B are done, otherwise the repo will be changing ownership layout and core model semantics at the same time.

## Immediate Implementation Order

1. Finish cleaning `internal/reflect`, then move it into `plugins/reflect`.
2. Extract and move QQ plugin-owned runtime/config code into `plugins/channels/qq`.
3. Do the same for Feishu.
4. Do the same for Weixin.
5. Delete hardcoded pluginhost registration shims and static host-backed channel lists.
6. Re-evaluate whether to do the full naming/ID/interface rewrite from `extension-design.md`.

### 2026-04-08 — MCP ownership correction

- Re-checked the extension boundary and concluded `pkg/mcp` was only a migration waypoint, not a stable shared package.
- Moved MCP config, IDs, manager, session, supervisor, and status/result/tool data from `pkg/mcp` into `plugins/tools/mcp`.
- Updated CLI/admin/pluginhost tests and callers to import `plugins/tools/mcp` directly for MCP-owned constants and types.
- Removed the `pkg/mcp` package entirely.
- Ran focused tests for `plugins/tools/mcp`, `cmd/anna`, `internal/admin`, and `internal/pluginhost`, then ran `go test ./...` successfully.

### 2026-04-08 — skills tool ownership correction

- Re-checked `pkg/skills` and split it by real responsibility instead of keeping the mixed library-plus-tool shape.
- Kept `pkg/skills` as the shared library for discovery, prompt visibility, install/remove, and skill file management.
- Moved the agent-facing `skills` tool wrapper from `pkg/skills` into `plugins/tools/skills`.
- Registered `tool/skills` as a required built-in tool plugin and added it to the blank-import bootstrap.
- Removed the manual `skills` tool injection/replacement logic from `internal/agent`; per-session scoping now comes from the plugin tool builder instead.
- Updated reflect and admin callers to use the plugin-owned tool definition/constructor where they need the actual tool wrapper.
- Ran focused tests for `plugins/tools/skills`, `pkg/skills`, `internal/agent`, `plugins/reflect`, `internal/admin`, and `cmd/anna`, then ran `go test ./...` successfully.

### 2026-04-08 — channel config helper extraction

- Moved generic channel plugin config decode/clone helpers from `internal/channel` into `pkg/channel`.
- Updated Telegram config handling to use `pkg/channel.DecodePluginConfig(...)` and `pkg/channel.CloneConfigMap(...)` directly.
- Updated the internal QQ, Feishu, and Weixin managed runtime config helpers to use the same public helper functions, so there is only one channel config decode path.
- Removed the dead `internal/channel` wrapper exports and deleted `internal/channel/plugin_runtime_config.go`.
- Ran focused tests for `pkg/channel`, `internal/channel`, `plugins/channels/telegram`, `internal/admin`, and `cmd/anna`, then ran `go test ./...` successfully.

### 2026-04-08 — Telegram runtime extraction

- Moved the generic managed channel bot runtime out of `internal/channel` into a public package helper.
- Updated Telegram managed runtime wiring to use a managed runtime helper plus the public `pkg/plugins.NotificationRegistry` interface instead of `internal/channel.Dispatcher`.
- Updated internal QQ, Feishu, and Weixin managed runtimes to use the same public runtime helper, then removed `internal/channel/bot_runtime.go`.
- Updated downstream tests and host wiring to the new Telegram runtime dependency shape.
- Verified that `plugins/channels/telegram` no longer imports `internal/channel` in production code.
- Ran focused tests for `pkg/channel`, the managed runtime helper package, `internal/channel`, `plugins/channels/telegram`, `internal/pluginhost`, and `cmd/anna`, then ran `go test ./...` successfully.

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

### 2026-04-08 — plugin ownership cleanup completed

- Extracted skills into `pkg/skills` and removed the old `internal/skills` package plus the runner-local skill catalog implementation.
- Moved the reflect plugin from `internal/reflect` to `plugins/reflect`.
- Added `pkg/plugins.ReflectRuntimeServices` plus the `pluginhost` service extension that feeds reflect its app-private runtime inputs without making the plugin import `internal/...`.
- Moved QQ, Feishu, and Weixin runtime/config ownership into their plugin packages and removed:
  - `internal/pluginhost/qq.go`
  - `internal/pluginhost/feishu.go`
  - `internal/pluginhost/weixin.go`
  - `internal/channel/qq_plugin_runtime.go`
  - `internal/channel/feishu_plugin_runtime.go`
  - `internal/channel/weixin_plugin_runtime.go`
- Extracted QQ, Feishu, and Weixin persisted config types into `pkg/channel`.
- Removed `internal/channel/host_backed.go`; gateway and admin now drive channel behavior from `pluginhost` registrations instead of an internal hardcoded plugin list.
- Verified repeatedly with focused package tests and ran `go test ./...` successfully after each phase.

### 2026-04-08 — notification dispatch extraction

- Moved notification dispatch, per-user notification routing, and the notify tool from `internal/channel` into the new app-private package `internal/notify`.
- Updated gateway setup, scheduler heartbeat/job delivery, admin tests, and Telegram plugin tests to use `internal/notify.Dispatcher` directly.
- Deleted the old `internal/channel/notifier.go`, `internal/channel/notifier_test.go`, and `internal/channel/notify_tool.go` files.
- Verified with focused tests for `internal/notify`, `internal/scheduler`, `plugins/channels/telegram`, `internal/admin`, and `cmd/anna`, then ran `go test ./...`.

### 2026-04-08 — chat routing extraction

- Moved chat identity resolution, link-code handling, agent selection, and session-key resolution from `internal/channel` into the new app-private package `internal/chatroute`.
- Updated the channel coordinator and generic command handling to depend on `internal/chatroute.ResolvedChat` instead of local `internal/channel` routing types.
- Moved the routing and access regression tests from `internal/channel` into `internal/chatroute`.
- Deleted the old `internal/channel/identity.go`, `internal/channel/linkcode.go`, `internal/channel/resolved.go`, and `internal/channel/agent_command.go` files.
- Verified with focused tests for `internal/chatroute`, `internal/channel`, `plugins/channels/{telegram,qq,feishu,weixin}`, and `cmd/anna`.

### 2026-04-08 — channel config access extraction

- Moved store-backed channel config loading, validity checks, and notification-enable checks from `internal/channel` into the new app-private package `internal/channelconfig`.
- Updated gateway and channel construction to use `internal/channelconfig` plus the public `pkg/channel` config types directly.
- Updated admin Telegram runtime tests to stop depending on the removed `internal/channel` config aliases.
- Deleted the old `internal/channel/config.go` and `internal/channel/config_test.go` files.
- Verified with focused tests for `internal/channelconfig`, `internal/channel`, `internal/admin`, and `cmd/anna`.

### 2026-04-08 — pkg channel shim removal

- Removed the remaining `internal/channel` alias layer for platform constants, model-option types, and formatting helpers.
- Updated CLI, admin, and command/model callers to use `pkg/channel` directly where the stable public API already existed.
- Deleted `internal/channel/model.go`, `internal/channel/util.go`, and the redundant `internal/channel/util_test.go`.
- Verified with focused tests for `internal/channel`, `internal/channel/cli`, `internal/admin`, and `cmd/anna`.

### 2026-04-08 — shared command extraction

- Moved the shared `/start`, `/help`, `/new`, `/compact`, and `/whoami` handler from `internal/channel` into the new app-private package `internal/chatcommand`.
- Updated the channel coordinator to call `internal/chatcommand.Handle(...)` instead of carrying the command logic in-package.
- Deleted the old `internal/channel/command.go` and `internal/channel/command_test.go` files.
- Verified with focused tests for `internal/chatcommand`, `internal/channel`, `internal/channel/cli`, and `cmd/anna`.

### 2026-04-08 — CLI package extraction

- Moved the terminal chat UI package from `internal/channel/cli` to `internal/chatcli`.
- Updated the `anna chat` command to import `internal/chatcli` directly.
- Left package internals unchanged; this phase only fixed package ownership.

### 2026-04-08 — channel balance reset

- Reviewed the previous `internal/channel` split and decided the micro-packages added more navigation cost than architectural value.
- Folded `internal/chatroute`, `internal/chatcommand`, and `internal/channelconfig` back into `internal/channel` while keeping the file-level split:
  - `identity.go`
  - `linkcode.go`
  - `resolved_chat.go`
  - `agent_switch.go`
  - `commands.go`
  - `config.go`
- Kept `internal/notify` separate because it is genuinely cross-cutting runtime infrastructure, and kept `internal/chatcli` separate because it is a distinct terminal UI package.
- Updated gateway/channel construction to use `internal/channel` directly again.
- Verified with focused tests for `internal/channel` and `cmd/anna`, then ran `go test ./...`.

### 2026-04-08 — channel runtime helper moved into plugins

- Removed the top-level `pkg/channelruntime` package.
- Moved the generic bot managed-runtime helper into `pkg/plugins/bot_runtime.go`, because it depends on plugin lifecycle types (`ManagedRuntime`, `PluginState`, `RuntimeSnapshot`, `NotificationRegistry`) more than on channel-domain contracts.
- Updated Telegram, QQ, Feishu, and Weixin runtime wiring to import the helper from `pkg/plugins` directly.
- Verified with focused tests for `pkg/plugins`, `plugins/channels/...`, and `cmd/anna`, then ran `go test ./...`.

### 2026-04-08 — admin route registration extraction

- Moved admin mux wiring out of `internal/admin/server.go` into the new `internal/admin/routes.go`.
- Split route registration by concern: static/auth/profile/pages/providers/agents/channels/users/auth-users/sessions/plugins/models/tools/scheduler.
- Reduced `server.go` to construction, channel lifecycle management, middleware, and JSON helpers instead of one large constructor with every route inline.
- Verified with focused tests for `internal/admin` and `cmd/anna`.

### 2026-04-08 — admin channel lifecycle extraction

- Moved channel lifecycle methods out of `internal/admin/server.go` into the new `internal/admin/channel_lifecycle.go`.
- Kept the lifecycle API unchanged:
  - `RegisterChannelStop(...)`
  - `SetChannelLifecycle(...)`
  - `startChannel(...)`
  - `stopChannel(...)`
- Reduced `server.go` further so it now mostly owns construction and HTTP/middleware helpers.
- Verified with focused tests for `internal/admin` and `cmd/anna`.

### 2026-04-08 — admin HTTP helper extraction

- Moved `redirectRoot(...)`, `Handler()`, `corsMiddleware(...)`, and `jsonMiddleware(...)` out of `internal/admin/server.go` into `internal/admin/http.go`.
- Moved `writeData(...)`, `writeError(...)`, and `decodeJSON(...)` out of `internal/admin/server.go` into `internal/admin/response.go`.
- Reduced `server.go` again so it now mostly holds server state, construction, and the `LinkCodes()` accessor.
- Verified with focused tests for `internal/admin` and `cmd/anna`.

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
