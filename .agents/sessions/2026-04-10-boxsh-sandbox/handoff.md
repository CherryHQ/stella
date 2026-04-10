# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1 — managed boxsh binary and config foundation

- **Status:** complete
- **What was done:**
  - added `boxsh` to the managed `mise` download flow
  - implemented managed-only boxsh resolution and Linux/macOS runner preflight in `internal/sandbox/`
  - moved sandbox network policy config to the per-agent config path via `settings_agents.sandbox`
  - added schema/query/sqlc updates, DB migration, config/store/snapshot plumbing, and validation on create/update
  - updated docs to describe the Phase 1 scope precisely and kept Windows on the existing runtime path
- **What changed:**
  - touched managed binary download/extraction, runner preflight, config/store/db layers, admin validation, generated SQLC models, migration files, and user/builtin docs
  - changed embedded tool extraction from a process-global `sync.Once` to per-destination guarded extraction so different `ANNA_HOME` targets remain safe in tests and startup flows
  - clarified docs that Phase 1 validates prerequisites/config but does not yet switch core tool execution to boxsh
- **Commits:**
  - `87fe422` — `✨ feat: add boxsh to managed tool downloads`
  - `67ac2c1` — `✨ feat: add boxsh phase 1 config and preflight`
- **Context for next phase:**
  - per-agent sandbox config now lives in `settings_agents.sandbox` and is available on `config.Snapshot.Sandbox`
  - runner preflight currently validates binary/config/workspace-state-dir shape only; actual boxsh RPC lifecycle and tool wiring are still Phase 2/3 work
  - tests use stub `boxsh` scripts in isolated `ANNA_HOME` dirs to avoid coupling runner tests to platform execution of the real embedded binary
- **Blockers:** none

## Phase 2 — boxsh RPC client and session model

- **Status:** complete
- **What was done:**
  - implemented `internal/sandbox/boxshclient/` package with process lifecycle and JSON-RPC transport
  - added tool RPC methods for `Exec`, `Read`, `Write`, `Edit`, `List`, and `Stat`
  - implemented `SharedBackend` for unified boxsh session shared by all four core tools
  - implemented session workspace helpers: `SessionManager`, sandbox root derivation, ephemeral DST creation
  - implemented response normalization layer to convert boxsh responses into Anna-compatible tool outputs
  - added comprehensive unit tests for all components (client, tools, backend, session, normalizer)
- **What changed:**
  - new package `internal/sandbox/boxshclient/` with:
    - `client.go`: JSON-RPC client, process lifecycle, session config
    - `tools.go`: Typed RPC methods for bash/read/write/edit operations
    - `backend.go`: SharedBackend for per-runner unified boxsh session
    - `session.go`: SessionManager and workspace root helpers
    - `normalize.go`: Response normalization for Anna tool compatibility
    - comprehensive test suite in `*_test.go` files
  - all Phase 2.1-2.5 tasks completed and marked in `tasks.md`
- **Commits:**
  - `e8824ad` — `✨ feat: implement boxsh RPC client with process lifecycle and JSON-RPC transport`
  - `8c5cda0` — `✨ feat: implement tool RPC methods for exec, read, write, edit`
  - `412a541` — `✨ feat: implement shared backend for unified boxsh session`
  - `7f3f016` — `✨ feat: implement session workspace helpers`
  - `837821d` — `✨ feat: implement response normalization for Anna tool compatibility`
  - `0a94e71` — `🧪 test: add comprehensive tests for boxshclient package`
- **Context for next phase:**
  - Phase 2 provides the full boxsh client infrastructure, but core tools still use direct Go execution
  - Phase 3 must wire the boxsh backend into `gorunner.go` tool construction for Linux/macOS
  - Windows continues using current backend (no changes needed)
  - Key integration points for Phase 3:
    - Update `buildToolRegistry()` in `gorunner.go` to use boxsh backend on Linux/macOS
    - Create boxsh-backed tool adapters wrapping the existing tool interfaces
    - Ensure shared backend lifecycle matches runner lifecycle (close on runner close)
    - Propagate backend health into runner `Alive()` status
- **Blockers:** none
- **Fixes after review:**
  - removed `Client.Start`/`Close` self-deadlocks by moving handshake and shutdown RPCs outside the lifecycle mutex and adding explicit `started`/`closing` state
  - replaced the shared response channel with per-request pending channels plus serialized stdin writes so concurrent tool calls cannot steal or drop each other's responses
  - added process-exit monitoring and subprocess-backed tests for successful start/close, handshake failure, dead-process detection, and out-of-order concurrent responses
