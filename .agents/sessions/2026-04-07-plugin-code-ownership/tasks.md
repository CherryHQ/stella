# Tasks: Plugin Code Ownership Migration

## Phase 1: Host metadata and service-extension scaffolding

- [ ] 1.1 — Add channel runtime service interfaces and host extension plumbing (`pkg/plugins/host.go`, `pkg/plugins/services_channel.go`, `internal/pluginhost/host.go`, `internal/pluginhost/service_extensions.go`)
- [ ] 1.2 — Add plugin metadata registration and host discovery APIs with the minimum schema needed for admin/gateway replacement (`pkg/plugins/metadata.go`, `internal/pluginhost/host.go`, `internal/pluginhost/metadata.go`, `internal/pluginhost/discovery.go`)
- [ ] 1.3 — Add registration completeness validation so metadata-declared managed/config/status plugins cannot exist in a partial registration state (`internal/pluginhost/host.go`, `internal/pluginhost/metadata.go`, related tests)
- [ ] 1.4 — Define merged built-in + persisted-state discovery semantics for admin/bootstrap (`internal/pluginhost/discovery.go`, admin/bootstrap tests as needed)
- [ ] 1.5 — Wire channel runtime services into bootstrap without changing behavior (`cmd/anna/commands.go`, `cmd/anna/gateway.go`)
- [ ] 1.6 — Add/adjust host tests for metadata, completeness validation, duplicate protection, merged discovery, and service extensions (`internal/pluginhost/host_test.go`, related new tests)

## Phase 2: Telegram pilot — registration ownership only

- [ ] 2.1 — Add Telegram plugin self-registration package entrypoint (`plugins/channels/telegram/plugin.go`)
- [ ] 2.2 — Remove Telegram-specific registration glue from host/bootstrap in the same cutover (`internal/pluginhost/telegram.go`, `cmd/anna/plugins_imports.go`, `cmd/anna/gateway.go`)
- [ ] 2.3 — Add regression coverage for self-registered Telegram runtime/config/status behavior (Telegram plugin/runtime tests)

## Phase 3: Telegram completion — plugin-owned config/runtime

- [ ] 3.1 — Extract generic managed channel runtime scaffolding into plugin-agnostic internal support (`internal/channelruntime/...` or refactored equivalents)
- [ ] 3.2 — Move Telegram config schema/defaults/validation/redaction into plugin package (`plugins/channels/telegram/config.go`, related callers/tests)
- [ ] 3.3 — Move Telegram managed runtime ownership into plugin package (`plugins/channels/telegram/runtime.go`, generic runtime support files)
- [ ] 3.4 — Remove Telegram-specific ownership from `internal/channel` (`internal/channel/telegram_plugin_runtime.go`, `internal/channel/config.go`, related tests)

## Phase 4: Remaining host-backed channels

- [ ] 4.1 — Migrate QQ to plugin-owned registration/config/runtime (`plugins/channels/qq/...`, remove `internal/pluginhost/qq.go`, `internal/channel/qq_plugin_runtime.go`)
- [ ] 4.2 — Migrate Feishu to plugin-owned registration/config/runtime (`plugins/channels/feishu/...`, remove `internal/pluginhost/feishu.go`, `internal/channel/feishu_plugin_runtime.go`)
- [ ] 4.3 — Migrate Weixin to plugin-owned registration/config/runtime (`plugins/channels/weixin/...`, remove `internal/pluginhost/weixin.go`, `internal/channel/weixin_plugin_runtime.go`)
- [ ] 4.4 — Add/adjust per-channel regression coverage for apply/status/bootstrap behavior (channel runtime tests, startup tests)

## Phase 5: Discovery-driven admin and gateway cleanup

- [ ] 5.1 — Inventory all remaining consumers of static host-backed channel knowledge and branch logic (`internal/admin/channels.go`, `internal/admin/plugins.go`, `cmd/anna/gateway.go`, `internal/channel/config.go`, related tests)
- [ ] 5.2 — Adopt merged host metadata + persisted-state discovery in admin without duplicate or phantom entries (`internal/admin/channels.go`, `internal/admin/plugins.go`, `internal/pluginhost/discovery.go`)
- [ ] 5.3 — Replace static host-backed channel branching with host metadata/discovery in admin and gateway (`internal/admin/channels.go`, `internal/admin/plugins.go`, `cmd/anna/gateway.go`)
- [ ] 5.4 — Replace gateway channel registration tables with host-driven managed plugin apply flow (`cmd/anna/gateway.go`)
- [ ] 5.5 — Standardize config validity / notify-enabled checks behind one host-driven accessor contract and remove obsolete static helpers (`internal/channel/host_backed.go`, `internal/channel/config.go`, related tests)

## Phase 6: Reflect plugin migration

- [ ] 6.1 — Add reflect plugin package entrypoint and registration (`plugins/runtimes/reflect/plugin.go`)
- [ ] 6.2 — Move reflect config/runtime registration ownership into plugin package using only explicit narrow host service interfaces while keeping core implementation in `internal/reflect/...` (`plugins/runtimes/reflect/config.go`, `plugins/runtimes/reflect/runtime.go`, remove `internal/pluginhost/reflect.go`)
- [ ] 6.3 — Add/update reflect runtime/status regression coverage (reflect host/runtime tests)

## Phase 7: Legacy registry retirement

- [ ] 7.1 — Migrate built-in providers to direct host registration and switch provider builds to host-native lookup (`plugins/providers/*/plugin.go`, `internal/pluginhost/builders.go`)
- [ ] 7.2 — Migrate built-in hooks to direct host registration and switch hook builds to host-native lookup (`plugins/hooks/*/plugin.go`, `internal/pluginhost/builders.go`)
- [ ] 7.3 — Migrate built-in memory providers to direct host registration and switch memory builds to host-native lookup (`plugins/memory/*/plugin.go`, `internal/pluginhost/builders.go`)
- [ ] 7.4 — Migrate remaining tool plugins to direct host registration, rewrite builders to use host-native capability lookup only, and remove compatibility adapters (`plugins/tools/*/plugin.go`, `internal/pluginhost/builders.go`, `internal/pluginhost/adapters.go`)
- [ ] 7.5 — Add regression coverage for host-native build/hot-reload behavior across plugin kinds after old paths are removed (plugin host/build tests)

## Phase 8: Final cleanup and docs sync

- [ ] 8.1 — Remove any remaining obsolete internal paths and finalize plugin-owned architecture cleanup (remaining obsolete internal files)
- [ ] 8.2 — Update plugin system documentation (`docs/content/docs/features/plugin-system.md`)
- [ ] 8.3 — Sync built-in anna skill documentation (`internal/agent/runner/builtin/anna/SKILL.md`)
- [ ] 8.4 — Run format, lint, and full tests; document final outcomes (`mise run format`, `mise run lint`, `mise run test`)
