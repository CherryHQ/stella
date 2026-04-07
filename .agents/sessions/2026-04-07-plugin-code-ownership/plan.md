# Plan: Plugin Code Ownership Migration

## Overview

Move built-in plugin-specific implementation ownership out of scattered `internal/...` entrypoints and into `plugins/...` packages so that adding a new plugin primarily means adding code under `plugins/{kind}/{name}` and blank-importing it at startup.

This plan preserves the current plugin host architecture and migration invariants:

- no DB schema changes
- preserve existing `settings_plugins` rows and IDs
- keep admin UX stable unless intentionally redesigned
- keep plugin identity and capability identity separate
- keep declarative runtime lifecycle via `Apply(ctx, desired PluginState)`
- keep config/status split intact
- avoid import cycles
- avoid unrelated cleanup
- do not touch `internal/agent/runner/.agents/`
- do not use `.agents/sessions/2026-04-07-mcp-plugin/*` as implementation input

### Goals

- Make `plugins/{kind}/{name}` the ownership boundary for plugin-specific config, runtime wiring, status, and capability registration.
- Reduce plugin-specific files in `internal/pluginhost`, `internal/channel`, and `cmd/anna`.
- Replace static plugin-specific bootstrap wiring with host discovery and plugin self-registration.
- Migrate in clean cutovers, deleting old paths as each new path lands instead of preserving compatibility scaffolding.

### Success Criteria

- [ ] Built-in channel plugins can self-register config, runtime, and status from `plugins/...` without plugin-specific registration shims in `internal/pluginhost`.
- [ ] `cmd/anna/gateway.go` no longer manually registers concrete built-in host-backed channels.
- [ ] `internal/channel` contains only generic channel platform/runtime support, not plugin-specific channel ownership code.
- [ ] Admin and gateway logic use host discovery/metadata instead of static host-backed channel lists.
- [ ] Existing plugin row IDs, config payloads, runtime semantics, and admin UX remain stable.
- [ ] Legacy per-kind registries are no longer the source of truth for built-in plugins by the end of the migration.
- [ ] After Phase 5, adding a **new channel plugin of the existing `channel` kind** should require only code under `plugins/channels/{name}` plus a blank import in `cmd/anna/plugins_imports.go`, with no new plugin-specific files in `internal/pluginhost`, `internal/channel`, `internal/admin`, or `cmd/anna`.
- [ ] After Phase 7, the same “plugin package + blank import” rule applies to built-in plugins of existing supported kinds (`tool`, `provider`, `hook`, `memory`, `channel`, runtime-style plugins routed through host metadata).

### Out of Scope

- DB schema changes or plugin row renames.
- Admin UX redesign beyond backend/internal plumbing needed to preserve current behavior.
- Reworking plugin host lifecycle semantics.
- Moving core non-plugin subsystems (auth, scheduler, agent pool, DB) into `plugins/...`.
- Any changes under `internal/agent/runner/.agents/`.

## Technical Approach

Adopt a three-layer ownership model:

1. `pkg/...` defines narrow, stable plugin contracts and service interfaces.
2. `internal/...` provides generic host/platform infrastructure only.
3. `plugins/...` owns plugin-specific registration, config, runtime wiring, and status.

The migration starts by enabling plugin packages to self-register managed channel runtimes without relying on plugin-specific glue in `internal/pluginhost`. That requires adding narrow host service extensions and host metadata/discovery support.

Once those primitives exist, migrate a single representative plugin family first: host-backed channels. Within that family, Telegram is the pilot because it exercises config + runtime + status + notification wiring with the smallest config surface and lowest special-case risk.

### Design Decisions

- **Self-registration becomes the default**: built-in plugins register via `pkg/plugins.Register(...)` from their package, following the MCP pattern.
- **Generic internal support stays generic**: reusable managed channel runtime code is extracted/kept in a generic internal package and may be consumed by plugin packages, but never imports them back.
- **Host discovery replaces static plugin lists**: admin/backend/bootstrap query host metadata/capabilities instead of hardcoded host-backed channel tables.
- **Migration proceeds registration-first**: move registration ownership before moving all implementation files, to minimize blast radius.
- **No compatibility layers**: when a plugin family is migrated, old registration/build paths are deleted in the same phase rather than retained behind mixed-mode compatibility logic.
- **Metadata must be sufficient for replacement work**: plugin metadata is not decorative; it is the minimum discovery contract admin/gateway use to replace current static branching.
- **Metadata and capability registrations must be internally consistent**: if a plugin metadata record declares `Managed`, `HasConfig`, `HasStatus`, or a capability in `Capabilities`, the corresponding runtime/config/status/capability registrations must exist. Host startup and tests should fail loudly on incomplete managed plugin registration.
- **Discovery uses registered built-ins merged with persisted state**: host metadata defines the set of available built-in plugins, while `settings_plugins` remains the source of desired state/config for each plugin row. Admin/backend discovery merges these sources so built-in plugins remain visible before configuration without inventing duplicate entries.
- **Reflect follows channels**: reflect is migrated after the channel family because it is more coupled to core services and is not the best pilot.
- **Legacy registries are retired decisively**: once the host-native path for a plugin kind exists and is proven on a pilot, the legacy registry path for that built-in kind should be removed rather than run in parallel.

### Proposed Package Boundaries

#### `pkg/plugins`
Owns plugin contracts and narrow service interfaces.

Planned additions:

- `pkg/plugins/services_channel.go`
  - `NotificationRegistry`
  - `ChannelRuntimeServices`
- `pkg/plugins/metadata.go`
  - `PluginMeta`
  - `MetadataRegistration`

Minimum metadata shape:

- `ID`
- `Kind`
- `Name`
- `DisplayName`
- `Managed` (runtime-backed / host-applied)
- `AdminVisible`
- `HasConfig`
- `HasStatus`
- `Capabilities` (normalized capability names, e.g. `channel`, `runtime`, `config`, `status`, `tool`, `provider`)
- optional kind-specific attributes only when they replace existing static branching, e.g. `SupportsNotifications` for channels

#### `internal/pluginhost`
Owns generic host implementation only.

Keeps:

- catalog loading
- config persistence bridge
- runtime orchestration
- capability registration maps
- build/discovery helpers

Adds:

- metadata registration and lookup
- service extension injection/access
- registration completeness validation between metadata and capability hooks
- discovery APIs (`PluginsByKind`, `ManagedPlugins`, `HasRuntime`, etc.)
- merged built-in + persisted-state discovery views for admin/bootstrap

Removes over time:

- `internal/pluginhost/telegram.go`
- `internal/pluginhost/qq.go`
- `internal/pluginhost/feishu.go`
- `internal/pluginhost/weixin.go`
- `internal/pluginhost/reflect.go`

#### `internal/channelruntime` (new)
Owns generic managed channel runtime scaffolding extracted from `internal/channel`.

Planned contents:

- generic managed bot runtime lifecycle
- generic notifier registration/unregistration hooks
- generic apply/start/stop/snapshot helpers
- shared config map decode helpers where plugin-agnostic

Must not import concrete plugin packages.

#### `internal/channel`
Retains channel platform concerns only:

- coordinator
- dispatcher
- identity resolution
- shared channel models/routing

Removes over time:

- plugin-specific config structs
- plugin-specific runtime constructors
- static host-backed plugin/channel tables

#### `plugins/{kind}/{name}`
Owns plugin-specific:

- plugin constants/IDs
- config structs/defaults/validate/redact
- managed runtime constructors
- runtime status metadata
- capability registration
- concrete implementation

### Dependency Direction Rules

1. `pkg/...` must never import `internal/...`.
2. `plugins/...` may import `pkg/...` and narrow generic `internal/...` support packages only.
3. Generic internal support packages must never import concrete plugin packages.
4. `internal/pluginhost`, `internal/admin`, and `internal/channel` must not import concrete built-in plugin packages, except for bootstrap-only blank imports in `cmd/anna` and tests.
5. Plugin-specific IDs/config/runtime logic lives with the plugin package in the final state.
6. If a helper accumulates concrete Telegram/QQ/Feishu/Weixin branching, it belongs in `plugins/...`, not in generic `internal/...`.

### Components

- **Host service extensions**: narrow interfaces for channel runtime services and later metadata-driven capability queries.
- **Host metadata/discovery**: generic host-side metadata registration and lookup for admin/bootstrap.
- **Generic channel runtime support**: extracted shared managed runtime support for host-backed channels.
- **Plugin-owned channel registration**: per-channel `plugin.go`, `config.go`, `runtime.go` under `plugins/channels/...`.
- **Discovery-driven admin/bootstrap**: host metadata replaces static host-backed channel lists and registration maps.
- **Direct host-native builds**: builders read host registrations directly; no long-lived adapter layer remains.

## Implementation Phases

### Phase 1: Host metadata and service-extension scaffolding

1. Add narrow plugin-host service interfaces for channel runtime services and notification registration (`pkg/plugins/host.go`, new `pkg/plugins/services_channel.go`).
2. Add plugin metadata registration contracts and host-side metadata storage/lookup (`pkg/plugins/metadata.go`, `internal/pluginhost/host.go`, new `internal/pluginhost/metadata.go`, new `internal/pluginhost/discovery.go`).
3. Add generic host service-extension injection/access for channel runtime services (`internal/pluginhost/host.go`, new `internal/pluginhost/service_extensions.go`).
4. Define registration completeness validation so metadata-declared managed/config/status capabilities must have matching registrations, and fail loudly otherwise (`internal/pluginhost/host.go`, `internal/pluginhost/metadata.go`, host tests).
5. Define built-in discovery semantics: host metadata provides the available plugin catalog, persisted `settings_plugins` rows provide desired state/config, and discovery APIs compose both without duplicate/phantom entries (`internal/pluginhost/discovery.go`, admin/bootstrap tests as needed).
6. Wire channel runtime services into setup/bootstrap without changing current plugin behavior (`cmd/anna/commands.go`, `cmd/anna/gateway.go` as needed for construction flow).
7. Add host tests covering metadata registration, duplicate protection, registration completeness, discovery, and service extension access (`internal/pluginhost/host_test.go`, new focused tests if needed).

**Discovery API target shape**

- `ListRegisteredPlugins()` — built-in registered plugins from host metadata
- `ListAdminVisiblePlugins()` — admin-facing merged view of built-in registrations + persisted state
- `PluginsByKind(kind)` — registered plugin IDs by kind
- `ManagedPlugins()` — registered plugin IDs with managed runtime semantics
- `HasRuntime(pluginID)` / `HasConfig(pluginID)` / `HasStatus(pluginID)` — capability presence checks

**Phase 1 exit criteria**

- host can store and return metadata sufficient for admin/gateway replacement work
- metadata-declared managed/config/status plugins cannot exist in a partially registered state
- host has one registration path for migrated plugin families; no duplicated source of truth remains
- merged discovery can show built-in plugins before configuration while preserving persisted desired state/config
- no observable behavior change in current startup/admin flows

### Phase 2: Telegram pilot — registration ownership only

1. Add `plugins/channels/telegram/plugin.go` that self-registers metadata, config, runtime, and status via `pkg/plugins.Register(...)`.
2. Keep the Telegram implementation simple during the pilot by reusing only generic internal helpers; do not introduce any compatibility adapter or duplicate registration path.
3. Remove plugin-specific Telegram registration glue from `internal/pluginhost/telegram.go` in the same cutover.
4. Update bootstrap to rely on catalog loading + plugin self-registration instead of `RegisterTelegram(...)` (`cmd/anna/plugins_imports.go`, `cmd/anna/gateway.go`).
5. Add/adjust tests proving Telegram runtime/config/status behavior remains unchanged, registration is complete before apply/build runs, and duplicate plugin/capability registration fails loudly (`internal/channel/telegram_plugin_runtime_test.go`, plugin registration tests if introduced).

**Phase 2 exit criteria**

- Telegram is registered only through `plugins/channels/telegram/plugin.go`
- startup no longer needs a Telegram-specific host registration call
- startup diagnostics can show Telegram as a self-registered managed channel plugin

### Phase 3: Telegram completion — move Telegram-specific ownership fully into plugin package

1. Extract/relocate reusable managed runtime scaffolding into generic internal support (`internal/channelruntime/...` or equivalent refactor of `internal/channel/bot_runtime.go` and `internal/channel/plugin_runtime_config.go`) without changing Telegram ownership yet.
2. Add `plugins/channels/telegram/config.go` with `Config`, defaults, decode, validate, and redact logic.
3. Add `plugins/channels/telegram/runtime.go` with `PluginID`, runtime name, managed runtime constructor, and runtime snapshot metadata.
4. Delete `internal/channel/telegram_plugin_runtime.go` and remove Telegram-specific ownership from `internal/channel/config.go`.
5. Move/update tests so Telegram plugin package owns Telegram-specific runtime/config tests.

**Phase 3 exit criteria**

- reusable channel runtime scaffolding is plugin-agnostic and imports no concrete channel plugins
- Telegram-specific config/runtime/status ownership is fully under `plugins/channels/telegram`
- `internal/channel` no longer owns Telegram plugin semantics

### Phase 4: Migrate remaining host-backed channels

**Gate before starting Phase 4**: Telegram migration must prove that the Phase 1 channel service interfaces are sufficient. If Telegram required broader host reach-through or plugin-specific exceptions in generic internals, pause and redesign before propagating the pattern.

1. Repeat the Telegram pattern for QQ (`plugins/channels/qq/{plugin,config,runtime}.go`, remove `internal/pluginhost/qq.go`, remove `internal/channel/qq_plugin_runtime.go`).
2. Repeat for Feishu (`plugins/channels/feishu/{plugin,config,runtime}.go`, remove `internal/pluginhost/feishu.go`, remove `internal/channel/feishu_plugin_runtime.go`).
3. Repeat for Weixin (`plugins/channels/weixin/{plugin,config,runtime}.go`, remove `internal/pluginhost/weixin.go`, remove `internal/channel/weixin_plugin_runtime.go`).
4. Keep the admin UX and persisted config payloads stable while moving ownership.
5. Add targeted regression tests per channel runtime and startup/apply path.

**Phase 4 exit criteria**

- all built-in host-backed channels self-register from `plugins/channels/...`
- no per-channel registration shims remain in `internal/pluginhost`
- no built-in channel requires new plugin-specific bootstrap code in `cmd/anna`

### Phase 5: Discovery-driven admin and gateway cleanup

1. Inventory all remaining consumers of static host-backed channel knowledge across admin, gateway, and config/status paths before replacement (`internal/admin/channels.go`, `internal/admin/plugins.go`, `cmd/anna/gateway.go`, `internal/channel/config.go`, related tests).
2. Adopt the Phase 1 merged discovery contract in admin so built-in channel plugins are listed from registered metadata while persisted rows continue to supply desired state/config and no duplicate/phantom entries appear (`internal/admin/channels.go`, `internal/admin/plugins.go`, `internal/pluginhost/discovery.go`).
3. Replace static host-backed channel lists and `IsHostBackedPlugin`-style branching with host metadata/capability discovery (`internal/admin/channels.go`, `internal/admin/plugins.go`, `cmd/anna/gateway.go`).
4. Remove channel registration tables from gateway startup and apply managed plugins by host discovery.
5. Standardize config validity / notification-enabled checks behind one host-driven accessor contract rather than dual metadata-vs-accessor paths (`internal/pluginhost/...`, `internal/channel/config.go` or replacement package).
6. Delete obsolete static channel metadata helpers such as `internal/channel/host_backed.go` once no longer referenced.
7. Add tests covering admin toggle/config/status flows and persisted pre-migration config round-trips for discovery-driven managed channels.

**Phase 5 exit criteria**

- no static host-backed channel list or plugin-ID branch is required for current built-in channels
- admin can show built-in channel plugins before configuration via merged host metadata + persisted state discovery
- adding a new built-in channel plugin requires only a plugin package and blank import, not admin/gateway/internal special-casing

**Phase 5 exit criteria**

- no static host-backed channel list or plugin-ID branch is required for current built-in channels
- adding a new built-in channel plugin requires only a plugin package and blank import, not admin/gateway/internal special-casing

### Phase 6: Reflect plugin migration

1. Add `plugins/runtimes/reflect/{plugin,config,runtime}.go` that self-registers metadata, config, runtime, and status.
2. Keep core review/service implementation in `internal/reflect/...` but remove reflect-specific registration glue from `internal/pluginhost/reflect.go` in the same cutover.
3. Expose any reflect runtime dependencies only through explicit narrow host service interfaces in `pkg/plugins`, not ad hoc host reach-through.
4. Update bootstrap to rely on plugin self-registration for reflect.
5. Add/update reflect host/runtime tests and preserve current plugin ID and status payloads.

**Phase 6 exit criteria**

- reflect registration/config/runtime/status are plugin-owned
- reflect does not require broad host service exposure beyond explicit typed interfaces

### Phase 7: Legacy registry removal for providers/hooks/memory/tools

1. Add direct `pkg/plugins.Register(...)`-based registration to built-in provider plugins and switch provider builds to host-native capability lookup.
2. Do the same for hooks, memory providers, and eligible tool plugins.
3. Rewrite `internal/pluginhost/builders.go` to build exclusively from host capability registrations.
4. Delete `internal/pluginhost/adapters.go` and stop using legacy per-kind registries as plugin-host inputs.
5. Add regression tests covering startup/build/hot-reload paths for each plugin kind after the old paths are removed.

**Phase 7 exit criteria**

- builders use host-native capability registrations only
- compatibility adapters are deleted
- adding a new built-in plugin of an existing supported kind does not require legacy registry edits

### Phase 8: Final cleanup and documentation sync

1. Remove now-dead plugin-specific internal files and simplify bootstrap/admin code paths that existed only for migration compatibility.
2. Update `docs/content/docs/features/plugin-system.md` to describe the final plugin-owned architecture and new plugin authoring model.
3. Update `internal/agent/runner/builtin/anna/SKILL.md` so the built-in skill reflects the final plugin architecture.
4. Run formatting, linting, and targeted/full test suites; document any intentional deviations in final notes.

## Testing Strategy

### Architectural tests

- Host metadata registration and duplicate protection.
- Host registration completeness validation for metadata-declared managed/config/status plugins.
- Host discovery methods return correct plugins by kind / managed state / runtime presence.
- Merged discovery correctly combines built-in registered plugins with persisted `settings_plugins` state without duplicate or phantom entries.
- Service extension injection is accessible to runtime factories without nil panics.
- No migrated plugin family retains a second registration/build path after its phase completes.

### Channel migration regression tests

For each migrated channel:

- enabling valid config starts runtime and registers notifier when enabled
- disabling plugin stops runtime and unregisters notifier
- invalid config produces existing validation errors
- runtime restart/reapply preserves current `Apply` semantics
- status payload shape remains stable enough for admin consumers

### Bootstrap/admin regression tests

- catalog loading + blank import is sufficient for migrated channel startup
- registration is complete before catalog load / build / apply paths run
- admin channel update/toggle routes still persist the same plugin row and re-apply runtime
- persisted pre-migration config JSON can be loaded, updated through admin flows, persisted, and re-applied without payload drift
- no plugin-specific registration call is required in gateway startup for migrated plugins

### Legacy compatibility tests

- existing `settings_plugins` row IDs are unchanged
- existing config JSON payloads still decode for each migrated plugin
- admin channel list/status routes still work during mixed migration states

### Verification commands per implementation phase

- `mise run format`
- `mise run lint`
- targeted `go test` packages during phase work
- `mise run test` before phase completion when blast radius justifies full verification

## Risks

| Risk | Impact | Mitigation |
| ---- | ------ | ---------- |
| Service host grows into a god object | High | Add only narrow typed extension interfaces; do not expose DB/store directly unless a later phase proves it necessary. |
| Import cycles between generic internal runtime helpers and plugin packages | High | Extract generic support into plugin-agnostic packages and enforce one-way imports (`plugins/...` may import generic internals; generic internals never import concrete plugins). |
| Channel admin behavior drifts during migration | High | Keep plugin IDs/config payloads/status shape stable; migrate registration ownership first, then implementation files. |
| Discovery-driven cleanup lands before enough metadata exists | Medium | Introduce metadata/discovery in Phase 1 and only remove static lists after at least the channel family is metadata-backed. |
| Legacy registry retirement causes broad startup/hot-reload regressions | Medium | Retire adapters last, after host-native registration exists for all built-ins and regression coverage is in place. |
| Reflect migration introduces coupling pressure on host services | Medium | Delay reflect until channel path is proven; allow reflect plugin package to delegate to `internal/reflect/...` internals. |
| Cutting over without compatibility layers increases per-phase change size | Medium | Keep phases narrow and family-scoped; cut over one coherent ownership boundary at a time and require passing tests before moving to the next family. |
| Self-registration failures become harder to diagnose than static bootstrap wiring | Medium | Add startup diagnostics/logging for registered plugins by kind, metadata, and capabilities so missing blank imports or duplicate registrations fail loudly and are easy to trace. |

## Assumptions

- Channel runtime services can be expressed via narrow interfaces without exposing broad app internals to plugins.
- `cmd/anna/plugins_imports.go` remains the acceptable bootstrap point for built-in plugin blank imports.
- Existing admin/UI consumers can tolerate host metadata-driven backend changes as long as response payloads remain stable.
- Mixed migration states are acceptable temporarily, provided one plugin/family at a time has a clear source of truth.

## Open Questions

- [ ] User preference applied: do not preserve backward-compatible parallel code paths. Each migration phase should prefer deletion over compatibility scaffolding.

## Review Feedback

Pending reviewer feedback.

## Final Status

Not started.
