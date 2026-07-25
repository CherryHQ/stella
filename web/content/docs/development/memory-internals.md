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

| Interface                                            | Description                                                            |
| ---------------------------------------------------- | ---------------------------------------------------------------------- |
| `Compactor`                                          | Context window compaction                                              |
| `Searcher`                                           | Full-text search across messages and summaries                         |
| `Explorer`                                           | Inspect and drill into summaries                                       |
| `ProfileStore`                                       | Per-user-per-agent profile and soul text                               |
| `ConstraintStore`                                    | Per-user-per-agent hard constraints                                    |
| `ChangelogReader` / `ChangelogWriter`                | Version history for memory writes                                      |
| `VersionedProfileStore` / `VersionedConstraintStore` | Read identity/constraint state at a frozen version                     |
| `SessionSnapshotStore`                               | Freeze and advance per-session memory versions                         |
| `SessionManager`                                     | Session metadata and history management                                |
| `ReviewSource`                                       | Self-improvement review data for Reflect                               |
| `GroupEventIngestor` / `GroupCursorCommitter`        | Idempotent public Group Event replication and post-turn cursor commits |
| `GroupFactStore`                                     | Group-scoped atomic Fact reads and version checks                      |

The LCM plugin implements the full set. The Simple plugin implements the core provider plus identity, constraints, changelog, snapshots, and session management, but it does not compact/search/explore.

## Memory Tool

`memory.BuildTool(provider)` inspects the provider's capabilities and generates a `tools.Tool` with matching actions. Callers can further narrow the action set: ordinary chat runners use `WithSessionReadOnlyWrites()` so model-facing sessions only expose read/retrieval actions, while Reflect/manual paths opt into the specific write actions they own.

| Action              | Requires                           | Description                                                              |
| ------------------- | ---------------------------------- | ------------------------------------------------------------------------ |
| `status`            | always                             | Show session stats                                                       |
| `search`            | `Searcher`                         | Search messages and summaries by pattern                                 |
| `describe`          | `Explorer`                         | Inspect a summary's metadata and lineage                                 |
| `expand`            | `Explorer`                         | Drill into compacted summaries                                           |
| `profile_get`       | `ProfileStore`                     | Read persistent user profile notes                                       |
| `profile_update`    | `ProfileStore`                     | Replace persistent user profile notes; Reflect/manual only               |
| `soul_get`          | `ProfileStore`                     | Read per-user agent soul override                                        |
| `soul_update`       | `ProfileStore`                     | Update per-user agent soul override; manual only                         |
| `profile_history`   | `ChangelogReader`                  | Read recent profile/soul change history                                  |
| `profile_rollback`  | `ChangelogReader` + `ProfileStore` | Restore profile/soul text from a previous changelog version; manual only |
| `constraint_list`   | `ConstraintStore`                  | List hard constraints                                                    |
| `constraint_add`    | `ConstraintStore`                  | Add a hard constraint; manual only                                       |
| `constraint_remove` | `ConstraintStore`                  | Remove a hard constraint by ID; manual only                              |

The tool's JSON schema, description, and dispatch all adapt dynamically. A provider with fewer capabilities produces a tool with fewer actions. A model-facing chat session is additionally read-only for durable memory writes: it cannot call `profile_update`, `soul_update`, `profile_rollback`, `constraint_add`, or `constraint_remove`.

### Group turns

`STELLA_GROUP_MEMORY_MODE` controls the transition:

- `legacy` keeps the old shared Blob prompt and its compatibility behavior.
- `structured` exposes only `status`, `search`, `describe`, `expand`, and `get_message` for the current group session. It does not expose Profile, Soul, Constraint, or one-to-one Knowledge actions and does not use current-speaker Profile fallback.

The runtime group identity contains `(group_id, agent_id)` and no authenticated user. This same boundary hides user/user_agent Skills; group Agents can read system, their own system_agent, and project Skills only.

## System Prompt Layers

Each turn can rebuild the system prompt from the current or frozen memory version. The prompt order is:

1. **Base system prompt** — agent configuration / `SYSTEM.md` override.
2. **Tools and plugin prompt inventory** — available tools, plugin capabilities, skills.
3. **Constraints** — user-approved hard rules from `ConstraintStore`; injected before soul/profile and not touched by Reflect.
4. **Agent soul** — agent identity/personality text.
5. **User profile or Group Facts** — DM sessions use their frozen user profile. Structured group sessions inject all active atomic Group Facts through the per-turn before-run hook; they do not render a user profile or legacy Group Memory Blob.
6. **Knowledge** — active `subject=world` facts from the facts table.
7. **Project context** — `AGENTS.md` and related project instructions.

Conversation history is assembled separately by the memory provider. Constraints, identity, and knowledge live in the system prompt, so conversation compaction does not remove them.

Structured Group Facts are not snapshotted per session. A process-shared cache is keyed only by `group_id`: it reuses the last successful block for two hours, checks the group version after expiry, and reloads all active Facts only when the version changes. Current public messages take precedence over stale or conflicting Facts. Facts provide context only and cannot grant permissions or override system/constraint instructions.

## Changelog and Rollback

`ctx_agent_memory` has a row-level `version`. Writes to profile, soul, and constraints increment the version and append a row to `memory_changelog` in the same database transaction.

The changelog records:

- user and agent
- scope (`profile`, `soul`, `constraint`, `skill`, `compaction`)
- action (`create`, `update`, `delete`, `deprecate`, `compact`)
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

Reflect is explicitly instructed not to add, remove, or edit constraints. Normal session tools also do not expose constraint write actions; constraints are written through manual UI/API/CLI paths after explicit user intent.

## Session Snapshots

Session snapshots prevent background memory updates from changing an active conversation mid-stream.

On the first chat turn, Stella stores a frozen `ctx_agent_memory.version` in `memory_snapshots` for `(session_id, user_id, agent_id)`. On every turn, `runtime.Runtime` rebuilds the system prompt at that snapshot version and injects it with a per-run system override.

Visibility rules:

| Write path                                  | Current session sees it? | Why                                                                  |
| ------------------------------------------- | ------------------------ | -------------------------------------------------------------------- |
| Manual UI/API/CLI updates profile/soul      | New sessions             | Manual writes update durable facts; active sessions stay snapshotted |
| Manual UI/API/CLI adds/removes a constraint | New sessions             | Manual writes update constraints; active sessions stay snapshotted   |
| Reflect updates profile in the background   | No                       | Reflect has no active session context and does not advance snapshots |
| A new session starts                        | Yes                      | It snapshots the latest memory version                               |

This prevents both foreground tool writes and background reflection from changing behavior inside an ongoing session.

## Knowledge

Knowledge is stored as active `facts` rows with `subject=world` and `scope=user_agent` in v1. The prompt renderer projects those facts into the `## Knowledge` section.

Skills remain reusable procedures and no longer create or store fact/context knowledge via `metadata.knowledge_type`. Legacy `user_agent` skill-backed knowledge is migrated into `subject=world` facts by the v1 facts migration; broader knowledge scopes are intentionally left for a follow-up design.

Normal conversation tools do not directly write facts. Structured Reflect generates and evaluates Fact and Skill candidates, discovers related Reflect-owned records, reconciles accepted candidates, and writes each line through host-validated operations. Usage tracking and the curator maintain the lifecycle of active Reflect-owned Knowledge and Skills.

## Structured Group Facts

Group Facts are a separate public collaboration memory scoped by `group_id`. They never read or write one-to-one Profile, Soul, Constraint, Knowledge, or user-owned Skills.

- Subjects are `group`, `human:<actor_id>`, or `agent:<actor_id>`.
- Group Reflect runs every six hours on a dedicated 128k+ model and reads bounded public Event Log windows only.
- Generation emits at most five candidates; an independent evaluator applies the stricter group rubric and deterministic host gate.
- Accepted candidates are reconciled against all active Facts in that group using `noop`, `create`, `replace_many`, or `deprecate_many`.
- Facts, changelog, group version, and `group_reflect` cursor commit in one short per-group transaction.
- There is no group snapshot, usage/LRU expiry, private Profile fallback, or automatic Skill generation.

Each Agent keeps an independent LCM. Public Group Events are copied into that LCM with `origin_group_message_id`, so retries are idempotent. Pending public Events are synchronized before compaction; group LCM uses an 80k budget and preserves the six newest Group Event input anchors plus their causal assistant/tool tail.

## Structured Reflect and Curator

Structured Reflect is the only scheduler writer. It runs the Fact and Skill lines concurrently with independent failures and watermarks; one failed line does not cancel or advance the other. The obsolete `STELLA_REFLECT_MODE` variable no longer selects a writer. During the transition release, an empty value or `structured` is accepted for deployment compatibility, while an explicit `legacy` or unknown value fails startup.

The cutover migration copies every legacy session `review_watermark` into missing `reflect_watermark:fact` and `reflect_watermark:skill` state. When a line already exists, the newer timestamp wins; if the legacy timestamp wins, the old line sequence is cleared because it belongs to an earlier boundary. The migration is idempotent and leaves global rows untouched as inert rollback data. Runtime code reads and advances only the two line watermarks.

The curator remains an independent boot-time control:

| Variable                      | Values            | Default | Meaning                                                          |
| ----------------------------- | ----------------- | ------- | ---------------------------------------------------------------- |
| `STELLA_REFLECT_CURATOR_MODE` | `armed`, `shadow` | `armed` | Executes lifecycle writes or keeps a non-mutating emergency stop |

In `armed`, startup fails closed unless all Structured Reflect and curator read/write dependencies are available. Eligible Reflect-owned Knowledge is deprecated and can be recovered through authenticated management APIs; eligible Reflect-owned Skills are permanently deleted.

Curator Shadow executes the same deterministic eligibility scan and records candidate kind, record ID, matched rule, activity inputs, candidate counts, rule distribution, duration, and errors, but does not mutate record status, changelog, or usage state. It is the emergency stop for future lifecycle writes and a read-only way to inspect production scan volume and wiring. Automated tests enforce ownership/scope gates, usage checks, write-time rechecks, and fail-closed dependencies.

After deployment, verify one complete Structured Reflect run, armed curator eligibility writes, Knowledge recovery, and the switch back to non-mutating Shadow. Rolling back the entire release means deploying the previous binary; the retained global and line watermark state lets that binary resume conservatively.

## LCM Plugin

### Architecture

```
ai.Message (user/assistant/tool_result)
        |
        v
  +----------+     Append     +-----------+
  | Provider | ------------> | Postgres  |
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

Compaction reduces the conversation window by summarizing older messages and summaries. If the provider has no summarizer, compaction is disabled and no empty-summary fallback is used.

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

1. Separate context items into **fresh tail** (last N user turns, default 6) and **older** items. A turn starts at a user message and includes every following item until the next user message.
2. If the turn-based tail would contain more than 120 message items, fall back to the last 120 message items with tool-pair boundary correction. This keeps degenerate single-user agent loops compactable.
3. Resolve fresh tail items to `ai.Message`s.
4. Replace large processed tool results (>2000 tokens) in the tail with a compact placeholder, preserving `ToolCallID`, `ToolName`, `IsError`, and `Timestamp` so the model knows the content was omitted and can re-invoke the tool if needed. Tool results that are still in-flight (no assistant reply yet) are passed through at full size.
5. During assembly only, cap the fresh tail at 40% of the token budget by demoting whole oldest tail turns back into older budget competition. At least one turn always remains in the tail. The cap is evaluated against the placeholder-compacted tail, so an oversized tool result does not demote its own turn.
6. Compute tail token cost against the already-compacted tail, then fill remaining budget with older items, newest first.
7. Return older events in chronological order followed by the tail.

Every assembly emits a structured log entry (`lcm tail telemetry`) with `tail_items`, `tail_messages`, `user_turns`, `items_per_turn`, and `tool_results_before/after` for observability-driven tuning.

### Search

Search runs on PostgreSQL with **pg_search BM25** ranking. The `ctx_message` and `ctx_summary` tables each carry a `USING bm25` index over `content`, tokenized with the **ICU** tokenizer, so CJK is segmented into words (部署方案 matches the segmented 部署 / 方案 in a longer sentence) and English matches whole tokens. There is **no fallback tier** — pg_search hard-requires the `pg_search` extension (and `vector` for the semantic lane), which ship in the downloaded runtime or an external PostgreSQL.

- Raw user text goes straight to `paradedb.match`, which tokenizes with ICU and never errors on punctuation or query-syntax characters — so there is no separate sanitize step, and short or CJK queries match natively (no minimum-token-length rule, no `LIKE` fallback).
- Hits carry a pg_search snippet (`<b>term</b>` highlights) and a `paradedb.score` BM25 score (higher is better).
- `both` scope queries messages and summaries separately with the full limit, then merges by score and keeps the top N — a strong summary hit can outrank a weak message hit. Summary hits drill down via `describe`/`expand`.
- The BM25 indexes live in the schema baseline (`internal/db/migrations`); the `vector`/`pg_search` extensions are created at **runtime** (`ensureExtensions` in `internal/db/database.go`) before migrations run, because `CREATE EXTENSION` needs binaries (and `shared_preload_libraries=pg_search`) that a migration cannot guarantee.
- Semantic search uses per-source sidecar tables (`ctx_message_embedding`, `ctx_summary_embedding`, `recally_article_embedding`) holding a `vector(1536)` with an HNSW (`vector_cosine_ops`) index, keyed by the source id. The lane is **opt-in and runtime-configured** — there are no embedding env vars. Admins set the provider key, base URL, model, dimension, and normalization on the **Settings → Embedding** page (stored as one JSON value under the `embedding` key in `app_setting`); changes take effect immediately, with no restart.
- When enabled, a River-backed worker embeds new content and backfills the existing rows; when disabled, the worker idles and search falls back to pure BM25. Each row records a **space key** (`model@dim`) in its `model` column, and queries filter `WHERE model = $space`, so a query embedded under a different model/dimension simply returns no rows rather than mismatched ones — switching model or dimension re-embeds into a fresh space.
- When both lanes return hits, `retrieval.go` fuses them by min-max normalizing each lane's scores independently and combining them with a 50/50 weight, merging the two lanes by `source_type/source_id`.

## Simple Plugin

The Simple plugin uses a sliding-window approach:

1. **Append** stores messages in `ctx_messages`.
2. **Assemble** returns the last N messages that fit within the token budget, always honoring freshTail.
3. No summaries, no compaction, no search, no explore.

This is suitable for short-lived conversations or resource-constrained environments.

## Database

- **Location:** PostgreSQL — embedded cluster under `~/.stella/postgres` by default, or an external server via `STELLA_DATABASE_URL`
- **Driver:** `pgx/v5`
- **Migrations:** goose, embedded and auto-applied on startup.

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

| Constant                     | Value | Description                                                            |
| ---------------------------- | ----- | ---------------------------------------------------------------------- |
| `DefaultFreshTail`           | 6     | User turns protected from compaction                                   |
| `CompactionConfig.MaxTokens` | 80000 | Absolute context token count that triggers compaction                  |
| `DefaultLeafChunkSize`       | 10    | Minimum messages per leaf summary                                      |
| `OversizedToolResultTokens`  | 2000  | Tail tool results above this threshold are replaced with a placeholder |

## Agent Workspaces

Each agent has its own workspace at `$STELLA_HOME/agents/{agent_id}/` for file overrides, skills, and per-agent data.
