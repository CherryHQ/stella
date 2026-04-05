---
title: Memory System
---

## Lossless Context Management (LCM)

### Overview

The memory system provides lossless context management for anna. Every message is persisted in a SQLite database and organized into a DAG (directed acyclic graph) of summaries. When the conversation grows too long, older messages are compacted into leaf summaries, and groups of leaf summaries are further condensed into higher-level summaries. The agent can drill back into any summary to recover the original detail — nothing is ever deleted.

Package: `internal/memory/` (core) + `internal/memory/tool/` (agent tool wrappers).

### Architecture

```
ai.Message (user/assistant/tool_result)
        |
        v
  +----------+     ingest      +-----------+
  |  Engine   | -------------> | SQLite DB |
  +----------+                 +-----+-----+
     |    |                          |
     |    | compact                  |  Tables:
     |    v                          |    ctx_conversations
     | +------------------+          |    ctx_messages
     | | CompactionEngine | <--------+    ctx_summaries
     | +------------------+          |    ctx_items
     |                               |    ctx_summary_messages
     |  assemble (budget)            |    ctx_summary_parents
     v                               |
  +------------+                     |
  | Assembler  | <-------------------+
  +------------+
        |
        v
  []ai.Message (fresh tail + summaries within token budget)
        |
        v
  LLM context window
```

### Engine API

The `Engine` interface (`internal/memory/types.go`) is the main entry point:

| Method                                        | Description                                                |
| --------------------------------------------- | ---------------------------------------------------------- |
| `Bootstrap(ctx, sessionID)`                   | Ensures a conversation record exists for the session       |
| `Ingest(ctx, sessionID, msg)`                 | Persists a single `ai.Message` and appends a context item  |
| `IngestBatch(ctx, sessionID, msgs)`           | Persists multiple messages in a single transaction         |
| `Assemble(ctx, sessionID, budget, freshTail)` | Builds context within token budget, returns `[]ai.Message` |
| `Compact(ctx, sessionID, mode)`               | Runs compaction passes (leaf + condensation)               |
| `NeedsCompaction(ctx, sessionID, threshold)`  | Checks if context tokens exceed the absolute threshold     |
| `Retrieval()`                                 | Returns the `RetrievalEngine` for search/explore tools     |
| `Close()`                                     | Releases database resources                                |

**Constructor:** `NewEngineFromDB(db *sql.DB, summarizer Summarizer, opts ...EngineOption)` accepts an existing `*sql.DB` connection so the memory engine can share the same database handle used by the config store and other subsystems.

Engine options: `WithFreshTail(n)`, `WithLogger(log)`.

### Database

- **Location:** `~/.anna/anna.db`
- **Driver:** `modernc.org/sqlite` (pure Go, no CGO)
- **Mode:** WAL (concurrent reads during writes), foreign keys enabled
- **Migrations:** Atlas-generated SQL files in `internal/db/migrations/`, embedded via `MigrationsFS` and applied on `db.OpenDB()`. Applied versions are tracked in a `schema_migrations` table.

**Schema change workflow:**

```bash
# 1. Edit schema source files
vim internal/db/schemas/tables/conversations.sql

# 2. Generate migration
mise run db:diff -- add_column_name

# 3. Regenerate sqlc
mise run generate

# 4. Runtime auto-applies pending migrations on OpenDB()
```

**Schema:**

| Table                     | Purpose                                                                                                                                       |
| ------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `ctx_conversations`       | One per session (`session_id` -> `id` mapping). Includes `agent_id` and `user_id` columns to track which agent and user own the conversation. |
| `ctx_messages`            | Raw messages with `role`, `content`, `token_count`, sequential `seq`                                                                          |
| `ctx_summaries`           | Summary nodes: `kind` (`leaf`/`condensed`), `depth`, `content`, token stats, time range                                                       |
| `ctx_items`               | Ordered context window: each item points to either a `message_id` or `summary_id`                                                             |
| `ctx_summary_messages`    | Links leaf summaries to their source messages (preserves lineage)                                                                             |
| `ctx_summary_parents`     | Links condensed summaries to their parent summaries (DAG edges)                                                                               |
| `ctx_message_parts`       | Structured message parts (`text`, `reasoning`, `tool`) for future use                                                                         |
| `ctx_agent_memory`        | Per-user-per-agent notes. Primary key is `(user_id, agent_id)`. Content is injected into the system prompt at session start.                  |
| `settings_agents`         | Agent configuration including `system_prompt` (agent soul), model selection, and workspace path                                               |
| `settings_plugins`        | Unified plugin table (tools, channels, hooks, providers). Provider credentials stored in `config` JSON.                                        |
| `settings_channels`       | Channel (Telegram, QQ, Feishu, WeChat) configuration                                                                                          |
| `settings_users`          | User records referenced by `ctx_agent_memory` and `ctx_conversations`                                                                         |
| `settings_channel_agents` | Maps channels to agents for multi-agent routing                                                                                               |

### Compaction

Compaction reduces the context window by summarizing older messages and summaries.

**Modes:**

| Mode                    | Behavior                                                                                    |
| ----------------------- | ------------------------------------------------------------------------------------------- |
| `CompactionIncremental` | Single leaf pass + one condensed pass. Runs automatically when context exceeds threshold.   |
| `CompactionFull`        | Repeats leaf + condensed passes until no more compaction is possible (up to 10 iterations). |

**Passes:**

1. **Leaf pass** — Finds contiguous runs of message context items outside the fresh tail. Groups of ≥ `DefaultLeafChunkSize` (10) messages are summarized into a `leaf` summary (depth 0). The message context items are replaced by a single summary context item.

2. **Condensed pass** — Finds contiguous runs of summary context items at the same depth. Groups of ≥ 2 summaries are condensed into a `condensed` summary at depth+1. Uses a summary cache from the prefetch to avoid redundant queries.

Both passes run within the `runPasses` helper, which fetches context items once and re-fetches only between passes when mutations occur.

**Summarization escalation** (`internal/memory/summarize.go`):

The `LLMSummarizer` implements a three-tier escalation strategy:

1. **Normal mode** — Preserves key decisions, rationale, constraints, active tasks. Target: input_tokens/3.
2. **Aggressive mode** — Keeps only durable facts and current task state. Triggered when normal mode exceeds 150% of target.
3. **Deterministic fallback** — Truncates to target at a sentence/line boundary. Triggered when aggressive mode still exceeds 150%.

Leaf summaries target 1/3 of source tokens. Condensed summaries target 1/2 (less aggressive to preserve detail).

### Context Assembly

The `Assembler` builds the context window for each LLM call (`internal/memory/assembler.go`):

1. Separate context items into **fresh tail** (last N message items, default 20) and **older** items.
2. Resolve fresh tail items to `ai.Message`s — these are always included regardless of budget.
3. Fill remaining budget with older items, newest first. Each item is resolved and its tokens estimated. Items that would exceed the budget are excluded.
4. Return older events (chronological order) + tail events.

**Summary XML format** (injected as synthetic user messages):

```xml
<summary id="sum_abc123" kind="leaf" depth="0" earliest_at="..." latest_at="...">
  <parents>
    <summary_ref id="sum_parent1" />
  </parents>
  <content>
    Summary text here...
  </content>
</summary>
```

**Token estimation:** `(len(text) + 3) / 4` (~4 chars per token).

### Memory Tool

A unified `memory` tool in `internal/memory/tool/` provides all memory operations via an `action` parameter:

| Action               | Purpose                                                                                                                                                     | Key Parameters                                                                      |
| -------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `grep`               | Search messages and summaries by substring pattern                                                                                                          | `pattern` (required), `scope` (`messages`/`summaries`/`both`), `limit` (default 20) |
| `describe`           | Inspect a summary's metadata, content, and lineage (parents/children)                                                                                       | `summary_id`                                                                        |
| `expand`             | Drill into a summary: returns source messages (leaf) or child summaries (condensed)                                                                         | `summary_id`, `token_cap` (default 4000)                                            |
| `user_memory_update` | Write-only update of persistent per-user-per-agent notes in `ctx_agent_memory`. Content is always injected into the system prompt; this action only writes. | `content` (required)                                                                |

The tool extracts session ID, user ID, and agent ID from context (set by `Pool.Chat()`).

### Concurrency

- **Per-session mutex** — `Ingest`, `IngestBatch`, and `Compact` acquire a per-session lock via `withSessionLock()` to prevent concurrent mutations on the same conversation.
- **Global mutex** — Protects the session mutex map and conversation ID cache.
- **Conversation ID cache** — `getOrCreateConversation` caches the `sessionID → convID` mapping since it's immutable once created.

### Configuration Defaults

| Constant                  | Value | Description                                 |
| ------------------------- | ----- | ------------------------------------------- |
| `DefaultFreshTail`        | 20    | Messages protected from compaction          |
| `DefaultContextThreshold` | 0.75  | Fraction of budget that triggers compaction |
| `DefaultLeafChunkSize`    | 10    | Minimum messages per leaf summary           |

### Integration

The memory engine is wired into the agent Pool. When a session uses it:

1. Each message is ingested into the database after every turn.
2. Context is assembled from the database before each LLM call.
3. Compaction runs automatically based on the context threshold.

---

## Identity & User Memory

Agent identity and per-user memory are stored in the database and optionally overridden by workspace files.

### 3-Layer System Prompt

The system prompt sent to the LLM is assembled from three layers, each overridable:

| Layer           | Default source                  | File override                  | Description                                                                |
| --------------- | ------------------------------- | ------------------------------ | -------------------------------------------------------------------------- |
| **Basic**       | Built-in system instructions    | `SYSTEM.md` in agent workspace | Core behavioral instructions for the LLM.                                  |
| **Agent soul**  | `settings_agents.system_prompt` | `SOUL.md` in agent workspace   | Agent identity, personality, and tone.                                     |
| **User memory** | `ctx_agent_memory.content`      | (none -- always from DB)       | Per-user-per-agent notes. Automatically injected; not overridable by file. |

Layers are concatenated in order (basic, then agent soul, then user memory) to form the final system prompt.

### Agent Soul

The agent soul defines personality and tone. It is stored in `settings_agents.system_prompt` and can be managed through the admin panel. If a `SOUL.md` file exists in the agent workspace (`$ANNA_HOME/workspaces/{agent_id}/SOUL.md`), its contents take precedence over the database value.

### User Memory

User memory is stored in the `ctx_agent_memory` table, keyed by `(user_id, agent_id)`. This allows each user to have distinct notes per agent. The content is always injected into the system prompt at session start.

The agent updates user memory via the `memory` tool's `user_memory_update` action. This action is **write-only** -- it replaces the entire content of the `ctx_agent_memory` row for the current user/agent pair. The agent cannot read user memory through the tool; it is always provided via the system prompt injection.

### Agent Workspaces

Each agent has its own workspace directory at `$ANNA_HOME/workspaces/{agent_id}/`. This directory holds file overrides (`SYSTEM.md`, `SOUL.md`), skills, and any other per-agent data.
