---
title: Memory System
---

## Overview

The memory system is **plugin-based**. A `memory.Provider` interface in `pkg/memory/` defines the contract, and concrete implementations live in `plugins/memory/`. Two built-in plugins ship with anna:

| Plugin     | Package                  | Default | Description                                                                 |
| ---------- | ------------------------ | ------- | --------------------------------------------------------------------------- |
| **LCM**    | `plugins/memory/lcm/`    | Yes     | Lossless Context Management — DAG of summaries, compaction, search, explore |
| **Simple** | `plugins/memory/simple/` | No      | Sliding-window — keeps last N messages within token budget, no summaries    |

### Switching Plugins

Memory plugins are managed like other plugins. In the admin panel or via `anna plugin`:

```bash
anna plugin disable memory/lcm
anna plugin enable memory/simple
```

Only one memory plugin should be enabled at a time. Both use the same underlying `ctx_messages` table, so switching preserves stored messages.

## Provider Interface

The core `Provider` interface (`pkg/memory/provider.go`) has 5 methods:

| Method                                      | Description                                                |
| ------------------------------------------- | ---------------------------------------------------------- |
| `Bootstrap(ctx, session)`                   | Ensures a conversation record exists for the session       |
| `Append(ctx, session, msgs)`                | Persists messages and appends context items                |
| `Assemble(ctx, session, budget, freshTail)` | Builds context within token budget, returns `[]ai.Message` |
| `Stats(ctx, session)`                       | Returns session statistics (token count, message count)    |
| `Close()`                                   | Releases resources                                         |

### Optional Capabilities

Providers can implement additional interfaces detected via type assertion:

| Interface        | Methods                                                | Description                                    |
| ---------------- | ------------------------------------------------------ | ---------------------------------------------- |
| `Compactor`      | `NeedsCompaction`, `Compact`                           | Context window compaction                      |
| `Searcher`       | `Search`                                               | Full-text search across messages and summaries |
| `Explorer`       | `Describe`, `Expand`                                   | Inspect and drill into summaries               |
| `ProfileStore`   | `GetProfile`, `SetProfile`                             | Per-user-per-agent persistent notes            |
| `SessionManager` | `SaveInfo`, `LoadInfo`, `ListInfo`, `LoadHistory`      | Session metadata and history management        |
| `ReviewSource`   | `ListUnreviewed`, `BuildReviewContext`, `MarkReviewed` | Self-improvement review data                   |

The LCM plugin implements all 7 interfaces. The Simple plugin implements `Provider`, `ProfileStore`, and `SessionManager`.

## Tool Auto-Generation

`memory.BuildTool(provider)` inspects the provider's capabilities and generates a `tools.Tool` with matching actions:

| Action           | Requires       | Description                              |
| ---------------- | -------------- | ---------------------------------------- |
| `status`         | (always)       | Show session stats (tokens, messages)    |
| `search`         | `Searcher`     | Search messages and summaries by pattern |
| `describe`       | `Explorer`     | Inspect a summary's metadata and lineage |
| `expand`         | `Explorer`     | Drill into compacted summaries           |
| `profile_get`    | `ProfileStore` | Read persistent per-user notes           |
| `profile_update` | `ProfileStore` | Update persistent per-user notes         |

The tool's JSON schema, description, and dispatch all adapt dynamically. A provider with fewer capabilities produces a tool with fewer actions.

## LCM Plugin

### Architecture

```
ai.Message (user/assistant/tool_result)
        |
        v
  +----------+     Append     +-----------+
  | Provider  | ------------> | SQLite DB |
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

### Compaction

Compaction reduces the context window by summarizing older messages and summaries.

**Modes:**

| Mode          | Behavior                                                                                    |
| ------------- | ------------------------------------------------------------------------------------------- |
| `Incremental` | Single leaf pass + one condensed pass. Runs automatically when context exceeds threshold.   |
| `Full`        | Repeats leaf + condensed passes until no more compaction is possible (up to 10 iterations). |

**Passes:**

1. **Leaf pass** — Groups contiguous runs of message context items outside the fresh tail. Groups of 10+ messages are summarized into a `leaf` summary (depth 0).
2. **Condensed pass** — Groups contiguous runs of summary context items at the same depth. Groups of 2+ summaries are condensed into a `condensed` summary at depth+1.

**Summarization escalation:**

1. **Normal mode** — Preserves key decisions, rationale, constraints. Target: input_tokens/3.
2. **Aggressive mode** — Keeps only durable facts. Triggered when normal exceeds 150% of target.
3. **Deterministic fallback** — Truncates at sentence boundary. Triggered when aggressive still exceeds 150%.

### Context Assembly

1. Separate context items into **fresh tail** (last N message items, default 20) and **older** items.
2. Resolve fresh tail items to `ai.Message`s — always included regardless of budget.
3. Fill remaining budget with older items, newest first.
4. Return older events (chronological) + tail events.

### Concurrency

- **Per-session mutex** — `Append` and `Compact` acquire a per-session lock to prevent concurrent mutations.
- **Conversation ID cache** — Immutable `sessionID -> convID` mapping cached after first lookup.

## Simple Plugin

The Simple plugin uses a sliding-window approach:

1. **Append** stores messages in `ctx_messages` (same schema as LCM).
2. **Assemble** returns the last N messages that fit within the token budget, always honoring freshTail.
3. No summaries, no compaction, no search, no explore.

This is suitable for short-lived conversations or resource-constrained environments.

## Database

- **Location:** `~/.anna/anna.db`
- **Driver:** `modernc.org/sqlite` (pure Go, no CGO)
- **Mode:** WAL, foreign keys enabled
- **Migrations:** Atlas-generated, embedded via `MigrationsFS`, auto-applied on startup.

**Schema:**

| Table                  | Purpose                                                                |
| ---------------------- | ---------------------------------------------------------------------- |
| `ctx_conversations`    | One per session (`session_id` -> `id` mapping), includes agent/user ID |
| `ctx_messages`         | Raw messages with `role`, `content`, `token_count`, sequential `seq`   |
| `ctx_summaries`        | Summary DAG nodes: `kind`, `depth`, `content`, token stats, time range |
| `ctx_items`            | Ordered context window: points to message or summary                   |
| `ctx_summary_messages` | Links leaf summaries to source messages                                |
| `ctx_summary_parents`  | Links condensed summaries to parent summaries (DAG edges)              |
| `ctx_agent_memory`     | Per-user-per-agent persistent notes (used by ProfileStore)             |

## Configuration Defaults

| Constant                  | Value | Description                                 |
| ------------------------- | ----- | ------------------------------------------- |
| `DefaultFreshTail`        | 20    | Messages protected from compaction          |
| `DefaultContextThreshold` | 0.75  | Fraction of budget that triggers compaction |
| `DefaultLeafChunkSize`    | 10    | Minimum messages per leaf summary           |

---

## Identity & User Memory

### 3-Layer System Prompt

| Layer           | Default source                  | File override                  | Description                                         |
| --------------- | ------------------------------- | ------------------------------ | --------------------------------------------------- |
| **Basic**       | Built-in system instructions    | `SYSTEM.md` in agent workspace | Core behavioral instructions for the LLM            |
| **Agent soul**  | `settings_agents.system_prompt` | `SOUL.md` in agent workspace   | Agent identity, personality, and tone               |
| **User memory** | `ctx_agent_memory.content`      | (none — always from DB)        | Per-user-per-agent notes, injected via ProfileStore |

### User Memory

User memory is stored in `ctx_agent_memory`, keyed by `(user_id, agent_id)`. The agent updates it via the memory tool's `profile_update` action and reads it via `profile_get`.

### Agent Workspaces

Each agent has its own workspace at `$ANNA_HOME/workspaces/{agent_id}/` for file overrides, skills, and per-agent data.
