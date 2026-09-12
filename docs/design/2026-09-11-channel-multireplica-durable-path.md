# Channel durable path (multi-replica)

Status: the durable path is the only channel pipeline; there is no legacy
in-process fallback.

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
- `channel_outbox`: one row per platform operation (`send_text`,
  `draft_update`, `send_attachment`, …) with a stable `delivery_key`,
  ordered dependencies, and classified outcomes — `sent` / `failed` /
  `unknown`. A transport failure after a possibly-landed request is never
  blind-retried. A completed turn decomposes into one row per external
  call — `send_reply` carries only the primary segment (or the draft
  finalize), overflow text rides `send_text` siblings, and every image or
  file is its own `send_attachment` — so a mid-chain retry never replays
  a segment that already landed.
- `channel` runtime lease: only the lease holder starts the adapter and
  dispatches its outbox; tokens fence every state mutation. The lease is
  re-admitted before every claimed op, and multi-call ops carry a claim-
  time `Guard` the adapter invokes before each SDK call after the first —
  a fenced-out owner stops mid-operation rather than finishing a
  fallback, overflow chunk, or upload+send pair.
- `ctx_session_event`: every published turn event with a session-scoped
  contiguous seq. SSE attach replays/tails this log when the turn runs on
  another replica, honoring `Last-Event-ID` as the resume cursor; a cursor
  behind the retained window answers 409 so the client rebuilds from the
  transcript.

## Live progress

The channel lease holder tails `ctx_session_event` for runs whose reply
address lands on its channel and folds committed events into a
`draft_update` op per run (`delivery_key = live:<run-id>`). Pending drafts
for the same run coalesce in place; once one is `sending`/`sent`, later
snapshots queue behind it, so the platform sees one message edited in
place. `LatestSentDraftMessageID` lets a new owner recover the platform
message id after handoff, and a seq fence cancels drafts that can no
longer add anything. `ListPendingChannelOutbox` holds a run's terminal
ops behind any in-flight draft for the same run, so a stale edit can
never overwrite the final reply. Draft dependencies resolve on any
terminal predecessor state — a draft snapshot is self-contained, so
`failed`/`canceled`/`unknown` no longer block the successor, and the
claim-time run-liveness + seq fences cancel the released draft instead of
letting it park the terminal barrier forever. Non-draft ops keep the
strict `sent` dependency rule, and a chain broken by a `failed` or
`canceled` link is canceled by `CancelBlockedChannelOutbox` rather than
parked.

`unknown` is _not_ treated as proof the attempt died — a delayed platform
apply or an owner paused between its lease check and the SDK call can
still land the edit after the row went unknown. Instead, a draft that may
have edited a live message _pollutes_ that message identity:
`LatestSentDraftMessageID` returns empty once an `unknown` draft sits on
top of a sent identity, so every later draft and the terminal reply take
a fresh message instead of editing the contaminated one. The abandoned
preview may linger in the chat or receive the late edit; the final reply
is a separate message that the zombie can never overwrite.

## Platform notes

- Draft-capable adapters implement `channel.DraftSender`: Telegram edits
  via `Bot.Edit` (treating "message is not modified" as success), Discord
  reuses its stream/edit machinery and clears components on finalize,
  Feishu patches the reply card, QQ posts Stream API chunks with a durable
  `streamID:index` cursor (the terminal `send_reply` closes the stream
  with `state=10`), and testchan posts `message_id` edits to the fake
  platform. Adapters without it (Weixin, DingTalk) drop drafts and send
  only the terminal reply.
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
`STELLA_RUN_WORKER=off` pins a replica out of run claiming (observe/send
only). Graceful drain joins the worker `Run` loop so a committed claim is
executed and finished inside the drain budget before teardown.

## Known limits

- Group chat still publishes through the group dispatcher rather than
  per-run drafts, but `send_group_reply` decomposes like `send_reply`:
  the primary op carries the group metadata and first text segment,
  overflow rides `send_text` siblings, and every attachment is its own
  `send_attachment` (QQ folds media into text markers — no binary group
  upload). `pollPublishOutcomes` waits for the whole chain and treats a
  `canceled` link as a terminal failure so a broken chain cannot park
  the dispatch. Notifications decompose the same way via `NotifyChain` —
  one op per segment, one platform call each.
- Group replies fence on the responding channel's platform account, not the
  observing one: a shared trigger can wake members that reply through other
  channels. Each channel's lease owner registers its adapter's account
  identity on `channel.runtime_account_key` (fenced by the lease token,
  never cleared — a stale last-known key is the safe direction). At
  dispatch-accept `enqueueAccepted` snapshots the reply channel's
  `runtime_account_key` into every op of the chain; send-time account checks
  then compare against that frozen value, so re-binding the channel to a
  different account fails old ops `account_mismatch` while credential
  rotation under the same identity still owns them. The observing account is
  kept on `ctx_group_message.source_account_key` as audit data only — like
  `source_channel_id`. Ops whose reply channel has no registered account
  (agent/system-origin messages, notifications) carry an empty key and are
  deliberately unfenced — the channel's current account is the right sender.
- Draft updates are sent only by adapters implementing `DraftSender`;
  Weixin/DingTalk show the final reply only.
- Real-platform credential paths (Telegram/Discord/Feishu/QQ/Weixin/
  DingTalk) are exercised only by their adapters' unit surfaces plus
  testchan end-to-end; no live-platform soak yet.
- No backlog metrics yet.
