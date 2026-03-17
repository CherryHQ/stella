# Plan: Lossless Context Management (LCM)

## Overview

Implement a DAG-based lossless context management system inspired by lossless-claw, replacing anna's current lossy compaction and file-based memory system with a SQLite-backed solution that preserves all messages and enables hierarchical summarization with full recall capabilities.

### Goals

- Never lose raw message data — persist every message to SQLite
- Implement hierarchical DAG-based summarization (leaf → condensed summaries)
- Replace the current flat compaction with incremental leaf/condensed passes
- Provide retrieval tools (`memory_grep`, `memory_describe`, `memory_expand`) for recalling compacted details
- Replace the existing `internal/memory/` package (FACT.md, JOURNAL.jsonl) with unified LCM storage

### Success Criteria

- [ ] All messages persisted to SQLite with structured parts
- [ ] Leaf summarization creates depth-0 summaries from message chunks
- [ ] Condensed summarization creates depth-1+ summaries from other summaries
- [ ] Context assembly combines summaries + fresh tail within token budget
- [ ] `memory_grep` tool searches messages and summaries
- [ ] `memory_describe` tool inspects summary content and lineage
- [ ] `memory_expand` tool drills into summaries (manual mode, sub-agent deferred)
- [ ] Existing compaction triggers (`/compact`, auto-compact) use new system
- [ ] Tests pass with >80% coverage on new code

### Out of Scope

- FTS5 full-text search (use basic LIKE/regex for now)
- Large file interception and storage
- Cross-conversation search
- Sub-agent spawning for `memory_expand` (document as TODO)
- Migration tooling for existing sessions

## Technical Approach

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         agent/Pool                              │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ Chat() → Ingest → AssembleContext → Runner.Chat()        │   │
│  │ CompactSession() → LeafPass → CondensedPass              │   │
│  └──────────────────────────────────────────────────────────┘   │
├─────────────────────────────────────────────────────────────────┤
│                      lcm/ (new package)                         │
│  ┌─────────────┐ ┌─────────────┐ ┌────────────────────────────┐ │
│  │ Engine      │ │ Compaction  │ │ Assembler                  │ │
│  │ - Ingest()  │ │ - LeafPass  │ │ - Assemble(budget, tail)   │ │
│  │ - Compact() │ │ - Condense  │ │ - ResolveItems()           │ │
│  └─────────────┘ └─────────────┘ └────────────────────────────┘ │
│  ┌─────────────┐ ┌─────────────┐ ┌────────────────────────────┐ │
│  │ Store       │ │ Retrieval   │ │ Summarize                  │ │
│  │ - Messages  │ │ - Grep()    │ │ - LeafPrompt()             │ │
│  │ - Summaries │ │ - Describe()│ │ - CondensedPrompt(depth)   │ │
│  │ - Context   │ │ - Expand()  │ │ - Escalation logic         │ │
│  └─────────────┘ └─────────────┘ └────────────────────────────┘ │
├─────────────────────────────────────────────────────────────────┤
│                      lcm/tool/ (new)                            │
│  ┌─────────────┐ ┌─────────────┐ ┌────────────────────────────┐ │
│  │ GrepTool    │ │ DescribeTool│ │ ExpandTool                 │ │
│  └─────────────┘ └─────────────┘ └────────────────────────────┘ │
├─────────────────────────────────────────────────────────────────┤
│                      SQLite Database                            │
│  conversations │ messages │ message_parts │ summaries           │
│  summary_messages │ summary_parents │ context_items             │
└─────────────────────────────────────────────────────────────────┘
```

### Components

- **lcm/engine.go**: Main LCM engine — lifecycle hooks (bootstrap, ingest, afterTurn, compact)
- **lcm/db.go**: Database connection — open/close, WAL mode, migrations
- **lcm/schema.sql**: SQLite schema definition (tables, indexes, constraints)
- **lcm/queries.sql**: SQL queries with sqlc annotations
- **lcm/sqlc.yaml**: sqlc configuration
- **lcm/models.go**: sqlc-generated model structs (auto-generated)
- **lcm/db.sqlc.go**: sqlc-generated query methods (auto-generated)
- **lcm/compaction.go**: Compaction engine — leaf passes, condensation, escalation
- **lcm/assembler.go**: Context assembly — budget-based selection, fresh tail protection
- **lcm/summarize.go**: Summarization prompts — depth-aware prompts, fallback logic
- **lcm/retrieval.go**: Retrieval engine — grep, describe, expand operations
- **lcm/tool/grep.go**: `memory_grep` tool implementation
- **lcm/tool/describe.go**: `memory_describe` tool implementation
- **lcm/tool/expand.go**: `memory_expand` tool implementation

### Data Models

```go
// Conversation maps a session to an LCM conversation
type Conversation struct {
    ID            int64
    SessionID     string
    Title         string
    BootstrappedAt *time.Time
    CreatedAt     time.Time
    UpdatedAt     time.Time
}

// Message stores a single message in the conversation
type Message struct {
    ID             int64
    ConversationID int64
    Seq            int       // monotonically increasing within conversation
    Role           string    // "user", "assistant", "tool"
    Content        string    // plain text extraction
    TokenCount     int
    CreatedAt      time.Time
}

// MessagePart stores structured content blocks
type MessagePart struct {
    ID          string
    MessageID   int64
    PartType    string    // "text", "reasoning", "tool"
    Ordinal     int
    TextContent *string
    ToolCallID  *string
    ToolName    *string
    ToolInput   *string
    ToolOutput  *string
    Metadata    *string   // JSON for provider-specific data
}

// Summary stores leaf and condensed summaries
type Summary struct {
    ID                      string    // "sum_" + 16 hex chars
    ConversationID          int64
    Kind                    string    // "leaf" or "condensed"
    Depth                   int       // 0 for leaf, 1+ for condensed
    Content                 string
    TokenCount              int
    EarliestAt              *time.Time
    LatestAt                *time.Time
    DescendantCount         int
    DescendantTokenCount    int
    SourceMessageTokenCount int
    CreatedAt               time.Time
}

// ContextItem tracks what the model sees (message or summary reference)
type ContextItem struct {
    ConversationID int64
    Ordinal        int
    ItemType       string  // "message" or "summary"
    MessageID      *int64
    SummaryID      *string
    CreatedAt      time.Time
}

// SummaryMessage links summaries to source messages
type SummaryMessage struct {
    SummaryID string
    MessageID int64
    Ordinal   int
}

// SummaryParent links condensed summaries to parent summaries
type SummaryParent struct {
    SummaryID       string
    ParentSummaryID string
    Ordinal         int
}
```

### APIs / Interfaces

```go
// Engine is the main LCM interface
type Engine interface {
    // Bootstrap reconciles session state on startup
    Bootstrap(ctx context.Context, sessionID string) error

    // Ingest persists a message and appends to context
    Ingest(ctx context.Context, sessionID string, evt runner.RPCEvent) error

    // IngestBatch persists multiple messages
    IngestBatch(ctx context.Context, sessionID string, evts []runner.RPCEvent) error

    // Assemble builds context for the model within token budget
    // Returns []runner.RPCEvent for compatibility with existing runner pipeline
    Assemble(ctx context.Context, sessionID string, budget int, freshTail int) ([]runner.RPCEvent, error)

    // Compact runs compaction passes
    Compact(ctx context.Context, sessionID string) (*CompactionResult, error)

    // NeedsCompaction checks if compaction should run
    NeedsCompaction(ctx context.Context, sessionID string, threshold float64) bool

    // Retrieval returns the retrieval engine for tools
    Retrieval() *RetrievalEngine
}

// Summarizer generates summaries from content
type Summarizer interface {
    // Summarize generates a summary from text
    Summarize(ctx context.Context, text string, opts SummarizeOptions) (string, error)
}

// SummarizeOptions controls summarization behavior
type SummarizeOptions struct {
    IsCondensed  bool
    Depth        int
    Aggressive   bool
    Previous     string // previous summary for continuity
    TargetTokens int
}
```

### Database Implementation

**SQLite Driver:** `modernc.org/sqlite` (pure Go, no CGO required)
**Query Generation:** `sqlc` for type-safe database operations

```yaml
# lcm/sqlc.yaml
version: "2"
sql:
  - engine: "sqlite"
    queries: "queries.sql"
    schema: "schema.sql"
    gen:
      go:
        package: "lcm"
        out: "."
        emit_json_tags: true
        emit_empty_slices: true
```

**Schema file** (`lcm/schema.sql`):
```sql
CREATE TABLE conversations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL UNIQUE,
    title TEXT,
    bootstrapped_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool')),
    content TEXT NOT NULL,
    token_count INTEGER NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (conversation_id, seq)
);

CREATE TABLE message_parts (
    id TEXT PRIMARY KEY,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    part_type TEXT NOT NULL CHECK (part_type IN ('text', 'reasoning', 'tool')),
    ordinal INTEGER NOT NULL,
    text_content TEXT,
    tool_call_id TEXT,
    tool_name TEXT,
    tool_input TEXT,
    tool_output TEXT,
    metadata TEXT,
    UNIQUE (message_id, ordinal)
);

CREATE TABLE summaries (
    id TEXT PRIMARY KEY,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('leaf', 'condensed')),
    depth INTEGER NOT NULL DEFAULT 0,
    content TEXT NOT NULL,
    token_count INTEGER NOT NULL,
    earliest_at TEXT,
    latest_at TEXT,
    descendant_count INTEGER NOT NULL DEFAULT 0,
    descendant_token_count INTEGER NOT NULL DEFAULT 0,
    source_message_token_count INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE summary_messages (
    summary_id TEXT NOT NULL REFERENCES summaries(id) ON DELETE CASCADE,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL,
    PRIMARY KEY (summary_id, message_id)
);

CREATE TABLE summary_parents (
    summary_id TEXT NOT NULL REFERENCES summaries(id) ON DELETE CASCADE,
    parent_summary_id TEXT NOT NULL REFERENCES summaries(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL,
    PRIMARY KEY (summary_id, parent_summary_id)
);

CREATE TABLE context_items (
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    item_type TEXT NOT NULL CHECK (item_type IN ('message', 'summary')),
    message_id INTEGER REFERENCES messages(id) ON DELETE RESTRICT,
    summary_id TEXT REFERENCES summaries(id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (conversation_id, ordinal),
    CHECK (
        (item_type = 'message' AND message_id IS NOT NULL AND summary_id IS NULL) OR
        (item_type = 'summary' AND summary_id IS NOT NULL AND message_id IS NULL)
    )
);

-- Indexes
CREATE INDEX idx_messages_conv_seq ON messages(conversation_id, seq);
CREATE INDEX idx_summaries_conv ON summaries(conversation_id, created_at);
CREATE INDEX idx_context_items_conv ON context_items(conversation_id, ordinal);
```

**Query file** (`lcm/queries.sql`) will contain all SQL queries with sqlc annotations.

### Token Estimation

Use the same 4-chars/token heuristic as existing anna code for consistency:

```go
// EstimateTokens returns a rough token count (~4 chars per token).
// This matches the existing estimator in agent/store/store.go.
func EstimateTokens(text string) int {
    return (len(text) + 3) / 4
}
```

This is stored on Message.TokenCount and Summary.TokenCount at creation time.

### Concurrency Model

Session-level serialization via mutex in the Engine:

```go
type engine struct {
    db         *sql.DB
    sessionMu  map[string]*sync.Mutex  // per-session mutex
    globalMu   sync.Mutex              // guards sessionMu map
    // ...
}

func (e *engine) withSessionLock(sessionID string, fn func() error) error {
    e.globalMu.Lock()
    mu, ok := e.sessionMu[sessionID]
    if !ok {
        mu = &sync.Mutex{}
        e.sessionMu[sessionID] = mu
    }
    e.globalMu.Unlock()

    mu.Lock()
    defer mu.Unlock()
    return fn()
}
```

All mutating operations (Ingest, Compact) acquire the session lock. SQLite WAL mode allows concurrent reads during writes.

### Transaction Boundaries

Compaction operations are atomic within a transaction:

```go
func (c *CompactionEngine) leafPass(ctx context.Context, convID int64) error {
    tx, _ := c.db.BeginTx(ctx, nil)
    defer tx.Rollback()

    // 1. Create summary
    // 2. Link to source messages (summary_messages)
    // 3. Replace context items range with summary

    return tx.Commit()
}
```

### Summary XML Format

Summaries are presented to the model as user messages with XML wrapper:

```xml
<summary id="sum_abc123def456" kind="leaf" depth="0"
         earliest_at="2026-03-10T14:30:00" latest_at="2026-03-10T15:45:00">
  <content>
    Session focused on implementing user authentication...

    Expand for details about: exact error messages, config values, intermediate steps
  </content>
</summary>
```

For condensed summaries, include parent references:

```xml
<summary id="sum_xyz789" kind="condensed" depth="1" descendant_count="4" ...>
  <parents>
    <summary_ref id="sum_abc123" />
    <summary_ref id="sum_def456" />
  </parents>
  <content>...</content>
</summary>
```

### Fresh Tail Definition

**Fresh tail** = the last N **raw messages** (item_type='message') in context_items, protected from compaction. Summaries are never part of the fresh tail. Default: 20 messages.

### Store Interface Migration

LCM **replaces** `store.Store` entirely. The existing interface methods map to:

| Old `store.Store` | New LCM Engine |
|-------------------|----------------|
| `Append()` | `Ingest()` / `IngestBatch()` |
| `Load()` | `Assemble()` (returns context, not raw history) |
| `Compact()` | `Compact()` (DAG-based, not flat) |
| `EstimateTokens()` | `NeedsCompaction()` + internal token tracking |
| `SaveInfo()` / `LoadInfo()` | Stored in conversations table |

The `agent/pool.go` will be updated to use `lcm.Engine` directly instead of `store.Store`.

### Tool Registration

LCM tools require Engine access. Inject via factory function:

```go
// In agent/tool/registry.go or similar
func NewLCMTools(engine *lcm.Engine) []tool.Tool {
    return []tool.Tool{
        lcmtool.NewGrepTool(engine),
        lcmtool.NewDescribeTool(engine),
        lcmtool.NewExpandTool(engine),
    }
}

// Pool creates tools with Engine reference
func (p *Pool) initTools() {
    lcmTools := tool.NewLCMTools(p.lcmEngine)
    p.registry.Register(lcmTools...)
}
```

## Implementation Steps

### Phase 1: Foundation — SQLite Store

1. **Task 1.1**: Create `lcm/` package structure and types
   - Files: `lcm/types.go`
   - Define all data model structs (Conversation, Message, Summary, etc.)

2. **Task 1.2**: Set up sqlc and SQLite connection
   - Files: `lcm/db.go`, `lcm/sqlc.yaml`, `lcm/schema.sql`, `lcm/queries.sql`
   - Use `modernc.org/sqlite` (pure Go, no CGO)
   - Use `sqlc` for type-safe query generation
   - Create schema.sql with all table definitions
   - Run `sqlc generate` to create Go code

3. **Task 1.3**: Write sqlc queries for conversations
   - Files: `lcm/queries.sql`
   - Queries: CreateConversation, GetConversation, GetConversationBySessionID, UpdateConversation

4. **Task 1.4**: Write sqlc queries for messages
   - Files: `lcm/queries.sql`
   - Queries: CreateMessage, GetMessage, GetMessagesByConversation, GetMessageCount
   - Queries: CreateMessagePart, GetMessageParts

5. **Task 1.5**: Write sqlc queries for summaries
   - Files: `lcm/queries.sql`
   - Queries: CreateSummary, GetSummary, GetSummariesByConversation
   - Queries: LinkSummaryToMessage, LinkSummaryToParent
   - Queries: GetSummaryParents, GetSummaryChildren, GetSummarySubtree (recursive CTE)

6. **Task 1.6**: Write sqlc queries for context items
   - Files: `lcm/queries.sql`
   - Queries: AppendContextItem, GetContextItems, DeleteContextItemsInRange
   - Queries: GetContextTokenCount, ResequenceContextItems

### Phase 2: Context Assembly

7. **Task 2.1**: Implement context assembler
   - Files: `lcm/assembler.go`
   - Resolve items (message → content, summary → XML wrapper)
   - Budget-based selection with fresh tail protection

8. **Task 2.2**: Implement summary XML formatting
   - Files: `lcm/assembler.go`
   - Format summaries with XML attributes (id, kind, depth, timestamps)
   - Include parent references for condensed summaries

### Phase 3: Compaction Engine

9. **Task 3.1**: Implement summarization prompts
   - Files: `lcm/summarize.go`
   - Leaf prompt (normal/aggressive modes)
   - Condensed prompts (D1, D2, D3+ depth-aware)
   - Target token calculation
   - See "Summarization Prompts" section below for templates

10. **Task 3.2**: Implement summarization with escalation
    - Files: `lcm/summarize.go`
    - Normal → aggressive → deterministic fallback
    - Normalize provider response blocks to plain text

11. **Task 3.3**: Implement leaf compaction pass
    - Files: `lcm/compaction.go`
    - Find eligible message chunks outside fresh tail
    - Create leaf summary, link to messages, replace in context
    - **Atomic transaction**: BEGIN → create summary → link messages → update context → COMMIT

12. **Task 3.4**: Implement condensed compaction pass
    - Files: `lcm/compaction.go`
    - Find eligible same-depth summaries
    - Create condensed summary, link to parents, replace in context
    - **Atomic transaction**: BEGIN → create summary → link parents → update context → COMMIT

13. **Task 3.5**: Implement compaction orchestration
    - Files: `lcm/compaction.go`
    - Incremental mode (after turn): leaf + optional condensation
    - Full sweep mode (manual): repeated leaf + condensation passes
    - Budget-targeted mode: compact until under threshold
    - Session-level lock held during entire compaction cycle

### Phase 4: Engine Integration

14. **Task 4.1**: Implement LCM engine
    - Files: `lcm/engine.go`
    - Bootstrap, ingest, ingest batch, assemble, compact
    - Wire together store, assembler, compaction engine

15. **Task 4.2**: Integrate with agent/Pool
    - Files: `agent/pool.go`, `agent/session.go`
    - Replace `store.Store` with `lcm.Engine` field
    - Update `Chat()`: call `lcm.Ingest()` for user/assistant messages
    - Update `Chat()`: call `lcm.Assemble()` instead of `sess.Events` for runner history
    - Update `CompactSession()`: call `lcm.Compact()` instead of `store.Compact()`
    - Update `NeedsCompaction()`: call `lcm.NeedsCompaction()`
    - Remove `persist()` helper — replaced by `lcm.Ingest()`

### Phase 5: Retrieval Tools

16. **Task 5.1**: Implement retrieval engine
    - Files: `lcm/retrieval.go`
    - Grep (regex/LIKE search across messages and summaries)
    - Describe (fetch summary with lineage)
    - Expand (walk DAG to retrieve details)

17. **Task 5.2**: Implement `memory_grep` tool
    - Files: `lcm/tool/grep.go`
    - Tool definition with JSON schema
    - Execute: pattern, mode (regex/text), scope (messages/summaries/both)

18. **Task 5.3**: Implement `memory_describe` tool
    - Files: `lcm/tool/describe.go`
    - Tool definition with JSON schema
    - Execute: return summary content, metadata, lineage

19. **Task 5.4**: Implement `memory_expand` tool
    - Files: `lcm/tool/expand.go`
    - Tool definition with JSON schema
    - Execute: walk DAG, return children/messages up to token cap
    - Document sub-agent spawning as TODO

20. **Task 5.5**: Register LCM tools
    - Files: `agent/tool/registry.go` (or equivalent)
    - Add memory_grep, memory_describe, memory_expand to available tools

### Phase 6: Cleanup and Documentation

21. **Task 6.1**: Remove old memory package
    - Files: `internal/memory/` (delete entire directory)
    - Remove references from imports

22. **Task 6.2**: Update documentation
    - Files: `docs/memory-system.md`
    - Document new LCM architecture, tools, configuration
    - Add TODO for sub-agent expansion

23. **Task 6.3**: Update configuration
    - Files: `docs/configuration.md`
    - Add LCM config options (freshTailCount, contextThreshold, etc.)

## Testing Strategy

### Unit Tests

- `lcm/store_test.go`: Message/conversation CRUD, bulk operations
- `lcm/summary_test.go`: Summary CRUD, lineage queries, subtree CTE
- `lcm/context_test.go`: Context item management, resequencing
- `lcm/assembler_test.go`: Budget selection, fresh tail protection
- `lcm/compaction_test.go`: Leaf pass, condensed pass, escalation
- `lcm/retrieval_test.go`: Grep, describe, expand operations
- `lcm/tool/*_test.go`: Tool execution, parameter validation

### Integration Tests

- Full compaction cycle: ingest → threshold → leaf → condense → assemble
- Retrieval after compaction: grep finds content, expand recovers details
- Engine lifecycle: bootstrap → multiple turns → compaction → continue

### Edge Cases

- Empty conversation (no messages to compact)
- Single message (below fresh tail threshold)
- Summary larger than input (triggers escalation)
- All escalation levels fail (deterministic fallback)
- Concurrent ingest/compact (serialization)
- Database corruption recovery (graceful degradation)

## Considerations

### Security

- SQLite file permissions (0600)
- No sensitive data in summary content (tool outputs may contain secrets)
- Input validation on tool parameters (prevent SQL injection via LIKE)

### Performance

- Batch message inserts for bulk operations
- Lazy loading of message parts (only when needed for assembly)
- Index on (conversation_id, seq) for message queries
- Index on (conversation_id, ordinal) for context items
- Connection pooling not needed (single writer for SQLite)

### Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| SQLite write latency in hot path | Low | Medium | Batch writes, async where possible |
| Summary quality degrades at depth | Medium | Medium | Depth-aware prompts, escalation |
| Token estimates drift from reality | Medium | Low | Use same estimator as current code (~4 chars/token) |
| Breaking change to session format | High | High | New DB file, don't migrate existing sessions |
| Compaction runs too frequently | Low | Low | Configurable threshold, incremental mode |

### Open Questions (Resolved)

- [x] Replace or augment existing JSONL store? → Replace
- [x] Keep existing memory package? → Replace entirely
- [x] Sub-agent support for expand? → Defer, document as TODO
- [x] Default fresh tail count? → **20** (maintain anna's current behavior)
- [x] Default context threshold? → **0.75** (LCM default, triggers at 75% of budget)
- [x] Database file location? → **~/.anna/lcm.db** (global, session isolation via conversation_id)

## Summarization Prompts

### Leaf Summary Prompt (Normal Mode)

```
You summarize a SEGMENT of a conversation for future model turns.
Treat this as incremental memory compaction input, not a full-conversation summary.

Normal summary policy:
- Preserve key decisions, rationale, constraints, and active tasks.
- Keep essential technical details needed to continue work safely.
- Remove obvious repetition and conversational filler.

Output requirements:
- Plain text only. No preamble, headings, or markdown formatting.
- Track file operations (created, modified, deleted, renamed) with file paths.
- If no file operations appear, include exactly: "Files: none".
- End with: "Expand for details about: <comma-separated list of what was dropped>".
- Target length: about {targetTokens} tokens or less.

<previous_context>
{previousSummary or "(none)"}
</previous_context>

<conversation_segment>
{text}
</conversation_segment>
```

### Leaf Summary Prompt (Aggressive Mode)

Same structure but with:
```
Aggressive summary policy:
- Keep only durable facts and current task state.
- Remove examples, repetition, and low-value narrative details.
- Preserve explicit TODOs, blockers, decisions, and constraints.
```

### Condensed D1 Prompt

```
You are compacting leaf-level conversation summaries into a single condensed memory node.
You are preparing context for a fresh model instance that will continue this conversation.

Preserve:
- Decisions made and their rationale when rationale matters going forward.
- Earlier decisions that were superseded, and what replaced them.
- Completed tasks/topics with outcomes.
- In-progress items with current state and what remains.
- Blockers, open questions, and unresolved tensions.

Drop low-value detail:
- Context unchanged from previous_context.
- Intermediate dead ends where the conclusion is already known.
- Tool-internal mechanics and process scaffolding.

Use plain text. Include a timeline with timestamps for significant events.
End with: "Expand for details about: <list>".
Target length: about {targetTokens} tokens.
```

### Condensed D2+ Prompt

```
You are condensing multiple session-level summaries into a higher-level memory node.
A future model should understand trajectory, not per-session minutiae.

Preserve:
- Decisions still in effect and their rationale.
- Completed work with outcomes.
- Active constraints, limitations, and known issues.
- Current state of in-progress work.

Drop:
- Session-local operational detail.
- Identifiers that are no longer relevant.
- Intermediate states superseded by later outcomes.

Use plain text. Include a timeline with dates for key milestones.
End with: "Expand for details about: <list>".
Target length: about {targetTokens} tokens.
```

## Review Feedback

### Round 1

**Reviewer findings addressed:**

| Issue | Severity | Resolution |
|-------|----------|------------|
| Missing token counting integration | 🔴 Critical | Added "Token Estimation" section - use 4-chars/token heuristic |
| Context assembly incompatible with runner | 🔴 Critical | Changed `Assemble()` to return `[]runner.RPCEvent` |
| No transaction boundaries | 🔴 Critical | Added "Transaction Boundaries" section with tx pattern |
| Open questions unresolved | 🟠 Important | Resolved: fresh_tail=20, threshold=0.75, db=~/.anna/lcm.db |
| Concurrency model not specified | 🟠 Important | Added "Concurrency Model" section with session mutex |
| Store interface migration undefined | 🟠 Important | Added "Store Interface Migration" section |
| Tool registration unclear | 🟠 Important | Added "Tool Registration" section with factory pattern |
| Summary prompts not detailed | 🟠 Important | Added "Summarization Prompts" section |
| XML format undocumented | 🟡 Suggestion | Added "Summary XML Format" section |
| Fresh tail definition unclear | 🟡 Suggestion | Added "Fresh Tail Definition" section |

## Implementation Progress

(Updated during implementation phase)
