# Plan: Remove store.Store — SQLite-only session persistence

## Overview

Remove the JSONL-based `store.Store` package and make `internal/memory.Engine` (SQLite) the single source of truth for all session data: events, metadata, and history.

Currently, every event is dual-written to JSONL files and SQLite. The JSONL path preserves full RPCEvent fidelity (tool IDs, tool names, multimodal content) while the SQLite path is lossy. This plan fixes the SQLite path to be lossless, extends it with session metadata, then removes the JSONL path entirely.

### Goals

- Eliminate redundant dual-write persistence
- Single source of truth (SQLite) for session events and metadata
- Lossless RPCEvent round-tripping through the database
- Simplify the codebase by removing ~800 lines of JSONL code

### Success Criteria

- [ ] `store/` package deleted, zero references in codebase
- [ ] All session operations route through `internal/memory.Engine`
- [ ] RPCEvent round-trips losslessly (tool call IDs, tool names, error flags, multimodal content preserved)
- [ ] `mise run test` passes with `-race`
- [ ] `mise run lint` clean
- [ ] Docs updated to reflect new architecture

### Out of Scope

- Migration tooling for existing JSONL data
- Changes to runner or channel packages beyond removing `store` imports
- Wiring a real LLM summarizer (pre-existing TODO, not a regression)
- New features — pure infrastructure simplification

## Technical Approach

### Architecture

**Before:**
```
Pool → store.Append() → JSONL files     (full fidelity)
     → mem.Ingest()   → SQLite messages  (lossy)
     → mem.Assemble() ← SQLite           (for runner context)
     → store.Load()   ← JSONL files      (for history restore)

     sess.Events []RPCEvent              (in-memory fallback)
```

**After:**
```
Pool → mem.Ingest()   → SQLite messages  (full fidelity)
     → mem.Assemble() ← SQLite           (for runner context)
     → mem.Load()     ← SQLite           (for history restore)
```

### Key Design Decisions

1. **`event_type` column on messages table for type discrimination.** Rather than sniffing JSON content to detect structured tool envelopes (fragile — assistant text could start with `{`), add an `event_type TEXT NOT NULL DEFAULT 'text'` column with values: `text`, `tool_call`, `tool_result`. This makes the content format unambiguous at the schema level. `messageToRPCEvents` dispatches on `(role, event_type)` rather than parsing heuristics.

2. **Preserve multimodal content.** User messages with images currently lose their `Content` field (only `evt.Summary` text is stored). For user messages where `evt.Content` is non-nil, store the raw `Content` JSON in the content column and set `event_type` to `multimodal`. On load, detect this and reconstruct both `Summary` and `Content` fields.

3. **Session metadata lives in `conversations` table.** Add `channel`, `archived`, `last_active` columns rather than creating a separate table. The conversations table already maps 1:1 with sessions.

4. **`internal/memory.Engine` becomes required on Pool.** No optional/nil checks. The `store.Store` field and `WithStore` option are removed entirely.

5. **Remove `sess.Events` in-memory event log.** Currently Pool maintains `sess.Events` as an in-memory copy of all events, used as a fallback when `mem.Assemble()` fails. With SQLite as the sole persistence layer, this fallback is unnecessary and duplicates state. Remove the `Events` field from `Session` — the runner gets context exclusively from `mem.Assemble()`, and `History()` uses `mem.Load()`.

6. **Wrap `ingestEvent` in a transaction.** The single-event path currently does two separate queries (`CreateMessage` + `AppendContextItem`) without a transaction. With SQLite as the sole store, partial writes would be data loss. Wrap in a transaction like `IngestBatch` already does.

7. **Use `PRAGMA user_version` for schema migrations.** Instead of checking individual column existence for ALTER TABLE, use SQLite's built-in `user_version` pragma. Version 0 = fresh schema needed, version 1 = original schema (needs ALTERs), version 2 = current (full schema with new columns).

8. **ALTERs for existing databases.** The migration in `memory/database.go` adds columns via `ALTER TABLE` for databases at version 1. New databases get the full schema at version 2.

### Data Models

Messages table — new `event_type` column values:

| event_type | role | content format |
|---|---|---|
| `text` | user/assistant | Plain text string |
| `multimodal` | user | JSON: `[{"kind":"text","text":"..."},{"kind":"image","data":"...","mime_type":"..."}]` |
| `tool_call` | assistant | JSON: `{"id":"call_123","tool":"read_file","args":{"path":"/tmp/x"}}` |
| `tool_result` | tool | JSON: `{"id":"call_123","tool":"read_file","result":"file contents","error":""}` |

Conversations table — new columns:

| column | type | default |
|---|---|---|
| `channel` | TEXT NOT NULL | `''` |
| `archived` | INTEGER NOT NULL | `0` |
| `last_active` | TEXT NOT NULL | `datetime('now')` |

### APIs / Interfaces

New methods on `internal/memory.Engine`:
```go
SaveInfo(ctx context.Context, info SessionInfo) error
LoadInfo(ctx context.Context, sessionID string) (SessionInfo, error)
ListInfo(ctx context.Context, includeArchived bool) ([]SessionInfo, error)
Load(ctx context.Context, sessionID string) ([]runner.RPCEvent, error)
```

New type in `internal/memory/types.go`:
```go
type SessionInfo struct {
    ID         string
    Channel    string
    Title      string
    CreatedAt  time.Time
    LastActive time.Time
    Archived   bool
}
```

Removed type: `store.SessionInfo` (replaced by above)
Removed interface: `store.Store` (entire package deleted)

Removed from `Session` struct:
```go
// Before
type Session struct {
    Info   SessionInfo
    Events []runner.RPCEvent  // REMOVED
    Runner runner.Runner
    Model  string
}

// After
type Session struct {
    Info   SessionInfo
    Runner runner.Runner
    Model  string
}
```

### Context propagation

Several Pool methods (`activeSessionLocked`, `ActiveSession`, `ResolveSession`) currently don't take `context.Context` but will need it to call `mem.ListInfo(ctx, ...)`. Use `context.Background()` internally for these — they're quick metadata queries, not long-running operations. This avoids API churn on the Pool's public interface.

## Implementation Steps

### Phase 1: Lossless storage + schema extension

Foundation work — make SQLite capable of replacing JSONL.

1. **Fix lossy event storage** (files: `memory/engine.go`, `internal/memory/assembler.go`)
   - Add `event_type` column to messages table schema
   - Change `eventToRoleContent()` → `eventToMessage()` returning `(role, eventType, content)`
   - Store structured JSON for tool_call and tool_result events
   - Store raw `Content` JSON for multimodal user messages
   - Preserve `RPCEvent.Error` in tool_result envelopes
   - Change `messageToRPCEvents()` to dispatch on `(role, event_type)` and reconstruct full RPCEvents
   - Wrap `ingestEvent` in a transaction
   - Update `CreateMessage` SQLC query to include event_type
   - Add tests for round-trip fidelity (all event types including multimodal and error flags)

2. **Extend conversations table** (files: `internal/db/schemas/tables/conversations.sql`, `db/queries/conversations.sql`, `memory/database.go`)
   - Add `channel`, `archived`, `last_active` columns to schema
   - Add `event_type` column to messages schema
   - Implement `PRAGMA user_version` migration (0→2 fresh, 1→2 ALTERs)
   - Add SQLC queries: CreateConversationFull, UpdateConversationArchived, UpdateConversationLastActive, UpdateConversationTitle (by session_id), ListConversations, ListConversationsAll
   - Run `sqlc generate`

### Phase 2: Engine methods

Build the new Engine capabilities on top of Phase 1.

3. **Add session metadata methods to Engine** (files: `internal/memory/types.go`, `memory/engine.go`)
   - Define `SessionInfo` type in `internal/memory/types.go`
   - Implement `SaveInfo` — upsert into conversations (create or update channel/title/archived/last_active)
   - Implement `LoadInfo` — GetConversationBySessionID + map to SessionInfo
   - Implement `ListInfo` — ListConversations/ListConversationsAll + map
   - Update `getOrCreateConversation` to accept channel parameter for new conversations
   - Add tests

4. **Add Load method to Engine** (files: `memory/engine.go`)
   - Implement `Load()` — GetMessagesByConversation ordered by seq, convert each via `messageToRPCEvents`
   - Return nil, nil for non-existent sessions
   - Add tests verifying full event reconstruction with mixed event types

### Phase 3: Rewire agent package

Remove store.Store dependency, rewire Pool to use memory.Engine.

5. **Update Pool to use memory.Engine exclusively** (files: `agent/pool.go`, `agent/pool_options.go`, `internal/agent/pool_compaction.go`, `agent/session.go`)
   - Remove `store store.Store` field from Pool
   - Remove `persist()`, `saveInfo()`, `touchLastActive()` helpers
   - Remove `WithStore` option from pool_options.go
   - Remove `Events []runner.RPCEvent` from Session struct
   - `SessionInfo` type alias → `memory.SessionInfo`
   - `CreateSession` / `persistNewSession` → `mem.SaveInfo()`
   - `activeSessionLocked` → `mem.ListInfo(context.Background(), false)` replacing `store.ListInfo(false)`
   - `GetSession` → `mem.LoadInfo()`
   - `ListSessions` → `mem.ListInfo()`
   - `ArchiveSession` → `mem.LoadInfo()` + `mem.SaveInfo()` with archived=true
   - `Chat()` — remove `p.persist()` calls, remove `sess.Events` appends, remove in-memory fallback; `mem.Assemble()` is the only context path
   - `getOrCreateRunner()` — remove `store.Load()` history restore and `store.LoadInfo()` metadata restore; use `mem.LoadInfo()` for metadata
   - `History()` → `mem.Load()`
   - Auto-title in `Chat()` → `mem.SaveInfo()` to persist title
   - touchLastActive → `mem.SaveInfo()` to update last_active
   - Make `mem` required: remove nil checks, constructor validates non-nil

6. **Simplify compaction** (files: `internal/agent/pool_compaction.go`)
   - Remove JSONL compaction path — `CompactSession` always calls `compactSessionMemory`
   - Remove `collectFullResponse` helper (only used by deleted JSONL path)
   - `NeedsCompaction` — remove store fallback, only use `mem.NeedsCompaction()`

7. **Update cmd/anna** (files: `cmd/anna/commands.go`)
   - Remove FileStore creation, WithStore option, sessionsPath logic
   - Remove `store` import

### Phase 4: Cleanup

Remove dead code, fix tests, update docs.

8. **Delete store/ package** (files: `store/store.go`, `store/index.go`, `store/store_test.go`, `store/index_test.go`, `store/real_pi_test.go`)

9. **Update agent tests** (files: `agent/pool_test.go`)
   - Replace `store.NewFileStore` with in-memory SQLite memory.Engine
   - Remove tests that verified JSONL-specific behavior
   - Rewrite session lifecycle tests against memory.Engine
   - Update compaction tests (no JSONL compaction path)

10. **Config cleanup** (files: `config/paths.go`, `config/config_test.go`)
    - Remove `SessionsPath()` if no other consumers
    - Update config tests

11. **Documentation** (files: `docs/architecture.md`, `docs/session-compaction.md`, `README.md`, `internal/agent/runner/builtin/anna/`)
    - Remove JSONL/store references
    - Update architecture diagrams
    - Update builtin anna skill if it references session files

## Testing Strategy

### Unit Tests

- `memory/engine_test.go`: Round-trip — Ingest all event types (user text, user multimodal, assistant text, tool_call, tool_result with error), Load back, verify all RPCEvent fields match
- `memory/engine_test.go`: SaveInfo/LoadInfo/ListInfo — create, update title, update last_active, archive, list with/without archived
- `memory/engine_test.go`: Load — full history reconstruction with mixed event types
- `memory/engine_test.go`: ingestEvent transaction safety — verify no orphan messages on simulated failure
- `memory/assembler_test.go`: messageToRPCEvents — dispatch on event_type, backward compat with old rows (event_type defaults to 'text')

### Integration Tests

- `agent/pool_test.go`: Full Chat flow with memory-backed Pool — verify events queryable after chat
- `agent/pool_test.go`: Session lifecycle (create → chat → archive → list) all via memory.Engine
- `agent/pool_test.go`: Compaction with memory engine only
- `agent/pool_test.go`: History() returns full event log via memory.Engine

### Edge Cases

- Existing DB rows with old lossy content format (event_type='text', role='assistant', content like "toolname: {args}") — treated as plain assistant text, graceful degradation
- Empty sessions — Load returns nil, nil
- Concurrent session access — mutex coverage on new Engine methods
- User message with images — multimodal content preserved through round-trip
- Tool result with error flag — error field preserved through round-trip

## Considerations

### Performance

- `Load()` reads all messages for a session — fine for typical session sizes (<1000 messages). For very large sessions, the assembler with budget is used for runner context anyway.
- Session metadata queries indexed by `session_id` (UNIQUE constraint).
- `ListInfo` scans conversations table — acceptable at expected scale (hundreds, not millions).
- Removing `sess.Events` in-memory copy reduces memory usage for long sessions.

### Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Existing JSONL sessions become inaccessible | Certain | Medium | Accepted — out of scope. Users start fresh. |
| Old DB rows with lossy content (event_type defaults to 'text') | Medium | Low | `messageToRPCEvents` treats as plain text — graceful degradation |
| ALTER TABLE migration fails on corrupted DB | Low | Medium | `PRAGMA user_version` gating + idempotent ALTERs |
| Pool tests rely heavily on store.Store | Certain | Medium | Rewrite tests in Phase 4, task 9 |
| Removing sess.Events removes Assemble failure fallback | Low | Low | If Assemble fails, Chat returns error — better than silently using stale in-memory data |

### Open Questions

- [x] Backward compat for existing JSONL? → No, per V's instruction
- [x] Keep `SessionsPath()` config? → Remove if unused after store deletion
- [x] Content detection heuristic? → Use `event_type` column, not JSON sniffing
- [x] Multimodal content? → Store raw Content JSON with event_type='multimodal'
- [x] Keep sess.Events fallback? → No, remove entirely
- [x] Compaction summary quality? → Pre-existing TODO (StaticSummarizer), not a regression from this change

## Review Feedback

### Round 1

- 🔴 Content detection via JSON sniffing is fragile → **Fixed:** added `event_type` column for type discrimination
- 🔴 `collectFullResponse` removal eliminates LLM summary path → **Clarified:** compaction already uses `StaticSummarizer` (pre-existing TODO), not a regression
- 🟠 Multimodal content (images) silently dropped → **Fixed:** store raw Content JSON with event_type='multimodal'
- 🟠 RPCEvent.Error not preserved in tool_result envelope → **Fixed:** explicit error field in envelope, documented in data model
- 🟠 ingestEvent lacks transaction safety → **Fixed:** wrap in transaction (task 1)
- 🟠 sess.Events fallback behavior undefined → **Fixed:** remove sess.Events entirely (design decision 5)
- 🟠 activeSessionLocked needs context.Context → **Fixed:** use context.Background() internally (documented in Context propagation section)
- 🟡 ON CONFLICT for CreateConversation → Deferred: existing mutex protection sufficient, can optimize later
- 🟡 Use PRAGMA user_version → **Adopted** (design decision 7)

## Implementation Progress

(Updated during Phase 3)
