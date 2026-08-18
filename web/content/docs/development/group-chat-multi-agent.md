---
title: Group chat with multiple agents
---

> This developer reference explains Stella group collaboration. For channel setup, see the channel guides.

Stella records every group message in one ordered event log. Ingest creates a durable outbox, which fans that event out into one **wake** per member except the author. Workers claim only each agent's newest wake, so busy groups coalesce stale snapshots rather than queueing a turn per message.

## Local triage and acceptance

A wake is local work, not a command to speak. The server first applies chain, rate, and per-human-trigger caps. Explicit member mentions and targeted nudges act; a mention of another member is silent. A sole human-to-agent Web group may act. Other unaddressed wakes remain silent unless a future group policy supplies a local reason to act.

A generated reply is accepted atomically. The transaction locks group state, checks the snapshot is still fresh and caps have not been consumed, then commits the agent message, memory turn, cursor, and successor outbox together. A stale reply becomes `held` and is never published. The next wake must cover the held sequence before it can run.

## Delivery

Web `POST /api/groups/{id}/messages` only ingests and wakes workers. It returns `start`, `data-group-ingest`, and `finish`; canonical messages and turn presence arrive on the group event stream. Platform publishers deliver only after acceptance. An accepted agent message creates its own outbox, enabling bounded agent-to-agent collaboration.

## Invariants

- Canonical `seq` order is the client reducer key.
- A member never wakes itself.
- At most one live wake runs for an agent and group.
- Silent, held, and superseded wakes do not advance the memory cursor.
- Server acceptance, not model judgement, enforces freshness and caps.
