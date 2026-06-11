---
title: Memory System Internals
---

> This section is for developers contributing to Stella.

## Provider Interface

The core `Provider` interface (`internal/memory/provider.go`) has 5 methods:

| Method                                      | Description                                             |
| ------------------------------------------- | ------------------------------------------------------- |
| `Bootstrap(ctx, session)`                   | Ensures a conversation record exists for the session    |
| `Append(ctx, session, msgs)`                | Persists messages and appends context items             |
| `Assemble(ctx, session, budget, freshTail)` | Builds conversation context within token budget         |
| `Stats(ctx, session)`                       | Returns session statistics (token count, message count) |
| `Close()`                                   | Releases resources                                      |

### Optional Capabilities

Providers can implement additional interfaces detected via type assertion:

| Interface                                            | Description                                        |
| ---------------------------------------------------- | -------------------------------------------------- |
| `Compactor`                                          | Context window compaction                          |
| `Searcher`                                           | Full-text search across messages and summaries     |
| `Explorer`                                           | Inspect and drill into summaries                   |
| `ProfileStore`                                       | Per-user-per-agent profile and soul text           |
| `ConstraintStore`                                    | Per-user-per-agent hard constraints                |
| `ChangelogReader` / `ChangelogWriter`                | Version history for memory writes                  |
| `VersionedProfileStore` / `VersionedConstraintStore` | Read identity/constraint state at a frozen version |
| `SessionSnapshotStore`                               | Freeze and advance per-session memory versions     |
| `SessionManager`                                     | Session metadata and history management            |
| `ReviewSource`                                       | Self-improvement review data for Reflect           |

The LCM plugin implements the full set. The Simple plugin implements the core provider plus identity, constraints, changelog, snapshots, and session management, but it does not compact/search/explore.

## Memory Tool

`memory.BuildTool(provider)` inspects the provider's capabilities and generates a `tools.Tool` with matching actions:

| Action              | Requires                           | Description                                                   |
| ------------------- | ---------------------------------- | ------------------------------------------------------------- |
| `status`            | always                             | Show session stats                                            |
| `search`            | `Searcher`                         | Search messages and summaries by pattern                      |
| `describe`          | `Explorer`                         | Inspect a summary's metadata and lineage                      |
| `expand`            | `Explorer`                         | Drill into compacted summaries                                |
| `profile_get`       | `ProfileStore`                     | Read persistent user profile notes                            |
| `profile_update`    | `ProfileStore`                     | Replace persistent user profile notes                         |
| `soul_get`          | `ProfileStore`                     | Read per-user agent soul override                             |
| `soul_update`       | `ProfileStore`                     | Update per-user agent soul override                           |
| `profile_history`   | `ChangelogReader`                  | Read recent profile/soul change history                       |
| `profile_rollback`  | `ChangelogReader` + `ProfileStore` | Restore profile/soul text from a previous changelog version   |
| `constraint_list`   | `ConstraintStore`                  | List hard constraints                                         |
| `constraint_add`    | `ConstraintStore`                  | Add a hard constraint after user confirmation in conversation |
| `constraint_remove` | `ConstraintStore`                  | Remove a hard constraint by ID                                |

The tool's JSON schema, description, and dispatch all adapt dynamically. A provider with fewer capabilities produces a tool with fewer actions.

### Group turns: current-speaker fallback

In a group session the runtime identity is the group, so there is no session user (D9). To still let an agent remember facts about the person speaking, `profile_get` and `profile_update` fall back to the current speaker when no session user is present:

1. Session user (`UserIDFromContext`) — normal DM behavior.
2. Otherwise the linked current speaker (`CurrentSpeaker.UserID`) — group personalization.
3. Otherwise fail closed with `no linked current speaker` (unlinked sender).

The fallback is deliberately narrow. **Only `profile_get` / `profile_update` get it, and only when the model explicitly calls the tool.** `soul_get`, `soul_update`, `constraint_*`, `profile_history`, and `profile_rollback` stay on the strict session-user resolver, so in a group turn they fail closed — a public room is not a place to read or rewrite one member's soul, constraints, or history through a shared agent. A `profile_update` via the fallback advances the speaker's own snapshot row `(session, speaker.UserID, agent)`, never the group's.

See [Group chat: current speaker (D10)](/docs/development/group-chat-multi-agent#current-speaker-per-turn-personalization-d10).

## System Prompt Layers

Each turn can rebuild the system prompt from the current or frozen memory version. The prompt order is:

1. **Base system prompt** — agent configuration / `SYSTEM.md` override.
2. **Tools and plugin prompt inventory** — available tools, plugin capabilities, skills.
3. **Constraints** — user-approved hard rules from `ConstraintStore`; injected before soul/profile and not touched by Reflect.
4. **Agent soul** — agent identity/personality text.
5. **User profile** — durable user notes. **Group turns replace this with `## Group Memory` (the shared group drawer) plus an optional `## Current Speaker` section** that contains only speaker name and linked status; the per-user profile is never rendered in a group turn. Group mode is keyed on the session having a `group_id`, not on group memory being non-empty.
6. **Knowledge** — active fact/context entries from `KnowledgeStore`.
7. **Project context** — `AGENTS.md` and related project instructions.

Conversation history is assembled separately by the memory provider. Constraints, identity, and knowledge live in the system prompt, so conversation compaction does not remove them.

For group turns the PoolManager before-run path re-renders the full prompt with the current speaker metadata; the cached group runner never holds speaker data, so one speaker's turn context cannot leak into another speaker's turn. The speaker's profile blob and dated entries are intentionally not auto-injected into public group prompts; profile access remains behind explicit `memory.profile_get` / `memory.profile_update` tool calls.

## Changelog and Rollback

`ctx_agent_memory` has a row-level `version`. Writes to profile, soul, and constraints increment the version and append a row to `memory_changelog` in the same database transaction.

The changelog records:

- user and agent
- scope (`profile`, `soul`, `constraint`, `skill`, `compaction`)
- action (`create`, `update`, `delete`, `compact`)
- source (`user`, `agent`, `reflect`, `system`)
- before/after text
- before/after memory versions
- optional session/entity metadata

This enables `profile_history`, `profile_rollback`, auditability, and versioned reads for session snapshots.

## Constraints

Constraints are stored as a JSON array in `ctx_agent_memory.constraints`. Each entry has an ID, text, and creation timestamp.

Constraints are intended for rules the user explicitly wants Stella to preserve, for example:

- "Ask before deleting files."
- "Never run production database migrations unless I approve."
- "Do not expose secrets in chat."

Reflect is explicitly instructed not to add, remove, or edit constraints. The current protection is convention-level: the model should propose a constraint in natural language and call `constraint_add` only after the user agrees.

## Session Snapshots

Session snapshots prevent background memory updates from changing an active conversation mid-stream.

On the first chat turn, Stella stores a frozen `ctx_agent_memory.version` in `memory_snapshots` for `(session_id, user_id, agent_id)`. On every turn, `runtime.Runtime` rebuilds the system prompt at that snapshot version and injects it with a per-run system override.

Visibility rules:

| Write path                                             | Current session sees it? | Why                                                                  |
| ------------------------------------------------------ | ------------------------ | -------------------------------------------------------------------- |
| User asks Stella to remember something via memory tool | Yes, from the next turn  | The memory tool advances the current session snapshot                |
| User adds/removes a constraint via memory tool         | Yes, from the next turn  | The snapshot advances after the foreground write                     |
| Reflect updates profile/knowledge in the background    | No                       | Reflect has no active session context and does not advance snapshots |
| A new session starts                                   | Yes                      | It snapshots the latest memory version                               |

This keeps foreground user intent immediate while preventing background reflection from causing behavior drift inside an ongoing session.

## Knowledge

Knowledge extends the skills table with `metadata.knowledge_type`:

| Type      | Meaning                           | Model-callable? | Default expiry     |
| --------- | --------------------------------- | --------------- | ------------------ |
| `skill`   | Reusable procedure or workflow    | Yes             | 30 days for drafts |
| `fact`    | Durable project/domain fact       | No              | 90 days for drafts |
| `context` | Time-bound background information | No              | 30 days for drafts |

Fact/context entries are stored in `skills` with `disable_model_invocation=true`. They do not appear in `<available_skills>` and cannot be loaded through the skills tool as executable skills. Active entries are injected into the `## Knowledge` section of the system prompt.

Reflect may draft fact/context entries, but drafts do not affect sessions until activated through the skills/admin management path.

## LCM Plugin

### Architecture

```
ai.Message (user/assistant/tool_result)
        |
        v
  +----------+     Append     +-----------+
  | Provider | ------------> | SQLite DB |
  +----------+                +-----+-----+
     |    |                          |
     |    | Compact                  |  Tables:
     |    v                          |    ctx_conversations
     | +------------------+          |    ctx_messages
     | | CompactionEngine | <--------+    ctx_summaries
     | +------------------+          |    ctx_items
     |                               |    ctx_summary_messages
     |  Assemble (budget)            |    ctx_summary_parents
     v                               |
  +-----------+                      |
  | Assembler | <--------------------+
  +-----------+
        |
        v
  []ai.Message (fresh tail + summaries within token budget)
```

### Compaction

Compaction reduces the conversation window by summarizing older messages and summaries.

1. **Leaf pass** — groups contiguous message items outside the fresh tail. Groups of 10+ messages become `leaf` summaries.
2. **Condensed pass** — groups summaries at the same depth. Groups of 2+ summaries become a higher-depth `condensed` summary.

Before summarization, tool results and tool calls are formatted compactly so the LLM receives readable metadata instead of raw JSON envelopes:

| Event type            | Formatter output example                                  |
| --------------------- | --------------------------------------------------------- |
| `tool_result`         | `[tool:read_file] result(1234 chars): first 300 chars...` |
| `tool_result` (error) | `[tool:read_file] error: file not found`                  |
| `tool_call`           | `[assistant:call bash] args: {"command":"ls"}`            |
| text / image / other  | `[user] hello` (original format)                          |

If a message cannot be parsed (legacy row or malformed JSON), the formatter falls back to the original `[role] content` string.

Summarization escalates from normal mode, to aggressive durable-facts mode, to deterministic sentence-boundary truncation if needed.

### Context Assembly

1. Separate context items into **fresh tail** (last N message items, default 20) and **older** items.
2. Resolve fresh tail items to `ai.Message`s.
3. Replace large processed tool results (>2000 tokens) in the tail with a compact placeholder, preserving `ToolCallID`, `ToolName`, `IsError`, and `Timestamp` so the model knows the content was omitted and can re-invoke the tool if needed. Tool results that are still in-flight (no assistant reply yet) are passed through at full size.
4. Compute tail token cost against the already-compacted tail, then fill remaining budget with older items, newest first.
5. Return older events in chronological order followed by the tail.

> **Note:** `defaultFreshTail` counts `CtxItem` rows, not conversation turns. A single agent turn with multiple tool calls can produce 4+ items (user message + assistant text + tool calls + tool results). If your sessions are tool-heavy and the typical item-per-turn ratio is high, you may need to raise `fresh_tail` via plugin config after observing telemetry.

Every assembly emits a structured log entry (`lcm tail telemetry`) with `tail_items`, `tail_messages`, `user_turns`, `items_per_turn`, and `tool_results_before/after` for observability-driven tuning.

### Search

Search runs on SQLite FTS5 with BM25 ranking. Two external-content virtual tables — `ctx_message_fts` and `ctx_summary_fts` — index `content` with the `trigram` tokenizer and are kept in sync by insert/update/delete triggers on the base tables. Trigram gives substring recall — 部署方案 matches inside a longer CJK sentence, and English queries also match substrings ("ploy" hits "deployment").

- Query text is tokenized into letter/digit/underscore runs, each quoted and OR-joined into a `MATCH` expression, so FTS5 operators in user input never break the query. Tokens shorter than 3 characters are dropped — trigram `MATCH` silently returns nothing for them.
- Queries whose tokens are all shorter than 3 characters (部署, "go") fall back to an unranked, recency-ordered `LIKE` scan of the content tables: no BM25 score, no snippet highlights.
- `MATCH` results carry an FTS snippet (`<<term>>` highlights) and a normalized score (`-bm25`, higher is better).
- `both` scope queries messages and summaries separately with the full limit, then merges by score and keeps the top N — a strong summary hit can outrank a weak message hit. Summary hits drill down via `describe`/`expand`.
- The FTS schema lives in `internal/db/schemas/tables/ctx_*_fts.sql` but is applied at **runtime** (`internal/db/fts.go`), not through Atlas migrations — Atlas's dev SQLite lacks the fts5 module. The index is rebuilt from existing rows on first creation (so pre-FTS history is searchable), whenever the sync triggers are found missing — the signature of an Atlas table-rebuild migration, which drops triggers and shifts rowids — and when an existing table carries a different tokenizer (the unicode61 → trigram migration). If the SQLite build lacks FTS5, startup fails with an explicit error.

## Simple Plugin

The Simple plugin uses a sliding-window approach:

1. **Append** stores messages in `ctx_messages`.
2. **Assemble** returns the last N messages that fit within the token budget, always honoring freshTail.
3. No summaries, no compaction, no search, no explore.

This is suitable for short-lived conversations or resource-constrained environments.

## Database

- **Location:** `~/.stella/stella.db`
- **Driver:** `modernc.org/sqlite` (pure Go, no CGO)
- **Mode:** WAL, foreign keys enabled
- **Migrations:** Atlas-generated, embedded via `MigrationsFS`, auto-applied on startup.

**Core schema:**

| Table                  | Purpose                                                                |
| ---------------------- | ---------------------------------------------------------------------- |
| `ctx_conversations`    | One per session (`session_id` -> `id` mapping), includes agent/user ID |
| `ctx_messages`         | Raw messages with `role`, `content`, `token_count`, sequential `seq`   |
| `ctx_summaries`        | Summary DAG nodes                                                      |
| `ctx_items`            | Ordered context window: points to message or summary                   |
| `ctx_summary_messages` | Links leaf summaries to source messages                                |
| `ctx_summary_parents`  | Links condensed summaries to parent summaries                          |
| `ctx_agent_memory`     | Profile, soul, constraints, and row-level version                      |
| `memory_changelog`     | Append-only audit log for memory writes                                |
| `memory_snapshots`     | Per-session frozen memory version                                      |
| `skills`               | Skills plus non-callable fact/context knowledge entries                |

## Configuration Defaults

| Constant                    | Value | Description                                                            |
| --------------------------- | ----- | ---------------------------------------------------------------------- |
| `DefaultFreshTail`          | 20    | Messages protected from compaction                                     |
| `DefaultContextThreshold`   | 0.75  | Fraction of budget that triggers compaction                            |
| `DefaultLeafChunkSize`      | 10    | Minimum messages per leaf summary                                      |
| `OversizedToolResultTokens` | 2000  | Tail tool results above this threshold are replaced with a placeholder |

## Agent Workspaces

Each agent has its own workspace at `$STELLA_HOME/workspaces/{agent_id}/` for file overrides, skills, and per-agent data.
