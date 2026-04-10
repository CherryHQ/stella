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
