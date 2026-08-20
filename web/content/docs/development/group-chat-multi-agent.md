---
title: Group chat with multiple agents
---

> This developer reference explains Stella group collaboration. For channel setup, see the channel guides.

Stella records every group message in one ordered event log. Ingest creates a durable outbox, which fans that event out into one **wake** per member except the author. Workers claim only each agent's newest wake, so busy groups coalesce stale snapshots rather than queueing a turn per message.

## The wake gate and acceptance

Every message an agent reads is labelled `[seq:N who]` with the participant's group name, including the message that woke the turn, and each turn is prefixed with a `<wake>` block naming why it is running. Agents address each other by those names only; platform user ids never reach a model.

A wake is local work, not a command to speak. Before a turn runs, the server applies only deterministic rules: chain, rate, and per-human-trigger caps first, with no bypass; then explicit member mentions and targeted nudges act (a nudge whose target has already spoken since it was raised is silent), a mention of another member is silent, and an agent that already spoke in the current agent-only run stays silent unless a live work claim shows the run is not idle chatter. Any wake no rule addresses runs the turn. No model decides whether another model may speak.

A nudge is bounded twice: one per group per cooldown, and at most three between two real messages. Any human or agent message resets that streak.

The rules are the first gate; the agent itself is the second. An agent that has read the group and has nothing to add replies with exactly `PASS` (or nothing at all). The turn is retired as `silent` with reason `model_pass`: no post, no outbox, no cap or hold accounting, but everything else the turn did still commits: the peer rows it read, its ingest cursor, and any tool calls it made, so an agent that claims work and then passes still remembers the claim it holds.

A generated reply is accepted atomically. The transaction locks group state, checks the snapshot is still fresh and caps have not been consumed, then commits the agent message, memory turn, cursor, and successor outbox together. A stale reply becomes `held` and is never published. The next wake must cover the held sequence before it can run.

## Delivery

Web `POST /api/groups/{id}/messages` only ingests and wakes workers. It returns `start`, `data-group-ingest`, and `finish`; canonical messages and turn presence arrive on the group event stream. Publishers deliver only after acceptance. Web is a platform whose publisher is a noop, so a web reply runs the same lifecycle as a platform one: born `pending`, marked `delivered` when the publisher returns, `failed` (with held peers requeued) when it permanently cannot. An accepted agent message creates its own outbox, enabling bounded agent-to-agent collaboration.

## Turn presence

`GET /api/groups/{id}/events` carries `turn` frames alongside canonical `message` frames. A turn frame is live presence for one member, never replayed history.

A wake that passes the gate emits `running` before the model starts, and exactly one terminal frame retires it: `done` once the accepted reply has been published, `held` when the reply was stale, `silent` when the gate or the model declined to speak, `failed` otherwise. Egress compensation (republishing an accepted reply after a restart) skips `running` — no model is taking that turn — and still emits `done` on delivery.

Because turn frames are not replayed, a fresh subscriber is handed a snapshot: one synthetic `running` frame per dispatch row that is executing at connect time. A worker that crashes leaves its row `running` until the lease expires (5 min), so a snapshot can show a stale `running` for that long; the reaper's requeue then emits fresh frames, and every reconnect re-snapshots, so clients self-heal rather than needing a repair path.

## Invariants

- Canonical `seq` order is the client reducer key.
- A member never wakes itself.
- At most one live wake runs for an agent and group.
- Held and superseded wakes, and wakes silenced before the turn ran, do not advance the memory cursor. A `model_pass` does: the agent read those messages.
- Server acceptance, not model judgement, enforces freshness and caps.
- A `running` turn frame is always followed by exactly one terminal turn frame.
