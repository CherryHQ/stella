# Memory System

## Status

Two memory subsystems coexist:

- **Legacy** (stable) -- flat-file `memory/` package with FACT.md, JOURNAL.jsonl, and system prompt integration.
- **LCM** (experimental) -- Lossless Context Management via SQLite-backed DAG of summaries in the `lcm/` package.

During the transition period both systems are active. The legacy system handles cross-session persistent knowledge (facts, journal). LCM handles per-session conversation memory with compaction and retrieval.

---

## LCM (Lossless Context Management)

### Overview

LCM gives the agent lossless recall of arbitrarily long conversations. Every message is persisted to a SQLite database and organized into a directed acyclic graph (DAG) of summaries. When the context window fills up, older messages are compacted into progressively higher-level summaries -- but the original messages are never deleted. The agent can drill back into any summary to recover full detail on demand.

### Architecture

```
Agent (via tool call)
    |
    |  memory_grep / memory_describe / memory_expand
    v
+---------------+       +-----------------+       +------------+
| LCM Tools     | ----> | RetrievalEngine | ----> |  SQLite DB |
+---------------+       +-----------------+       +------+-----+
                                                         |
                        +----------------+               |
                        | Engine         |---------------+
                        | (ingest,       |
                        |  assemble,     |
                        |  compact)      |
                        +----------------+
                                |
                        +-------v--------+
                        | Assembler      |  context_items table
                        +----------------+  (ordinal-ordered view
                                |           of messages + summaries)
                        +-------v--------+
                        | Compaction     |  summaries table
                        | Engine         |  (DAG of leaf + condensed)
                        +----------------+
```

Storage: `~/.anna/workspace/lcm.db` (SQLite)

### DAG-Based Summarization Hierarchy

Compaction creates a multi-level DAG where each node is a summary:

```
         [condensed depth=2]
            /           \
  [condensed depth=1]  [condensed depth=1]
    /       \              /       \
[leaf d=0] [leaf d=0]  [leaf d=0] [leaf d=0]
  |  |       |  |        |  |       |  |
 msgs       msgs        msgs       msgs
```

**Leaf summaries** (depth 0) are created from contiguous runs of raw messages (default chunk size: 10 messages). They link back to their source messages via `summary_messages`.

**Condensed summaries** (depth N+1) are created from runs of adjacent same-depth summaries. They link to their child summaries via `summary_parents`. Each condensed pass reduces token count by roughly 50%.

The fresh tail (default: 20 most recent messages) is always protected from compaction.

### Compaction Modes

| Mode | Behavior |
|------|----------|
| `incremental` | Single leaf pass + single condensed pass |
| `full` | Repeated leaf + condensed passes until no further compaction is possible (safety limit: 10 iterations) |

### Engine Lifecycle

1. **Bootstrap** -- ensures a conversation record exists for the session.
2. **Ingest** -- each message (user, assistant, tool) is persisted and appended to the context items list.
3. **Assemble** -- builds a context window within a token budget. Includes all fresh-tail messages, then fills remaining budget with older items (summaries or messages) newest-first.
4. **Compact** -- triggered when token count exceeds threshold. Runs leaf and condensed passes within a transaction.

### Tool Interface

Three tools for retrieval (read-only access to compacted history):

#### `memory_grep`

Search conversation history for messages and summaries matching a pattern.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `pattern` | string | yes | Text to search for (case-insensitive substring match) |
| `scope` | string | no | `"messages"`, `"summaries"`, or `"both"` (default) |
| `limit` | integer | no | Max results (default 20) |

Returns JSON array of `GrepResult` with `source_type`, `source_id`, `content` (truncated to 500 chars), and `timestamp`.

#### `memory_describe`

Inspect a summary's content, metadata, and lineage (parents/children).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `summary_id` | string | yes | Summary ID (e.g., `"sum_abc123def456"`) |

Returns JSON with `summary_id`, `kind`, `depth`, `content`, `earliest_at`, `latest_at`, `descendant_count`, `parent_ids`, and `child_ids`.

#### `memory_expand`

Drill into a summary to retrieve original messages (leaf) or child summaries (condensed).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `summary_id` | string | yes | Summary ID to expand |
| `token_cap` | integer | no | Max tokens of content to return (default 4000) |

For **leaf** summaries: returns the source messages with role, content, and timestamp.
For **condensed** summaries: returns child summaries with their kind, depth, and content.

> **TODO**: `memory_expand` currently returns raw data. A future enhancement will use a sub-agent to synthesize expanded content into a coherent answer, allowing deeper multi-level drill-down without overwhelming the context window.

### Typical Retrieval Workflow

1. **Grep** -- agent calls `memory_grep` to find relevant content by keyword.
2. **Describe** -- if grep returns summary hits, agent calls `memory_describe` to see the summary's place in the DAG (depth, children, time range).
3. **Expand** -- agent calls `memory_expand` to drill into the summary and recover original messages or child summaries.

### Database Schema

Key tables:

| Table | Purpose |
|-------|---------|
| `conversations` | Session-to-conversation mapping |
| `messages` | All raw messages (never deleted) |
| `context_items` | Ordered view of the current context (mix of messages and summaries) |
| `summaries` | Leaf and condensed summary nodes |
| `summary_messages` | Links leaf summaries to source messages |
| `summary_parents` | Links condensed summaries to child summaries |

### File Layout

```
lcm/
  types.go            # Engine interface, type constants, result types
  engine.go           # Engine implementation (bootstrap, ingest, assemble, compact)
  assembler.go        # Context assembler with budget selection and XML formatting
  compaction.go       # Leaf and condensed compaction passes
  summarize.go        # LLM-backed summarizer
  retrieval.go        # RetrievalEngine (grep, describe, expand)
  context.go          # Session ID context propagation
  database.go         # SQLite setup and migrations
  db.go               # SQL queries (sqlc generated)
  models.go           # sqlc model types
  queries.sql.go      # sqlc query implementations
  tool/
    grep.go           # memory_grep tool
    describe.go       # memory_describe tool
    expand.go         # memory_expand tool
    helpers.go        # Shared argument parsing helpers
```

---

## Legacy Memory System

> The legacy system predates LCM and handles **cross-session** persistent knowledge. It remains active.

### Overview

Flat-file storage with agent tool access. Three markdown files are loaded into the system prompt; a JSONL journal is searchable via the `memory` tool.

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
