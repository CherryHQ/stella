# Pluggable Memory System Design

## Table of Contents

1. [Problem Statement](#1-problem-statement)
2. [Goals and Non-Goals](#2-goals-and-non-goals)
3. [Architecture Overview](#3-architecture-overview)
4. [Core Concepts](#4-core-concepts)
   - 4.1 [Session](#41-session)
   - 4.2 [Provider (core interface)](#42-provider-core-interface)
5. [Capability Interfaces](#5-capability-interfaces)
   - 5.1 [Compactor](#51-compactor)
   - 5.2 [Searcher](#52-searcher)
   - 5.3 [Explorer](#53-explorer)
   - 5.4 [ProfileStore](#54-profilestore)
   - 5.5 [SessionManager](#55-sessionmanager)
   - 5.6 [ReviewSource](#56-reviewsource)
6. [Concurrency Contract](#6-concurrency-contract)
7. [Tool Auto-Generation](#7-tool-auto-generation)
8. [Plugin Registry](#8-plugin-registry)
9. [Built-in Implementations](#9-built-in-implementations)
   - 9.1 [LCM Plugin (default)](#91-lcm-plugin-default)
   - 9.2 [Simple Plugin](#92-simple-plugin)
10. [Integration Points](#10-integration-points)
    - 10.1 [Pool / Chat loop](#101-pool--chat-loop)
    - 10.2 [System prompt injection](#102-system-prompt-injection)
    - 10.3 [Self-improve review loop](#103-self-improve-review-loop)
    - 10.4 [Admin panel](#104-admin-panel)
    - 10.5 [Wiring in cmd/anna](#105-wiring-in-cmdanna)
11. [Data Flow Diagram](#11-data-flow-diagram)
12. [How to Write a Memory Plugin](#12-how-to-write-a-memory-plugin)
13. [Testing](#13-testing)
14. [Implementation Order](#14-implementation-order)
15. [Design Decisions and Trade-offs](#15-design-decisions-and-trade-offs)
16. [Package Layout](#16-package-layout)

---

## 1. Problem Statement

The current memory system in anna is split across at least six distinct locations with different owners, storage models, and access patterns:

| Location | What it stores | Who owns it |
|---|---|---|
| `internal/memory/engine.go` | Conversation transcript (messages + summaries) | `memory.Engine` |
| `internal/memory/usermemory.go` | Per-user-per-agent persistent notes | Wraps `config.Store` |
| `internal/config/store.go` | `GetUserAgentMemory` / `SetUserAgentMemory` | Config layer |
| `internal/memory/tool/memory.go` | LLM-facing tool (grep, describe, expand, update) | Hard-coded actions |
| `internal/agent/selfimprove/memorytool.go` | Duplicate get/update tool for review agent | `selfimprove` package |
| `internal/agent/selfimprove/review.go` | Raw SQL queries over `ctx_summaries`, `ctx_messages` | Self-improve package |

This fragmentation causes several concrete problems:

1. **Wrong ownership boundary.** `ctx_agent_memory` is part of the `ctx_*` schema family but is accessed through `config.Store`. Any code that needs "memory" must import both `memory` and `config` packages.

2. **Duplicated logic.** The agent-facing tool and the review-agent tool both implement get/update on the same profile table with duplicated validation logic.

3. **Tight schema coupling.** Self-improve directly queries `ctx_summaries` and `ctx_messages` SQL tables, coupling it to the LCM schema. If someone wants to store conversations in a different backend (Redis, vector DB, remote service), the entire self-improve loop breaks.

4. **No extension point.** There is no way for a user to swap out the memory backend. The LCM engine is instantiated directly in `cmd/anna/commands.go`. An operator who wants semantic search, a cloud-hosted memory service, or simpler truncation-only storage has no path to do so.

5. **Too many "memory" concepts.** Engineers reading the codebase encounter: `Engine`, `UserMemoryStore`, `RetrievalEngine`, `MemoryTool`, `ReviewMemoryTool`, `UserMemory` in prompts, and `ctx_agent_memory` in SQL — all called "memory" with no clear relationship.

**The goal of this design is to replace all of the above with a single plugin interface that any engineer can implement.**

---

## 2. Goals and Non-Goals

### Goals

- Define a **minimal, stable plugin contract** (`memory.Provider`) that any storage backend can implement in under 200 lines.
- Make the LCM engine the **default plugin**, not a hard dependency.
- Let operators **swap or combine** memory backends by changing plugin configuration.
- **Unify all memory tool logic** so there is one tool definition that adapts to the active plugin's capabilities.
- **Remove memory access from `config.Store`** — profile memory belongs to the memory plugin.
- **Decouple self-improve** from raw SQL queries so it works with any memory backend.
- Keep **agent soul / identity** separate — it is configuration, not memory.

### Non-Goals

- This design does not change how LLM providers, hooks, or channels work.
- This design does not add vector search to the default LCM plugin (that can be a future separate plugin).
- This design does not support multiple memory plugins active simultaneously on the same session.
- This design does not change the database schema for the LCM plugin (migrations are not needed for the default backend, only for cleanup of `config.Store` delegation).

---

## 3. Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                        cmd/anna                             │
│                                                             │
│  memProvider := pluginmemory.Build("lcm", buildCtx)        │
│  pool := agent.NewPool(factory, memProvider, opts...)       │
│  memTool := memory.BuildTool(memProvider)   // auto        │
└───────────────────┬─────────────────────────────────────────┘
                    │ memory.Provider (interface)
                    │
        ┌───────────▼────────────────────────────┐
        │          pkg/memory/                   │
        │                                        │
        │  Provider (core interface)             │
        │  Compactor  (optional capability)      │
        │  Searcher   (optional capability)      │
        │  Explorer   (optional capability)      │
        │  ProfileStore (optional capability)    │
        │  SessionManager (optional capability)  │
        │  ReviewSource (optional capability)    │
        │                                        │
        │  BuildTool(Provider) → tools.Tool      │
        └───────────┬────────────────────────────┘
                    │ implements
          ┌─────────┴──────────┐
          │                    │
   ┌──────▼──────┐    ┌────────▼──────┐
   │plugins/     │    │plugins/       │
   │memory/lcm/  │    │memory/simple/ │   (or any custom plugin)
   │             │    │               │
   │ Implements  │    │ Implements    │
   │ all 6       │    │ Provider +    │
   │ capabilities│    │ ProfileStore  │
   └─────────────┘    └───────────────┘
```

**Key idea:** The `memory.Provider` interface is the only required contract. Everything else — compaction, search, profile storage, session listing, summary navigation, review context — is an *optional capability* that the system discovers at runtime using Go type assertions. This mirrors how the hook system works: a hook plugin implements `HookPlugin` as its base and optionally implements `PreToolCallHook`, `PostToolCallHook`, etc.

---

## 4. Core Concepts

### 4.1 Session

`Session` replaces the scattered `WithSessionID` / `WithUserID` / `WithAgentID` context helpers. Instead of fishing values out of `context.Context`, all memory operations receive a `Session` struct explicitly.

```go
// pkg/memory/types.go

// Session identifies the context of a single conversation.
// It is created by Pool.Chat and passed to all Provider methods.
type Session struct {
    ID      string // unique session key (e.g. "default:cli:42:main")
    AgentID string // agent this session belongs to (e.g. "default")
    UserID  int64  // internal user ID (0 for anonymous/legacy)
    Channel string // originating channel (e.g. "cli", "telegram")
}
```

**Why explicit instead of context?**

Context-carried values are invisible — there is no compile-time guarantee that a value was set. When `SessionIDFromContext` returns an empty string because someone forgot to call `WithSessionID`, the failure is silent and surfaces as a confusing database error. An explicit `Session` parameter makes the requirement clear at every call site. It is also easier to test: you construct a `Session` value directly without building a context chain.

**Why not just pass sessionID string like the current Engine?**

The current `Engine.Ingest(ctx, sessionID, msg)` requires the engine to resolve `sessionID → conversationID` on every call. It also needs `userID` and `agentID` separately from the context for profile operations. Bundling them into `Session` avoids repeated context reads and makes every method self-contained.

---

### 4.2 Provider (core interface)

```go
// pkg/memory/provider.go

// SessionStats contains basic statistics about a session's memory state.
// Every provider can produce this since it only requires knowledge of
// what was stored via Append.
type SessionStats struct {
    MessageCount int   // total messages stored for this session
    TokenCount   int   // estimated total tokens across all stored messages
    SummaryCount int   // number of summaries (0 for providers without Compactor)
    OldestAt     time.Time // timestamp of the earliest message (zero if empty)
    NewestAt     time.Time // timestamp of the most recent message (zero if empty)
}

// Provider is the memory plugin contract.
// Every memory plugin must implement this interface.
// Optional capabilities are discovered via type assertion — see Capability Interfaces.
type Provider interface {
    // Name returns the plugin identifier (e.g. "lcm", "simple").
    // Used in logs and admin UI.
    Name() string

    // Bootstrap ensures the session is initialized and ready for use.
    // Called once at the start of every Pool.Chat call before any Append or Assemble.
    // Implementations use this to create conversation records, initialize caches,
    // or establish remote connections for the session.
    Bootstrap(ctx context.Context, session Session) error

    // Append persists one or more messages to the session's event log.
    // Messages must be appended in the order they arrive.
    // Callers pass all messages from a single turn together so implementations
    // can store them in a single atomic transaction if they choose.
    //
    // Concurrency: implementations MUST be safe for concurrent Append calls
    // on different sessions. Concurrent Append calls on the SAME session
    // MUST be serialised by the implementation (see Concurrency Contract).
    Append(ctx context.Context, session Session, msgs ...ai.Message) error

    // Assemble builds the context window to send to the LLM.
    // budget: maximum number of tokens the returned messages may consume.
    // freshTail: minimum number of recent messages to always include verbatim,
    //   regardless of budget pressure. Implementations MUST honour this.
    // Returns messages in chronological order (oldest first).
    // Older content that does not fit in the budget is either summarised
    // (if the plugin supports Compactor) or omitted.
    Assemble(ctx context.Context, session Session, budget, freshTail int) ([]ai.Message, error)

    // Stats returns basic statistics about a session's memory state.
    // Used by the memory tool's "status" action and by admin endpoints.
    // Returns zero-value stats (not an error) if the session does not exist.
    Stats(ctx context.Context, session Session) (SessionStats, error)

    // Close releases any resources held by the provider (DB connections, caches, etc.).
    // Called when the Pool shuts down. Must be safe to call multiple times.
    Close() error
}
```

**Why 5 methods (not fewer, not more)?**

Any engineer should be able to write a working memory plugin in an afternoon. The bar for "works" is: persist messages, build a context window, report basic stats. That is it. A plugin that does nothing but keep a slice of messages in memory satisfies this contract and is useful for testing or stateless deployments.

`Stats` is on the core interface (not optional) because every provider knows how many messages it stored — this is trivially derivable from the data `Append` already writes. It powers the `status` tool action (always available) and admin endpoints without requiring any optional capability.

Compaction, search, profile notes, and session listing are all powerful features but none of them are *required* for the agent to function. They are additive.

---

## 5. Capability Interfaces

Capabilities are optional interfaces. The system calls `if c, ok := provider.(memory.Compactor); ok { ... }` — the same pattern used by the hooks system (`PreToolCallHook`, `PostToolCallHook`). A plugin declares its capabilities simply by implementing the additional interfaces.

### 5.1 Compactor

**What it is:** The ability to reduce the context window size by summarising older messages. Without this capability the provider either truncates old messages (lossy) or keeps everything verbatim until the LLM's context limit is hit.

**When to implement:** Any plugin that wants lossless or near-lossless long-context support. The default LCM plugin implements this. A simple truncation plugin does not.

```go
// pkg/memory/provider.go

type CompactionMode int

const (
    CompactionIncremental CompactionMode = iota // one leaf pass + one condensed pass
    CompactionFull                              // repeat until no more compaction possible
)

type CompactionResult struct {
    LeafSummariesCreated      int
    CondensedSummariesCreated int
    MessagesCompacted         int
    TokensBefore              int
    TokensAfter               int
    Duration                  time.Duration
}

// Compactor is implemented by providers that support background compaction.
// The Pool calls NeedsCompaction before each chat turn and Compact when needed.
type Compactor interface {
    // NeedsCompaction returns true if the session's context has grown large enough
    // to warrant compaction. threshold is a fraction of the session's token budget
    // (e.g. 0.75 means "compact when context is 75% full").
    NeedsCompaction(ctx context.Context, session Session, threshold float64) bool

    // Compact runs the compaction algorithm on the session.
    // Incremental mode runs a single summarisation pass (fast, called automatically).
    // Full mode runs repeated passes until no further reduction is possible
    // (slow, called on demand e.g. via /compact slash command).
    Compact(ctx context.Context, session Session, mode CompactionMode) (*CompactionResult, error)
}
```

**Data stored by LCM's Compactor:**

```
ctx_summaries
  id          "sum_a1b2c3d4"           (random hex, "sum_" prefix)
  conv_id     42
  kind        "leaf" | "condensed"
  depth       0 (leaf), 1+ (condensed)
  content     "User set up Go module, created main.go, fixed import. Files: main.go (created)"
  token_count 87
  earliest_at "2026-04-01 10:00:00"    (timestamp of first source message)
  latest_at   "2026-04-01 10:05:00"    (timestamp of last source message)
  descendant_count    12               (number of original messages compressed)

ctx_summary_messages                   (leaf → source messages)
  summary_id  "sum_a1b2c3d4"
  message_id  101
  ordinal     0

ctx_summary_parents                    (condensed → child summaries)
  summary_id        "sum_condensed_1"
  parent_summary_id "sum_leaf_1"
  ordinal           0
```

The context window (`ctx_items`) replaces message rows with a single summary row atomically within a transaction. Original messages are never deleted — this is the "lossless" guarantee.

---

### 5.2 Searcher

**What it is:** The ability for the agent to search through its own conversation history. Without this capability the `search` action is absent from the memory tool.

**When to implement:** Any plugin that can search stored content. The LCM plugin implements keyword (LIKE) search. A vector-DB plugin would implement semantic (cosine similarity) search using the same interface.

```go
// pkg/memory/provider.go

type SearchScope int

const (
    SearchScopeBoth      SearchScope = iota // default: search everything
    SearchScopeMessages                     // raw messages only
    SearchScopeSummaries                    // summaries only
)

type SearchQuery struct {
    Text  string      // search term (keyword or natural language depending on plugin)
    Scope SearchScope // which layer of storage to search
    Limit int         // max results (default 20)
}

type SearchResult struct {
    SourceType string    // "message" or "summary"
    SourceID   string    // message ID or summary ID
    Content    string    // snippet of the matching content (truncated at ~500 chars)
    Score      float64   // relevance score: 0 for keyword match, 0.0-1.0 for semantic
    Timestamp  time.Time // when the source was created
}

// Searcher is implemented by providers that support history search.
type Searcher interface {
    Search(ctx context.Context, session Session, query SearchQuery) ([]SearchResult, error)
}
```

**Data read (not written) by the LCM Searcher:**

```sql
-- Keyword search over raw messages
SELECT id, content, created_at
FROM ctx_messages
WHERE conversation_id = ?
  AND content LIKE '%pattern%'
LIMIT ?

-- Keyword search over summaries
SELECT id, content, created_at
FROM ctx_summaries
WHERE conversation_id = ?
  AND content LIKE '%pattern%'
LIMIT ?
```

**Why not call it `Grep`?**

The current `RetrievalEngine.Grep` method is named for its SQL LIKE implementation. The interface uses `Search` because a vector plugin would not "grep" — it embeds the query and retrieves by similarity. The tool action presented to the LLM is also called `search` for the same reason.

---

### 5.3 Explorer

**What it is:** The ability to navigate the summary hierarchy — inspect a summary's metadata, then drill into it to see its children or the original source messages. This is specific to hierarchical summarisation and only makes sense for the LCM plugin.

**When to implement:** Only when the plugin stores summaries in a DAG structure. A simple truncation plugin or a flat vector store would not implement this. It is optional even for LCM-style plugins.

```go
// pkg/memory/provider.go

type DescribeResult struct {
    SummaryID       string
    Kind            string     // "leaf" or "condensed"
    Depth           int        // 0 = leaf, 1+ = condensed
    Content         string     // the summary text
    EarliestAt      *time.Time // timestamp of oldest source message
    LatestAt        *time.Time // timestamp of newest source message
    DescendantCount int        // total original messages this summary covers
    ParentIDs       []string   // summaries that contain this one (condensed parents)
    ChildIDs        []string   // summaries or messages this one was built from
}

type ExpandResult struct {
    SummaryID string
    // For leaf summaries: the original source messages
    Messages []ExpandMessage
    // For condensed summaries: the child summaries
    Children []ExpandChild
}

type ExpandMessage struct {
    MessageID int64
    Role      string
    Content   string
    CreatedAt time.Time
}

type ExpandChild struct {
    SummaryID string
    Kind      string
    Depth     int
    Content   string
}

// Explorer is implemented by providers that store summaries in a navigable hierarchy.
// It lets the agent inspect and drill into compressed history.
//
// Note: these methods take summaryID only, not Session. Summary IDs are globally
// unique (e.g. "sum_a1b2c3d4") and already scoped to a conversation internally.
// Passing Session would be redundant and force callers to carry it through the
// tool execution path where only the summary ID is known.
type Explorer interface {
    // Describe returns metadata and lineage for a summary ID.
    // The agent uses this to understand what a summary covers before expanding.
    Describe(ctx context.Context, summaryID string) (*DescribeResult, error)

    // Expand drills into a summary:
    //   - For leaf summaries: returns the original source messages
    //   - For condensed summaries: returns the child summaries
    // tokenCap limits how many tokens of content are returned.
    Expand(ctx context.Context, summaryID string, tokenCap int) (*ExpandResult, error)
}
```

**How the agent uses it:**

The agent's context window contains XML-formatted summaries that look like:

```xml
<summary id="sum_a1b2" kind="condensed" depth="2" earliest_at="2026-03-01" latest_at="2026-03-20"
         descendant_count="340">
  <content>
    Three weeks of work on auth system. JWT implementation complete. RBAC added.
    Expand for details about: specific policy configs, test failures, migration steps.
  </content>
</summary>
```

When the agent needs to recall specific details, it calls:
1. `describe("sum_a1b2")` → sees it has children `["sum_leaf_1", "sum_leaf_2", ...]`
2. `expand("sum_a1b2")` → gets child summaries
3. `expand("sum_leaf_1")` → gets the original messages from that period

This is a zoom metaphor: the agent starts coarse and drills down to the detail it needs without expanding the full history.

---

### 5.4 ProfileStore

**What it is:** Per-user-per-agent persistent notes. These are durable facts the agent learns about the user across sessions: preferences, work context, recurring patterns. They are injected into every system prompt as a `<user_memory>` block.

**When to implement:** Any plugin that wants to support personalisation. This is the most universally useful capability — even a simple truncation plugin should implement it.

```go
// pkg/memory/provider.go

// ProfileStore is implemented by providers that support per-user-per-agent
// persistent notes (also called "user memory").
//
// Profile content is free-form text managed entirely by the agent.
// The system injects it into the system prompt at session start.
// The agent updates it via the memory tool when it learns something worth keeping.
type ProfileStore interface {
    // GetProfile returns the current profile notes for the (userID, agentID) pair.
    // Returns ("", nil) if no profile exists yet (not an error).
    GetProfile(ctx context.Context, userID int64, agentID string) (string, error)

    // SetProfile overwrites the profile notes for the (userID, agentID) pair.
    // Callers are responsible for merging new content with existing content
    // before calling SetProfile — this method always replaces, never appends.
    SetProfile(ctx context.Context, userID int64, agentID string, content string) error
}
```

**Data stored:**

```
ctx_agent_memory
  user_id    42
  agent_id   "default"
  content    "V prefers CLI tools. Senior Go dev. Dislikes unnecessary abstractions.
              Uses conventional commits with emoji. Prefers concise responses."
  updated_at "2026-04-05 14:22:00"
```

The key is `(user_id, agent_id)`. One row per user per agent. Content is free text — the agent decides what to write and how to structure it.

**Why free text instead of structured JSON?**

The agent writes and reads this content directly. Free text is more flexible and easier for the LLM to reason about. Structured JSON would require a schema and serialisation logic in the agent's prompt. The content is also injected verbatim into the system prompt, so plain prose reads more naturally than JSON.

**Merge semantics:**

`SetProfile` always replaces. The caller (agent tool or self-improve reviewer) is responsible for reading the current value, merging new observations into it, and writing the result. This gives the LLM full control over the format and avoids unexpected data loss from partial updates.

**Lifetime of profile notes:**

Profile notes persist indefinitely. They are not session-scoped. The same notes are available to every conversation the user has with the same agent, regardless of which channel it came from.

---

### 5.5 SessionManager

**What it is:** Listing, archiving, and loading the full history of sessions. Used by the admin panel to show conversation history and by the self-improve loop to find unreviewed sessions.

**When to implement:** Any plugin that stores sessions persistently and wants to support the admin UI or self-improve. A stateless or in-memory plugin would not implement this.

```go
// pkg/memory/provider.go

type SessionInfo struct {
    ID         string    // session key
    AgentID    string
    UserID     int64
    Channel    string
    Title      string    // auto-generated from first message
    CreatedAt  time.Time
    LastActive time.Time
    Archived   bool
}

type ListOptions struct {
    AgentID         string    // filter by agent (empty = all)
    UserID          int64     // filter by user (0 = all)
    IncludeArchived bool
    Limit           int       // 0 = no limit
}

// SessionManager is implemented by providers that support session lifecycle management.
type SessionManager interface {
    // SaveInfo persists or updates session metadata.
    // Called after each chat turn to update Title and LastActive.
    SaveInfo(ctx context.Context, info SessionInfo) error

    // LoadInfo retrieves metadata for a single session.
    LoadInfo(ctx context.Context, sessionID string) (SessionInfo, error)

    // ListInfo lists sessions matching the options.
    ListInfo(ctx context.Context, opts ListOptions) ([]SessionInfo, error)

    // LoadHistory returns the complete raw message history for a session
    // in chronological order. Used by the admin panel viewer and export.
    // This returns raw messages, not summaries — callers get the full event log.
    LoadHistory(ctx context.Context, sessionID string) ([]ai.Message, error)
}
```

**Data stored (LCM backend):**

```
ctx_conversations
  id           42         (internal integer PK)
  session_id   "default:cli:42:main"   (the external Session.ID)
  title        "Go project setup"
  channel      "cli"
  agent_id     "default"
  user_id      42
  archived     0
  last_active  "2026-04-05 15:00:00"
  created_at   "2026-04-01 10:00:00"
  bootstrapped_at "2026-04-01 10:00:00"
  self_improve_reviewed_at NULL        (watermark for review loop)
```

The `self_improve_reviewed_at` column is internal to the LCM plugin and used by its `ReviewSource` implementation. It is not part of `SessionInfo` — that struct contains only what the admin panel and callers need.

---

### 5.6 ReviewSource

**What it is:** A way for the self-improve loop to get conversation content in a format suitable for the reviewer agent. The reviewer agent needs to see what happened in a conversation — but different backends store history differently.

**When to implement:** Any plugin that stores persistent conversation history and wants to support self-improvement. The LCM plugin implements this. A stateless plugin cannot.

```go
// pkg/memory/provider.go

// ReviewCandidate is a session that has new content ready for self-improve review.
// Returned by ListUnreviewed so the review loop knows when each session was
// last reviewed and can pass the right `since` timestamp to BuildReviewContext.
type ReviewCandidate struct {
    Session        Session   // the session to review
    LastReviewedAt time.Time // zero if never reviewed
    LastActive     time.Time // timestamp of most recent message
}

// ReviewSource is implemented by providers that can supply conversation content
// to the self-improvement review loop.
type ReviewSource interface {
    // BuildReviewContext returns a text representation of the conversation
    // suitable for passing to the reviewer agent's prompt.
    //
    // since: if non-zero, only include content created after this time.
    //   The provider should include prior context (e.g. summaries from before
    //   this timestamp) to give the reviewer enough background.
    //   If zero, include all content.
    //
    // The format is up to the provider. The LCM plugin returns:
    //   - Existing summaries wrapped in <prior_context> XML
    //   - New messages since `since` as "[role] content" lines
    //
    // Returns ("", nil) if there is no content to review.
    BuildReviewContext(ctx context.Context, session Session, since time.Time) (string, error)

    // MarkReviewed records that the session was reviewed at the given timestamp.
    // The self-improve loop calls this after a successful review so the next
    // run only processes content added after this point.
    //
    // Concurrency note: the self-improve loop calls MarkReviewed only after
    // the review completes. The watermark should be the timestamp captured
    // BEFORE loading messages (so concurrent Appends are not skipped).
    // The caller passes this timestamp, not the provider.
    MarkReviewed(ctx context.Context, session Session, at time.Time) error

    // ListUnreviewed returns sessions that have new content since their last review.
    // The self-improve loop calls this to find work to do.
    // Returns ReviewCandidate (not bare Session) so the caller gets the
    // LastReviewedAt watermark without a separate round-trip.
    ListUnreviewed(ctx context.Context, agentID string, limit int) ([]ReviewCandidate, error)
}
```

**Why is `MarkReviewed` / `ListUnreviewed` part of the interface rather than in `SessionManager`?**

These are review-specific operations. `SessionManager` is about user-visible session metadata (titles, last-active, archive status). The review watermark (`self_improve_reviewed_at`) is an internal implementation detail of the review loop and should not appear in the general session metadata. Putting these in `ReviewSource` keeps the boundary clear.

**What the LCM plugin returns from `BuildReviewContext`:**

```
<prior_context>
<summary id="sum_a1b2" kind="condensed" depth="1" ...>
  <content>
  Three weeks of Go development work. Auth system complete. Tests written.
  </content>
</summary>
</prior_context>

[user] Can you help me debug this race condition?
[assistant] Sure, let me look at the code.
[assistant] I can see the issue — the map isn't protected by a mutex.
[user] Great, fixed. Thanks.
```

This lets the reviewer see both historical context (via summaries) and the new content since the last review.

---

## 6. Concurrency Contract

Memory providers are shared across all pools and sessions. The concurrency rules are explicit so plugin authors know exactly what they must handle.

### Rules for providers

1. **Different sessions, concurrent calls: MUST be safe.** Multiple goroutines will call `Append`, `Assemble`, `Search`, etc. on different sessions simultaneously. Providers that use a shared database handle this naturally (SQLite WAL mode, PostgreSQL connections). Providers that use in-memory state must use per-session locking.

2. **Same session, concurrent mutation: provider MUST serialise.** The pool does not hold a per-session lock across the full chat turn — it calls `Bootstrap`, `Assemble`, runs the LLM (which may take seconds), then calls `Append`. During that time the self-improve loop or another channel may call `Append` on the same session. The provider must use internal locking to prevent data corruption.

3. **`Compact` vs `Append` on the same session: provider MUST serialise.** Compaction rewrites the context window. If an `Append` arrives mid-compaction, either the append must block until compaction finishes, or the provider must handle interleaving safely. The LCM plugin uses a per-session mutex for this.

4. **`SetProfile` concurrent with itself: last write wins.** Profile updates are infrequent (at most once per conversation turn or review). The race between the agent updating profile and the self-improve reviewer updating profile is possible but rare. The provider may use a row-level lock or CAS (compare-and-swap) if it wants to prevent overwrites, but last-write-wins is acceptable.

5. **`Close` must be safe to call while other methods are in-flight.** `Close` should signal shutdown and wait for in-progress operations to finish or abandon them gracefully.

### How the LCM plugin implements this

```go
type Provider struct {
    // ...
    sessionMu map[string]*sync.Mutex  // per-session lock
    globalMu  sync.Mutex              // protects sessionMu map
}

// withSessionLock acquires the per-session mutex before running fn.
// Used by Append, Compact, and any mutation path.
func (p *Provider) withSessionLock(sessionID string, fn func() error) error {
    p.globalMu.Lock()
    mu, ok := p.sessionMu[sessionID]
    if !ok {
        mu = &sync.Mutex{}
        p.sessionMu[sessionID] = mu
    }
    p.globalMu.Unlock()

    mu.Lock()
    defer mu.Unlock()
    return fn()
}
```

Read-only methods (`Assemble`, `Stats`, `Search`, `Describe`, `Expand`, `GetProfile`) do not acquire the session lock — they operate on committed state and are safe to call concurrently with mutations (SQLite WAL mode guarantees snapshot isolation for readers).

### Watermark race prevention

The self-improve loop captures the watermark timestamp **before** loading messages:

```go
watermark := time.Now().UTC()  // captured first
text, err := rs.BuildReviewContext(ctx, session, candidate.LastReviewedAt)
// ... run review ...
rs.MarkReviewed(ctx, session, watermark)  // uses pre-captured timestamp
```

Any messages appended after `watermark` but before `MarkReviewed` will have a timestamp > watermark, so they will be picked up by the next review cycle. No messages are skipped.

---

## 7. Tool Auto-Generation

The current system has two hard-coded memory tools: `MemoryTool` (for the agent) and `ReviewMemoryTool` (for the reviewer). Both are specific to the LCM backend and duplicate profile get/update logic.

The new design generates the memory tool automatically based on what the active provider supports:

```go
// pkg/memory/tool.go

// BuildTool inspects provider capabilities and returns a memory tool
// whose available actions match exactly what the provider supports.
// If the provider supports no optional capabilities, the tool still exists
// but only offers a "status" action.
func BuildTool(provider Provider, opts ...ToolOption) tools.Tool

// ToolOption configures the generated tool.
type ToolOption func(*toolConfig)

// WithReadOnlyProfile disables the profile_update action.
// Used when building the tool for the self-improve reviewer agent,
// which should read but not arbitrarily overwrite profile notes.
// (The reviewer uses a separate merging path instead.)
func WithReadOnlyProfile() ToolOption

// WithActionsOnly restricts the tool to the named actions.
// Used for testing or for restricted agent contexts.
func WithActionsOnly(actions ...string) ToolOption
```

**Capability → action mapping:**

| Provider implements | Actions added to tool |
|---|---|
| *(always)* | `status` — calls `Provider.Stats()`, shows message count, token usage, summary count |
| `Searcher` | `search` — search history by keyword or natural language |
| `Explorer` | `describe` — inspect a summary's metadata and lineage |
| `Explorer` | `expand` — drill into a summary's contents |
| `ProfileStore` | `profile_get` — read current profile notes (injected into prompt, but agent can re-read explicitly) |
| `ProfileStore` | `profile_update` — write new profile notes (respects `WithReadOnlyProfile`) |

**How the tool description is generated:**

The tool's `Description` field is built dynamically to only mention the available actions. If the active plugin is `simple` (no Explorer), the tool description does not mention `describe` or `expand`. The agent's system prompt teaches it what the memory tool can do — a misleading description is worse than no description.

**Why auto-generation instead of a fixed interface?**

A fixed tool interface would force every plugin to implement stub actions for capabilities it does not have (returning "not supported" errors). This is worse than the current situation. Auto-generation means the tool contract matches the plugin contract exactly — the agent is never offered actions that will fail.

---

## 8. Plugin Registry

Follows the same pattern as `plugins/tools/`, `plugins/hooks/`, and `plugins/providers/`.

```go
// plugins/memory/registry.go

package pluginmemory

import (
    "context"
    "database/sql"

    "github.com/vaayne/anna/pkg/memory"
)

// BuildContext is passed to the factory when constructing a provider.
// Not all fields are populated for every plugin — a plugin that manages
// its own storage does not need DB.
type BuildContext struct {
    // DB is the shared SQLite connection. Provided when the plugin
    // wants to share the application database (e.g. the LCM plugin).
    // May be nil for plugins that manage their own storage.
    DB *sql.DB

    // AnnaHome is the path to ~/.anna/
    AnnaHome string

    // Config holds the plugin-specific configuration from settings_plugins.config JSON.
    // The plugin interprets this map however it needs.
    // This is JSON-serialisable and persisted — no functions or Go objects.
    Config map[string]any

    // SummarizerFn provides LLM access for plugins that need to generate
    // summaries (e.g. LCM compaction). Injected by the pool manager.
    // Plugins that do not compact may ignore this. If nil, the LCM plugin
    // falls back to the deterministic truncation fallback.
    SummarizerFn func(ctx context.Context, prompt string) (string, error)
}

// Factory creates a Provider from a BuildContext.
type Factory func(ctx context.Context, bc BuildContext) (memory.Provider, error)

// ProviderMeta is displayed in the admin UI plugin list.
type ProviderMeta struct {
    Name         string   // display name (e.g. "Lossless Context Management")
    Description  string   // one-line description
    Capabilities []string // declared capability names for UI display
}

// Registration bundles a factory and its metadata.
type Registration struct {
    Factory Factory
    Meta    ProviderMeta
}

// Register adds a memory plugin to the global registry.
// Called from init() in each plugin package.
func Register(name string, reg Registration)

// Build constructs a named provider from the registry.
// Returns an error if the name is not registered.
func Build(ctx context.Context, name string, bc BuildContext) (memory.Provider, error)

// List returns all registered plugin names.
func List() []string
```

**Registration from a plugin package:**

```go
// plugins/memory/lcm/plugin.go

func init() {
    pluginmemory.Register("lcm", pluginmemory.Registration{
        Meta: pluginmemory.ProviderMeta{
            Name:        "Lossless Context Management",
            Description: "Hierarchical summarisation with full history preservation",
            Capabilities: []string{
                "compactor", "searcher", "explorer",
                "profile", "sessions", "review",
            },
        },
        Factory: func(ctx context.Context, bc pluginmemory.BuildContext) (memory.Provider, error) {
            if bc.DB == nil {
                return nil, errors.New("lcm plugin requires a shared DB connection")
            }
            return New(bc.DB, bc.Config)
        },
    })
}
```

**Database storage for plugin configuration:**

Memory plugins use the existing `settings_plugins` table with `kind = "memory"`:

```sql
INSERT INTO settings_plugins (id, kind, name, enabled, config)
VALUES ('memory/lcm', 'memory', 'lcm', 1, '{}');
```

The `config` JSON column stores plugin-specific settings. For the LCM plugin this might include `{"fresh_tail": 20, "leaf_chunk_size": 10}`. For a hypothetical Redis plugin it might include `{"host": "localhost", "port": 6379}`.

**Why not add a new `PluginKindMemory` constant automatically?**

`config/plugin.go` defines `PluginKindTool`, `PluginKindHook`, etc. Adding `PluginKindMemory = "memory"` is the only change needed in the config package. The seeding logic in `SeedDefaults()` would seed `memory/lcm` as the default enabled memory plugin.

---

## 9. Built-in Implementations

### 9.1 LCM Plugin (default)

Location: `plugins/memory/lcm/`

This is a repackaging of `internal/memory/` as a plugin. Functionally identical to the current engine but registered through the plugin system.

**Implements:** All 6 capability interfaces — `Compactor`, `Searcher`, `Explorer`, `ProfileStore`, `SessionManager`, `ReviewSource`.

**Storage:** The same SQLite tables (`ctx_*`) used today. No schema changes.

**Constructor:**

```go
// plugins/memory/lcm/provider.go

type Provider struct {
    db         *sql.DB
    q          *sqlc.Queries
    assembler  *assembler
    compaction *compactionEngine
    retrieval  *retrievalEngine
    summarizer memory.Summarizer
    sessionMu  map[string]*sync.Mutex
    convCache  map[string]int64
    globalMu   sync.Mutex
    freshTail  int
    log        *slog.Logger
}

func New(db *sql.DB, cfg map[string]any) (*Provider, error)
```

The LCM plugin owns the summarizer. The summarizer needs an LLM to generate summaries. The LLM access function is injected via `BuildContext.SummarizerFn` — a typed callback field on `BuildContext`, not part of the JSON config:

```go
// plugins/memory/registry.go (updated BuildContext)

type BuildContext struct {
    DB       *sql.DB
    AnnaHome string
    Config   map[string]any  // JSON-serialisable plugin settings

    // SummarizerFn provides LLM access for plugins that need to generate
    // summaries (e.g. LCM compaction). It is injected by the pool manager
    // at construction time. NOT stored in JSON config — it is a runtime
    // callback, not a persisted setting.
    //
    // Plugins that do not compact may ignore this.
    // If nil, the LCM plugin falls back to the deterministic truncation fallback.
    SummarizerFn func(ctx context.Context, prompt string) (string, error)
}
```

The pool manager injects `SummarizerFn` when building the provider. It creates a closure that calls the agent's configured model via the provider registry. This separation keeps plugin config (JSON, persisted) cleanly separated from runtime dependencies (Go functions, injected).

The LCM plugin reads JSON config for tuning parameters only:

```go
// plugins/memory/lcm/provider.go

type Config struct {
    FreshTail    int // default 20
    LeafChunkMin int // default 10
}

func New(db *sql.DB, jsonCfg map[string]any, summarizerFn func(context.Context, string) (string, error)) (*Provider, error)
```

### 9.2 Simple Plugin

Location: `plugins/memory/simple/`

A minimal implementation for users who do not need compaction or hierarchical summaries. Suitable for short conversations, testing, or environments where the LLM's native context window is large enough that compaction is unnecessary.

**Implements:** `Provider` + `ProfileStore` + `SessionManager` (no `Compactor`, `Searcher`, or `Explorer`).

**Storage:** Same SQLite database, but only uses `ctx_conversations`, `ctx_messages`, and `ctx_agent_memory`. Does not write to `ctx_summaries`, `ctx_items`, or the summary relation tables.

**Assemble algorithm:** Returns the last `budget` tokens of messages from the event log. Always honours `freshTail`. Old messages are dropped (lossy).

```go
// plugins/memory/simple/provider.go

type Provider struct {
    db  *sql.DB
    q   *sqlc.Queries
    log *slog.Logger
}

// Assemble returns the last N messages that fit within budget.
// No summaries. No compaction. Simple sliding window.
func (p *Provider) Assemble(ctx context.Context, s memory.Session, budget, freshTail int) ([]ai.Message, error) {
    msgs, err := p.q.GetMessagesByConversation(ctx, convID)
    // ... select from tail, respect budget ...
}
```

**When to use:**

- Development and testing (fast, no LLM calls for compaction)
- Agents that are stateless by design (each conversation is short)
- Users who explicitly opt out of LCM overhead

**How to switch:**

```bash
anna plugin config memory/lcm enabled=false
anna plugin config memory/simple enabled=true
```

Or via the admin panel under Plugins → Memory.

---

## 10. Integration Points

### 10.1 Pool / Chat loop

`internal/agent/pool.go` holds a `memory.Provider` instead of `memory.Engine`. The interface methods used in the chat loop map directly:

| Current (Engine) | New (Provider + capabilities) |
|---|---|
| `mem.Bootstrap(ctx, sessionID)` | `mem.Bootstrap(ctx, session)` |
| `mem.Ingest(ctx, sessionID, msg)` | `mem.Append(ctx, session, msg)` |
| `mem.IngestBatch(ctx, sessionID, msgs)` | `mem.Append(ctx, session, msgs...)` |
| `mem.Assemble(ctx, sessionID, budget, tail)` | `mem.Assemble(ctx, session, budget, tail)` |
| `mem.NeedsCompaction(ctx, sessionID, threshold)` | `if c, ok := mem.(memory.Compactor); ok { c.NeedsCompaction(...) }` |
| `mem.Compact(ctx, sessionID, mode)` | `c.Compact(ctx, session, mode)` |
| `mem.SaveInfo(ctx, info)` | `if sm, ok := mem.(memory.SessionManager); ok { sm.SaveInfo(...) }` |

```go
// internal/agent/pool_chat.go

func (p *Pool) Chat(ctx context.Context, sessionKey string, ...) (*ChatStream, error) {
    session := memory.Session{
        ID:      sessionKey,
        AgentID: p.agentID,
        UserID:  userID,
        Channel: channel,
    }

    if err := p.mem.Bootstrap(ctx, session); err != nil {
        return nil, err
    }

    // Optional: compact before assembling context
    if c, ok := p.mem.(memory.Compactor); ok {
        if c.NeedsCompaction(ctx, session, p.compaction.Threshold) {
            _, _ = c.Compact(ctx, session, memory.CompactionIncremental)
        }
    }

    msgs, err := p.mem.Assemble(ctx, session, budget, p.compaction.KeepTail)
    // ... build system prompt, load profile, run runner ...

    if err := p.mem.Append(ctx, session, turnMessages...); err != nil {
        p.log.Warn("memory append failed", "error", err)
    }
}
```

**Session construction:** The pool constructs the `Session` value once per chat call and passes it through. The pool already has `agentID`. `userID` and `channel` come from the runner parameters passed in from the channel handler.

---

### 10.2 System prompt injection

`internal/agent/runner/prompt.go` currently loads user memory via `UserMemoryStore` (which wraps `config.Store`). In the new design it receives the active `memory.Provider` and checks for `ProfileStore`:

```go
// internal/agent/runner/prompt.go

type DBPromptParams struct {
    SystemPrompt  string
    AnnaHome      string
    Workspace     string
    Cwd           string
    UserSkillsDir string
    // Memory is the active provider. Used to inject profile notes if supported.
    Memory  memory.Provider
    UserID  int64
    AgentID string
}

func BuildSystemPromptFromDB(ctx context.Context, p DBPromptParams) string {
    // ... layer 1: basic prompt, layer 2: agent soul ...

    // Layer 3: profile notes (if the plugin supports it)
    if ps, ok := p.Memory.(memory.ProfileStore); ok {
        if content, err := ps.GetProfile(ctx, p.UserID, p.AgentID); err == nil && content != "" {
            prompt += formatUserMemorySection(content)
        }
    }

    // ... skills, project context ...
    return prompt
}
```

This removes the `UserMemoryStore` from `runner.DBPromptParams` and from `pool_runner.go` entirely. The memory plugin is the single source of truth for profile data.

---

### 10.3 Self-improve review loop

`internal/agent/selfimprove/review.go` currently bypasses the memory engine entirely, querying sqlc directly. In the new design it uses `ReviewSource`:

```go
// internal/agent/selfimprove/review.go

func reviewConversation(ctx context.Context, deps ReviewDeps, snap *config.Snapshot, candidate memory.ReviewCandidate) {
    rs := deps.Memory.(memory.ReviewSource) // already checked by caller

    // Capture watermark BEFORE loading messages (see Concurrency Contract §6)
    watermark := time.Now().UTC()

    text, err := rs.BuildReviewContext(ctx, candidate.Session, candidate.LastReviewedAt)
    if err != nil || text == "" {
        return
    }

    // Build the memory tool for the reviewer (profile read-only)
    reviewTool := memory.BuildTool(deps.Memory, memory.WithActionsOnly("profile_get", "profile_update"))

    // ... run reviewer agent with reviewTool ...

    if err := rs.MarkReviewed(ctx, candidate.Session, watermark); err != nil {
        log.Error("mark reviewed", "error", err)
    }
}

func ReviewTask(ctx context.Context, deps ReviewDeps, cfg config.SelfImproveConfig) {
    rs, ok := deps.Memory.(memory.ReviewSource)
    if !ok {
        return // memory plugin does not support review
    }

    for _, agent := range agents {
        candidates, err := rs.ListUnreviewed(ctx, agent.ID, cfg.Batch())
        if err != nil {
            log.Error("list unreviewed", "agent", agent.ID, "error", err)
            continue
        }
        for _, c := range candidates {
            reviewConversation(ctx, deps, snap, c)
        }
    }
}
```

The `ReviewDeps` struct gains a `Memory memory.Provider` field and drops the raw `*sql.DB` dependency (the DB is now internal to the plugin).

---

### 10.4 Admin panel

`internal/admin/server.go` currently holds a `memory.Engine`. In the new design it holds a `memory.Provider` and uses type assertions for admin-specific operations:

```go
// internal/admin/server.go

type Server struct {
    store  config.Store
    mem    memory.Provider
    // ...
}

// Session list endpoint
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
    sm, ok := s.mem.(memory.SessionManager)
    if !ok {
        http.Error(w, "memory plugin does not support session listing", http.StatusNotImplemented)
        return
    }
    sessions, err := sm.ListInfo(ctx, memory.ListOptions{...})
    // ...
}

// Session history endpoint (for admin viewer)
func (s *Server) getSessionHistory(w http.ResponseWriter, r *http.Request) {
    sm, ok := s.mem.(memory.SessionManager)
    if !ok {
        http.Error(w, "not supported", http.StatusNotImplemented)
        return
    }
    msgs, err := sm.LoadHistory(ctx, sessionID)
    // ...
}

// Compact endpoint (triggered by /compact slash command or admin button)
func (s *Server) compactSession(w http.ResponseWriter, r *http.Request) {
    c, ok := s.mem.(memory.Compactor)
    if !ok {
        http.Error(w, "memory plugin does not support compaction", http.StatusNotImplemented)
        return
    }
    result, err := c.Compact(ctx, session, memory.CompactionFull)
    // ...
}
```

Admin endpoints that require a capability return `501 Not Implemented` if the active plugin does not support it. The admin UI hides or disables these features accordingly.

---

### 10.5 Wiring in cmd/anna

```go
// cmd/anna/commands.go

func setup(ctx context.Context) (*appState, error) {
    db, err := database.OpenDB(dbPath)
    // ...

    // Determine which memory plugin is configured
    memPlugin, err := store.GetPlugin(ctx, config.PluginID("memory", "lcm"))
    if err != nil || !memPlugin.Enabled {
        memPlugin, _ = store.GetPlugin(ctx, config.PluginID("memory", "simple"))
    }

    // Build the memory provider
    memProvider, err := pluginmemory.Build(ctx, memPlugin.Name, pluginmemory.BuildContext{
        DB:       db,
        AnnaHome: config.AnnaHome(),
        Config:   memPlugin.Config,
    })
    if err != nil {
        return nil, fmt.Errorf("memory plugin %q: %w", memPlugin.Name, err)
    }

    // Build the memory tool (adapts to provider capabilities automatically)
    memTool := memory.BuildTool(memProvider)

    sharedTools := []tools.Tool{memTool, skills.NewTool(...)}

    poolMgr := agent.NewPoolManager(store, memProvider,
        agent.WithSharedExtraTools(sharedTools),
        agent.WithPluginToolsBuilder(pluginToolsBuilder),
        agent.WithPluginHooksBuilder(pluginHooksBuilder),
        agent.WithCompactionPM(agent.CompactionConfig{
            MaxTokens: snap.Runner.Compaction.MaxTokens,
            KeepTail:  snap.Runner.Compaction.KeepTail,
        }),
    )
    // ...
}
```

---

## 11. Data Flow Diagram

```
User message arrives (any channel)
        │
        ▼
pool.Chat(ctx, sessionKey, message)
        │
        ├─► mem.Bootstrap(ctx, session)
        │       Creates conversation record if needed.
        │
        ├─► Compactor?.NeedsCompaction(ctx, session, threshold)
        │       If true → Compactor?.Compact(ctx, session, Incremental)
        │
        ├─► mem.Assemble(ctx, session, budget, freshTail)
        │       Returns []ai.Message for LLM context window.
        │
        ├─► BuildSystemPromptFromDB(ctx, params)
        │       ProfileStore?.GetProfile(ctx, userID, agentID)
        │       Injects profile notes into system prompt.
        │
        ├─► Run LLM (with memory tool injected)
        │       Agent may call tool actions:
        │         Searcher?.Search(...)           → "search" action
        │         Explorer?.Describe(...)         → "describe" action
        │         Explorer?.Expand(...)           → "expand" action
        │         ProfileStore?.GetProfile(...)   → "profile_get" action
        │         ProfileStore?.SetProfile(...)   → "profile_update" action
        │
        └─► mem.Append(ctx, session, allTurnMessages...)
                Persists the turn to the event log.

═══════════════════════════════════════════════════════════

Background: self-improve loop (gateway mode only)
        │
        ├─► ReviewSource?.ListUnreviewed(ctx, agentID, batch)
        │       Finds sessions with new content since last review.
        │
        ├─► ReviewSource?.BuildReviewContext(ctx, session, since)
        │       Returns text of the conversation for the reviewer.
        │
        ├─► Run reviewer agent
        │       Reviewer calls ProfileStore?.GetProfile to read current notes.
        │       Reviewer calls ProfileStore?.SetProfile to update notes.
        │       Reviewer creates/updates skill files on disk.
        │
        └─► ReviewSource?.MarkReviewed(ctx, session, timestamp)
                Records the watermark for next run.
```

---

## 12. How to Write a Memory Plugin

This section is a step-by-step guide for any engineer who wants to implement a custom memory backend.

### Step 1: Create the package

```
plugins/memory/myplugin/
  plugin.go       // init() registration
  provider.go     // Provider implementation
  profile.go      // ProfileStore (if supported)
  config.go       // configuration parsing
```

### Step 2: Implement the core interface

```go
// plugins/memory/myplugin/provider.go

package myplugin

import (
    "context"
    "github.com/vaayne/anna/pkg/ai"
    "github.com/vaayne/anna/pkg/memory"
)

type Provider struct {
    // your storage client here
}

func (p *Provider) Name() string { return "myplugin" }

func (p *Provider) Bootstrap(ctx context.Context, session memory.Session) error {
    // ensure the session exists in your storage
    // e.g. create a Redis key, open a file, call a remote API
    return nil
}

func (p *Provider) Append(ctx context.Context, session memory.Session, msgs ...ai.Message) error {
    // persist msgs to your storage in order
    return nil
}

func (p *Provider) Assemble(ctx context.Context, session memory.Session, budget, freshTail int) ([]ai.Message, error) {
    // retrieve messages from your storage
    // respect budget (token count) and freshTail (always include last N messages)
    // return in chronological order
    return nil, nil
}

func (p *Provider) Close() error {
    return nil
}
```

### Step 3: Add optional capabilities (as needed)

Implement any of `Compactor`, `Searcher`, `Explorer`, `ProfileStore`, `SessionManager`, `ReviewSource` on the same `*Provider` type. The system will discover them automatically via type assertions.

Example: adding profile support:

```go
// plugins/memory/myplugin/profile.go

// GetProfile implements memory.ProfileStore.
func (p *Provider) GetProfile(ctx context.Context, userID int64, agentID string) (string, error) {
    // fetch from your storage
    return "", nil
}

// SetProfile implements memory.ProfileStore.
func (p *Provider) SetProfile(ctx context.Context, userID int64, agentID string, content string) error {
    // write to your storage
    return nil
}
```

No registration step needed — the compiler will verify that `*Provider` implements the interface, and `memory.BuildTool` will add `profile_get` and `profile_update` actions to the tool automatically.

### Step 4: Register in init()

```go
// plugins/memory/myplugin/plugin.go

package myplugin

import (
    pluginmemory "github.com/vaayne/anna/plugins/memory"
    "github.com/vaayne/anna/pkg/memory"
)

func init() {
    pluginmemory.Register("myplugin", pluginmemory.Registration{
        Meta: pluginmemory.ProviderMeta{
            Name:         "My Custom Plugin",
            Description:  "Stores conversations in Redis with semantic search",
            Capabilities: []string{"profile", "searcher"},
        },
        Factory: func(ctx context.Context, bc pluginmemory.BuildContext) (memory.Provider, error) {
            cfg := parseConfig(bc.Config)
            return New(ctx, cfg)
        },
    })
}
```

### Step 5: Add the blank import

```go
// cmd/anna/plugins_imports.go

import (
    // existing imports ...
    _ "github.com/vaayne/anna/plugins/memory/myplugin"
)
```

### Step 6: Enable via CLI or admin panel

```bash
anna plugin disable memory/lcm
anna plugin enable memory/myplugin
anna plugin config memory/myplugin redis_host=localhost redis_port=6379
```

### Step 7: Run conformance tests

```go
// plugins/memory/myplugin/provider_test.go

func TestConformance(t *testing.T) {
    p, err := New(ctx, testConfig)
    require.NoError(t, err)
    defer p.Close()
    memorytest.RunConformance(t, p)
}
```

### Checklist

- [ ] `Name()` returns a stable, lowercase slug
- [ ] `Bootstrap` is idempotent (safe to call multiple times for the same session)
- [ ] `Append` is atomic (partial writes must not corrupt state)
- [ ] `Append` serialises concurrent calls on the same session (see §6 Concurrency Contract)
- [ ] `Assemble` always honours `freshTail` (even if budget is exceeded, return the tail)
- [ ] `Assemble` returns messages in chronological order (oldest first)
- [ ] `Stats` returns zero-value `SessionStats` (not error) for non-existent sessions
- [ ] `Close` is safe to call multiple times
- [ ] `Close` is safe to call while other methods are in-flight
- [ ] `ProfileStore.SetProfile` replaces (callers merge before calling)
- [ ] `ReviewSource.BuildReviewContext` returns `("", nil)` for empty sessions (not an error)
- [ ] Thread-safe for concurrent calls on different sessions
- [ ] Thread-safe for concurrent mutation + read on the same session
- [ ] `memorytest.RunConformance` passes

---

## 13. Testing

### Test double: `memorytest.Fake`

`pkg/memory/memorytest/` provides an in-memory fake that implements **all** capability interfaces. This is the primary test double for any code that depends on `memory.Provider`.

```go
// pkg/memory/memorytest/fake.go

package memorytest

// Fake is an in-memory Provider that implements all optional capability interfaces.
// Use it in unit tests to avoid database setup.
type Fake struct {
    // Sessions maps session ID → ordered slice of ai.Message
    Sessions map[string][]ai.Message
    // Profiles maps "userID:agentID" → content
    Profiles map[string]string
    // Summaries maps summary ID → content (for Explorer)
    Summaries map[string]fakeSummary
    // ReviewWatermarks maps session ID → last reviewed timestamp
    ReviewWatermarks map[string]time.Time
    // mu protects all maps
    mu sync.Mutex
}

func New() *Fake

// Implements: Provider, Compactor, Searcher, Explorer,
//             ProfileStore, SessionManager, ReviewSource
```

**Why all interfaces?** Tests need to exercise code paths that check for capabilities. A fake that only implements `Provider` would cause all capability checks to fail, hiding bugs. The fake is the one place where it is appropriate to implement everything — because its purpose is to exercise the calling code, not to test a real storage backend.

### Testing a custom plugin

Plugin authors should write two kinds of tests:

1. **Unit tests** against their own storage backend (Redis, file system, etc.)
2. **Conformance test** using the shared test suite:

```go
// pkg/memory/memorytest/conformance.go

// RunConformance runs the standard conformance suite against any Provider.
// It tests: Bootstrap idempotency, Append ordering, Assemble budget/freshTail,
// Stats accuracy, and any optional capabilities detected via type assertion.
func RunConformance(t *testing.T, provider memory.Provider)
```

Plugin authors call `memorytest.RunConformance(t, myProvider)` in their test file. The conformance suite automatically detects which capabilities the provider implements and runs the relevant tests.

---

## 14. Implementation Order

This is a clean-slate replacement, not an incremental migration. Build bottom-up:

### Step 1 — Interfaces and types (`pkg/memory/`)

Create `provider.go`, `types.go`. No implementation code. Compile and verify all types are correct.

### Step 2 — Test infrastructure (`pkg/memory/memorytest/`)

Create `Fake` and `RunConformance`. Write conformance tests against the fake itself to validate the test suite.

### Step 3 — Plugin registry (`plugins/memory/`)

Create `registry.go` with `Register`, `Build`, `List`. Add `PluginKindMemory` to `config/plugin.go`.

### Step 4 — LCM plugin (`plugins/memory/lcm/`)

Move code from `internal/memory/` into the plugin package. Implement all 6 capabilities. Run conformance tests. This is the bulk of the work — it is a refactor, not a rewrite.

### Step 5 — Tool auto-generation (`pkg/memory/tool.go`)

Implement `BuildTool`. Test with the `Fake` provider (all actions available) and with a bare `Provider` (only `status` action).

### Step 6 — Wire into callers

Update `Pool`, `PoolManager`, `BuildSystemPromptFromDB`, self-improve `ReviewDeps`, and admin `Server` to use `memory.Provider` and type assertions. Update `cmd/anna/commands.go` to build the provider via the registry. Update `selfimprove/prompts.go` to reference new tool action names (`profile_get`/`profile_update` instead of `review_memory get`/`update`).

### Step 7 — Simple plugin (`plugins/memory/simple/`)

Implement and run conformance tests. This validates the interface is not over-fitted to LCM.

### Step 8 — Delete old code

Remove `internal/memory/` entirely. Remove `GetUserAgentMemory`/`SetUserAgentMemory` from `config.Store`. Remove `internal/memory/tool/`, `internal/agent/selfimprove/memorytool.go`, `internal/memory/usermemory.go`, `internal/memory/context.go`. Update docs.

---

## 15. Design Decisions and Trade-offs

### Why capability interfaces via type assertion instead of a single large interface?

**Alternative considered:** A single `MemoryProvider` interface with all 20+ methods, where unsupported methods return `ErrNotSupported`.

**Why rejected:** A minimal plugin (simple truncation, in-memory test double) would need to implement stub methods for compaction, summary exploration, review context, etc. — all returning `ErrNotSupported`. This is the hallmark of a poorly designed interface: callers cannot know at compile time whether a method works or fails.

**Why type assertion is better:** The capability set is a property of the type, checked once at construction. `BuildTool` checks capabilities once when building the tool and generates the right action list. `Pool` checks `Compactor` once and either sets up auto-compaction or skips it. There are no runtime surprises.

This mirrors how Go's `io.Reader` / `io.Writer` / `io.Seeker` interfaces compose: you check once whether something is an `io.Seeker` before seeking, rather than calling `Seek` and handling `ErrNotSupported`.

### Why is `Session` a struct instead of context values?

**Alternative considered:** Keep the current `WithSessionID` / `WithUserID` / `WithAgentID` helpers.

**Why rejected:** Context-carried values are invisible in function signatures and untestable without building a full context chain. They also create a hidden coupling: if any layer in the call stack forgets to set one of the values, the failure is silent.

**Why explicit struct is better:** The compiler enforces that every caller provides a `Session`. Tests construct `Session{ID: "test", AgentID: "a", UserID: 1}` directly. Function signatures are self-documenting.

### Why does `SetProfile` always replace instead of merge?

**Alternative considered:** A `MergeProfile(ctx, userID, agentID, delta string)` method.

**Why rejected:** Merge semantics depend on the content format. If the content is plain prose, "merging" is a natural language operation — only an LLM can do it well. The current approach (agent reads current content, decides what to keep, writes the merged result) is already working and produces better results than algorithmic merging would.

**Trade-off:** If two concurrent processes both call `SetProfile`, one will overwrite the other. In practice this does not happen: only one agent tool call runs at a time per session, and the self-improve reviewer runs at most once per session per interval.

### Why is SummarizerFn on BuildContext, not on Config?

**Alternative considered:** Putting the summarizer function in the `Config map[string]any` JSON.

**Why rejected:** A Go function cannot be serialised to JSON. `Config` is persisted in `settings_plugins.config` — it must be JSON-serialisable. Putting a function there would create a confusing split between "config I can edit in the admin panel" and "config that only exists at runtime."

**Why BuildContext is better:** `BuildContext` is a runtime-only Go struct constructed in `cmd/anna/commands.go`. It already holds `*sql.DB` (also not serialisable). Adding `SummarizerFn` here follows the same pattern: runtime dependencies go on `BuildContext`, persistent settings go in `Config`. The pool manager injects a closure that calls the agent's configured model. The LCM plugin receives it and uses it for compaction.

### Why must providers handle same-session concurrency internally?

**Alternative considered:** The pool holds a per-session lock, guaranteeing that only one call to `Append`/`Compact` is in flight for a given session at any time.

**Why rejected:** The pool's chat loop does `Assemble` → LLM call (seconds) → `Append`. Holding a session lock across the entire LLM call would block all concurrent access (self-improve, admin, other channels) to that session for seconds. This is unacceptable.

**Why provider-internal locking is better:** The provider knows which operations actually conflict. In the LCM plugin, `Append` and `Compact` both mutate `ctx_items`, so they need a mutex. But `Assemble` and `Search` are read-only under WAL mode and can run concurrently with mutations. The provider can use fine-grained locking. An external per-session lock would be too coarse.

### Why keep agent soul separate from memory?

Agent soul (identity, personality, tone) is stored in `settings_agents.system_prompt` and optionally overridden by `SOUL.md`. It is **not** per-user — it is per-agent. It also does not change based on conversation history. These properties disqualify it from being "memory."

Memory is conversational: it accumulates during and between sessions, specific to a user, and changes over time. Soul is configuration: it is set once by the operator, applies to all users, and is edited deliberately.

Mixing them would mean a memory plugin that wanted to do something clever with the user profile would also have to handle agent identity, which has completely different lifecycle and ownership semantics.

### Why not support multiple simultaneous memory plugins?

**Alternative considered:** Route different session types to different plugins (e.g., CLI sessions use LCM, Telegram sessions use simple).

**Why deferred:** The complexity of multi-plugin routing (routing table, per-session plugin selection, admin UI) is not justified by a concrete use case. The current single-plugin model is sufficient for the expected deployment scenarios (one anna instance, one memory backend). Per-agent plugin selection can be added later by storing the plugin name in `settings_agents`.

---

## 16. Package Layout

After migration is complete:

```
pkg/memory/
  provider.go        Provider interface + all 6 capability interfaces
  types.go           Session, SessionStats, SearchQuery, SearchResult, CompactionMode,
                     CompactionResult, SessionInfo, ListOptions, ReviewCandidate,
                     DescribeResult, ExpandResult, ExpandMessage, ExpandChild
  tool.go            BuildTool(Provider, ...ToolOption) tools.Tool
  summarize.go       Summarizer interface + BuildPrompt + LLMSummarizer + StaticSummarizer
                     (kept public so the LCM plugin and tests can use them)

  memorytest/        Test infrastructure
    fake.go          In-memory Fake implementing all capabilities
    conformance.go   RunConformance(t, Provider) — standard test suite

plugins/memory/
  registry.go        Register, Build, List, BuildContext, Registration, ProviderMeta

  lcm/               Default plugin (lossless context management)
    plugin.go        init() registration
    provider.go      Provider struct + Bootstrap, Append, Assemble, Close, Name
    compaction.go    Compactor implementation (leaf + condensed passes)
    assembler.go     Assemble algorithm (fresh tail + budget fill)
    retrieval.go     Searcher + Explorer implementation
    profile.go       ProfileStore implementation
    sessions.go      SessionManager implementation
    review.go        ReviewSource implementation
    engine.go        internal helpers (session mutex, conv cache)

  simple/            Minimal plugin (sliding window, no compaction)
    plugin.go        init() registration
    provider.go      Provider + ProfileStore + SessionManager
```

The `internal/memory/` package is replaced entirely. Its code moves to `pkg/memory/` (public interfaces and shared types) and `plugins/memory/lcm/` (the implementation).

The `internal/memory/tool/` directory is deleted. `internal/agent/selfimprove/memorytool.go` is deleted.

`internal/config/store.go` loses `GetUserAgentMemory` and `SetUserAgentMemory`. `internal/config/dbstore.go` loses their implementations. `internal/memory/usermemory.go` is deleted.
