# Handoff

## Goal

Implement a DAG-based Lossless Context Management (LCM) system for anna, replacing the flat compaction system and legacy memory package with SQLite-backed message persistence, hierarchical summarization, and retrieval tools.

## Progress

- **Phase 1 (Foundation)**: Complete — SQLite schema, sqlc queries, database connection, all model types
- **Phase 2 (Context Assembly)**: Complete — budget-based selection, fresh tail protection, summary XML formatting
- **Phase 3 (Compaction Engine)**: Complete — leaf/condensed passes, summarization with escalation, orchestration
- **Phase 4 (Engine Integration)**: Complete — LCM engine lifecycle, Pool integration with ingest/assemble/compact
- **Phase 5 (Retrieval Tools)**: Complete — retrieval engine, memory_grep/describe/expand tools, tool registration
- **Phase 6 (Cleanup & Docs)**: Complete — `internal/memory/` package deleted, FACT.md/JOURNAL.jsonl removed, docs updated

**PR**: https://github.com/vaayne/anna/pull/45 (branch: `lcm-memory-rebased`)

## Key Decisions

- **Context-based session ID propagation**: `GrepTool` resolves `convID` at execution time via `lcm.WithSessionID` set in `pool.Chat`, avoiding per-session tool construction
- **GrepBySession method**: Added to `RetrievalEngine` to bridge session ID → conversation ID lookup for the grep tool
- **Tool registration before runner factory**: LCM tools must be appended to `extraTools` before `newRunnerFactory()` captures the slice (Go slice aliasing)
- **SOUL.md and USER.md kept as files**: Identity files live at workspace root (`~/.anna/workspace/SOUL.md`, `~/.anna/workspace/USER.md`), read directly via `readFileIfExists` — no Store abstraction needed
- **FACT.md and JOURNAL.jsonl replaced by LCM**: Dynamic knowledge is now managed entirely by LCM's DAG summaries and retrieval tools (`memory_grep`, `memory_describe`, `memory_expand`)
- **`internal/memory/` package deleted**: `BuildSystemPrompt` refactored to take `memoryDir string` instead of `*memory.Store`. No more `MemoryTool` — the agent edits SOUL.md/USER.md via the `write` tool
- **StaticSummarizer placeholder**: `commands.go` uses `&lcm.StaticSummarizer{Response: "compacted"}` — needs real `LLMSummarizer` wired to runner factory

## Files Changed

### New files (`lcm/` package — ~5,500 lines)
- `lcm/types.go` — Engine/Summarizer interfaces, data model types, constants
- `lcm/database.go` — OpenDB, WAL mode, schema migration
- `lcm/schema.sql` — SQLite table definitions (conversations, messages, summaries, context_items, etc.)
- `lcm/queries.sql` — sqlc-annotated SQL queries
- `lcm/sqlc.yaml` — sqlc configuration
- `lcm/db.go`, `lcm/models.go`, `lcm/queries.sql.go` — sqlc-generated code
- `lcm/assembler.go` — Context assembly with budget selection and XML formatting
- `lcm/compaction.go` — Leaf/condensed compaction passes with escalation
- `lcm/summarize.go` — Prompt templates (leaf normal/aggressive, condensed D1/D2+), LLMSummarizer
- `lcm/engine.go` — Main engine: bootstrap, ingest, assemble, compact, per-session mutex
- `lcm/retrieval.go` — Grep, Describe, Expand operations on DAG
- `lcm/context.go` — WithSessionID/SessionIDFromContext context helpers
- `lcm/tool/grep.go` — memory_grep tool
- `lcm/tool/describe.go` — memory_describe tool
- `lcm/tool/expand.go` — memory_expand tool
- `lcm/tool/helpers.go` — shared intArg helper
- All `*_test.go` counterparts with >80% coverage

### Deleted files
- `memory/memory.go` — Store: Read/Write files, Append/Search journal
- `memory/memory_test.go` — Store tests
- `memory/tool.go` — MemoryTool (update FACT.md, append/search JOURNAL.jsonl)
- `agent/runner/template/fact.md` — Default FACT.md content

### Modified files
- `agent/pool.go` — Added `lcm` field, `WithLCM` option, ingest/assemble/compact integration
- `cmd/anna/commands.go` — LCM engine creation, removed memory store/tool, `memoryDir` is now `cfg.Workspace`
- `cmd/anna/chat.go` — Updated `modelSwitcher` call signature
- `cmd/anna/gateway.go` — Updated `modelSwitcher` call signature
- `agent/runner/prompt.go` — `BuildSystemPrompt` takes `memoryDir string` instead of `*memory.Store`, removed Facts from template
- `agent/runner/gorunner.go` — `MemoryStore *memory.Store` → `MemoryDir string`
- `agent/runner/template/memories.md.tmpl` — Removed Facts section
- `agent/runner/template/user.md` — Removed FACT.md reference
- `config/config.go` — Removed `MemoryPath()` method
- `docs/memory-system.md` — Rewrote: LCM architecture + identity files section, legacy section removed
- `docs/architecture.md` — Removed memory package listing, updated tools table with LCM tools
- `docs/configuration.md` — Updated directory layout (SOUL.md, USER.md, lcm.db)
- `docs/deployment.md` — Updated directory layout
- `CLAUDE.md` — Updated package listing (`internal/memory/` → `lcm/`)
- `internal/agent/runner/builtin/anna/SKILL.md` — Updated memory references
- `internal/agent/runner/builtin/anna/references/configuration.md` — Updated file layout

## Current State

The LCM system is fully functional end-to-end: messages are persisted to SQLite, compaction creates DAG summaries, context assembly respects token budgets, and three retrieval tools are available to the agent. The `internal/memory/` package has been fully removed.

SOUL.md and USER.md live at the workspace root and are loaded into the system prompt. The agent edits them with the standard `write` tool.

The `StaticSummarizer` in `commands.go` produces dummy summaries ("compacted") — real summarization requires wiring `LLMSummarizer` to a runner/provider.

## Blockers / Gotchas

- **LLMSummarizer not wired**: `commands.go` has a TODO. The summarizer needs access to an LLM provider (runner factory creates providers, but summarizer is created before the factory).
- **`GrepResult.Relevance`**: Always 0 — reserved for future scoring.
- **Grep with scope "both"**: Can return up to 2×limit results when messages fill the full limit.

## Next Steps

1. **Wire `LLMSummarizer`** — resolve the chicken-and-egg: summarizer needs a provider, provider needs the factory. Options: lazy initialization, or create a dedicated lightweight provider just for summarization.
2. **Make LCM defaults configurable** — add `lcm:` section to config.yaml with `fresh_tail`, `context_threshold`, `leaf_chunk_size` options.
3. **Sub-agent spawning for `memory_expand`** — currently returns raw content; could spawn a focused sub-agent for deeper analysis.
4. **Integration testing** — end-to-end test with real LLM summarization to validate compaction quality at depth.
