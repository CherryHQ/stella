---
title: Group-chat multi-agent
---

> This page is for developers working on Stella's group-chat support: channel adapters, the message event log, the arbiter/dispatcher, group memory, or session identity. For the user-facing guide, see the channels docs.

Stella's group chat hosts **multiple agents in one physical group**. Each agent is its own platform bot; a single backend process owns all of them, so a central arbiter can act as a real, enforceable speaking gate. This page documents the data model and the identity rules that make that safe.

The design is locked up front because most of it is hard to change once it carries data: new tables (event log, group memory, membership, ingest cursor) and the `ctx_conversation` ownership column are all "ship-it-with-data-and-you-can't-go-back" decisions.

## The one rule that governs everything

A group has **three identity dimensions that must never borrow each other's name**:

| Dimension                      | Value                                                              | Used for                                                                 | Never used for                                              |
| ------------------------------ | ------------------------------------------------------------------ | ------------------------------------------------------------------------ | ----------------------------------------------------------- |
| **Session scope**              | `group_id` (= `platform:chat_id`)                                  | LCM lookup key, conversation history, group-memory drawer key            | Runtime identity (vault/token/workspace)                    |
| **Runtime execution identity** | the agent's own group principal `group:{group_id}` (not any human) | tool execution, vault, scoped token, workspace path                      | impersonating any member; reading any human's private vault |
| **Per-turn actor**             | the real human speaker's `auth_user`                               | @-addressing, writing the speaker's _own_ private memory, access control | session lookup key, runtime execution identity              |

If you only remember one thing: **a group session never touches any member's private resources.** The speaker's `user_id` is carried per-turn for addressing and access control, then discarded — it never reaches the workspace path, the vault, or the scoped-token subject.

## Canonical group identity (D0)

One canonical key system-wide: `group_id = platform + ":" + chat_id`. It is the unique key of one physical group chat and is **stable across every bot that observes it**.

Why not `channel_id`? Under the per-bot architecture, each agent is a distinct bot = a distinct `channel_id`, so **one physical group is observed by N channel_ids**. The platform's `chat_id` is group-global (it does not change with which bot received the message), so `(platform, chat_id)` is the stable group identity; `channel_id` only answers "which bot saw it."

`source_channel_id` (which bot observed an inbound message) is recorded on the event-log row **for audit only**. It never enters an idempotency key, `seq`, cursor, or membership primary key, and it is **never a reply route**. The reply route is always the speaking agent's own `reply_channel_id` (see membership).

Every module reuses this one value — event log, group memory, membership, ingest cursor, session key. No module invents its own notion of "group."

## How agents join a group (D1)

Each agent in a group is an independent bot bound to its own channel config (its own token). The platform provides identity, @mention, and delivery. A single backend process hosts every channel, which is what makes the arbiter a real gate rather than a soft suggestion.

Message-ingress topology:

- **Bound bots must read all group messages.** Joining requires disabling the platform privacy mode (Telegram BotFather `/setprivacy` → Disable; equivalent authorization scope on Feishu/QQ). Without full visibility the arbiter has nothing to decide on.
- **One human message may be delivered by several bots.** The event-log idempotency key converges them to one row regardless of which bot delivered it.
- **Dispatch/arbiter fire exactly once**, only when the message is first successfully inserted into the event log.

Rejected alternative: one bot puppeteering multiple virtual agents. The platform can't distinguish virtual identities, so @mention and delivery would have to be simulated and the arbiter degrades to advice.

## The event log (D2)

`ctx_group_message` is the authoritative, deduplicated copy of every group message. Key columns:

| Column                     | Notes                                                                        |
| -------------------------- | ---------------------------------------------------------------------------- |
| `id TEXT PRIMARY KEY`      | app-generated uuid/ulid (schema rule requires a TEXT PK)                     |
| `group_id TEXT NOT NULL`   | canonical identity (D0); all dedup/ordering keys off this                    |
| `source_channel_id TEXT`   | observing bot — **audit only**, not in any unique key                        |
| `actor_type TEXT NOT NULL` | `human` / `agent` — schema-level, never guessed from content                 |
| `actor_id TEXT NOT NULL`   | human → platform sender_id; agent → agent_id                                 |
| `source_agent_id TEXT`     | which agent, when `actor_type=agent` (publisher writes its own message back) |
| `platform_message_id TEXT` | platform id, nullable (Phase 1 adapters may not fill it)                     |
| `seq INTEGER NOT NULL`     | per-group monotonic ordering token, starting at 1                            |

### Deduplication: "rather duplicate than silently drop"

Three tiers, in priority order:

1. Stable `platform_message_id` present → dedup via partial unique `(group_id, platform_message_id)`. No idempotency key generated.
2. No stable id but a **high-precision platform timestamp** → `idempotency_key = hash(group_id, actor_id, platform_timestamp, content)`, partial unique (non-null only).
3. Neither → **no idempotency key** — the message is not idempotent but is never swallowed (occasional duplicates are accepted).

Never use the local receive time or a low-precision/default timestamp to build the hash: that would misclassify "two identical messages sent back-to-back" as a redelivery and silently drop data.

### seq allocation

`AUTOINCREMENT` is unavailable — SQLite only allows it on an `INTEGER PRIMARY KEY`, and this table's PK is `TEXT`. App-level `max+1` races under concurrency. Instead a per-group counter table allocates it:

```sql
CREATE TABLE ctx_group_state (
  group_id   TEXT PRIMARY KEY,
  next_seq   INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
-- allocation: UPDATE ... SET next_seq = next_seq + 1 RETURNING next_seq (post-update; first = 1)
```

### The single write path

All appends go through one primitive — **never raw `INSERT`**:

```
AppendGroupMessage(ctx, msg) -> (result{inserted|existing}, seq)
```

The closed, idempotent algorithm (SQLite is single-writer; `BEGIN IMMEDIATE` serializes per-group writes):

1. `INSERT OR IGNORE INTO ctx_group_state(group_id)` — create the state row on first message.
2. **Inside the lock, check for an existing row** by unique key (`platform_message_id` or fallback `idempotency_key`).
3. **Exists** → return "no insert / no bump / no dispatch."
4. **Not exists** → `UPDATE ctx_group_state SET next_seq = next_seq + 1 ... RETURNING next_seq` → `INSERT` the message row with that seq → dispatch only after commit.

Because both the bump and the insert happen only in the cache-miss branch and inside the same write lock, an idempotent redelivery neither inserts a row nor consumes a seq. `ON CONFLICT DO NOTHING` is a last-resort backstop only; it does not carry the dedup decision.

## IncomingMessage fields (D3)

`pkg/channel.IncomingMessage` gains `MessageID / Timestamp / ReplyTo / Mentions`. `Mentions` is a normalized structure, not the platform's raw string:

```go
type Mention struct {
    Raw        string // raw @ text (@username / <at open_id> ...), for audit/fallback
    PlatformID string // platform-side mentioned id (username / open_id / qq number)
    AgentID    string // resolved Stella agent; empty if unresolved
}
```

Adapters fill `Raw` and `PlatformID` (they know the platform id). The **dispatcher resolves `AgentID`** by looking up membership. @-routing honors only `Mention.AgentID != ""` — no component guesses usernames or open_ids on its own.

## Memory: subject axis (D4)

A separate group-memory table, **keyed by `(group_id)` only — not per-agent**:

```sql
CREATE TABLE ctx_group_memory (
  group_id TEXT PRIMARY KEY,
  -- ... blob drawer, no auth_user FK
);
```

The three existing user-memory tables (`ctx_agent_memory` / `_changelog` / `_snapshot`) are left untouched. Two reasons:

1. Those tables have `user_id REFERENCES auth_user(id) ON DELETE CASCADE`. A `group_id` is not an `auth_user`, so generalizing into the same tables would mean dropping the FK and losing "delete user → cascade-clean memory."
2. A separate table **upgrades the privacy wall from discipline to the type system**: the DM write path simply has no handle to `ctx_group_memory`, so `private → group` leakage is structurally impossible.

Why `(group_id)` and not `(group_id, agent_id)`? v1 extraction is generic (not agent-role-specific), so per-agent drawers would be identical copies of the same extraction — no benefit, and they'd drag in cursor agent-dimension, a membership dependency, and N× cost. Group memory is the group's shared knowledge; all agents in the group read the same drawer. Agent-specific group memory is a future, additive change (add an `agent_id` axis then).

Write rules, decided by hard facts about message origin (never by the LLM):

| Message origin                  | Written to                                                                        |
| ------------------------------- | --------------------------------------------------------------------------------- |
| User speaks publicly in a group | group-shared drawer `(group_id)` **+** that user's private drawer `(user, agent)` |
| User sends a DM                 | private drawer only — **never** the group-shared drawer                           |
| Agent speaks                    | no memory written                                                                 |

`private → group` is a one-way wall enforced by path isolation, not by prompt pleading.

## Memory timestamps (D5)

`profile` moves from a single blob to dated entries (aligning with how constraints already carry `CreatedAt`), and the **date must render into the system prompt** (today it does not, so the timestamp is wasted). HTTP stays compatible: entries are stored internally, but the read API flattens manual entries back into a string, so OpenAPI / SDK / UI are unchanged.

## Async memory ingest (D6)

Memory is never written on the reply path. A background single-consumer pulls from the event log by `seq > cursor`, batches, runs a lightweight LLM extraction, and routes per D4.

- Cursor: `ctx_group_ingest_cursor(group_id, pipeline)`, value = last consumed `seq`.
- Dead-letter: `ctx_group_ingest_error(id, group_id, pipeline, seq, reason, created_at)`. Transient failure (LLM timeout/rate-limit) → cursor does not advance, retry the same batch. Bad message (unparseable) → record in the dead-letter table, then the cursor steps over that seq.
- The cursor only advances to the end of the contiguous prefix of "extracted-or-dead-lettered," so it neither skips nor stalls.

## Arbiter: the speaking gate (D7)

Group messages no longer flow each-bot-directly-into-runtime. The new path:

```
group message (delivered by any bot)
  → append to event log (D2 idempotent; if not first insert, drop here)
  → single group dispatcher
      → cheap rule prefilter → arbiter.decide (intent only)
      → debounce window
      → for each agent that chose to speak: runtime → publisher (to the group ChatID)
```

- An @-mentioned agent bypasses the arbiter and answers directly.
- decide and generate are separate; **decide emits intent only, not a draft** (saves tokens).
- A hard cap of N public replies per human trigger prevents runaway flooding.
- An agent's own message is logged (passively readable) but **does not wake other agents' arbiters by default.** The exception is an explicit `@otherAgent`, which is handled by a separate handoff dispatcher — the normal arbiter only reacts to `actor_type=human`, so the two paths don't conflict.

## Reply egress: group only (D8)

No agent-initiated DMs to group members (platforms forbid bots from DMing strangers). All output lands on the group ChatID; @mentions are a content-layer concern over the same delivery route.

## Session ownership (D9): why this must be fixed at design time

`ctx_conversation.session_id` is `UNIQUE`, and the LCM lookup is `(session_id, user_id, agent_id)`. If a shared session kept passing the current speaker's `user_id`:

- A speaks → row `(S, A, agentX)` created.
- B speaks → lookup `(S, B, agentX)` misses → create-if-missing inserts `session_id = S` → **collides with the UNIQUE constraint.**

So the group session stores `group_id` in `ctx_conversation.user_id` as a **lookup key only** (the column has no `auth_user` FK). The group branch of `requireSessionScope` / `GetConversationBySessionID` matches `(session_id, group_id, agent_id)` and does not require an actor match.

The critical constraint: that `group_id` must **never flow into the runtime identity surfaces.** The runtime detects "this is a group session" in one place and reroutes all four surfaces:

| Surface                 | Code                                                              | Group-session behavior                                                   |
| ----------------------- | ----------------------------------------------------------------- | ------------------------------------------------------------------------ |
| memory / prompt profile | `runtime/chat.go` (`memory.WithUserID`), `prompt/prompt.go`       | inject no human; read the group drawer, never a member's private profile |
| workspace               | `runner_builder.go`, `workspace.go`                               | path `workspaces/{agentID}/groups/{group_id}`, not `users/{userID}`      |
| vault                   | `sandbox/env.go`                                                  | resolve only agent/group-scoped secrets, never a member's private vault  |
| scoped token            | `auth/token_service.go`, `auth/scoped_token.go`, `sandbox/env.go` | subject = group principal `group:{group_id}`, not a human userID         |

`SignScopedToken` currently hard-requires `claims.UserID != ""`. That must be relaxed to accept a group principal, or gain a `Scope=group` dimension — **never stuff a real human userID in.** No synthetic `auth_user` is created: the group principal is a token subject / execution scope, not a row in `auth_user`.

The membership table closes the loop:

```sql
-- channel_group_member: PK (group_id, agent_id), plus reply_channel_id
--   reply_channel_id FK -> channel(id)
--   assert channel.agent_id == channel_group_member.agent_id at membership write AND publisher send
```

`channel.agent_id` still means bot→agent binding; `channel_agent`'s single-active semantics stay for DM/non-group only. The dispatcher receives a message from any bot, resolves all agents in the group by `group_id`, and each agent replies via its own `reply_channel_id`. The dual assertion stops a misconfiguration or malicious write from letting agentB speak through agentA's bot.

## Implementation order

Data model (the hard-to-change parts) first, behavior second:

1. **Phase 1** — IncomingMessage fields (D3).
2. **Phase 2** — event log + group session ownership at the DB layer (D2, D9 session scope). **Safety gate: group sessions are NOT wired into `Runtime.Chat` here** — testing stays at the schema/event-log/session-registry layer so `group_id` can't leak into runtime identity.
3. **Phase 2b** — group runtime identity isolation (D9 runtime surfaces), a cross-cut over the eight files above; the prerequisite for Phase 5 wiring.
4. **Phase 3** — group memory table + timestamps (D4, D5).
5. **Phase 4** — async ingest (D6).
6. **Phase 5** — multi-agent + arbiter (D1, D7), including the membership table.
7. **Phase 6** — reply egress (D8).

Migrations always go schema-file edit → `mise run db:diff` → `mise run generate`; never hand-write SQL.
