# Memory System

## Lossless Context Management (LCM)

### Status

Implemented -- `memory/` package with SQLite-backed message DAG, compaction engine, context assembler, and retrieval tools.

### Overview

The memory system provides lossless context management for anna using the LCM approach. Every message is persisted in a SQLite database and organized into a DAG (directed acyclic graph) of summaries. When the conversation grows too long, older messages are compacted into leaf summaries, and groups of leaf summaries are further condensed into higher-level summaries. The agent can drill back into any summary to recover the original detail -- nothing is ever deleted.

### Architecture

```
User / Assistant messages
        |
        v
  +----------+     ingest      +------------+
  |  Engine   | -------------> |  SQLite DB |
  +----------+                 +-----+------+
        |                            |
        |  assemble (budget)         |  DAG of nodes:
        v                            |    message -> leaf summary
  +------------+                     |    leaf summary -> condensed summary
  | Assembler  | <-------------------+
  +------------+
        |
        v
  Context window (fresh tail + summaries within token budget)
```

**Node types in the DAG:**

| Type | Description |
|------|-------------|
| `message` | Raw user/assistant message (leaf of the DAG) |
| `leaf_summary` | Summary of a chunk of consecutive messages |
| `condensed_summary` | Summary of a group of same-depth summaries |

### Database

- Location: `~/.anna/workspace/memory.db`
- Schema managed by embedded SQL migrations
- Tables: `nodes` (DAG nodes with content, token counts, depth, lineage) and `edges` (parent-child relationships)

### Retrieval Tools

Three tools provide read access to the compacted history:

| Tool | Purpose |
|------|---------|
| `memory_grep` | Search messages and summaries by keyword. Returns matching nodes with metadata. |
| `memory_describe` | Inspect a specific summary node: its metadata, depth, child count, and lineage. |
| `memory_expand` | Drill into a summary to retrieve its children (the original messages or sub-summaries it was built from). |

**TODO:** `memory_expand` will spawn a sub-agent to answer questions using the expanded context, rather than injecting raw children into the main context window.

### Compaction Modes

| Mode | Trigger | Behavior |
|------|---------|----------|
| **Incremental** | After each agent turn | Compacts only the oldest messages beyond the fresh tail. Runs automatically when context exceeds the threshold. |
| **Full** | Manual `/compact` command | Compacts all eligible messages and then runs a condensed pass to merge leaf summaries at the same depth. |

**Compaction passes:**

1. **Leaf pass** -- Groups consecutive raw messages into chunks, summarizes each chunk into a `leaf_summary` node, records parent-child edges.
2. **Condensed pass** -- Groups same-depth summaries, summarizes each group into a `condensed_summary` node at depth+1.

### Context Assembly

The assembler builds the context window for each LLM call:

1. Keep the **fresh tail** (most recent N messages, default 20) verbatim.
2. Fill remaining budget with summaries, preferring lower-depth (more detailed) summaries.
3. Format everything as XML blocks (`<context>` with `<summary>` and `<message>` children).

### Integration

The memory engine is wired into the agent Pool. When a session uses it:
- Each message is ingested into the database after every turn.
- Context is assembled from the database before each LLM call.
- Compaction runs automatically based on the context threshold.

---

## Identity Files (SOUL.md + USER.md)

Two persistent markdown files are loaded into the system prompt under `<memory>` tags:

| File | Purpose |
|------|---------|
| `SOUL.md` | Agent identity, personality, tone, communication style |
| `USER.md` | User preferences, name, timezone, personal context |

- Location: `~/.anna/workspace/`
- Editable by the agent via the `edit` or `write` tool
- Project-level overrides supported via `.agents/SOUL.md` and `.agents/USER.md`
- Case-insensitive file lookup
