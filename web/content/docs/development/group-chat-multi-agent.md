---
title: Group-chat multi-agent
---

> This page is for developers working on Stella's group-chat support: channel adapters, the message event log, the arbiter/dispatcher, group memory, or session identity. For the user-facing guide, see the channels docs.

Stella's group chat hosts **multiple agents in one physical group**. Each agent is its own platform bot; a single backend process owns all of them, so a central arbiter can act as a real, enforceable speaking gate. This page documents the data model and the identity rules that make that safe. For the target request-to-reply flow shared by Web UI and platform adapters, see [Group-chat dataflow](/docs/development/group-chat-dataflow).

The design is locked up front because most of it is hard to change once it carries data: new tables (event log, group memory, membership, ingest cursor) and the `ctx_conversation` ownership column are all "ship-it-with-data-and-you-can't-go-back" decisions.

## The one rule that governs everything

A group has **three identity dimensions that must never borrow each other's name**:

| Dimension                      | Value                                                              | Used for                                                                 | Never used for                                              |
| ------------------------------ | ------------------------------------------------------------------ | ------------------------------------------------------------------------ | ----------------------------------------------------------- |
| **Session scope**              | `group_id` (surrogate id from the `ctx_group_state` registry)      | LCM lookup key, conversation history, group-memory drawer key            | Runtime identity (vault/token/workspace)                    |
| **Runtime execution identity** | the agent's own group principal `group:{group_id}` (not any human) | tool execution, vault, workspace path                                    | impersonating any member; reading any human's private vault |
| **Per-turn actor**             | the real human speaker's `auth_user`                               | @-addressing, writing the speaker's _own_ private memory, access control | session lookup key, runtime execution identity              |

If you only remember one thing: **a group session never touches any member's private resources.** The speaker's `user_id` is carried per-turn for addressing and access control, then discarded — it never reaches the workspace path, the vault, or any agent tool execution identity.

## Canonical group identity (D0)

A physical group conversation is registered once in `ctx_group_state`, which mints a **surrogate `id`** (app uuid/ulid) as its primary key. Every group-scoped table references that `id` — never a re-derived string. The physical identity that maps to one `id` is the triple `(platform, platform_group_id, platform_thread_id)`, enforced by a `UNIQUE` index; any bot observing the same physical group/thread does a get-or-create on the triple and lands on the same `id`.

Why a surrogate id and not a derived `platform:chat_id` string? The id is opaque and stable, so it survives platform-side id reformatting and keeps FK joins cheap; the triple stays as the natural key for lookup. Under the per-bot architecture each agent is a distinct bot = a distinct `channel_id`, so **one physical group is observed by N channel_ids**. The platform's group id is group-global (it does not change with which bot received the message), so the triple is the stable group identity; `channel_id` only answers "which bot saw it."

**Threads are separate groups.** A Telegram forum topic (or any platform sub-thread) is its own conversation, so each distinct `platform_thread_id` gets its own registry row — its own event log, `seq`, memory drawer, and arbiter scope. `platform_thread_id` is `TEXT NOT NULL DEFAULT ''` (empty string, not `NULL`): PostgreSQL treats `NULL`s as distinct in a unique index by default, so a nullable column would break the `UNIQUE` triple.

`source_channel_id` (which bot observed an inbound message) is recorded on the event-log row **for audit only**. It never enters an idempotency key, `seq`, cursor, or membership primary key, and it is **never a reply route**. The reply route is always the speaking agent's own `reply_channel_id` (see membership).

Every module reuses the registry `id` — event log, group memory, membership, ingest cursor, session key. No module invents its own notion of "group."

## How agents join a group (D1)

Each agent in a group is an independent bot bound to its own channel config (its own token). The platform provides identity, @mention, and delivery. A single backend process hosts every channel, which is what makes the arbiter a real gate rather than a soft suggestion.

Message-ingress topology:

- **Bound bots must read all group messages.** Joining requires disabling the platform privacy mode (Telegram BotFather `/setprivacy` → Disable; equivalent authorization scope on Feishu/QQ). Without full visibility the arbiter has nothing to decide on.
- **One human message may be delivered by several bots.** The event-log idempotency key converges them to one row regardless of which bot delivered it.
- **Dispatch/arbiter fire exactly once**, only when the message is first successfully inserted into the event log.

Rejected alternative: one bot puppeteering multiple virtual agents. The platform can't distinguish virtual identities, so @mention and delivery would have to be simulated and the arbiter degrades to advice.

## The event log (D2)

`ctx_group_message` is the authoritative, deduplicated copy of every group message. Key columns:

| Column                     | Notes                                                                      |
| -------------------------- | -------------------------------------------------------------------------- |
| `id TEXT PRIMARY KEY`      | app-generated uuid/ulid (schema rule requires a TEXT PK)                   |
| `group_id TEXT NOT NULL`   | FK → `ctx_group_state(id)` (D0); all dedup/ordering keys off this          |
| `seq INTEGER NOT NULL`     | per-group monotonic ordering token, starting at 1; `UNIQUE(group_id, seq)` |
| `source_channel_id TEXT`   | observing bot — **audit only**, not in any unique key                      |
| `actor_type TEXT NOT NULL` | `human` / `agent` — schema-level, never guessed from content               |
| `actor_id TEXT NOT NULL`   | human → platform sender_id; agent → agent_id (no separate source_agent_id) |
| `platform_message_id TEXT` | platform id, nullable (some adapters cannot supply it)                     |
| `reply_to TEXT`            | platform id this message replies to; empty/NULL if none                    |
| `platform_timestamp TEXT`  | platform-reported send time (UTC); feeds the high-precision dedup fallback |
| `idempotency_key TEXT`     | fallback dedup key, set only when there is no stable `platform_message_id` |
| `content TEXT NOT NULL`    | JSON-serialized `[]ai.ContentBlock`                                        |

### Deduplication: "rather duplicate than silently drop"

Three tiers, in priority order:

1. Stable `platform_message_id` present → dedup via partial unique `(group_id, platform_message_id)`. No idempotency key generated.
2. No stable id but a **high-precision platform timestamp** → `idempotency_key = hash(group_id, actor_id, platform_timestamp, content)`, partial unique (non-null only).
3. Neither → **no idempotency key** — the message is not idempotent but is never swallowed (occasional duplicates are accepted).

Never use the local receive time or a low-precision/default timestamp to build the hash: that would misclassify "two identical messages sent back-to-back" as a redelivery and silently drop data.

### seq allocation

A global sequence can't give a **per-group** monotonic `seq`, and app-level `max+1` races under concurrency. Instead the registry row doubles as the per-group counter and write lock:

```sql
CREATE TABLE ctx_group_state (
  id                 UUID PRIMARY KEY DEFAULT uuidv7(),  -- surrogate group id (D0)
  platform           TEXT NOT NULL,              -- 'telegram' | 'feishu' | 'qq' | ...
  platform_group_id  TEXT NOT NULL,              -- native group/chat id
  platform_thread_id TEXT NOT NULL DEFAULT '',   -- sub-thread/topic; '' when none
  next_seq           BIGINT NOT NULL DEFAULT 0,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (platform, platform_group_id, platform_thread_id)
);
-- allocation: UPDATE ... SET next_seq = next_seq + 1 WHERE id = $1 RETURNING next_seq (post-update; first = 1)
```

### The single write path

All appends go through one primitive — **never raw `INSERT`**:

```
AppendGroupMessage(ctx, msg) -> (result{inserted|existing}, seq)
```

The closed, idempotent algorithm (a transaction takes a per-group row lock — `SELECT ... FOR UPDATE` on the registry row, or the atomic `UPDATE ... RETURNING` below — so writes to one group serialize):

0. **Get-or-create the registry row** by the `(platform, platform_group_id, platform_thread_id)` triple → obtain its surrogate `id` (= `group_id`).
1. **Inside the lock, check for an existing message** by unique key (`platform_message_id` or fallback `idempotency_key`).
2. **Exists** → return "no insert / no bump / no dispatch."
3. **Not exists** → `UPDATE ctx_group_state SET next_seq = next_seq + 1 WHERE id = $1 ... RETURNING next_seq` → `INSERT` the message row with that seq → dispatch only after commit.

Because both the bump and the insert happen only in the cache-miss branch and inside the same write lock, an idempotent redelivery neither inserts a row nor consumes a seq. `ON CONFLICT DO NOTHING` is a last-resort backstop only; it does not carry the dedup decision.

## IncomingMessage fields (D3)

`pkg/channel.IncomingMessage` gains `ThreadID / MessageID / Timestamp / ReplyTo / Mentions`. `ThreadID` is the platform sub-thread/topic id within `ChatID` (e.g. a Telegram forum topic); it feeds the D0 registry triple so a thread becomes its own group. `Mentions` is a normalized structure, not the platform's raw string:

```go
type Mention struct {
    Raw        string // raw @ text (@username / <at open_id> ...), for audit/fallback
    PlatformID string // platform-side mentioned id (username / open_id / qq number)
    AgentID    string // resolved Stella agent; empty if unresolved
}
```

Adapters fill `Raw` and `PlatformID` (they know the platform id). Ingest resolves `AgentID` best-effort and stores the result in the outbox envelope; the dispatcher re-resolves any still-empty `AgentID` before routing. @-routing honors only `Mention.AgentID != ""` — no component guesses usernames or open_ids on its own.

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

Group messages no longer flow each-bot-directly-into-runtime. The durable path:

```
group message (delivered by any bot)
  → append to event log (D2 idempotent; if not first insert, drop here)
  → create outbox work
  → single group dispatcher
      → L0 rule gate: resolved @mention → deterministic responders
      → L1 semantic gate: no mention → fast-model JSON decision
      → materialize ctx_group_dispatch rows
      → for each selected agent: runtime → publisher (through reply_channel_id)
```

- Any `@mention` signal stays on the L0 rule path. Resolved mentions bypass `MaxRepliesPerTrigger`: if a user mentions multiple group agents, every mentioned member replies. A platform mention that cannot be resolved to a Stella group member is not silently dropped: the dispatcher falls through to the same no-mention semantic path, so an explicit `@mention` is never less reliable than a plain message. Web text mentions that do not resolve remain ordinary text and use the same no-mention path.
- No-mention messages use L1 semantic routing when the semantic arbiter is configured. The classifier can return silence, one agent, or a capped multi-agent broadcast. Failures, timeouts, invalid JSON, or no eligible routing model collapse to silence. Without a semantic arbiter, the only auto-reply is a Web single-member group routing to that one member; every other group stays silent, and any multi-member group also logs a WARN to configure the semantic arbiter.
- The L1 routing model is selected by ownership, not by arbitrary member order. Web groups prefer the group owner's own agent, then system-scope agents. Platform groups allow system-scope agents only, so private agents' credentials are not used for shared routing decisions.
- L1 sees only bounded public routing metadata: agent ID/name, member summary (the first 180 characters of `system_prompt`, intentionally sent for correct routing), and bounded prior group context with `seq < currentSeq`. Delayed outbox retries never see future messages as prior context.
- decide and generate are separate; **decide emits intent only, not a draft** (saves tokens).
- A hard cap of N public replies per human trigger prevents runaway flooding.
- An agent's own message is logged (passively readable) but **does not wake other agents' arbiters by default.** The exception is an explicit `@otherAgent`, which is handled by a separate handoff dispatcher — the normal arbiter only reacts to `actor_type=human`, so the two paths don't conflict.

### Durable dispatch correctness

- Platform ingest creates a pending outbox row in the same transaction as the event-log message. Web synchronous ingest creates the outbox as `running` with a lease in that same transaction, so the background worker cannot steal the request while the SSE path is executing. If the process crashes or the lease expires, the worker recovers it with `NoopGroupPublisher`; Web's durable delivery source is the event log, not the open SSE socket.
- Web disconnects do not cancel generation. The server uses a service-lifecycle context with a bounded timeout, drains the stream, and writes back only complete successful responses. Partial streams caused by cancellation or errors are not appended.
- Dispatch retries use linear backoff: `1s * attempts`, capped at 60s. Rows that exceed the retry budget are marked `failed`; there is no fallback that lets another channel impersonate the agent.
- Dispatch is ordered per `(group_id, agent_id)`: SQL only claims a row when no earlier `seq` for the same agent is pending or running with a live lease. Expired running rows are reclaimed instead of blocking forever.
- Reply publishing is at-least-once. The normal tail is publish → one DB transaction that appends the group reply and writes `result_message_id` → mark delivered → mark completed. The remaining duplicate window is publish succeeded but the writeback+marker transaction did not commit.
- **A generated response is never regenerated.** The response is recorded whether or not delivery succeeded, so a failed publish leaves a durable `result_message_id`. A retry that sees `result_message_id` with `delivery_complete = false` re-delivers that persisted text — it never calls the agent again. A retry that sees `delivery_complete` completes immediately.
- A publisher that buffers the whole response before it can send any of it — Discord must, to split it into chunks — records the response through `GroupPublishRequest.Record` as soon as the stream ends and before the first message reaches the platform. That closes the window in which a crash between the last agent token and the first delivered chunk would cost a second agent turn. The dispatcher records whatever a publisher did not, once `Publish` returns; whichever comes first wins, and the response is written exactly once. A failed record is a delivery failure: the publisher sends nothing.
- Re-delivery resumes from `delivery_cursor`, the number of leading response chunks a publisher confirmed the platform accepted. Discord confirms per 2000-character chunk, so a failure on chunk 2 of 3 re-delivers only chunks 2 and 3. Publishers that do not confirm chunks resend the whole persisted response. The cursor is reset whenever an attempt regenerates instead of re-delivering, because it indexes one specific response.
- Images and files are not persisted and never re-delivered. A failed attachment upload is reported in the group and logged, but it does not fail the publish: requeueing over an attachment would put a dispatch back in a state whose only recovery is re-running the agent.
- A running dispatch whose lease expires retires as completed only when its delivery finished (`result_message_id` set and `delivery_complete = true`). One that holds a recorded response it never delivered is requeued, including at `delivery_cursor = 0` — the crash may have landed between persisting the response and sending its first chunk, and retiring the row there would silence the agent for that turn. The retry re-delivers; it never re-runs the agent.
- Group context injection deduplicates already-persisted injected messages with an exact SQL content lookup across the conversation, not a token-budget window.

## Reply egress: group only (D8)

No agent-initiated DMs to group members (platforms forbid bots from DMing strangers). All output lands on the group ChatID; @mentions are a content-layer concern over the same delivery route.

## Session ownership (D9): why this must be fixed at design time

`ctx_conversation.session_id` is `UNIQUE`, and the LCM lookup is `(session_id, user_id, agent_id)`. If a shared session kept passing the current speaker's `user_id`:

- A speaks → row `(S, A, agentX)` created.
- B speaks → lookup `(S, B, agentX)` misses → create-if-missing inserts `session_id = S` → **collides with the UNIQUE constraint.**

So the group session stores `group_id` in `ctx_conversation.user_id` as a **lookup key only** (the column has no `auth_user` FK). The group branch of `requireSessionScope` / `GetConversationBySessionID` matches `(session_id, group_id, agent_id)` and does not require an actor match.

The critical constraint: that `group_id` must **never flow into member-owned runtime identity surfaces.** The runtime detects "this is a group session" in one place and reroutes these surfaces:

| Surface                 | Code                                                       | Group-session behavior                                                                             |
| ----------------------- | ---------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| memory / prompt profile | `runtime/chat.go` (`authz.WithUserID`), `prompt/prompt.go` | inject no human; read the group drawer, never a member's private profile                           |
| workspace               | `runner_builder.go`, `workspace.go`                        | path `users/group-{group_id}/` — the group is its own principal, never a member's `users/{userID}` |
| vault                   | `sandbox/env.go`                                           | resolve only agent/group-scoped secrets, never a member's private vault                            |
| agent tools             | `authz.Identity` facades and tool handlers                 | act as the group principal, not a human userID                                                     |

No synthetic `auth_user` is created: the group principal is an execution scope, not a row in `auth_user`.

The membership table closes the loop:

```sql
-- channel_group_member: PK (group_id, agent_id), plus reply_channel_id
--   reply_channel_id FK -> channel(id)
--   assert channel.agent_id == channel_group_member.agent_id at membership write AND publisher send
```

`channel.agent_id` still means bot→agent binding; `channel_agent`'s single-active semantics stay for DM/non-group only. The dispatcher receives a message from any bot, resolves all agents in the group by `group_id`, and each agent replies via its own `reply_channel_id`. The dual assertion stops a misconfiguration or malicious write from letting agentB speak through agentA's bot.

## Current speaker: per-turn personalization (D10)

D9 keeps the group session anonymous so no human owns the runtime. But the agent still needs to know **who is speaking right now** to personalize a reply. That is a second identity axis, deliberately kept separate from the runtime/session identity so it can never become it.

`memory.CurrentSpeaker` carries the per-turn speaker: `Platform`, `PlatformUserID` (lookup/audit only), `DisplayName`, and `UserID` (the resolved Stella user when the sender is linked; empty when unlinked). It travels on the context via `WithCurrentSpeaker` / `CurrentSpeakerFromContext`, parallel to — never merged with — `UserIDFromContext`.

The hard rules:

- **Personalization target, not runtime identity.** `CurrentSpeaker.UserID` must never be passed to `authz.WithUserID`, sandbox/vault/token code, plugin or delegate contexts, notify routing, or hook user metadata. `runtime/chat.go` attaches the speaker for group turns but still skips `WithUserID`, so all four D9 surfaces stay group-scoped.
- **Per-turn, never cached.** The prompt's `## Current Speaker` section is built fresh each turn by the PoolManager before-run prompt rebuild, which re-renders the full system prompt. The cached group runner never holds speaker context, so one speaker's turn metadata can't leak into another's turn.
- **Prompt rendering is keyed on `GroupID`, not on group memory being non-empty.** A group turn renders `## Group Memory` (+ optional `## Current Speaker`) and never falls back to the per-user `## User Profile` section, even when the group drawer is empty.
- **No automatic private profile injection.** `## Current Speaker` exposes the display name and linked/unlinked status only. It does not include the speaker's profile blob, dated entries, soul, or constraints: a public room is not the place to disclose one member's private memory or apply their hard rules to the whole group.
- **Resolution by hard facts.** Platform senders resolve through channel identity lookup (linked → auth user id, unlinked → empty UserID → name only). Web senders trust the authenticated `actor_id` as the speaker only for a genuine human actor, failing closed otherwise.

The `memory` tool mirrors this in group turns: with no session user, ordinary chat can only use read-only `profile_get` to fall back to the current speaker when the model explicitly calls the tool. `profile_update` exists only for explicitly write-enabled internal tools. `soul_*`, `constraint_*`, and `profile_history` / `profile_rollback` stay strict and fail closed.

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
