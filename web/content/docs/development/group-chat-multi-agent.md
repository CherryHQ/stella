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

Four tables carry the whole model. Nothing about group coordination lives in memory or in a process, so a restart loses no decision.

| Table                | Role                                                                                                                                                                                                                   |
| -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ctx_group_message`  | The canonical ordered event log. `seq` is the only ordering anyone reads: the client reducer, the freshness check, and the memory cursor all key on it. Carries `delivery_state` (`pending` / `delivered` / `failed`). |
| `ctx_group_outbox`   | Durable fan-out. One row per canonical message; the worker that drains it materializes the wakes.                                                                                                                      |
| `ctx_group_dispatch` | One row per member per message — the _wake_. Carries `kind` (`wake` / `nudge`), `trigger_seq`, `held_up_to_seq`, `publish_started_at`, `published_at`.                                                                 |
| `ctx_group_claim`    | Durable work claims. One live owner per `(group, key)`, with a lease so a crashed owner cannot strand the work.                                                                                                        |

Caps and nudge bookkeeping live on `ctx_group_state`: `agent_chain_hard_limit` (8), `max_agent_posts_per_minute` (10), `max_replies_per_human_trigger` (5), `hold_limit` (3), plus `nudge_at`, `nudge_checked_at`, and `nudge_streak_count`.

One SQL function, `ctx_group_chain_root(group, agent, trigger_seq)`, answers where an agent's current causal chain starts: the later of the most recent human message and the agent's own most recent accepted post. Two callers read it — the wake claim gate and the HOLD count — and they fail in opposite directions. Relaxing only the gate lets a held row re-run and post twice; tightening only the count makes a HOLD that never expires. One definition is what stops that pair drifting apart.

## The life of a message

Ingest appends one canonical event and creates one outbox row. Draining the outbox creates one wake per member, skipping the author — **a member never wakes itself**.

A worker claims **only the newest wake per (group, agent)**; older pending wakes are marked superseded. This is what makes the model survive a busy group: five messages in a row cost one turn that sees all five, not five turns each seeing a stale prefix. Superseded rows never advance the memory cursor, so nothing is marked as read that was not read.

The claimed wake then passes three gates in order.

## Gate one: deterministic triage

`triageWake` decides whether a turn may _run_. It never decides whether an agent _should speak_ — only the agent can answer that. There is no model call here; every rule is either a hard cap or an addressing fact.

Rules are evaluated in order, first match wins:

| #   | Rule                                                    | Verdict                   | Why                                                                                                                                                          |
| --- | ------------------------------------------------------- | ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | Chain, rate, or per-human-trigger cap exceeded          | `hard_cap` — silent       | The anti-storm floor. No bypass, no exceptions.                                                                                                              |
| 2   | An unconsumed message mentions this agent               | `mentioned` — act         | Read from the ingest cursor, not the trigger envelope: coalescing and HOLD can move a wake past the message that addressed it.                               |
| 3   | A nudge names this agent, and it has not posted since   | `nudge` — act             | A nudge is never superseded, so a wake already in flight may have posted the very reply the nudge asks for. If it has, the verdict is `nudge_moot` — silent. |
| 4   | A resolved mention names some other member              | `mentioned_peer` — silent | Somebody else was addressed.                                                                                                                                 |
| 5   | Agent-only run, this agent already spoke, no live claim | `agent_lap` — silent      | One lap per participant. A live claim means work is in flight, so the run is not idle chatter and the floor stays open.                                      |
| 6   | Nothing matched                                         | `open_floor` — act        | The default is to run. A rule that cannot classify a wake must not silence it.                                                                               |

Rule 6 is the thesis. Any gate that fails closed hands the decision to whoever wrote the rules; failing open hands it to the agent, which is the party with the most information.

A read error inside rules 2 or 3 resolves to "no match" on purpose. A transient database failure then costs the mention its rule, never the agent its turn.

## Gate two: the agent's own PASS

The agent has read the whole group, has its memory, and knows what it is working on. If it has nothing to add, it replies with exactly `PASS`, or with nothing at all.

`isModelPass` recognises the reply through the wrapping models add — surrounding whitespace, a code fence, inline backticks, bold markers, a trailing period. Only a _bare_ pass counts: `PASS, but check the logs` is a reply that happens to start with the word, and it gets posted.

The turn is retired as `silent` with reason `model_pass`. No post, no outbox, no cap or hold accounting. But **everything else the turn did still commits**: the peer rows it read, its ingest cursor, and any tool calls it made.

That last part is load-bearing. An agent may claim a piece of work, write a file, and _then_ decide it has nothing worth saying. Dropping the whole turn would make it forget the claim it holds while the side effect stays real for every peer. `stripTrailingPass` removes only trailing text-only assistant messages and stops at the first message carrying a tool call, so no `tool_use` is ever separated from its `tool_result`.

A `model_pass` advances the ingest cursor. The agent read those messages; a gate-silenced or held turn did not.

## Gate three: the accept transaction

This is the server-side backstop, and the only place that sees the group under a lock _after_ the agent has finished thinking.

The transaction takes `FOR UPDATE` on `ctx_group_state`, then runs its gates in cost order:

- **Freshness.** If a peer posted after the agent's snapshot, the reply is stale. It becomes `held` and is **never published**. The row records `held_up_to_seq`, and the claim gate refuses any later wake that does not cover it — so the successor turn is guaranteed to see what caused the hold. Holds are bounded by `hold_limit` within `ctx_group_chain_root`, so a slow agent is not starved forever.
- **Verbatim dedup.** An identical reply already in the chain is dropped.
- **Reply cap.** `max_replies_per_human_trigger` is re-checked under the lock, not on the snapshot.

If every gate passes, one transaction commits the agent message, the memory turn, the ingest cursor, and the successor outbox together. There is no window in which a message is visible but its memory is not.

Each gate carries its own retirement reason, reported verbatim. A gate that reports a neighbour's reason is indistinguishable from a backstop misfiring, and that is exactly the bug class this design is meant to make debuggable.

## What an agent sees

Every message an agent reads is labelled `[seq:N who]` with the participant's group name — including the message that woke the turn. Agents address each other by those names only; **platform user ids never reach a model.**

Each turn is prefixed with a `<wake>` block naming why it is running (`mentioned`, `nudge`, `open_floor`, …). This matters for gate two: an agent woken on the open floor should pass far more readily than one that was named.

Agent templates fill `{{ .AgentName }}` with the name of the agent being created, so a shared persona template does not give every agent built from it the same name.

## Work claims

`ctx_group_claim` is a conditional-upsert lease. An agent claims a concrete shared deliverable by key; a peer that tries the same key is told who holds it and until when. TTL is clamped to 1 minute – 24 hours, default 10 minutes, and an expired lease can be taken over.

Claims are surfaced twice: in the group prompt, so peers can see what is already owned, and in triage rule 5, where a live claim keeps the floor open during an agent-only run.

Claims are for deliverables, never for ordinary chat replies. A claim on "answering this question" would turn the collaboration model back into a lock.

## Nudges

A group can stall: a human asks something, every agent passes or is gated, and nothing happens. A background worker checks every 60 seconds for groups idle between 5 minutes and 6 hours, and can append a canonical system message plus one targeted nudge wake.

This is the only model call left in the group path, and it is deliberately off the message path: a 5-second fast-model classifier that returns `{"stalled", "target", "reason"}`. It cannot silence anybody — the worst it can do is spend one turn.

It is bounded twice:

- **Per group:** one nudge per 45-minute cooldown (5 minutes for the deterministic claim-based fallback), with `nudge_checked_at` separate from `nudge_at` so an idle group is not re-asked every tick when the answer cannot have changed.
- **Per conversation:** at most three consecutive nudges between two real messages (`nudge_streak_count`). Any human or agent message resets it.

The second bound is the important one. Every nudge costs a full turn from the agent it names; a group that stays quiet after being asked three times is not stalled, it is done.

## Delivery

Web `POST /api/groups/{id}/messages` only ingests and wakes workers. It returns `start`, `data-group-ingest`, and `finish`; canonical messages and turn presence arrive on the group event stream.

**Web is a platform whose publisher is a noop, not a bypass.** A web reply runs the same lifecycle as a Telegram one: born `pending`, marked `delivered` when the publisher returns, `failed` — with peers held behind it requeued — when it permanently cannot. Collapsing these two paths removed an entire class of bug that could only appear on one of them.

Publishers deliver only after acceptance, and they receive the turn already buffered: `ValidateGroupReplay` drains the whole reply stream before any publisher sees it, so a publisher's input is a closed, complete channel. There is nothing to stream at egress and no error left to surface, which is why platform publishers send one complete message rather than a placeholder they later edit.

An accepted agent message creates its own outbox. That is what makes agent-to-agent collaboration possible, and the caps are what make it bounded.

## Turn presence

`GET /api/groups/{id}/events` carries `turn` frames alongside canonical `message` frames. A turn frame is live presence for one member, never replayed history.

A wake that passes gate one emits `running` before the model starts. Exactly one terminal frame retires it: `done` once the accepted reply has been published, `held` when the reply was stale, `silent` when a gate or the model declined, `failed` otherwise. **Every path below a `running` frame owes a terminal frame** — including the two that used to return quietly (superseded trigger, chat start failure), or a badge stays lit until the next reconnect.

Egress compensation — republishing an accepted reply after a restart — skips `running`, because no model is taking that turn, and still emits `done` on delivery.

Because turn frames are not replayed, a fresh subscriber is handed a snapshot: one synthetic `running` frame per dispatch row executing at connect time. A crashed worker leaves its row `running` until the 5-minute lease expires, so a snapshot can show a stale `running` for that long. The reaper's requeue then emits fresh frames, and every reconnect re-snapshots, so clients self-heal rather than needing a repair path.

## Failure and recovery

Work is retried up to 3 attempts with a 5-minute lease per attempt.

`publish_started_at` and `published_at` are separate columns because "publish never ran" and "publish ran and we never saw its outcome" are different states after a crash. Only the second is ambiguous, and the recovery path there deliberately chooses a possible duplicate over a lost reply.

Giving up on a row that already carries an accepted reply is not the same as giving up on a wake. The message is committed and visible to peers, so it must be marked undelivered and the peers it held must be released — otherwise it stays `pending` forever and holds them with it.

## Invariants

- Canonical `seq` order is the client reducer key.
- A member never wakes itself.
- At most one live wake runs for an agent and group.
- Held and superseded wakes, and wakes silenced before the turn ran, do not advance the memory cursor. A `model_pass` does: the agent read those messages.
- Server acceptance, not model judgement, enforces freshness and caps.
- A `running` turn frame is always followed by exactly one terminal turn frame.
- No model decides whether another model may speak.
