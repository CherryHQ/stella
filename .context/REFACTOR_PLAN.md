# Anna Refactor Plan

This document outlines the planned refactoring for improving code organization, reducing file sizes, and simplifying the package structure.

**Status: Phases 1-5 complete.** Phase 6 (test utilities) is optional.

## Overview

Current issues (resolved):
- ~~Large files (700+ lines) with multiple responsibilities~~
- ~~Deep package nesting in `ai/` requiring verbose imports~~
- ~~Some packages tightly coupled to parent packages~~

Goals:
- Files under 300 lines where practical
- Flatter package structure
- Clear separation of concerns
- Easier navigation and maintenance

---

## Phase 1: `ai/` Package Flattening

**Priority: High**
**Estimated effort: Medium**

### Current Structure
```
ai/
  types/
    events.go, events_test.go
    message.go, message_test.go
    model.go
    options.go
  providers/
    register_builtins.go
    anthropic/
      client.go, convert_messages.go, options.go, stream.go, stream_test.go
    openai/
      client.go, convert_messages.go, options.go, stream.go, stream_test.go
    openai-response/
      client.go, convert_messages.go, options.go, stream.go, stream_test.go
  registry/
    register.go, registry.go
  stream/
    event_stream.go, integration_test.go, stream.go
  transform/
    transform_messages.go, transform_messages_test.go
```

### Target Structure
```
ai/
  message.go        # Message, Content types (from types/message.go)
  message_test.go
  model.go          # Model, ModelCost types (from types/model.go)
  options.go        # RequestOptions (from types/options.go)
  events.go         # StreamEvent types (from types/events.go)
  events_test.go
  provider.go       # Provider interface, registry (merge registry/ + stream/)
  provider_test.go
  transform.go      # Message transforms (from transform/)
  transform_test.go
  anthropic/        # Keep provider implementations as subpackages
    anthropic.go    # Merge client.go + stream.go
    convert.go      # convert_messages.go
    options.go
    anthropic_test.go
  openai/
    openai.go
    convert.go
    options.go
    openai_test.go
  openai-response/
    openai_response.go
    convert.go
    options.go
    openai_response_test.go
```

### Changes
1. Move `ai/types/*.go` → `ai/*.go` (core types at package root)
2. Merge `ai/registry/` + `ai/stream/` → `ai/provider.go`
3. Move `ai/transform/` → `ai/transform.go`
4. Delete `ai/providers/register_builtins.go` (inline into provider.go)
5. Simplify provider subpackages (merge small files)

### Benefits
- Import `ai.Message` instead of `ai/types.Message`
- Remove 3 subpackages (types, registry, stream, transform)
- Clearer API surface

---

## Phase 2: `agent/pool.go` Split

**Priority: High**
**Estimated effort: Low**

### Current State
`agent/pool.go` - 776 lines containing:
- Pool struct and constructor
- Session management (Chat, get/create session)
- Runner lifecycle (reaper, idle timeout)
- Compaction logic
- Store integration

### Target Structure
```
agent/
  pool.go           # Pool struct, NewPool, Chat(), session CRUD (~300 lines)
  pool_options.go   # PoolOption funcs, CompactionConfig (~100 lines)
  pool_reaper.go    # StartReaper(), reapIdleSessions() (~150 lines)
  pool_compaction.go # triggerCompaction(), compactHistory() (~200 lines)
  session.go        # (existing, unchanged)
```

### Split Points
1. **pool_options.go**: Lines defining `PoolOption`, `CompactionConfig`, all `With*` functions
2. **pool_reaper.go**: `StartReaper()`, `reapIdleSessions()`, idle timeout logic
3. **pool_compaction.go**: `triggerCompaction()`, `compactHistory()`, compaction helpers

### Benefits
- Each file has single responsibility
- Easier to test compaction/reaper in isolation
- Matches project guideline (~300 lines max)

---

## Phase 3: `channel/cli/chat.go` Split

**Priority: Medium**
**Estimated effort: Medium**

### Current State
`channel/cli/chat.go` - 701 lines containing:
- Bubble Tea model and messages
- View rendering
- Input handling
- Model switching UI
- Session management

### Target Structure
```
channel/cli/
  chat.go       # RunChat(), main tea.Model, Update() (~250 lines)
  view.go       # View(), renderMessages(), formatters (~200 lines)
  input.go      # handleInput(), text area logic (~150 lines)
  model_picker.go # Model selection UI (~100 lines)
  style.go      # (existing, unchanged)
  command.go    # (existing, unchanged)
```

### Split Points
1. **view.go**: `View()` method, `renderMessages()`, message formatting helpers
2. **input.go**: Input handling in `Update()`, textarea management
3. **model_picker.go**: Model list UI, `/model` command handling

---

## Phase 4: Extract `store/` to Top-Level

**Priority: Medium**
**Estimated effort: Low**

### Current State
```
agent/store/
  store.go       # FileStore implementation
  store_test.go
  index.go       # Session index
  index_test.go
```

### Target Structure
```
store/
  store.go       # Store interface + FileStore
  store_test.go
  index.go
  index_test.go
```

### Changes
1. Move `agent/store/` → `store/`
2. Update imports in `agent/pool.go`, `cmd/anna/commands.go`

### Benefits
- Store is a cross-cutting concern, not agent-specific
- Flatter top-level structure
- Could be reused by other components

---

## Phase 5: `cron/cron.go` Split

**Priority: Low**
**Estimated effort: Low**

### Current State
`cron/cron.go` - 376 lines containing:
- Service struct (scheduler wrapper)
- Job struct and persistence
- Schedule parsing
- Cron tool integration

### Target Structure
```
cron/
  service.go    # Service struct, Start/Stop, scheduling (~150 lines)
  job.go        # Job struct, file I/O, SessionID() (~150 lines)
  schedule.go   # Schedule struct, parsing (~50 lines)
  tool.go       # (existing, unchanged)
```

---

## Phase 6: Consolidate Test Utilities (Optional)

**Priority: Low**
**Estimated effort: Low**

### Proposal
Create `internal/testutil/` for shared test helpers:
```
internal/
  testutil/
    config.go   # Test config helpers
    mock.go     # Common mocks
    assert.go   # Custom assertions
```

---

## Execution Order

1. **Phase 1: `ai/` flattening** - Most impactful, touches many files
2. **Phase 2: `agent/pool.go` split** - Quick win, no API changes
3. **Phase 3: `channel/cli/chat.go` split** - Improves TUI maintainability
4. **Phase 4: Extract `store/`** - Simple move
5. **Phase 5: `cron/` split** - Low priority
6. **Phase 6: Test utilities** - Optional cleanup

---

## Migration Notes

### Breaking Changes
- Phase 1 changes import paths: `ai/types` → `ai`
- Phase 4 changes import path: `agent/store` → `store`

### Non-Breaking
- Phases 2, 3, 5 are internal splits (no API changes)
- Phase 6 is test-only

### Testing Strategy
- Run full test suite after each phase
- Verify build with `mise run build`
- Run lint with `mise run lint`
