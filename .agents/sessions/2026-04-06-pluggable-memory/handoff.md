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

## Phase 4: Tool Auto-Generation

**Status:** Complete

**Date:** 2026-04-06

### What was done

Created `BuildTool(provider Provider, opts ...ToolOption) tools.Tool` in `pkg/memory/tool.go` that inspects provider capabilities via type assertion and dynamically generates a memory tool. The tool's JSON Schema, description, and Execute dispatch all adapt to the provider's actual capabilities.

### Files created

- `pkg/memory/tool.go` -- `BuildTool`, `ToolOption`, `WithReadOnlyProfile`, `WithActionsOnly`, `memoryTool` (unexported) implementing `tools.Tool`. Dynamic schema/description generation, Execute dispatch to status/search/describe/expand/profile_get/profile_update.
- `pkg/memory/tool_test.go` -- 16 tests covering full provider (all 6 actions), bare provider (status only), `WithReadOnlyProfile`, `WithActionsOnly`, Execute for every action, and error paths (missing args, missing context, unknown action).

### Files modified

- `pkg/memory/types.go` -- Added context helpers (`WithSessionID`/`SessionIDFromContext`, `WithUserID`/`UserIDFromContext`, `WithAgentID`/`AgentIDFromContext`) so the tool can extract session identity from context. Mirrors `internal/memory/context.go` but lives in the public package.

### Commits

1. `6a4fb037` -- `✨ feat: add BuildTool for dynamic memory tool generation`
2. `173636a4` -- `✨ feat: add tool auto-generation tests`

### Verification

- `go build ./pkg/memory/...` -- passes
- `go test -race ./pkg/memory/...` -- 30 tests pass across 2 packages
- `mise run format` -- clean
- `mise run lint` -- 0 issues

### Key decisions

- **Context helpers duplicated to `pkg/memory/`**: The tool needs `UserIDFromContext` and `AgentIDFromContext` to dispatch profile operations. Rather than importing `internal/memory/` (which will be deprecated), added equivalent helpers to `pkg/memory/types.go`. The `internal/memory/context.go` versions still exist for backward compatibility until Phase 7 cleanup.
- **`sessionFromContext` helper**: Builds a `Session` from context values for `Stats` and `Search` calls. This is a bridge pattern — callers that already set context values (the current runner) can use the tool without changes. Future callers will pass Session explicitly.
- **No `FormatSummaryXML` dependency**: The tool delegates formatting entirely to the provider's `Describe`/`Expand` return types and marshals them to JSON. No LCM-specific formatting in the generic tool.
- **`bareProvider` test type**: A minimal struct implementing only `Provider` (5 methods) to verify the tool works correctly with zero optional capabilities — only `status` action available.
- **`intArg` helper duplicated**: The `internal/memory/tool/helpers.go` version handles JSON number types (float64 from `json.Unmarshal`). Replicated in `pkg/memory/tool.go` to avoid importing internal packages.

### Context for Phase 5

The tool is ready for integration. Phase 5 should wire `BuildTool` into the pool/runner so it replaces the current `MemoryTool`. The existing `internal/memory/tool/memory.go` can be deprecated once callers switch to `memory.BuildTool(provider)`. The context helpers in `pkg/memory/types.go` are compatible with the existing context values set by the runner.

## Phase 5: Wire Provider into All Callers

**Status:** Complete

**Date:** 2026-04-06

### What was done

Replaced `memory.Engine` (from `internal/memory`) with `memory.Provider` (from `pkg/memory`) across all callers. Every method that was previously called directly on the Engine is now accessed either via the core Provider interface or via type assertions for optional capabilities.

### Files modified

- `internal/agent/pool.go` — `mem memory.Engine` → `mem memory.Provider`, removed `userMemory` field
- `internal/agent/pool_options.go` — Removed `WithUserMemory` option
- `internal/agent/pool_chat.go` — Build `memory.Session` at start, `Ingest` → `Append`, `Assemble` takes `Session`, `SaveInfo` via `SessionManager` type assertion
- `internal/agent/pool_compaction.go` — `Compact`/`NeedsCompaction` via `Compactor` type assertion
- `internal/agent/pool_runner.go` — Removed `UserMemoryStore` loading, pass `memory.Provider` + `AgentID` in `RunnerParams`, `LoadInfo`/`Bootstrap` via type assertions
- `internal/agent/pool_session.go` — All `SaveInfo`/`LoadInfo`/`ListInfo`/`Load` via `SessionManager` type assertion
- `internal/agent/pool_manager.go` — `mem memory.Engine` → `mem memory.Provider`, removed `userMemory` field and `NewUserMemoryStore` call
- `internal/agent/factory.go` — Extract `memory.Provider` from `RunnerParams.Memory`, pass to `DBPromptParams`
- `internal/agent/runner/runner.go` — `RunnerParams.UserMemory string` → `RunnerParams.Memory any` + `AgentID string`
- `internal/agent/runner/prompt.go` — `DBPromptParams` now has `Memory memory.Provider` + `UserID` + `AgentID` instead of `UserMemory string`. `BuildSystemPromptFromDB` takes `ctx context.Context`. Profile loaded via `ProfileStore` type assertion
- `internal/agent/runner/gorunner.go` — Updated fallback `BuildSystemPromptFromDB` call with `context.Background()`
- `internal/agent/selfimprove/review.go` — `ReviewDeps.DB *sql.DB` → `ReviewDeps.Memory memory.Provider`. Uses `ReviewSource` for `ListUnreviewed`/`BuildReviewContext`/`MarkReviewed`. Uses `memory.BuildTool` for reviewer tool. Removed `buildConversationText` and `appendMessages` (logic now in LCM plugin's `ReviewSource`)
- `internal/agent/selfimprove/reviewer.go` — `toolNameMemory = "review_memory"` → `"memory"`, action `"update"` → `"profile_update"`
- `internal/agent/selfimprove/prompts.go` — Updated prompt: `review_memory` → `memory tool`, action names `get`/`update` → `profile_get`/`profile_update`
- `internal/agent/session.go` — Import changed from `internal/memory` to `pkg/memory`
- `internal/admin/server.go` — `mem memory.Engine` → `mem memory.Provider`
- `internal/admin/sessions.go` — All session operations via `SessionManager` type assertion. Profile loading via `memory.Provider` in `BuildSystemPromptFromDB`
- `cmd/anna/commands.go` — Build provider via `pluginmemory.Build("lcm", ...)`, tool via `memory.BuildTool(memProvider)`. Removed `memory.NewEngineFromDB` and `memory.NewUserMemoryStore`
- `cmd/anna/gateway.go` — `ReviewDeps.DB` → `ReviewDeps.Memory`

### Test files modified

- `internal/agent/pool_test.go` — `testMemoryEngine` → `testMemoryProvider` using LCM plugin, all direct mem calls use `SessionManager` type assertion
- `internal/agent/pool_manager_test.go` — Use `testMemoryProvider`
- `internal/agent/integration_test.go` — Use `testMemoryProvider`
- `internal/agent/runner/skill_test.go` — Add `context.Background()` to `BuildSystemPromptFromDB` calls
- `internal/agent/selfimprove/review_test.go` — Removed `buildConversationText` tests (logic moved to LCM plugin)
- `internal/agent/selfimprove/reviewer_test.go` — Updated action names in test data (`"update"` → `"profile_update"`, `"review_memory"` → `"memory tool"`)
- `internal/agent/selfimprove/testhelper_test.go` — Removed unused `setupTestDB`, kept `containsSubstring`
- `internal/admin/server_test.go` — Use LCM plugin provider instead of `memory.NewEngineFromDB`
- `internal/channel/cli/cli_test.go` — Use LCM plugin provider instead of `memory.NewEngine`

### Commits

1. `ec70db02` — `♻️ refactor: wire memory.Provider into all callers, replacing memory.Engine`

### Verification

- `go build ./...` — passes
- `mise run format` — clean
- `mise run lint` — 0 issues
- `go test -race ./internal/agent/... ./internal/admin/... ./cmd/anna/...` — 255 tests pass, 0 failures

### Key decisions

- **Single commit for all tasks**: All 12 tasks were done in one commit because the changes are tightly coupled — changing `pool.go` requires changing `pool_chat.go`, `pool_runner.go`, `factory.go`, `prompt.go`, etc. to compile. Splitting would create non-compiling intermediate states.
- **`RunnerParams.Memory` typed as `any`**: The `runner` package cannot import `pkg/memory` without creating a dependency from the runner package on the memory package. Using `any` with a type assertion in the factory avoids this. The factory (in `internal/agent`) does the assertion.
- **`BuildSystemPromptFromDB` takes `ctx`**: Required because `ProfileStore.GetProfile` needs a context. All callers updated. The fallback in `gorunner.go` uses `context.Background()`.
- **Test helpers use LCM plugin**: Tests that previously used `memory.NewEngine` (creating an Engine directly) now use `pluginmemory.Build("lcm", ...)` to get a `memory.Provider`. This exercises the real plugin path and ensures the LCM plugin works correctly as a Provider.
- **`buildConversationText` removed from review.go**: The logic is now entirely inside the LCM plugin's `BuildReviewContext` method. Tests that tested this function were removed since they're now covered by the LCM plugin's own tests.

### Context for Phase 6

Phase 6 should update documentation and the builtin anna skill to reflect the new memory tool action names (`profile_get`/`profile_update` instead of `user_memory_get`/`user_memory_update`). The admin memory management endpoints (`/api/users/{id}/memories/*` and `/api/auth/profile/memories/*`) currently go through `config.Store.GetUserAgentMemory`/`SetUserAgentMemory` — Phase 6 or 7 should migrate these to use `ProfileStore` on the memory provider.

## Phase 6: Simple Plugin

**Status:** Complete

**Date:** 2026-04-06

### What was done

Created the minimal Simple memory plugin at `plugins/memory/simple/`. It implements `Provider` + `ProfileStore` + `SessionManager` (3 of 7 interfaces), validating that the memory interface is not over-fitted to LCM. The plugin uses a sliding-window Assemble algorithm: return the last N messages that fit within the token budget, always honouring freshTail. No summaries, no compaction, no search, no explore, no review.

### Files created

- `plugins/memory/simple/plugin.go` — `init()` registration with `pluginmemory.Register("simple", ...)`
- `plugins/memory/simple/provider.go` — `Provider` struct implementing `memory.Provider` + `memory.ProfileStore` + `memory.SessionManager`. Includes message conversion helpers duplicated from LCM (same storage format for compatibility).
- `plugins/memory/simple/provider_test.go` — Conformance test using `memorytest.RunConformance`

### Files modified

- `cmd/anna/plugins_imports.go` — Added `_ "github.com/vaayne/anna/plugins/memory/simple"` blank import
- `internal/config/plugin.go` — Added `"simple"` to `builtinMemoryNames`
- `internal/config/dbstore.go` — Seeds `memory/simple` with `enabled=0` (disabled by default; lcm remains the default)

### Commits

1. `ee0fab2b` — `✨ feat: add simple memory plugin registration`
2. `016550cd` — `✨ feat: implement simple sliding-window memory provider`
3. `8a22f225` — `✨ feat: add simple provider conformance test`
4. `abc17b81` — `✨ feat: add simple memory plugin blank import to trigger registration`
5. `3dc7175d` — `✨ feat: seed memory/simple plugin as disabled by default`

### Verification

- `go build ./...` — passes
- `go test -race ./plugins/memory/simple/...` — 10 tests pass (all conformance subtests for Provider + ProfileStore + SessionManager; Compactor/Searcher/Explorer/ReviewSource correctly skipped)
- `mise run format` — clean
- `mise run lint` — 0 issues

### Key decisions

- **Same storage format as LCM**: Messages are stored using the same `ctx_messages` schema with identical `messageToRows`/`rowsToMessages` conversion. This means switching between LCM and Simple preserves all stored messages. The only difference is Simple does not write `ctx_items` or `ctx_summaries`.
- **Message conversion duplicated, not shared**: The message conversion helpers (`messageToRows`, `rowsToMessages`, `estimateMessageTokens`) are duplicated from the LCM plugin rather than extracted to a shared package. This keeps the two plugins fully independent — changes to LCM's storage format don't break Simple, and vice versa. The duplication is ~200 lines of straightforward serialization code.
- **Token counting from messages directly**: Stats sums `token_count` from `ctx_messages` rows rather than using `GetContextTokenCount` (which queries `ctx_items`). Simple doesn't write context items, so the LCM query would always return 0.
- **Disabled by default**: `memory/simple` is seeded with `enabled=0`. Users must explicitly enable it and disable `memory/lcm` to switch.
- **No config options**: The simple plugin has no configurable parameters. The factory ignores `BuildContext.Config` and `BuildContext.SummarizerFn`.

### Context for Phase 7

Phase 7 should update documentation and the builtin anna skill to reflect the new memory tool action names (`profile_get`/`profile_update` instead of `user_memory_get`/`user_memory_update`). The admin memory management endpoints should migrate to use `ProfileStore` on the memory provider. The old `internal/memory/` package can begin deprecation now that both LCM and Simple plugins are complete and tested.

## Phase 7: Delete Old Code and Update Docs

**Status:** Complete

**Date:** 2026-04-06

### What was done

Deleted `internal/memory/` (engine, tool, usermemory, context, all tests — ~6600 lines). Deleted `internal/agent/selfimprove/memorytool.go` and its test. Removed `UserAgentMemory` type and 4 memory methods from `config.Store` interface and `DBStore`. Migrated admin endpoints (`profile.go`, `users.go`) to use `sqlc.Queries` directly. Switched `gorunner.go` from `internal/memory` to `pkg/memory`. Updated `admin/tools.go` to use `memory.BuildTool(provider)`. Updated all docs and the builtin anna skill.

### Files deleted

- `internal/memory/` — entire directory (16 files: engine, assembler, compaction, retrieval, summarize, types, context, usermemory, tool/, all tests)
- `internal/agent/selfimprove/memorytool.go` + `memorytool_test.go`

### Files modified

- `internal/config/store.go` — Removed `UserAgentMemory` type, `GetUserAgentMemory`, `SetUserAgentMemory`, `ListUserMemories`, `DeleteUserAgentMemory` from interface
- `internal/config/dbstore.go` — Removed implementations of the 4 memory methods
- `internal/config/dbstore_test.go` — Removed `TestUserAgentMemory`
- `internal/agent/pool_manager_test.go` — Removed mock implementations of removed Store methods
- `internal/agent/runner/gorunner.go` — `internal/memory` -> `pkg/memory` import
- `internal/admin/tools.go` — `memorytool.MemoryDefinition()` -> `memory.BuildTool(s.mem).Definition()`
- `internal/admin/profile.go` — `s.store.{List,Set,Delete}UserAgent*` -> `s.q.{ListUserAgentMemoriesByUser,UpsertUserAgentMemory,DeleteUserAgentMemory}`
- `internal/admin/users.go` — Same migration as profile.go
- `docs/content/docs/core/memory-system.md` — Full rewrite for pluggable architecture
- `docs/content/docs/core/architecture.md` — Updated package layout, memory tool description
- `internal/agent/runner/builtin/anna/SKILL.md` — Updated action names (`user_memory_update` -> `profile_update`, `grep` -> `search`)

### Commits

1. `33dd7e15` — `🗑️ refactor: delete internal/memory and remove config.Store memory methods`
2. `d072515b` — `📝 docs: update memory-system.md for pluggable architecture`
3. `c7e1ff49` — `📝 docs: update architecture.md for pluggable memory layout`
4. `637b4ee3` — `📝 docs: update builtin anna skill for new memory action names`

### Verification

- `go build ./...` — passes
- `mise run format` — clean
- `mise run lint` — 0 issues
- `mise run test` (with `-race`) — all tests pass across all packages
