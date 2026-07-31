---
title: Webhook
---

The webhook channel turns an agent into an HTTP endpoint you can trigger from any script, cron job, or third-party service. It is **inbound-only**: callers POST a payload, the bound agent runs, and you get either a fire-and-forget acknowledgement or the agent's reply in the HTTP response. There is no bot, no chat window, and no way for the agent to message you back out of band -- it is built for automation, not conversation.

Access is a **capability**: an admin activates a single endpoint for the channel, and Stella discloses a one-time URL that contains an opaque secret. Holding that URL is the entire credential -- there is no separate token or `Authorization` header, and the URL fixes exactly which user the agent runs as.

## Prerequisites

Before you start, make sure you have:

- A running Stella server (`stellad server`)
- At least one AI provider configured in the Web UI (e.g. Anthropic, OpenAI)
- An enabled agent you want the webhook to run

## How it works

1. An admin creates a webhook channel in the Web UI and binds it to **an agent**.
2. The admin **activates a capability endpoint** for the channel, choosing the **owner** -- the user the agent runs as on every trigger.
3. Stella discloses a one-time URL, `https://your-host/webhooks/<capability>`, exactly once. Copy it then; it is never shown again.
4. A caller sends `POST` to that URL. The URL is the credential -- no `Authorization` header is used, and any header sent is ignored.
5. The request body becomes the agent's message. The agent always runs **as the endpoint's fixed owner**, with that owner's tools, memory, and permissions, re-checked at every trigger.
6. Depending on the reply mode, the caller gets an immediate `202 Accepted` or waits for the agent's reply.

The owner's permission to run the bound agent is re-verified on every request. If the owner loses access (assignment removed, account deactivated, agent or channel disabled), later triggers fail closed with an opaque `404` -- the capability keeps working only while the fixed identity remains valid.

## Setup

### 1. Create the webhook channel

Creating channels is an admin action.

1. Go to the **Channels** page and add a new channel.
2. Choose **Webhook** as the platform and give it a channel ID (for example `deploy-notify`).
3. Pick the **bound agent** -- the agent that runs on every trigger.
4. Choose a **session mode** and whether to **wait for the reply by default** (see below), then save.

### 2. Activate the capability endpoint

1. Open the channel and find the **Capability endpoint** panel.
2. Click **Activate endpoint** and choose the **owner** -- the user the agent runs as. The owner must currently be allowed to run the bound agent.
3. Copy the **one-time URL** shown in the confirmation. It is displayed **once** and cannot be recovered; if you lose it, rotate the endpoint for a new one.

The endpoint's owner is fixed once activated. To change the owner, revoke the endpoint and activate a new one.

### 3. Trigger it

```bash
curl -X POST https://your-host/webhooks/stella_whk_your_one_time_capability \
  -H "Content-Type: text/plain" \
  --data 'Deployment v1.4.2 finished. Summarize the changelog and post it to #releases.'
```

The whole request body is passed to the agent as its message. Send plain text, JSON, or anything else -- the agent receives it verbatim and decides what to do with it. No `Authorization` header is required or read: the URL itself is the secret, so treat it like a password.

### Rotate or revoke

- **Rotate** issues a fresh one-time URL and immediately invalidates the previous one. Use it if a URL may have leaked, or to recover a URL you did not copy.
- **Revoke** deletes the endpoint; every URL for it stops working at once. The channel remains, and you can activate a new endpoint later.

## Reply modes

Each trigger is either asynchronous (fire-and-forget) or synchronous (wait for the reply). The channel's **Wait for reply by default** setting decides the default; a caller can override it per request with the `?wait=` query parameter.

| Mode         | How to select                 | Response                                                   |
| ------------ | ----------------------------- | ---------------------------------------------------------- |
| Asynchronous | default off, or `?wait=false` | `202 Accepted` immediately, with `{ "session_id": "..." }` |
| Synchronous  | default on, or `?wait=true`   | `200 OK` with `{ "session_id": "...", "output": "..." }`   |

Force synchronous for one call:

```bash
curl -X POST 'https://your-host/webhooks/stella_whk_your_one_time_capability?wait=true' \
  --data 'What is 2 + 2?'
```

In synchronous mode the caller waits up to the channel's **wait timeout** (60 seconds by default, configurable per channel up to 10 minutes) for the reply. If the agent is still working when the timeout hits, you get `504 Gateway Timeout` -- but the run keeps going in the background, and the response includes the `session_id` so you can inspect the result in the Web UI later.

Any value other than `true`/`false` (or `1`/`0`) for `?wait=` is rejected with `400 Bad Request`.

## Session modes

- **Ephemeral** (default): every trigger starts a fresh session with no memory of previous calls. Best for stateless automation -- each payload is handled on its own.
- **Persistent**: the endpoint's owner keeps one long-lived session per webhook, so the agent accumulates context across calls. Best when later triggers should build on earlier ones.

> **Persistent sessions and concurrency:** a persistent session runs one trigger at a time. If a trigger arrives while a previous run is still in flight, it is rejected with `429 Too Many Requests` (in both reply modes) -- wait for the in-flight run to finish and retry. Ephemeral mode is not affected: every trigger gets its own session.

> **Persistent sessions:** persistent mode uses one long-lived agent runner. Stella supports one server replica; the Helm chart enforces that topology.

## Response codes

| Code                    | Meaning                                                                          |
| ----------------------- | -------------------------------------------------------------------------------- |
| `200 OK`                | Synchronous run finished; body carries `output`                                  |
| `202 Accepted`          | Asynchronous run started; body carries `session_id`                              |
| `400 Bad Request`       | Empty body, or invalid `?wait=` value                                            |
| `404 Not Found`         | Unknown, rotated, or revoked capability, or the fixed owner may no longer run it |
| `408 Request Timeout`   | The request body was not fully received before the read deadline                 |
| `413 Payload Too Large` | Body exceeds 256 KiB                                                             |
| `429 Too Many Requests` | Rate limit, ingress capacity, or the persistent session is busy with another run |
| `500 Internal Error`    | Invalid webhook config, or session creation failed                               |
| `502 Bad Gateway`       | The agent run failed                                                             |
| `503 Unavailable`       | The bound agent's runtime is not available                                       |
| `504 Gateway Timeout`   | Synchronous wait timed out; the run continues in the background                  |

An invalid, rotated, or revoked capability always returns an opaque `404` -- Stella never reveals whether a capability once existed or why it was rejected.

## Limits

- **Payload size:** up to 256 KiB per request; larger bodies return `413` and never start a run.
- **Read deadline:** the request body must arrive promptly; a stalled upload returns `408` without starting a run.
- **Rate limit:** each endpoint allows a short burst and then a steady trickle; sustained flooding returns `429`.
- **Ingress concurrency:** each endpoint bounds how many requests may be reading a body and being admitted at once; excess requests return `429` immediately, before consuming any run capacity.
- **Run concurrency:** at most 10 runs of one endpoint may be in flight at once; extra triggers return `429` until a run finishes.
- **Session history:** in ephemeral mode every trigger creates a new session, so a high-volume webhook accumulates session records over time. Prune old sessions from the Web UI if the list grows noisy.

## Troubleshooting

**Getting `404 Not Found`?**

- The capability URL is unknown, rotated, or revoked. Copy the current URL from the channel's **Capability endpoint** panel, or rotate to issue a new one.
- The endpoint's owner may no longer be allowed to run the bound agent, or the owner, agent, or channel is disabled. Restore the owner's access, or enable the agent/channel.
- An old `/webhooks/<channel-id>` URL from a previous release is no longer valid; use the capability URL.

**Getting `413 Payload Too Large`?**

- The body exceeds 256 KiB. Send a smaller payload or a reference the agent can fetch.

**Getting `408 Request Timeout`?**

- The body did not finish uploading in time. Retry over a faster link or send a smaller payload.

**Getting `429 Too Many Requests`?**

- You hit the rate limit, ingress concurrency, or (in persistent mode) a busy session. Slow the trigger rate or wait for the in-flight run to finish.

**Synchronous call returns `504`?**

- The agent took longer than the wait timeout. Use asynchronous mode (`?wait=false`) for long-running tasks, and check the run in the Web UI using the returned `session_id`.
