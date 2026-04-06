# Plan: Pluggable Memory System

## Overview

Replace anna's fragmented memory system with a unified, pluggable `memory.Provider` interface. The current system splits memory across 6 locations (`internal/memory/`, `internal/memory/tool/`, `internal/config/store.go`, `internal/agent/selfimprove/memorytool.go`, `internal/memory/usermemory.go`, `internal/memory/context.go`). The new design consolidates everything behind a single plugin interface with optional capabilities discovered via type assertion — the same pattern used by the hooks system.

Design doc: `design/pluggable-memory.md`

### Goals

- Define `memory.Provider` (5-method core) + 6 optional capability interfaces in `pkg/memory/`
- Create plugin registry in `plugins/memory/` following the existing hooks/tools/providers pattern
- Repackage the LCM engine as a plugin in `plugins/memory/lcm/` implementing all 6 capabilities
- Create a simple sliding-window plugin in `plugins/memory/simple/`
- Auto-generate the memory tool from provider capabilities via `memory.BuildTool()`
- Wire the provider into Pool, PoolManager, prompt builder, self-improve, and admin
- Delete all old code: `internal/memory/`, `internal/memory/tool/`, `UserMemoryStore`, `config.Store` memory methods

### Success Criteria

- [ ] `mise run test` passes with `-race`
- [ ] `mise run lint` clean
- [ ] LCM plugin passes `memorytest.RunConformance`
- [ ] Simple plugin passes `memorytest.RunConformance`
- [ ] Memory tool actions adapt to active plugin capabilities
- [ ] Self-improve loop uses `ReviewSource` interface (no raw SQL)
- [ ] Admin panel uses type assertions (no direct `memory.Engine`)
- [ ] `internal/memory/` directory deleted entirely
- [ ] `config.Store` no longer has `GetUserAgentMemory`/`SetUserAgentMemory`

### Out of Scope

- Vector search plugin implementation
- Multiple simultaneous memory plugins per session
- Database schema changes (LCM plugin reuses existing `ctx_*` tables)
- Changes to LLM providers, hooks, or channels

## Technical Approach

**Core pattern:** Optional capability interfaces discovered via Go type assertion, matching the hooks system (`PreToolCallHook`, `PostToolCallHook`, etc.). A plugin implements `Provider` as the base and optionally implements `Compactor`, `Searcher`, `Explorer`, `ProfileStore`, `SessionManager`, `ReviewSource`.

**Session struct:** Replaces scattered `WithSessionID`/`WithUserID`/`WithAgentID` context helpers with an explicit `Session{ID, AgentID, UserID, Channel}` parameter passed to all provider methods.

**Tool auto-generation:** `memory.BuildTool(provider)` inspects which capability interfaces the provider implements and generates a tool with exactly the matching actions. No stub methods, no "not supported" errors.

**Concurrency:** Provider owns same-session locking internally. Read-only methods (`Assemble`, `Stats`, `Search`, `GetProfile`) run lock-free. Mutation methods (`Append`, `Compact`, `SetProfile`) serialise per-session.

### Components

- **`pkg/memory/`** — Public interface package: `Provider`, 6 capability interfaces, all types (`Session`, `SessionStats`, `SearchQuery`, etc.), `BuildTool()`, `Summarizer` interface
- **`pkg/memory/memorytest/`** — `Fake` (in-memory, all capabilities) + `RunConformance(t, Provider)`
- **`plugins/memory/registry.go`** — `Register`, `Build`, `List`, `BuildContext`, `Registration`
- **`plugins/memory/lcm/`** — Current `internal/memory/` repackaged: `provider.go`, `compaction.go`, `assembler.go`, `retrieval.go`, `profile.go`, `sessions.go`, `review.go`, `engine.go`
- **`plugins/memory/simple/`** — Sliding window: `Provider` + `ProfileStore` + `SessionManager`
- **Updated callers** — `Pool`, `PoolManager`, `factory.go`, `prompt.go`, `selfimprove/review.go`, `selfimprove/prompts.go`, `admin/server.go`, `cmd/anna/commands.go`, `cmd/anna/gateway.go`

## Implementation Phases

### Phase 1: Interfaces, Types, and Test Infrastructure

Create the public contract and test tooling. No existing code touched.

1. Create `pkg/memory/provider.go` — `Provider` interface + all 6 capability interfaces (`Compactor`, `Searcher`, `Explorer`, `ProfileStore`, `SessionManager`, `ReviewSource`) (files: `pkg/memory/provider.go`)
2. Create `pkg/memory/types.go` — `Session`, `SessionStats`, `SearchQuery`, `SearchResult`, `SearchScope`, `CompactionMode`, `CompactionResult`, `SessionInfo`, `ListOptions`, `ReviewCandidate`, `DescribeResult`, `ExpandResult`, `ExpandMessage`, `ExpandChild`, `EstimateTokens()` (files: `pkg/memory/types.go`)
3. Create `pkg/memory/summarize.go` — Move `Summarizer` interface, `SummarizeOptions`, `BuildPrompt`, `LLMSummarizer`, `StaticSummarizer`, `deterministicFallback` from `internal/memory/summarize.go` (files: `pkg/memory/summarize.go`)
4. Create `pkg/memory/memorytest/fake.go` — In-memory `Fake` implementing all 7 interfaces (Provider + 6 capabilities) (files: `pkg/memory/memorytest/fake.go`)
5. Create `pkg/memory/memorytest/conformance.go` — `RunConformance(t, Provider)` testing Bootstrap idempotency, Append ordering, Assemble budget/freshTail, Stats accuracy, and all detected capabilities (files: `pkg/memory/memorytest/conformance.go`)
6. Verify compilation: `go build ./pkg/memory/...` (no existing files changed)

### Phase 2: Plugin Registry

Create the memory plugin registry following the existing hooks/tools/providers pattern.

1. Create `plugins/memory/registry.go` — `BuildContext` (with `SummarizerFn`), `Factory`, `ProviderMeta`, `Registration`, `Register()`, `Build()`, `List()` with `sync.RWMutex`-protected global map (files: `plugins/memory/registry.go`)
2. Add `PluginKindMemory = "memory"` to `internal/config/plugin.go` and add `"lcm"` to built-in plugin list (files: `internal/config/plugin.go`)
3. Add `memory/lcm` seed to `SeedDefaults()` in `internal/config/dbstore.go` (files: `internal/config/dbstore.go`)

### Phase 3: LCM Plugin

Move current `internal/memory/` engine into `plugins/memory/lcm/` as a plugin implementing all 6 capabilities. This is the bulk of the work — a refactor, not a rewrite.

1. Create `plugins/memory/lcm/plugin.go` — `init()` registration with `pluginmemory.Register("lcm", ...)` (files: `plugins/memory/lcm/plugin.go`)
2. Create `plugins/memory/lcm/provider.go` — `Provider` struct (db, queries, assembler, compaction, retrieval, sessionMu, convCache, globalMu, freshTail, log), `New()` constructor, `Name()`, `Bootstrap()`, `Append()` (from current `engine.go` Ingest/IngestBatch), `Assemble()`, `Stats()`, `Close()` (files: `plugins/memory/lcm/provider.go`)
3. Create `plugins/memory/lcm/engine.go` — Internal helpers: `withSessionLock()`, `getOrCreateConversation()`, `cacheConvID()`, `ingestMessage()`, message-to-row conversion (`messageToRows`, `rowsToMessages`, etc.) from current `engine.go` (files: `plugins/memory/lcm/engine.go`)
4. Create `plugins/memory/lcm/assembler.go` — `Assembler` struct, `Assemble()` method, `splitFreshTail()`, `resolveItems()`, `resolveItem()`, `FormatSummaryXML()`, `estimateMessageTokens()` from current `assembler.go` (files: `plugins/memory/lcm/assembler.go`)
5. Create `plugins/memory/lcm/compaction.go` — `CompactionEngine`, `Compact()`, `leafPass()`, `condensedPass()`, `compactMessageRun()`, `condenseSummaryRun()`, `generateSummaryID()` from current `compaction.go`. Implements `memory.Compactor` (files: `plugins/memory/lcm/compaction.go`)
6. Create `plugins/memory/lcm/retrieval.go` — `RetrievalEngine`, `Search()` implementing `memory.Searcher`, `Describe()`/`Expand()` implementing `memory.Explorer` from current `retrieval.go` (files: `plugins/memory/lcm/retrieval.go`)
7. Create `plugins/memory/lcm/profile.go` — `GetProfile()`/`SetProfile()` implementing `memory.ProfileStore`, backed by `ctx_agent_memory` table via sqlc (files: `plugins/memory/lcm/profile.go`)
8. Create `plugins/memory/lcm/sessions.go` — `SaveInfo()`/`LoadInfo()`/`ListInfo()`/`LoadHistory()` implementing `memory.SessionManager` from current Engine methods (files: `plugins/memory/lcm/sessions.go`)
9. Create `plugins/memory/lcm/review.go` — `BuildReviewContext()`/`MarkReviewed()`/`ListUnreviewed()` implementing `memory.ReviewSource` from current `selfimprove/review.go` `buildConversationText()` logic (files: `plugins/memory/lcm/review.go`)
10. Write LCM conformance test: `plugins/memory/lcm/provider_test.go` calling `memorytest.RunConformance()` (files: `plugins/memory/lcm/provider_test.go`)
11. Add blank import `_ "github.com/vaayne/anna/plugins/memory/lcm"` to `cmd/anna/plugins_imports.go` (files: `cmd/anna/plugins_imports.go`)

### Phase 4: Tool Auto-Generation

Replace both `internal/memory/tool/MemoryTool` and `internal/agent/selfimprove/memorytool.go` with `memory.BuildTool()`.

1. Create `pkg/memory/tool.go` — `BuildTool(provider, ...ToolOption) tools.Tool`, `ToolOption`, `WithReadOnlyProfile()`, `WithActionsOnly()`. Generates tool definition dynamically: always `status` action; `search` if `Searcher`; `describe`/`expand` if `Explorer`; `profile_get`/`profile_update` if `ProfileStore` (files: `pkg/memory/tool.go`)
2. Write tool tests: `pkg/memory/tool_test.go` — test with Fake (all actions), with bare Provider (only status), with `WithReadOnlyProfile` (no profile_update), with `WithActionsOnly` (files: `pkg/memory/tool_test.go`)

### Phase 5: Wire Into Callers

Update all consumers to use `memory.Provider` and type assertions. This phase changes the most files but each change is mechanical.

1. Update `internal/agent/pool.go` — Change `mem memory.Engine` → `mem memory.Provider`, remove `userMemory *memory.UserMemoryStore` field, update `NewPool()` signature (files: `internal/agent/pool.go`)
2. Update `internal/agent/pool_chat.go` — Replace `p.mem.Ingest()` → `p.mem.Append()`, `p.mem.Assemble()` now takes `memory.Session`, add `Compactor` type assertion for auto-compact (files: `internal/agent/pool_chat.go`)
3. Update `internal/agent/pool_compaction.go` — Use `memory.Compactor` type assertion, `memory.Session` param (files: `internal/agent/pool_compaction.go`)
4. Update `internal/agent/pool_runner.go` — Remove `UserMemoryStore` usage, pass `memory.Provider` into `DBPromptParams` instead (files: `internal/agent/pool_runner.go`)
5. Update `internal/agent/pool_manager.go` — Change `mem memory.Engine` → `mem memory.Provider`, remove `userMemory *memory.UserMemoryStore`, update `NewPoolManager()` and pool creation (files: `internal/agent/pool_manager.go`)
6. Update `internal/agent/factory.go` — Pass `memory.Provider` through to `DBPromptParams` (files: `internal/agent/factory.go`)
7. Update `internal/agent/runner/prompt.go` — Change `DBPromptParams.UserMemory string` → `DBPromptParams.Memory memory.Provider` + `UserID int64` + `AgentID string`. Use `ProfileStore` type assertion to load profile. Add `ctx context.Context` param to `BuildSystemPromptFromDB` (files: `internal/agent/runner/prompt.go`)
8. Update `internal/agent/selfimprove/review.go` — Change `ReviewDeps.DB *sql.DB` → `ReviewDeps.Memory memory.Provider`. Use `ReviewSource` type assertion for `ListUnreviewed`/`BuildReviewContext`/`MarkReviewed`. Use `memory.BuildTool(deps.Memory, memory.WithActionsOnly("profile_get", "profile_update"))` for reviewer (files: `internal/agent/selfimprove/review.go`)
9. Update `internal/agent/selfimprove/prompts.go` — Change tool action names from `review_memory get`/`update` to `profile_get`/`profile_update` (files: `internal/agent/selfimprove/prompts.go`)
10. Update `internal/admin/server.go` — Change `mem memory.Engine` → `mem memory.Provider`, use `SessionManager`/`Compactor` type assertions in handlers (files: `internal/admin/server.go`)
11. Update `cmd/anna/commands.go` — Build provider via `pluginmemory.Build("lcm", buildCtx)`, build tool via `memory.BuildTool(memProvider)`, pass provider to `NewPoolManager` and admin `New()` (files: `cmd/anna/commands.go`)
12. Update `cmd/anna/gateway.go` — Pass `memProvider` to `ReviewDeps.Memory` instead of raw `*sql.DB` (files: `cmd/anna/gateway.go`)

### Phase 6: Simple Plugin

Create the minimal alternative to validate the interface isn't over-fitted to LCM.

1. Create `plugins/memory/simple/plugin.go` — `init()` registration (files: `plugins/memory/simple/plugin.go`)
2. Create `plugins/memory/simple/provider.go` — `Provider` struct, `Name()`, `Bootstrap()`, `Append()`, `Assemble()` (sliding window), `Stats()`, `Close()`, plus `ProfileStore` and `SessionManager` implementations (files: `plugins/memory/simple/provider.go`)
3. Write simple conformance test: `plugins/memory/simple/provider_test.go` calling `memorytest.RunConformance()` (files: `plugins/memory/simple/provider_test.go`)
4. Add blank import `_ "github.com/vaayne/anna/plugins/memory/simple"` to `cmd/anna/plugins_imports.go` (files: `cmd/anna/plugins_imports.go`)
5. Seed `memory/simple` (disabled) in `SeedDefaults()` (files: `internal/config/dbstore.go`)

### Phase 7: Delete Old Code and Update Docs

Remove all replaced code and update documentation.

1. Delete `internal/memory/` directory entirely (files: `internal/memory/`)
2. Delete `internal/agent/selfimprove/memorytool.go` (files: `internal/agent/selfimprove/memorytool.go`)
3. Remove `GetUserAgentMemory`, `SetUserAgentMemory`, `ListUserMemories`, `DeleteUserAgentMemory` from `config.Store` interface and `DBStore` implementation (files: `internal/config/store.go`, `internal/config/dbstore.go`)
4. Update `docs/content/docs/core/memory-system.md` to document the new pluggable architecture (files: `docs/content/docs/core/memory-system.md`)
5. Update `docs/content/docs/core/architecture.md` package layout section (files: `docs/content/docs/core/architecture.md`)
6. Run `mise run format && mise run lint && mise run test` — fix any issues

## Testing Strategy

- **Conformance suite** (`memorytest.RunConformance`) validates every Provider implementation:
  - Bootstrap idempotency (call twice, no error)
  - Append ordering (messages come back in order)
  - Assemble budget (returned messages fit within budget)
  - Assemble freshTail (last N messages always included)
  - Stats accuracy (counts match appended messages)
  - Compactor: NeedsCompaction/Compact round-trip
  - Searcher: Search returns matching content
  - Explorer: Describe/Expand round-trip after compaction
  - ProfileStore: Get/Set round-trip, replace semantics
  - SessionManager: SaveInfo/LoadInfo/ListInfo round-trip
  - ReviewSource: ListUnreviewed/BuildReviewContext/MarkReviewed round-trip
- **Fake provider** for unit testing callers (Pool, prompt builder, self-improve)
- **LCM plugin tests** — conformance + existing `internal/memory/*_test.go` logic migrated
- **Simple plugin tests** — conformance (validates interface isn't LCM-specific)
- **Tool tests** — capability detection, action filtering, WithReadOnlyProfile
- **Integration** — `mise run test` with `-race` flag catches concurrency issues

## Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| LCM refactor introduces subtle bugs in compaction/assembly | High | Conformance tests + migrate existing test logic, not just structure |
| Self-improve prompt references old tool action names | Medium | Phase 5 task 9 explicitly updates `prompts.go` |
| Admin panel handlers assume `memory.Engine` directly | Medium | Phase 5 task 10 updates all admin handlers |
| `SummarizerFn` nil in test/simple contexts causes panic in LCM | Medium | LCM plugin checks for nil, falls back to `deterministicFallback` |
| Circular import between `pkg/memory` and `internal/db/sqlc` | High | `pkg/memory/` has NO sqlc dependency — only plugins import sqlc |
| Concurrent test failures from shared test DB | Medium | Each test uses isolated DB via `t.TempDir()` |

## Open Questions

None — all resolved in design doc and Codex review.

## Review Feedback

(Updated during plan review rounds)

## Final Status

(Updated after implementation completes)
