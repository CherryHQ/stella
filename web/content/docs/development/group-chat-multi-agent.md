---
title: Group chat with multiple agents
description: How Stella lets several agents share one conversation without a router deciding who speaks.
---

> This developer reference explains the design of Stella group collaboration: what problem it solves, which alternatives were rejected, and which invariants the implementation is holding up. For the same material as diagrams, see [Group-chat dataflow](./group-chat-dataflow). For channel setup, see the channel guides.

## The problem

A group holds one human (or several) and several agents. Every agent has its own memory, tools, schedule, and sandbox. When a message arrives, something has to decide which agents respond — and that decision is genuinely hard, because "should I say something here" depends on the whole conversation, on what the agent knows, and on what it is already doing.

The design constraint that shapes everything below: **the entity best qualified to answer that question is the agent itself**, and it is the one entity that has not run yet when the question is asked.

## Why not a router

The previous implementation put a _semantic arbiter_ at ingest. Every incoming message triggered one fast-model call that read the last six messages plus a short summary of each member and returned the list of agents that should reply. Only those agents got dispatch rows.

Three problems made this untenable, and all three are structural rather than tuning issues.

**A small model was gating large ones.** The arbiter saw six truncated messages and a 180-rune summary per member. The agent it was gating saw the full transcript, its own long-term memory, and its live tool state. The decision was being made by the party with strictly less information.

**It sat on the hot path with a timeout.** The call had an 8-second budget. A timeout had to resolve to something, and both answers were bad: silence loses a reply the user asked for; broadcast is a storm. There is no safe default for a gate that can fail.

**Non-selected agents went blind.** An agent that was not picked never read the message, so its ingest cursor never moved. Its next turn began with a hole in the conversation, and that hole compounded.

The replacement inverts the assumption. Assume every member might have something to say, let each one decide locally with full information, and put the cost control _after_ the decision, where it can be exact instead of predictive.

## The shape of the system

Three tables carry the event and work ledger. Coordination also relies on `ctx_group_state` for the locked caps/nudge state and `ctx_group_ingest_cursor` for each agent's last completed trigger. Nothing load-bearing lives only in process memory, so a restart loses no decision.

| Table                | Role                                                                                                                                                                                                                                                                              |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ctx_group_message`  | The canonical ordered event log. `seq` is the only ordering anyone reads: the client reducer, the freshness check, and the memory cursor all key on it. Carries `delivery_state` (`pending` / `delivered` / `failed`).                                                            |
| `ctx_group_outbox`   | Durable fan-out source. Ingest creates one row for its canonical message; a delivered agent reply gets its peer outbox only in the publisher's successful finalization transaction.                                                                                               |
| `ctx_group_dispatch` | Durable wake ledger. Normal fan-out creates one row for each eligible member, skipping an agent author; a targeted nudge creates exactly one row for its target. Rows carry `kind` (`wake` / `nudge`), `trigger_seq`, `held_up_to_seq`, `publish_started_at`, and `published_at`. |

Caps and nudge bookkeeping live on `ctx_group_state`. All four caps are per-group state, settable through `PATCH /api/groups/{id}` (API only; the Web UI does not expose them): `agent_chain_hard_limit` (default 8, range 1–100), `max_agent_posts_per_minute` (default 10 per agent, range 1–1000), `max_replies_per_human_trigger` (default 5, range 1–100), `hold_limit` (default 3, range 0–20), plus `nudge_at`, `nudge_checked_at`, and `nudge_streak_count`.

One SQL function, `ctx_group_chain_root(group, agent, trigger_seq)`, answers where an agent's current causal chain starts: the later of the most recent human message at or before `trigger_seq` and the agent's own most recent accepted post. The trigger bound is deliberately asymmetric: it applies to the human branch, while the accepted-post branch reads the agent's latest committed result. Its four consumers are the wake claim's held-coverage gate, the HOLD budget count, the wake's `held_up_to_seq` hint, and chain-scoped verbatim dedup. These have different failure modes: relaxing only the claim gate lets a held row re-run and post twice; tightening only the count makes a HOLD that never expires; widening dedup suppresses an ordinary acknowledgement forever. One definition is what stops all four drifting apart.

## The life of a message

Ingest appends one canonical event and creates one outbox row. Draining a normal outbox materializes one wake per eligible member, skipping an agent author — **a member never wakes itself**. A nudge outbox instead materializes exactly its named target; that target exception is why neither the outbox nor dispatch cardinality is simply “one row per member.”

A worker coalesces ordinary wakes to the newest one per `(group, agent)` and marks older pending wakes `status='superseded'`. A targeted nudge is never coalesced this way. Both kinds nevertheless share **one kind-agnostic live slot per `(group, agent)`**: a wake and a nudge cannot run concurrently for the same agent in the same group. This is what makes the model survive a busy group: five messages in a row cost one turn that sees all five, not five turns each seeing a stale prefix. Rows superseded by claim-time coalescing never advance the memory cursor and emit no turn frame, so nothing is marked as read that was not read.

After a worker obtains a queue slot, it re-checks whether a targeted nudge is already moot. An ordinary wake may have posted while the nudge waited; that nudge retires silently rather than spending another turn.

The claimed wake then passes three gates in order.

## Gate one: deterministic triage

`triageWake` decides whether a turn may _run_. It never decides whether an agent _should speak_ — only the agent can answer that. There is no model call here; every rule is either a hard cap or an addressing fact.

Rules are evaluated in order, first match wins:

| #   | Rule                                           | Verdict                   | Why                                                                                                                                                                         |
| --- | ---------------------------------------------- | ------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Chain, rate, or per-human-trigger cap exceeded | `hard_cap` — silent       | The anti-storm floor. No bypass, no exceptions. The rate cap defaults to 10 posts per agent per minute.                                                                     |
| 2   | An unconsumed message mentions this agent      | `mentioned` — act         | Read from the ingest cursor, not only the trigger envelope: coalescing can move a wake past the message that addressed it.                                                  |
| 3   | A nudge names this agent                       | `nudge` — act             | A nudge is never superseded, so a wake already in flight may have posted the very reply the nudge asks for. The post-queue-slot recheck then returns `nudge_moot` — silent. |
| 4   | This wake covers an outstanding previous HOLD  | `held_successor` — act    | The held turn consumed its mention and history; until the cursor covers `held_up_to_seq`, its durable held row admits the required successor.                               |
| 5   | A resolved mention names some other member     | `mentioned_peer` — silent | Somebody else was addressed, unless this wake still owes a held successor turn.                                                                                             |
| 6   | Agent-only run, this agent already spoke       | `agent_lap` — silent      | One lap per participant. A peer that needs this agent to continue can @mention it, which rule 2 admits.                                                                     |
| 7   | Nothing matched                                | `open_floor` — act        | The default is to run. A rule that cannot classify a wake must not silence it.                                                                                              |

Rule 7 is the thesis. Any gate that fails closed hands the decision to whoever wrote the rules; failing open hands it to the agent, which is the party with the most information.

A read error inside rule 2 resolves to "no match" on purpose. A transient database failure then costs the mention its rule, never the agent its turn. The cap reads before rule 1 and the durable HOLD read for rule 4 are different: if any of them fail, triage returns `triage_db_error`, requeues while attempts remain, and retires silently only after the ordinary three-attempt budget is exhausted. The nudge-moot read runs later, inside the session queue; a failure there fails and retries the dispatch rather than risking duplicate work.

## Gate two: the agent's own PASS

The agent has read the whole group, has its memory, and knows what it is working on. If it has nothing to add, it replies with exactly `PASS`, or with nothing at all.

`isModelPass` recognises the reply through the wrapping models add — surrounding whitespace, a code fence, inline backticks, bold markers, a trailing period. Only a _bare_ pass counts: `PASS, but check the logs` is a reply that happens to start with the word, and it gets posted.

The turn is retired as `silent` with reason `model_pass`. No post, no outbox, no cap or hold accounting. But **everything else the turn did still commits**: its trigger anchor, private tool trajectory, and last-completed-trigger cursor.

That last part is load-bearing. An agent may write a file and _then_ decide it has nothing worth saying. Dropping the whole turn would erase its record of a side effect that stays real for every peer. `stripTrailingPass` removes only trailing text-only assistant messages and stops at the first message carrying a tool call, so no `tool_use` is ever separated from its `tool_result`.

A `model_pass` advances the cursor to `trigger_seq`. So does a post-turn backstop: after a `held`, duplicate, or cap verdict, the transaction commits the trigger anchor and private tool trajectory, then records that the agent durably completed that trigger; it discards only the trailing reply that was not accepted. The cursor does **not** mean that public history was copied into the agent's LCM. Because a HOLD has therefore completed its original trigger, the successor is admitted by the covered durable held row (`held_successor`), before peer-mention or lap triage can silence it. Once that successor commits a cursor through `held_up_to_seq`, the old HOLD stops granting admission. A claim-time superseded row or a pre-turn gate decline did not run the model, so neither advances the cursor.

## Gate three: the accept transaction

This is the server-side backstop, and the only place that sees the group under a lock _after_ the agent has finished thinking.

The transaction takes `FOR UPDATE` on `ctx_group_state`, then runs its gates in cost order:

- **Freshness.** While fewer than `hold_limit` holds have occurred in `ctx_group_chain_root`, a peer post after the agent's snapshot makes the reply `held` and it is **never published**. The old row normally remains held with `held_up_to_seq`; a later, separate pending wake whose snapshot covers that sequence is the successor turn. The compensation exception is terminal accepted-delivery failure, which requeues only the rows causally held by that failed post. Once the hold limit is exhausted, freshness no longer stops the stale reply: it continues through dedup and caps and is normally accepted.
- **Verbatim dedup.** An identical reply already in the chain retires as `silent` with reason `duplicate`. It does not consume HOLD budget.
- **Chain and reply caps.** `agent_chain_hard_limit` and `max_replies_per_human_trigger` are re-checked under the lock, not on the snapshot, and retire the reply as `silent` if spent.

If all gates pass, one transaction commits the **pending** agent message, the deferred memory turn and its cursor, and the dispatch result marker. There is deliberately no peer outbox in this transaction: this reply does not itself wake peers until the publisher returns successfully. That publisher finalization transaction marks the message `delivered` and creates its peer outbox together. A pending row is internal publish state, never peer context. The strict atomic guarantee is that an accepted message is never visible without its own agent memory.

Each gate carries its own retirement reason, reported verbatim. A gate that reports a neighbour's reason is indistinguishable from a backstop misfiring, and that is exactly the bug class this design is meant to make debuggable.

## What an agent sees

Each turn's prompt is the system prompt, a common group window, and the triggering message. The window is reverse-paged then restored to chronological order, bounded to 500 rows, 80k estimated tokens, and 4 MiB of rendered text. It includes delivered canonical rows before `trigger_seq`, plus this agent's own `pending` and `failed` rows. A peer sees only delivered rows; the author sees its own pending row marked `(sending)` and failed row marked `(delivery failed — peers never saw this)`, so its local view cannot contradict the delivery ledger. Historical media remains its `[image]` text projection.

A single `grouptranscript` renderer projects every event to exactly one physical line. It labels each line `[seq:N who]`, marks the current agent `(you)`, normalizes text with NFKC while removing default-ignorable code points, and escapes content newlines as literal `\n`. It removes `[` and `]` from participant names, collapses their whitespace, and limits them to 64 characters. When history is omitted, that same renderer emits the trusted `[system]: earlier group history omitted; use memory.search` marker, which a member cannot forge. Marker and private-note tokens are reserved before collection, then the window is collected inside the remaining budget without a second tail trim. Eviction removes whole 10k-token head blocks for prompt-cache stability; a coalesced waking mention is retained even when it falls below the normal floor.

An agent can request older public details with `memory.search`, then `memory.read`. Recall is scoped to this group only and can return only non-empty delivered public text strictly before the current trigger; it never searches private sessions, pending or failed rows, reasoning, or media payloads. Search returns evidence snippets and opaque refs, and read expands a bounded chronological neighborhood around a ref. Returned history is `information_only`, not an instruction.

Each agent LCM still atomically stores its own executed turn, including the public trigger anchor and private assistant/tool trace, for recovery and audit. It never renders that private trace into a later group prompt: the old continuation splice after the trigger, including its special case that skipped the agent's public output, no longer exists. After the last accepted turn, a later `silent` or `held` turn that ran tools instead adds one private note at the end of assembly, such as `[note: you ran tools (tool_name) at seq N without replying]`. The note contains tool names only, never arguments or results, never enters the public record, and disappears after a newer accepted turn.

Agents address other agents by the names in transcript labels only. On every surface, a human can write `@Name` in plain text, using a member's display name or ID, and it resolves the same way as a native platform mention. Participant naming tries the platform identity and account name, but resolution never fails: if that lookup or platform-ID resolution cannot produce a name, the model receives the stable raw actor ID instead.

Each turn is prefixed with a `<wake>` block naming why it is running (`mentioned`, `nudge`, `open_floor`, …). This matters for gate two: an agent woken on the open floor should pass far more readily than one that was named.

Agent templates fill `{{ .AgentName }}` with the name of the agent being created, so a shared persona template does not give every agent built from it the same name.

## Work coordination

There is no lock or lease on shared work. Coordination is the transcript itself: an agent that starts a deliverable says so in the group, and every peer's next turn sees that delivered message. The residual race — two agents starting the same deliverable in the same in-flight instant — costs duplicated work at worst, and the accept transaction's dedup still drops a verbatim duplicate reply.

## Nudges

A group can stall: a human asks something, every agent passes or is gated, and nothing happens. A background worker checks every 60 seconds for groups idle between 5 minutes and 6 hours, and can append a canonical system message plus one targeted nudge wake.

This is the only model call left in the group path, and it is deliberately off the message path: a 5-second fast-model classifier that returns `{"stalled", "target", "reason"}`. It cannot silence anybody — the worst it can do is spend one turn.

It is bounded twice:

- **Candidate work:** each pass reads at most 50 candidates. `nudge_checked_at` is separate from `nudge_at`: a group is reconsidered after new activity or after a 30-minute recheck, not on every tick while nothing has changed.
- **Per group:** one nudge per 45-minute cooldown.
- **Per conversation:** a nudge appends a canonical system message, so the group's latest message is no longer a human ask and candidacy lapses — one nudge per stalled ask until somebody actually speaks. `nudge_streak_count` (max three between two real messages) remains as a defense-in-depth bound; any human or agent message resets it.

When the classifier is unavailable, the tick is skipped: the candidate stays eligible and is re-asked once the classifier recovers. An outage can only delay a nudge, never invent one.

One-shot candidacy is the important bound. Every nudge that remains live after the queue-slot moot check costs a full turn from the agent it names; a group that stays quiet after being asked is not stalled, it is done.

## Delivery

Web `POST /api/groups/{id}/messages` only ingests and wakes workers. It returns `start`, `data-group-ingest`, and `finish`; canonical messages and turn presence arrive on the group event stream.

**Web is a platform whose publisher is a noop, not a bypass.** A web reply runs the same lifecycle as a Telegram one: born `pending`, marked `delivered` in the successful publisher finalization transaction, `failed` when accepted delivery permanently cannot complete. Only that accepted-delivery failure frees peers that were held behind the reply. A normal wake that fails before acceptance has no accepted peer post to compensate. A failed canonical row remains visible only in its author's rendered window, marked `(delivery failed — peers never saw this)`; it never enters a peer's context and is not retained through a private continuation.

The real prebuffer is `bufferGroupResponse`: it drains the runtime's complete event stream, up to its 8 MiB in-memory ceiling, before acceptance or any platform side effect. Publishers receive a closed replay. `ValidateGroupReplay` is a defensive publisher-side recheck of that replay, not the mechanism that buffers a live model stream. No live model stream or model error remains at egress; platform delivery can still fail and is handled by the dispatch retry state machine. This is why platform publishers send one complete message rather than a placeholder they later edit.

A delivered agent message creates its own outbox in that same finalization transaction. That is what makes agent-to-agent collaboration possible on every platform, and the caps are what make it bounded.

## Turn presence

`GET /api/groups/{id}/events` carries `turn` frames alongside canonical `message` frames. A turn frame is live presence for one member, never replayed history.

Claiming a row first makes its database status `running`. A wake or nudge emits a `running` frame only after it owns the per-session queue slot and the chat stream has started; a queued nudge that became moot emits only terminal `silent`. A gate decline likewise emits its terminal `silent` frame from an already-running database row, but never emits a `running` frame. A claimed row that fails before its `running` frame — context load, outbox decode, or publisher lookup — likewise emits a terminal `failed` frame, so a subscriber that saw the row as `running` in a presence snapshot is still retired. Rows marked `status='superseded'` by claim-time wake coalescing emit no turn frame at all. Separately, a claimed row whose trigger was already consumed across a session boundary retires with a terminal `silent` frame whose reason is also `superseded`; this is a verdict reason, not the database superseded status. Once a subscriber has observed `running`, either as a live frame or a presence snapshot, exactly one terminal frame retires it: `done` once the accepted reply has been delivered, `held` when freshness stopped it, `silent` when the model or a later backstop declined, `failed` otherwise.

Egress compensation — republishing an accepted reply after a restart — skips `running`, because no model is taking that turn, and still emits `done` on delivery.

Because turn frames are not replayed, a fresh subscriber is handed a snapshot: one synthetic `running` frame per distinct agent with an executing dispatch at connect time. A crashed worker leaves its row `running` until the 5-minute lease expires, so a snapshot can show a stale `running` for that long. The reaper then retires that attempt with a terminal `failed` frame before requeueing or failing it, and every reconnect re-snapshots, so clients self-heal rather than needing a repair path.

## Failure and recovery

An ordinary wake has at most 3 attempts: the initial attempt plus 2 retries, with a 5-minute lease per attempt.

`publish_started_at` and `published_at` are separate columns because "publish never ran" and "publish ran and we never saw its outcome" are different states after a crash. Only the second is ambiguous, and its accepted reply may be recovered up to a ceiling of 10 attempts. That path deliberately chooses a possible duplicate over a lost reply. Once `published_at` is durably set, platform delivery is known; local delivered/outbox finalization is idempotent and keeps retrying rather than falsely marking a delivered message failed.

Giving up on a row that already carries an accepted reply is not the same as giving up on a wake. The message is committed and visible to peers, so terminal accepted-delivery failure marks it `failed` and releases peers held behind it. A failed ordinary wake does neither.

## Invariants

- Canonical `seq` order is the client reducer key.
- A member never wakes itself; normal fan-out skips an agent author and a nudge targets one named member.
- At most one live dispatch, wake or nudge, runs for an agent and group.
- Superseded rows and pre-turn gate declines do not advance the memory cursor. Model passes and post-turn backstops atomically commit the trigger anchor, private tool work, and `last_completed_trigger_seq`, without retaining a rejected trailing reply or copying public peers into LCM. That private tool work is durable only; later group prompts render canonical rows and, at most, one private tool-name note.
- Server acceptance, not model judgement, enforces freshness and caps.
- A `running` turn frame is always followed by exactly one terminal turn frame; a gate decline and a superseded row never produce that `running` frame.
- No model decides whether another model may speak.
