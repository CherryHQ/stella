# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1 — Host metadata and service-extension scaffolding

- Status: complete
- What was done:
  - Added plugin metadata contracts and channel runtime service interfaces in `pkg/plugins`.
  - Added host-side metadata registration, completeness validation, merged discovery APIs, and mutable channel runtime service extensions in `internal/pluginhost`.
  - Wired the channel runtime service bag into bootstrap so runtime factories can consume gateway context/handler/notifier later without changing current channel behavior.
  - Added host tests for duplicate metadata protection, completeness failures, registered + persisted discovery semantics, and service extension access.
- What changed:
  - `pkg/plugins/host.go`, `pkg/plugins/metadata.go`, `pkg/plugins/services_channel.go`
  - `internal/pluginhost/host.go`, `internal/pluginhost/metadata.go`, `internal/pluginhost/discovery.go`, `internal/pluginhost/service_extensions.go`
  - `cmd/anna/commands.go`, `cmd/anna/gateway.go`
  - `internal/pluginhost/host_test.go`, `internal/pluginhost/discovery_test.go`, `plugins/tools/mcp/plugin_test.go`
  - `.agents/sessions/2026-04-07-plugin-code-ownership/tasks.md`
- Commits:
  - `65ade380` — `✨ feat: add plugin metadata and channel service contracts`
  - `31175bec` — `✨ feat: add plugin host metadata discovery and validation`
  - `7bbebe41` — `✨ feat: wire channel runtime services into bootstrap`
  - `9b641ef9` — `✅ test: cover host metadata discovery and service extensions`
- Context for next phase:
  - Phase 2 can register Telegram entirely from `plugins/channels/telegram` by using `host.Registry().RegisterMetadata(...)` plus `host.Services().ChannelRuntime()` for `ParentContext`, `Handler`, and `Notifications`.
  - `LoadCatalog()` now validates metadata-declared runtime/config/status completeness, so migrated plugins must register all declared capabilities in the same self-registration path.
  - `ListAdminVisiblePlugins()` merges registered admin-visible metadata with persisted plugin rows and preserves persisted row IDs in `PersistedID` for later admin/gateway adoption.
- Blockers:
  - None.

## Phase 2 — Telegram registration cutover

- Status: complete
- What was done:
  - Added Telegram plugin self-registration under `plugins/channels/telegram/plugin.go` for metadata, config, runtime, and status.
  - Removed the Telegram-specific host registration shim and switched app bootstrap to rely on the plugin catalog + blank import for Telegram.
  - Added regression tests covering self-registered Telegram completeness and apply/status behavior.
- What changed:
  - `plugins/channels/telegram/plugin.go`, `plugins/channels/telegram/plugin_test.go`
  - `cmd/anna/plugins_imports.go`, `cmd/anna/gateway.go`
  - `internal/admin/server_test.go`
  - deleted `internal/pluginhost/telegram.go`
  - `.agents/sessions/2026-04-07-plugin-code-ownership/tasks.md`
- Commits:
  - `d2859bfd` — `✨ feat: self-register telegram channel plugin`
  - `6dbe3b9c` — `♻️ refactor: cut over telegram bootstrap to self-registration`
  - `6bc5cee0` — `✅ test: cover self-registered telegram runtime behavior`
- Context for next phase:
  - Registration ownership is fully cut over to the plugin package.
  - The remaining Telegram-specific ownership move is config/runtime/test code out of `internal/channel`.
- Blockers:
  - None.

## Phase 3 — Telegram runtime/config ownership completion

- Status: complete
- What was done:
  - Promoted generic managed bot runtime scaffolding in `internal/channel/bot_runtime.go` into reusable exported support consumed by plugin-owned runtimes.
  - Moved Telegram config decode/redact/validate logic into `plugins/channels/telegram/config.go` and runtime ownership into `plugins/channels/telegram/runtime.go`.
  - Deleted `internal/channel/telegram_plugin_runtime.go` and moved Telegram runtime/config tests into the plugin package.
  - Added shared internal test helpers so QQ/Feishu/Weixin runtime tests no longer depended on Telegram test symbols.
- What changed:
  - `plugins/channels/telegram/config.go`, `plugins/channels/telegram/config_test.go`
  - `plugins/channels/telegram/runtime.go`, `plugins/channels/telegram/runtime_test.go`, `plugins/channels/telegram/test_helpers_test.go`
  - `plugins/channels/telegram/plugin.go`, `plugins/channels/telegram/plugin_test.go`
  - `internal/channel/bot_runtime.go`, `internal/channel/runtime_test_helpers_test.go`
  - `internal/channel/config.go`, `internal/channel/config_test.go`
  - `internal/channel/qq_plugin_runtime.go`, `internal/channel/feishu_plugin_runtime.go`, `internal/channel/weixin_plugin_runtime.go`
  - `internal/channel/host_backed.go`
  - deleted `internal/channel/telegram_plugin_runtime.go`, `internal/channel/telegram_plugin_runtime_test.go`
- Commits:
  - pending commit for Phase 3 cleanup
- Context for next phase:
  - Telegram is now cleanly owned under `plugins/channels/telegram` for registration, config, runtime, and plugin-specific tests.
  - `internal/channel` retains only generic managed runtime support plus shared channel config structs/accessors still used by current admin/gateway paths.
  - QQ/Feishu/Weixin can follow the same runtime ownership pattern in later phases.
- Blockers:
  - None.
