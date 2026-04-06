# Handoff

<!-- Append a new phase section after each phase completes. -->

## Phase 1: Interfaces, Types, and Test Infrastructure

**Status:** Complete

**Date:** 2026-04-06

### What was done

Created the public `pkg/memory/` package with all interfaces, types, and test infrastructure. No existing code was modified.

### Files created

- `pkg/memory/provider.go` — Core `Provider` interface (5 methods) + 6 optional capability interfaces (`Compactor`, `Searcher`, `Explorer`, `ProfileStore`, `SessionManager`, `ReviewSource`) + all supporting types (`SessionStats`, `CompactionMode`, `CompactionResult`, `SearchScope`, `SearchQuery`, `SearchResult`, `DescribeResult`, `ExpandResult`, `ExpandMessage`, `ExpandChild`, `ReviewCandidate`)
- `pkg/memory/types.go` — `Session`, `SessionInfo`, `ListOptions`, `EstimateTokens` helper, plus `MessageText`/`MessageRole`/`MessageTimestamp` helpers for extracting fields from `ai.Message` interface
- `pkg/memory/summarize.go` — `Summarizer` interface, `SummarizeOptions`, `SummarizePolicy` constants, `BuildPrompt`, `LLMSummarizer`, `StaticSummarizer`, `deterministicFallback` — moved from `internal/memory/summarize.go`
- `pkg/memory/memorytest/fake.go` — In-memory `Fake` implementing all 7 interfaces with map-based storage
- `pkg/memory/memorytest/conformance.go` — `RunConformance(t, Provider)` test suite covering Bootstrap idempotency, Append ordering, Assemble budget/freshTail, Stats accuracy, and all detected capabilities
- `pkg/memory/memorytest/conformance_test.go` — Runs conformance against `Fake` to validate the suite itself

### Commits

1. `4797ab4d` — `✨ feat: add memory.Provider interface and capability types`
2. `7333057b` — `✨ feat: add memory session and shared types`
3. `61f59633` — `✨ feat: add memory Summarizer interface and implementations`
4. `dd917d4e` — `✨ feat: add in-memory Fake provider for testing`
5. `7f5e9d49` — `✨ feat: add conformance test suite for memory providers`

### Verification

- `go build ./pkg/memory/...` — passes
- `go test -race ./pkg/memory/...` — 14 tests pass across 2 packages
- `mise run lint` — 0 issues

### Key decisions

- **`ai.Message` is an interface**, not a struct. Added `MessageText`, `MessageRole`, `MessageTimestamp` helpers to `types.go` since extracting fields requires type-switching across `UserMessage`, `AssistantMessage`, `ToolResultMessage`. These helpers will be used by the LCM plugin and other consumers.
- **Summarizer stays in `pkg/memory/`** (not `internal/`) so the LCM plugin (`plugins/memory/lcm/`) can import it without circular dependencies.
- **Conformance test uses shared state** across subtests (same provider, same session). ReviewSource test uses a future watermark (`time.Now() + 1min`) to ensure it's after all test message timestamps.
- **`FakeSummary` is exported** so test code outside the package can populate Explorer data via `AddSummary()`.

### Context for Phase 2

Phase 2 creates the plugin registry in `plugins/memory/registry.go` and adds `PluginKindMemory` to `internal/config/plugin.go`. The public interfaces are stable and ready for plugin implementations. The `Fake` provider can be used immediately for testing callers that accept `memory.Provider`.

## Phase 2: Plugin Registry and Config Integration

**Status:** Complete

**Date:** 2026-04-06

### What was done

Created the memory plugin registry and wired it into the config system. Three changes:

1. **`plugins/memory/registry.go`** — New package `pluginmemory` with Register/Build/List/Metas functions, protected by `sync.RWMutex`. `BuildContext` carries DB, AnnaHome, Config, and SummarizerFn. Follows the exact conventions of `plugins/tools/`, `plugins/hooks/`, and `plugins/providers/`.

2. **`internal/config/plugin.go`** — Added `PluginKindMemory = "memory"` constant, `builtinMemoryNames = []string{"lcm"}`, and included memory plugins in `BuiltinPluginIDs()`.

3. **`internal/config/dbstore.go`** — Added seed loop for `builtinMemoryNames` in `seedPlugins()`, using the same INSERT OR IGNORE pattern as tools/channels/hooks. Seeds `memory/lcm` as enabled with empty config.

### Files changed

- `plugins/memory/registry.go` (new)
- `internal/config/plugin.go` (modified)
- `internal/config/dbstore.go` (modified)

### Commits

1. `c281fd14` — `✨ feat: add memory plugin registry in plugins/memory`
2. `1a2e87fb` — `✨ feat: add PluginKindMemory constant and builtin memory plugin list`
3. `5b1449f0` — `✨ feat: seed memory/lcm plugin in SeedDefaults`

### Verification

- `go build ./...` — passes
- `mise run format` — clean
- `mise run lint` — 0 issues

### Context for Phase 3

The registry is ready for plugin implementations. Next phase should create the LCM plugin at `plugins/memory/lcm/` that calls `pluginmemory.Register("lcm", ...)` from `init()` and wraps the existing `internal/memory/` engine behind the `memory.Provider` interface. The `plugins/all.go` file will need an import for `_ "github.com/vaayne/anna/plugins/memory/lcm"` to trigger registration.

## Phase 3: LCM Plugin

**Status:** Complete

**Date:** 2026-04-06

### What was done

Repackaged the existing `internal/memory/` engine as a plugin at `plugins/memory/lcm/`. The `Provider` struct implements all 7 interfaces (core `Provider` + 6 capabilities: `Compactor`, `Searcher`, `Explorer`, `ProfileStore`, `SessionManager`, `ReviewSource`) with compile-time checks. All logic was ported from the existing code, not rewritten.

### Files created

- `plugins/memory/lcm/plugin.go` -- `init()` registration with `pluginmemory.Register("lcm", ...)`
- `plugins/memory/lcm/provider.go` -- `Provider` struct, `New()` constructor, `Name`, `Bootstrap`, `Append`, `Assemble`, `Stats`, `Close`, `NeedsCompaction`, `Compact`
- `plugins/memory/lcm/engine.go` -- Internal helpers: `withSessionLock`, `getOrCreateConversation`, `cacheConvID`, message conversion (`messageToRows`, `rowsToMessages`, etc.), constants
- `plugins/memory/lcm/assembler.go` -- `assembler` struct, `assemble` method, `splitFreshTail`, `resolveItems`, `resolveItem`, `FormatSummaryXML` (exported for use by review.go)
- `plugins/memory/lcm/compaction.go` -- `compactionEngine` struct, `compact`, `leafPass`, `condensedPass`, `compactMessageRun`, `condenseSummaryRun`, `generateSummaryID`
- `plugins/memory/lcm/retrieval.go` -- `retrievalEngine` struct, `Search` (Searcher), `Describe`/`Expand` (Explorer)
- `plugins/memory/lcm/profile.go` -- `GetProfile`/`SetProfile` (ProfileStore) backed by `ctx_agent_memory` via sqlc
- `plugins/memory/lcm/sessions.go` -- `SaveInfo`/`LoadInfo`/`ListInfo`/`LoadHistory` (SessionManager). `SaveInfo` enhanced to also update `agent_id`/`user_id` on existing conversations.
- `plugins/memory/lcm/review.go` -- `BuildReviewContext`/`MarkReviewed`/`ListUnreviewed` (ReviewSource) ported from `selfimprove/review.go`
- `plugins/memory/lcm/provider_test.go` -- Conformance test using `memorytest.RunConformance`

### Files modified

- `cmd/anna/plugins_imports.go` -- Added `_ "github.com/vaayne/anna/plugins/memory/lcm"` blank import

### Commits

1. `19f5def1` -- `✨ feat: add LCM plugin registration`
2. `de5b3dea` -- `♻️ refactor: port engine core to LCM provider`
3. `8cefeaf7` -- `♻️ refactor: port engine helpers and message conversion to LCM plugin`
4. `0d73d2a5` -- `♻️ refactor: port assembler to LCM plugin`
5. `4d85246e` -- `♻️ refactor: port compaction engine to LCM plugin`
6. `3d0583a1` -- `♻️ refactor: port retrieval engine to LCM plugin`
7. `b846f395` -- `♻️ refactor: implement ProfileStore on LCM provider`
8. `4f23a0ea` -- `♻️ refactor: implement SessionManager on LCM provider`
9. `15328210` -- `♻️ refactor: implement ReviewSource on LCM provider`
10. `c0305eef` -- `✨ feat: add LCM conformance test`
11. `bc7a6e57` -- `✨ feat: add LCM plugin blank import to trigger registration`

### Verification

- `go build ./...` -- passes
- `go test -race ./plugins/memory/lcm/...` -- 14 tests pass (all conformance subtests)
- `mise run format` -- clean
- `mise run lint` -- 0 issues

### Key decisions

- **`SaveInfo` now updates `agent_id`/`user_id`**: The old `engine.SaveInfo` only updated title/archived/last_active. The new implementation also updates `agent_id` and `user_id` when they differ, using the existing `UpdateConversationAgentUser` sqlc query. This fixes a gap where `Bootstrap` creates the conversation without agent metadata, and `SaveInfo` needed to fill it in.
- **`FormatSummaryXML` stays exported**: Used by both the assembler and the review module within the LCM plugin. Kept exported so it can also be used by callers that need to format summaries (e.g., the old `selfimprove/review.go` currently imports it from `internal/memory`).
- **Summarizer fallback**: When `SummarizerFn` is nil (e.g., in tests), the provider uses `StaticSummarizer{Response: ""}`. This means compaction produces empty summaries in tests, which is acceptable since the conformance test only verifies the compaction mechanics (non-nil result, no errors), not summary quality.
- **No `ownsDB` field**: The old `engine` had an `ownsDB` flag to optionally close the DB. The plugin always receives a shared DB from `BuildContext`, so `Close()` is a no-op. DB lifecycle is managed by the caller.
- **Constants kept internal**: All `kindLeaf`, `roleUser`, `itemTypeMessage` etc. are unexported package-level constants. Only `FormatSummaryXML` and `Provider`/`New` are exported.

### Context for Phase 4

The LCM plugin is complete and tested. Phase 4 creates `pkg/memory/tool.go` with `BuildTool(provider)` that inspects capabilities via type assertion and generates a tool with exactly the matching actions. The Fake provider from Phase 1 can be used for testing. The LCM plugin's `FormatSummaryXML` may need to be accessed by the tool for formatting describe/expand results.
