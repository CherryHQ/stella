# Tasks: Remove store.Store — SQLite-only session persistence

## Status Legend

- [ ] Pending
- [x] Completed

**Task States:** `PENDING` | `IMPLEMENTING` | `VALIDATING` | `REVIEWING` | `APPROVED`

## Phase 1: Lossless storage + schema extension

- [x] Task 1: Fix lossy event storage
  - **Files:** `memory/engine.go`, `internal/memory/assembler.go`, `internal/memory/types.go`, `internal/db/schemas/tables/messages.sql`, `db/queries/messages.sql`, `db/sqlc/*`, `memory/*_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Added event_type column (text/multimodal/tool_call/tool_result), stored structured JSON envelopes for tool events, wrapped ingestEvent in tx, dispatch on event_type in messageToRPCEvents
  - **Gotchas:** TextDeltaToRPCEvent (no Summary) events are silently skipped — by design, only final events are ingested. Legacy rows with event_type='text' default degrade gracefully.
  - **Commit:** 569e1a4

- [x] Task 2: Extend conversations table + migration
  - **Files:** `internal/db/schemas/tables/conversations.sql`, `db/queries/conversations.sql`, `memory/database.go`, `db/sqlc/*.go`, `memory/database_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Added channel/archived/last_active columns, PRAGMA user_version migration (0→2 fresh, 1→2 ALTER with backfill), 6 new SQLC queries
  - **Gotchas:** SQLite ALTER TABLE doesn't support datetime('now') defaults — used constant default + backfill from created_at for v1→v2 migration
  - **Commit:** 8948ef0

## Phase 2: Engine methods

- [x] Task 3: Add session metadata methods to Engine
  - **Files:** `internal/memory/types.go`, `memory/engine.go`, `memory/engine_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Added SessionInfo type, SaveInfo (upsert), LoadInfo, ListInfo methods using conversations table SQLC queries
  - **Gotchas:** SaveInfo uses separate UPDATE calls for title/archived/last_active to avoid overwriting unset fields
  - **Commit:** 14ac5fd

- [x] Task 4: Add Load method to Engine
  - **Files:** `memory/engine.go`, `memory/engine_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Load fetches all messages by conversation ID ordered by seq, converts each via messageToRPCEvents. Returns nil,nil for nonexistent sessions.
  - **Gotchas:** None
  - **Commit:** 14ac5fd

## Phase 3: Rewire agent package

- [x] Task 5: Update Pool to use memory.Engine exclusively
  - **Files:** `agent/pool.go`, `agent/pool_options.go`, `agent/session.go`, `agent/pool_test.go`, `agent/integration_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Removed store field, WithStore, persist/saveInfo/touchLastActive, sess.Events. All persistence via mem.SaveInfo/LoadInfo/ListInfo/Ingest/Assemble/Load. SessionInfo alias → memory.SessionInfo.
  - **Gotchas:** None
  - **Commit:** 2a5ccda

- [x] Task 6: Simplify compaction
  - **Files:** `internal/agent/pool_compaction.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Removed JSONL compaction path, collectFullResponse, compactionPrompt. CompactSession always delegates to compactSessionMemory. NeedsCompaction only uses mem.
  - **Gotchas:** None
  - **Commit:** 2a5ccda

- [x] Task 7: Update cmd/anna
  - **Files:** `cmd/anna/commands.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Removed store.NewFileStore creation, WithStore option, sessionsPath logic, store import.
  - **Gotchas:** None
  - **Commit:** 2a5ccda

## Phase 4: Cleanup

- [x] Task 8: Delete store/ package
  - **Files:** `store/store.go`, `store/index.go`, `store/store_test.go`, `store/index_test.go`, `store/real_pi_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Deleted entire store/ directory (~800 lines of JSONL code)
  - **Gotchas:** None — no remaining imports
  - **Commit:** da8719c

- [x] Task 9: Update agent tests
  - **Files:** `agent/pool_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Done as part of Task 5 — replaced store.NewFileStore/WithStore with WithMemoryEngine, replaced sess.Events checks with pool.History()
  - **Gotchas:** None
  - **Commit:** 2a5ccda

- [x] Task 10: Config cleanup
  - **Files:** `config/paths.go`, `config/config_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Removed SessionsPath() and its test assertions
  - **Gotchas:** None
  - **Commit:** da8719c

- [x] Task 11: Documentation
  - **Files:** `docs/architecture.md`, `docs/session-compaction.md`, `docs/configuration.md`, `docs/deployment.md`, `README.md`, `internal/agent/runner/builtin/anna/references/configuration.md`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Removed all JSONL/store references, updated to reflect SQLite-only persistence
  - **Gotchas:** None
  - **Commit:** 1942a7f

## Completion Summary

**Total Tasks:** 11
**Completed:** 11
**Remaining:** 0

### Final Notes

All 11 tasks completed successfully. The `store/` package has been removed and `internal/memory.Engine` (SQLite) is now the single source of truth for all session data. Key metrics:
- ~2,300 lines deleted (store package + JSONL compaction + dual-write code)
- ~900 lines added (lossless event storage, session metadata, migration, tests)
- Net reduction: ~1,400 lines
- All tests pass with `-race`
- Zero lint issues
