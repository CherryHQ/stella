# Channel durable path (multi-replica)

Status: implemented behind `STELLA_CHANNEL_DURABLE_INGRESS` (default off; the
legacy in-process pipeline still runs when the flag is unset).

## Goal

Decouple channel ingress, agent execution, and outbound delivery so any
replica can receive, route, execute, or send — with PostgreSQL as the only
coordination medium.

## Pipeline

```
adapter -> channel_inbox --route--> agent_run --worker--> channel_outbox --dispatch--> platform
              |                     |                        ^
              |                     +- ctx_session_execution -+ (reply ops inside the finish tx)
              +- dedup by (channel, event key)                |
                                                 ctx_session_event <--- every turn event
```

- `channel_inbox`: every inbound event is persisted before the adapter sees
  an acknowledgement. Routing is a short transaction that resolves actor,
  agent, session, and binding purely from persisted values, so it needs no
  local `agent.Service`.
- `agent_run`: a run is claimed by whichever replica's worker gets there
  first; claim links the run to a `ctx_session_execution` lease atomically,
  and the finish transaction commits result, history, and reply ops together.
- `channel_outbox`: one row per platform operation (`send_text`, …) with a
  stable `delivery_key`, ordered dependencies, and classified outcomes —
  `sent` / `failed` / `unknown`. A transport failure after a possibly-landed
  request is never blind-retried.
- `channel` runtime lease: only the lease holder starts the adapter and
  dispatches its outbox; tokens fence every state mutation.
- `ctx_session_event`: every published turn event with a session-scoped
  contiguous seq. SSE attach replays/tails this log when the turn runs on
  another replica, honoring `Last-Event-ID` as the resume cursor; a cursor
  behind the retained window answers 409 so the client rebuilds from the
  transcript.

## Platform notes

- Telegram/Discord: `send_text` returns a platform message id.
- Feishu: durable ops send the card form; the op's idempotency key is passed
  as Feishu's create `uuid`, so a replayed attempt cannot duplicate.
- QQ: `Address.Scope` picks C2C vs group endpoints.
- Weixin: `Address.Token` carries `context_token`; `client_id` is derived
  deterministically per op for platform-side dedup.
- DingTalk: the only outbound credential is the expiring session webhook —
  carried on `Address.Token`; an absent or expired one classifies permanent.

## Ops

The janitor sweep in `RunDurableLoops` expires stale outbox attempts to
`unknown` for probe/operator requeue, prunes `ctx_session_event` past
retention, and the run reaper interrupts runs whose execution lease died.

## Known limits

- Web sends still execute synchronously on the receiving replica (the
  durable observe path works; enqueue-then-observe is future work).
- Group chat and scheduler/goal/notify senders still use the legacy publish
  path.
- DingTalk replies cannot be sent after the session webhook expires.
- No backlog metrics yet.
