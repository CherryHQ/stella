# Tasks: Lossless Context Management (LCM)

## Status Legend

- [ ] Pending
- [x] Completed

**Task States:** `PENDING` | `IMPLEMENTING` | `VALIDATING` | `REVIEWING` | `APPROVED`

## Phase 1: Foundation — SQLite Store

- [x] Task 1.1: Create `lcm/` package structure and types
  - **Files:** `lcm/types.go`, `lcm/types_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Defined Engine interface, Summarizer interface, SummarizeOptions, CompactionResult, CompactionMode, retrieval result types, and EstimateTokens helper. DB model structs deferred to sqlc generation.
  - **Gotchas:** CompactionMode wired into Compact() signature per review. String() added for logging.
  - **Commit:** 626eec6

- [x] Task 1.2: Set up sqlc and SQLite connection
  - **Files:** `lcm/database.go`, `lcm/sqlc.yaml`, `lcm/schema.sql`, `lcm/db.go` (generated), `lcm/models.go` (generated), `lcm/queries.sql.go` (generated)
  - **Deps:** `modernc.org/sqlite`, `github.com/sqlc-dev/sqlc`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Batched Tasks 1.2-1.6 together since schema+queries+sqlc generation are tightly coupled. Created schema.sql, queries.sql, sqlc.yaml, ran sqlc generate, added database.go with OpenDB/WAL/migrate.
  - **Gotchas:** sqlc generates `db.go` for its DBTX interface — renamed custom DB file to `database.go`. COALESCE queries need CAST(... AS INTEGER) to avoid `interface{}` return type.
  - **Commit:** abf8609

- [x] Task 1.3: Write sqlc queries for conversations (batched with 1.2)
  - **Files:** `lcm/queries.sql`
  - **Queries:** CreateConversation, GetConversation, GetConversationBySessionID, UpdateConversationTitle, UpdateConversationBootstrapped
  - **State:** APPROVED
  - **Iterations:** 1
  - **Commit:** abf8609

- [x] Task 1.4: Write sqlc queries for messages (batched with 1.2)
  - **Files:** `lcm/queries.sql`
  - **Queries:** CreateMessage, GetMessage, GetMessagesByConversation, GetMessagesByConversationRange, GetMessageCount, GetMaxSeq, CreateMessagePart, GetMessageParts, GetMessagePartsByMessages
  - **State:** APPROVED
  - **Iterations:** 1
  - **Commit:** abf8609

- [x] Task 1.5: Write sqlc queries for summaries (batched with 1.2)
  - **Files:** `lcm/queries.sql`
  - **Queries:** CreateSummary, GetSummary, GetSummariesByConversation, GetSummariesByDepth, LinkSummaryToMessage, LinkSummaryToParent, GetSummaryMessages, GetSummaryParents, GetSummaryChildren, SearchMessages, SearchSummaries
  - **State:** APPROVED
  - **Iterations:** 1
  - **Commit:** abf8609

- [x] Task 1.6: Write sqlc queries for context items (batched with 1.2)
  - **Files:** `lcm/queries.sql`
  - **Queries:** AppendContextItem, GetContextItems, GetContextItemCount, GetMaxContextOrdinal, DeleteContextItemsInRange, GetContextTokenCount, GetContextMessageItems, GetFreshTailMessageIDs, DeleteAllContextItems
  - **State:** APPROVED
  - **Iterations:** 1
  - **Gotchas:** Replaced ResequenceContextItems with DeleteAllContextItems — resequencing better done in Go code via delete+re-insert in a transaction.
  - **Commit:** abf8609

## Phase 2: Context Assembly

- [x] Task 2.1: Implement context assembler
  - **Files:** `lcm/assembler.go`, `lcm/assembler_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Batched with 2.2. Budget-based selection with fresh tail protection, oldest-first exclusion. Resolves messages to RPCEvents, summaries to XML-wrapped user messages.
  - **Gotchas:** splitFreshTail counts only message items from the end, not summaries.
  - **Commit:** 3e923b1

- [x] Task 2.2: Implement summary XML formatting (batched with 2.1)
  - **Files:** `lcm/assembler.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** FormatSummaryXML with attributes (id, kind, depth, timestamps), parent refs for condensed, content block.
  - **Commit:** 3e923b1

## Phase 3: Compaction Engine

- [x] Task 3.1: Implement summarization prompts
  - **Files:** `lcm/summarize.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Batched with 3.2. Four prompt templates (leaf normal/aggressive, condensed D1/D2+), BuildPrompt dispatcher, LLMSummarizer with Generate callback, StaticSummarizer for tests.
  - **Gotchas:** None.
  - **Commit:** 25c1101

- [x] Task 3.2: Implement summarization with escalation (batched with 3.1)
  - **Files:** `lcm/summarize.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Three-stage escalation: normal → aggressive → deterministic fallback. 150% overshoot threshold before escalation. Fallback truncates at rune boundary, prefers sentence/line breaks.
  - **Gotchas:** None.
  - **Commit:** 25c1101

- [x] Task 3.3: Implement leaf compaction pass
  - **Files:** `lcm/compaction.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** splitFreshTail protects last N message items. findMessageRuns collects contiguous runs ≥ chunk size. compactMessageRun: atomic tx with create summary → link messages → replace context items.
  - **Gotchas:** None.
  - **Commit:** 25c1101

- [x] Task 3.4: Implement condensed compaction pass
  - **Files:** `lcm/compaction.go`
  - **State:** APPROVED
  - **Iterations:** 2
  - **Approach:** Pre-fetch summary depths, findSummaryRuns groups only same-depth consecutive summaries. condenseSummaryRun: atomic tx. Condensed target = totalTokens/2 (less aggressive since already compressed).
  - **Gotchas:** Initial impl missed same-depth enforcement in findSummaryRuns — fixed by adding depthOf map parameter.
  - **Commit:** 25c1101, 11e7e97

- [x] Task 3.5: Implement compaction orchestration
  - **Files:** `lcm/compaction.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** CompactionIncremental: single leaf + condensed pass. CompactionFull: repeated passes with safety limit of 10 iterations. Tracks TokensBefore/TokensAfter outside tx (safe under session-level mutex).
  - **Gotchas:** None.
  - **Commit:** 25c1101

## Phase 4: Engine Integration

- [x] Task 4.1: Implement LCM engine
  - **Files:** `lcm/engine.go`, `lcm/engine_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Concrete engine struct with per-session mutex, functional options (WithFreshTail, WithLogger). Bootstrap auto-creates conversations. Ingest converts RPCEvent→Message via eventToRoleContent. Assemble/Compact delegate to assembler/compaction. NeedsCompaction treats threshold as absolute token limit.
  - **Gotchas:** NeedsCompaction has no budget param — threshold treated as absolute max tokens. Skip unmappable events (agent_end, tool_start, etc.) silently.
  - **Commit:** b4b5e07

- [x] Task 4.2: Integrate with agent/Pool
  - **Files:** `agent/pool.go`, `main.go`
  - **State:** APPROVED
  - **Iterations:** 2
  - **Approach:** Added lcm field to Pool with WithLCM option. Chat() uses Ingest/Assemble with store fallback. CompactSession delegates to compactSessionLCM helper. NeedsCompaction queries LCM. getOrCreateRunner calls Bootstrap. Uses context.WithoutCancel for persistence in streaming goroutine.
  - **Gotchas:** Context cancellation in goroutine could lose events — fixed with persistCtx. StaticSummarizer placeholder in main.go — TODO to wire LLMSummarizer. LCM always created (no config gate yet).
  - **Commit:** 1a33b50

## Phase 5: Retrieval Tools

- [x] Task 5.1: Implement retrieval engine
  - **Files:** `lcm/retrieval.go`, `lcm/retrieval_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Grep (LIKE search across messages/summaries), Describe (summary metadata + lineage), Expand (drill into leaf messages or condensed children with token cap). Added NewRetrievalEngine constructor. UTF-8 safe truncation via unicode/utf8.
  - **Gotchas:** GrepResult.Relevance reserved for future scoring (currently 0). Grep with scope "both" can return up to 2*limit when messages fill full limit.
  - **Commit:** 223905d

- [x] Task 5.2: Implement `memory_grep` tool
  - **Files:** `lcm/tool/grep.go`, `lcm/tool/grep_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Batched 5.2-5.4 in parallel. GrepTool wraps RetrievalEngine.GrepBySession with JSON output. Shared intArg helper in helpers.go.
  - **Gotchas:** Parallel agents created duplicate helpers — consolidated into helpers.go and helpers_test.go.
  - **Commit:** 4f0a8e3

- [x] Task 5.3: Implement `memory_describe` tool
  - **Files:** `lcm/tool/describe.go`, `lcm/tool/describe_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** DescribeTool wraps RetrievalEngine.Describe, returns JSON with summary metadata and parent/child IDs.
  - **Gotchas:** None.
  - **Commit:** 4f0a8e3

- [x] Task 5.4: Implement `memory_expand` tool
  - **Files:** `lcm/tool/expand.go`, `lcm/tool/expand_test.go`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** ExpandTool wraps RetrievalEngine.Expand, returns JSON. Sub-agent spawning deferred (documented as TODO in plan).
  - **Gotchas:** None.
  - **Commit:** 4f0a8e3

- [x] Task 5.5: Register LCM tools
  - **Files:** `lcm/context.go`, `lcm/retrieval.go`, `lcm/tool/grep.go`, `lcm/tool/describe.go`, `lcm/tool/expand.go`, `agent/pool.go`, `main.go`
  - **State:** APPROVED
  - **Iterations:** 2
  - **Approach:** Context-based session ID propagation: lcm.WithSessionID in pool.Chat, lcm.SessionIDFromContext in GrepTool.Execute. Refactored all tools to accept lcm.Engine instead of *RetrievalEngine. Added GrepBySession to RetrievalEngine. Registered as extraTools in main.go.
  - **Gotchas:** Initial placement of tool registration was after runner factory creation (slice aliasing bug) — moved before factory to ensure tools visible to runner.
  - **Commit:** 3dedb45

## Phase 6: Cleanup and Documentation

- [ ] Task 6.1: Remove old memory package — **DEFERRED**
  - **Files:** `internal/memory/` (delete)
  - **State:** DEFERRED
  - **Iterations:** 0
  - **Approach:** Analysis found memory package deeply coupled to system prompt builder (agent/runner/prompt.go), GoRunnerConfig, and main.go. Removal requires refactoring BuildSystemPrompt to source SOUL/USER/FACT from LCM or a simpler abstraction. Deferred to a future task.
  - **Gotchas:** 4 consumers: main.go, gorunner.go, prompt.go, skill_test.go. The prompt builder uses 6 distinct memory API calls.
  - **Commit:** N/A

- [x] Task 6.2: Update documentation
  - **Files:** `docs/memory-system.md`, `README.md`, `internal/agent/runner/builtin/anna/SKILL.md`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Rewrote memory-system.md with LCM architecture, DAG hierarchy, tool reference tables. Legacy system preserved under "Legacy Memory System" heading. Updated README feature list and architecture layout. Updated anna SKILL.md with LCM tools.
  - **Gotchas:** None.
  - **Commit:** a252d45

- [x] Task 6.3: Update configuration docs
  - **Files:** `docs/configuration.md`
  - **State:** APPROVED
  - **Iterations:** 1
  - **Approach:** Added LCM database to directory layout table. Added "LCM Defaults" section with parameter table (fresh tail, threshold, chunk size) and compaction modes. Noted these are hardcoded defaults, will become configurable later.
  - **Gotchas:** None.
  - **Commit:** a252d45

## Completion Summary

**Total Tasks:** 23
**Completed:** 21
**Deferred:** 1 (Task 6.1 — remove old memory package)
**Remaining:** 1

### Final Notes

All LCM implementation tasks complete. The system provides:
- SQLite-backed message persistence with DAG-based hierarchical summarization
- Leaf and condensed compaction passes with escalation
- Context assembly with budget selection and fresh tail protection
- Three retrieval tools (memory_grep, memory_describe, memory_expand) registered as agent tools
- Context-based session ID propagation for tool execution

**Deferred work:**
- Task 6.1: Remove `internal/memory/` package (requires refactoring system prompt builder)
- Wire LLMSummarizer with runner factory (TODO in main.go)
- Sub-agent spawning for memory_expand (documented as TODO)
- Make LCM defaults configurable via config.yaml
