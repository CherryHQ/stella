# Handoff

## Goal

Remove the JSONL-based `store.Store` package and make `internal/memory.Engine` (SQLite) the single source of truth for all session data: events, metadata, and history. Then simplify the type conversion layers by replacing `RPCEvent` with `ai.Message` as the persistence and history format.

## Progress

- All 11 original tasks completed across 4 phases (store removal)
- Phase 5 added: eliminate RPCEvent from persistence layer
- PR created: https://github.com/vaayne/anna/pull/47
- Branch: `remove-store-package` (7 commits)

### Phase 1: Lossless storage + schema extension
- Added `event_type` column to messages table (`text`/`multimodal`/`tool_call`/`tool_result`)
- Structured JSON envelopes for tool events preserve IDs, tool names, args, error flags
- Raw Content JSON preserved for multimodal user messages
- `ingestEvent` wrapped in transaction for atomicity
- Conversations table extended with `channel`, `archived`, `last_active` columns
- PRAGMA `user_version` migration system (0→2 fresh, 1→2 ALTER with backfill)

### Phase 2: Engine methods
- `SessionInfo` type in `internal/memory/types.go`
- `SaveInfo`/`LoadInfo`/`ListInfo` for session metadata CRUD
- `Load` for full event history reconstruction from messages table

### Phase 3: Rewire agent package
- `SessionInfo` alias → `memory.SessionInfo` (was `store.SessionInfo`)
- Removed `Session.Events` in-memory event log
- Removed `store` field, `WithStore` option, `persist()`/`saveInfo()`/`touchLastActive()` helpers
- `Chat()` uses only `mem.Ingest()` + `mem.Assemble()` — no dual-write
- `CompactSession()` always delegates to memory engine (JSONL path removed)
- `cmd/anna` no longer creates FileStore

### Phase 4: Cleanup
- Deleted entire `store/` package (5 files, ~2,000 lines)
- Removed `SessionsPath()` from config
- Updated docs: architecture, compaction, config, deployment, README, builtin anna skill

### Phase 5: Replace RPCEvent with ai.Message (commit ad6f662)
- **Conversion chain reduced from 6 hops to 3:**
  - Before: `ai.Event → ai.Message → RPCEvent → DB → RPCEvent → ai.Message → SDK`
  - After: `ai.Event → ai.Message → DB → ai.Message → SDK`
- `internal/memory.Engine` interface now accepts/returns `ai.Message` (was `runner.RPCEvent`)
- `Runner.Chat` accepts `[]ai.Message` history directly (no `convertHistory` needed)
- `Event.Store` is `ai.Message` (was `*RPCEvent`)
- `Pool.History()` returns `[]ai.Message`
- `internal/memory/` no longer imports `agent/runner/` — cleaner dependency direction
- Deleted: `RPCEvent` type, 5 constructors, `convertHistory`, `decodeUserContent`, `eventToMessage`, `messageToRPCEvents`, `summaryToRPCEvent`, `ContentBlockJSON`, `AssistantMessageEvent`, `RPCCommand`
- Added: `messageToRows` (ai.Message → DB rows), `rowsToMessages` (DB rows → ai.Message), `mergeAssistantRows` (merges text + tool_call rows into single AssistantMessage)
- Net -431 lines across 14 files

## Key Decisions

- **`event_type` column over JSON sniffing** — explicit type discrimination is safer than parsing heuristics
- **Structured JSON envelopes** — `toolCallEnvelope` and `toolResultEnvelope` types store tool call/result data losslessly
- **PRAGMA user_version for migrations** — version 0→2 (fresh DB), version 1→2 (ALTER TABLE for existing DBs)
- **SQLite ALTER TABLE limitation** — `last_active` default uses constant `'1970-01-01 00:00:00'` in migration, then backfills from `created_at`
- **Remove sess.Events entirely** — no in-memory fallback; `mem.Assemble()` is the only context path
- **No JSONL migration tooling** — existing JSONL sessions become inaccessible (accepted trade-off)
- **context.Background() for metadata queries** — `activeSessionLocked`/`ActiveSession`/`ResolveSession` don't take context, use Background() internally
- **ai.Message over RPCEvent for persistence** — the engine loop already works with ai.Message natively; RPCEvent was an unnecessary intermediate format inherited from the old JSONL store
- **AssistantMessage → multiple DB rows** — a single AssistantMessage with text + N tool calls is split into N+1 rows (1 text + N tool_call); `mergeAssistantRows` re-merges them on load
- **RPCEvent kept for streaming only** — RPCEvent type was fully deleted since it's no longer needed; `runner.Event` carries text deltas and tool status for channel consumers

## Files Changed (Phase 5)

- `internal/memory/types.go` — Engine interface uses `ai.Message` instead of `runner.RPCEvent`
- `memory/engine.go` — `messageToRows`, `rowsToMessages`, `mergeAssistantRows`; deleted `eventToMessage`, `ingestEvent`
- `internal/memory/assembler.go` — returns `[]ai.Message`; `estimateMessageTokens` replaces `eventText`
- `agent/runner/runner.go` — deleted `RPCEvent`, constants, constructors; `Event.Store` is `ai.Message`; `Runner.Chat` takes `[]ai.Message`
- `agent/runner/gorunner.go` — deleted `convertHistory`, `decodeUserContent`; `Chat` passes history directly
- `agent/pool.go` — uses `ai.Message` for ingest/assemble/history
- `channel/cli/chat.go` — `renderResumedHistory` type-switches on `ai.Message`
- `*_test.go` — updated across all packages

## Current State

- All tests pass with `-race`, zero lint issues, clean build
- PR #47 open: https://github.com/vaayne/anna/pull/47
- Awaiting review/merge

## Next Steps

1. Review and merge PR #47
2. Manual smoke test: CLI chat session with tool use to verify end-to-end
3. Consider adding `go.sum` cleanup if `store` dependencies are now unused
