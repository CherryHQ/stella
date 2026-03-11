# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.2] - 2026-03-12

### ✨ Features

- **Channels**: Add /whoami command to all channels ([#44](https://github.com/vaayne/anna/pull/44))

**Full Changelog**: [v0.4.1...v0.4.2](https://github.com/vaayne/anna/compare/v0.4.1...v0.4.2)

## [0.4.1] - 2026-03-11

### ♻️ Refactoring

- Flatten packages, split large files, and improve structure ([#43](https://github.com/vaayne/anna/pull/43))

**Full Changelog**: [v0.4.0...v0.4.1](https://github.com/vaayne/anna/compare/v0.4.0...v0.4.1)

## [0.4.0] - 2026-03-11

### ✨ Features

- **CLI**: Add `version` and self-upgrade commands ([#41](https://github.com/vaayne/anna/pull/41))
- **Gateway**: Add heartbeat support for gateway channels ([#40](https://github.com/vaayne/anna/pull/40))
- **Config**: Add `enabled` and `enable_notify` toggles for channels

### 🐛 Bug Fixes

- **Notifications**: Respect channel `enabled` flag for notification routing
- **Gateway**: Skip persisted cron jobs for heartbeat-only gateway runs
- **Upgrade**: Harden self-upgrade target replacement

### 📝 Documentation

- **CLI**: Document version and upgrade commands

**Full Changelog**: [v0.3.0...v0.4.0](https://github.com/vaayne/anna/compare/v0.3.0...v0.4.0)

## [0.3.0] - 2026-03-10

### ✨ Features

- **Feishu**: Add Feishu (Lark) bot channel with WebSocket, streaming cards, image I/O, rich text, and group support ([#39](https://github.com/vaayne/anna/pull/39))
- **Skills**: Add builtin anna skill embedded in binary ([#37](https://github.com/vaayne/anna/pull/37))

### ♻️ Refactoring

- Add Channel interface and extract shared command/util logic ([#38](https://github.com/vaayne/anna/pull/38))

**Full Changelog**: [v0.2.0...v0.3.0](https://github.com/vaayne/anna/compare/v0.2.0...v0.3.0)

## [0.2.0] - 2026-03-09

### ✨ Features

- **QQ Bot**: Add QQ Bot channel support ([#33](https://github.com/vaayne/anna/pull/33))
- **Onboard**: Add onboard subcommand with web-based setup UI ([#36](https://github.com/vaayne/anna/pull/36))
- **Telegram**: Image input support ([#35](https://github.com/vaayne/anna/pull/35))
- **Telegram**: Tool summary with history, timing, and result status ([#32](https://github.com/vaayne/anna/pull/32))
- **Tools**: Add webfetch tool for fetching web pages as markdown ([#28](https://github.com/vaayne/anna/pull/28))

### 🐛 Bug Fixes

- **Telegram**: Use channel-scoped UUID sessions for /new command ([#31](https://github.com/vaayne/anna/pull/31))

### ♻️ Refactoring

- Flatten package structure and merge runner packages ([#27](https://github.com/vaayne/anna/pull/27))
- Flatten config structure and move workspace to ~/.anna ([#26](https://github.com/vaayne/anna/pull/26))

**Full Changelog**: [v0.1.0...v0.2.0](https://github.com/vaayne/anna/compare/v0.1.0...v0.2.0)

## [0.1.0] - 2026-03-07

### ✨ Features

- **CI/CD**: Add CI/CD workflows, Docker, and release infrastructure (#25)
- **Skills**: Native skill management tool (#23)
- **Cron**: Add one-time scheduled jobs (#20)
- **Telegram**: Add streaming draft support (#18)
- **Telegram**: Paginated model list with text filter
- **Telegram**: Notification channel, group support & model switch fix (#13)
- **Telegram**: Enhance bot UX (#12)
- **Telegram**: Add allowed_ids access control
- **Tools**: Truncate large tool outputs to temp file (#9)
- **Models**: Tiered model config (strong/worker/fast) (#10)
- **Session**: Session compaction with LLM-generated handoff summaries (#4)
- **Memory**: Persistent memory system with consolidated file layout (#3)
- **Context**: Support AGENTS.md project context files in system prompt
- **Cron**: Cron scheduling system (#2)

### 🐛 Bug Fixes

- **Docker**: Support multi-platform builds with TARGETARCH/TARGETOS
- **CI**: Resolve 50 errcheck lint issues and add coverage reporting (#26)
- **Telegram**: Notification SendOptions bug and missing callback updates
- **Telegram**: Callback guard in groups and cron startup race
- **Core**: Nil sender panic and notify tool in CLI mode

### ♻️ Refactoring

- Fix remaining lint issues (gocritic, gofmt, staticcheck, ineffassign)
- Multi-backend notification dispatcher
- Remove Pi runner (agent/runner/pi) (#8)
- Move restart-gateway.sh to mise file-based task

### 📝 Documentation

- Restructure README and docs for maintainability
- Add notification system design doc
- Clarify exclusive scope for memory files to prevent duplication (#21)

### 📦 Dependencies

- Bump github.com/cloudflare/circl from 1.6.1 to 1.6.3 (#24)

**Full Changelog**: [v0.1.0](https://github.com/vaayne/anna/commits/v0.1.0)
