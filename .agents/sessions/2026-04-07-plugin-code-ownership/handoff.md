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

## Phase 2 — Telegram pilot registration cutover

- Status: complete
- What was done:
  - Added Telegram plugin self-registration under `plugins/channels/telegram/plugin.go` for metadata, config, runtime, and status.
  - Removed the Telegram-specific host registration shim and switched app bootstrap to rely on the plugin catalog + blank import for Telegram.
  - Kept Telegram runtime/config ownership in `internal/channel` for now, but removed the old implicit default constructor path so the plugin package now owns runtime construction.
  - Added regression tests covering self-registered Telegram completeness and apply/status behavior, and updated existing runtime tests to reflect the explicit channel factory requirement.
- What changed:
  - `plugins/channels/telegram/plugin.go`, `plugins/channels/telegram/plugin_test.go`
  - `internal/channel/telegram_plugin_runtime.go`, `internal/channel/telegram_plugin_runtime_test.go`
  - `cmd/anna/plugins_imports.go`, `cmd/anna/gateway.go`
  - `internal/admin/server_test.go`
  - deleted `internal/pluginhost/telegram.go`
  - `.agents/sessions/2026-04-07-plugin-code-ownership/tasks.md`
- Commits:
  - `d2859bfd` — `✨ feat: self-register telegram channel plugin`
  - `6dbe3b9c` — `♻️ refactor: cut over telegram bootstrap to self-registration`
  - `6bc5cee0` — `✅ test: cover self-registered telegram runtime behavior`
- Context for next phase:
  - Telegram registration is now owned exclusively by the plugin package; the next step is moving Telegram config/runtime/status ownership itself out of `internal/channel`.
  - `internal/channel.NewTelegramManagedRuntime` now requires an explicit `NewChannel` factory, which lets plugin-owned registration provide the concrete Telegram implementation without an import cycle.
  - Gateway still has explicit host-backed bootstrap knowledge for QQ/Feishu/Weixin and a direct Telegram `ApplyPlugin` call; discovery-driven apply cleanup remains for later phases.
- Blockers:
  - None.
