# Memory System

## Lossless Context Management (LCM)

### Status

Implemented -- `lcm/` package with SQLite-backed message DAG, compaction engine, context assembler, and retrieval tools.

### Overview

LCM provides lossless context management for anna. Every message is persisted in a SQLite database and organized into a DAG (directed acyclic graph) of summaries. When the conversation grows too long, older messages are compacted into leaf summaries, and groups of leaf summaries are further condensed into higher-level summaries. The agent can drill back into any summary to recover the original detail -- nothing is ever deleted.

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

- Location: `~/.anna/workspace/lcm.db`
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

LCM is wired into the agent Pool. When a session uses LCM:
- Each message is ingested into the database after every turn.
- Context is assembled from the database before each LLM call.
- Compaction runs automatically based on the context threshold.

---

## Legacy Memory System (Facts + Journal)

> **Note:** The legacy memory system remains active during the transition to LCM. Both systems operate independently.

### Status

Implemented -- `memory/` package with multi-file storage, agent tool, and system prompt integration.

### Overview

Anna has persistent memory across sessions so the agent can recall facts, log events, and search past interactions. The design prioritizes simplicity: flat files, no database, no background consolidation, no new dependencies.

### Architecture

```
Agent (via tool call)
    |
    |  update / append / search
    v
+------------+       +----------+
| MemoryTool | ----> |  Store   |
+------------+       +----+-----+
                          |
              +-----------+-----------+
              |                       |
     ~/.anna/memory/            ~/.anna/memory/
     SOUL.md                    JOURNAL.jsonl
     USER.md                    (events -- searchable)
     FACT.md
     (in system prompt)
```

### Storage

#### Markdown Files

Three persistent markdown files, always loaded into the system prompt under `<memory>` tags:

| File | Purpose |
|------|---------|
| `SOUL.md` | Agent identity and personality |
| `USER.md` | User preferences and context |
| `FACT.md` | Durable knowledge, key decisions, project context |

- Written as a whole (full replacement via atomic write)
- Case-insensitive file lookup (resolves existing files regardless of case)
- Location: `~/.anna/memory/`

#### Journal (`JOURNAL.jsonl`)

- Append-only JSONL file, one entry per line
- NOT loaded into system prompt (too large over time)
- Searchable via the `memory` tool's `search` action
- Each entry has timestamp, tags, and text:

```json
{"ts":"2026-03-06T10:30:00Z","tags":["deploy","staging"],"text":"Deployed v2.1 to staging. User confirmed it works."}
```

- Location: `~/.anna/memory/JOURNAL.jsonl` (or `$ANNA_HOME/memory/JOURNAL.jsonl`)

### Tool Interface

A single `memory` tool with three actions:

| Action | Input | Effect |
|--------|-------|--------|
| `update` | `content` (string) | Atomically overwrite `FACT.md` |
| `append` | `text` (string), `tags` ([]string, optional) | Append timestamped entry to `JOURNAL.jsonl` |
| `search` | `query` (string), `tag` (string), `limit` (int) | Search journal by substring + tag filter |

#### Search Behavior

- Case-insensitive substring match on entry text
- Optional tag filter (also case-insensitive)
- Returns up to `limit` results (default 20) in reverse chronological order
- No indexing -- linear scan over JSONL is fast enough for years of personal assistant use

### System Prompt Integration

The system prompt instructs the agent:

1. Facts in `FACT.md` (plus `SOUL.md` and `USER.md`) are always visible under `<memory>` tags
2. Use `memory` tool action `update` to modify facts (not edit/write)
3. Use `append` to log events worth remembering
4. Use `search` to recall past events by keyword or tag

The agent decides what to save -- no automatic consolidation.

### Data Safety

- **Atomic writes** for markdown files: write to `.tmp` file, then `os.Rename`. No partial writes.
- **Append-only** for `JOURNAL.jsonl`: open with `O_APPEND|O_CREATE`, write one line, close. Crash-safe for single lines within filesystem block size.
- **No background goroutines**: all writes happen synchronously in tool execution.
- **Malformed lines skipped**: corrupted JSONL entries are skipped during search.

### Wiring

```go
// In main.go setup():
memStore := memory.NewStore(filepath.Join(configDir(), "memory"))
extraTools = append(extraTools, memory.NewTool(memStore))
```

The memory tool is always available. No config flag needed -- memory is a core capability.

### File Layout

```
memory/
  memory.go       # Store: Read, Write, Append, Search
  memory_test.go  # Tests covering all operations
  tool.go         # MemoryTool: Definition, Execute (update/append/search)
```
