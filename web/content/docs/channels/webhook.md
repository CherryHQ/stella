---
title: Webhook
---

The webhook channel turns an agent into an HTTP endpoint you can trigger from any script, cron job, or third-party service. It is **inbound-only**: callers POST a payload, the bound agent runs, and you get either a fire-and-forget acknowledgement or the agent's reply in the HTTP response. There is no bot, no chat window, and no way for the agent to message you back out of band -- it is built for automation, not conversation.

## Prerequisites

Before you start, make sure you have:

- A running Stella server (`stellad server`)
- At least one AI provider configured in the Web UI (e.g. Anthropic, OpenAI)
- An enabled agent you want the webhook to run

## How it works

1. An admin creates a webhook channel in the Web UI and binds it to **an agent**. It is enabled the moment it's created.
2. Stella gives you an ingress URL: `https://your-host/webhooks/<channel-id>`.
3. A caller sends `POST` to that URL with a personal access token (PAT). The token's user must be allowed to run the bound agent.
4. The request body becomes the agent's message. The agent runs **as the calling user** — with that user's tools, memory, and permissions. Different callers hitting the same URL each run as themselves.
5. Depending on the reply mode, the caller gets an immediate `202 Accepted` or waits for the agent's reply.

## Setup

### 1. Create a personal access token

The caller authenticates with a personal access token (PAT), the same token type the HTTP API uses.

1. Open the Web UI at `http://localhost:25678`.
2. Go to **Settings → Personal Access Tokens**.
3. Create a token, select the **`agent:write`** scope, and copy it. You only see the token once.

The token's user must be allowed to run the agent the webhook is bound to. Treat the token like a password.

### 2. Create the webhook channel

Creating channels is an admin action.

1. Go to the **Channels** page and add a new channel.
2. Choose **Webhook** as the platform and give it a channel ID (for example `deploy-notify`).
3. Pick the **bound agent** -- the agent that runs on every trigger.
4. Choose a **session mode** and whether to **wait for the reply by default** (see below), then save.
5. Open the channel again to copy its **ingress URL**.

Unlike chat bots, the webhook channel needs no server restart and no separate enable step -- it is live as soon as you save it. Anyone with a valid PAT whose user can run the bound agent may call the URL; each caller runs as themselves.

### 3. Trigger it

```bash
curl -X POST https://your-host/webhooks/deploy-notify \
  -H "Authorization: Bearer stella_pat_your_token_here" \
  -H "Content-Type: text/plain" \
  --data 'Deployment v1.4.2 finished. Summarize the changelog and post it to #releases.'
```

The whole request body is passed to the agent as its message. Send plain text, JSON, or anything else -- the agent receives it verbatim and decides what to do with it.

## Reply modes

Each trigger is either asynchronous (fire-and-forget) or synchronous (wait for the reply). The channel's **Wait for reply by default** setting decides the default; a caller can override it per request with the `?wait=` query parameter.

| Mode         | How to select                 | Response                                                   |
| ------------ | ----------------------------- | ---------------------------------------------------------- |
| Asynchronous | default off, or `?wait=false` | `202 Accepted` immediately, with `{ "session_id": "..." }` |
| Synchronous  | default on, or `?wait=true`   | `200 OK` with `{ "session_id": "...", "output": "..." }`   |

Force synchronous for one call:

```bash
curl -X POST 'https://your-host/webhooks/deploy-notify?wait=true' \
  -H "Authorization: Bearer stella_pat_your_token_here" \
  --data 'What is 2 + 2?'
```

In synchronous mode the caller waits up to a fixed timeout (60 seconds) for the reply. If the agent is still working when the timeout hits, you get `504 Gateway Timeout` -- but the run keeps going in the background, and the response includes the `session_id` so you can inspect the result in the Web UI later.

## Session modes

- **Ephemeral** (default): every trigger starts a fresh session with no memory of previous calls. Best for stateless automation -- each payload is handled on its own.
- **Persistent**: all triggers for this webhook share one long-lived session, so the agent accumulates context across calls. Best when later triggers should build on earlier ones.

> **Persistent sessions and concurrency:** a persistent session runs one trigger at a time. If a trigger arrives while a previous run is still in flight, it is rejected with `429 Too Many Requests` (in both reply modes) -- wait for the in-flight run to finish and retry. Ephemeral mode is not affected: every trigger gets its own session.

> **Persistent sessions and multiple replicas:** persistent mode assumes a single agent runner. If you run Stella across several replicas, concurrent triggers to the same persistent webhook may interleave. For high-frequency persistent webhooks, keep the agent on one replica.

## Response codes

| Code                    | Meaning                                                                     |
| ----------------------- | --------------------------------------------------------------------------- |
| `200 OK`                | Synchronous run finished; body carries `output`                             |
| `202 Accepted`          | Asynchronous run started; body carries `session_id`                         |
| `400 Bad Request`       | Empty body                                                                  |
| `401 Unauthorized`      | Missing, invalid, or non-PAT token                                          |
| `403 Forbidden`         | Token lacks `agent:write`, or its user isn't allowed to run the bound agent |
| `404 Not Found`         | No webhook with that ID                                                     |
| `409 Conflict`          | Webhook or bound agent is disabled, or no agent is bound                    |
| `413 Payload Too Large` | Body exceeds 256 KiB                                                        |
| `429 Too Many Requests` | Rate limit exceeded, or the persistent session is busy with another run     |
| `502 Bad Gateway`       | The agent run failed                                                        |
| `504 Gateway Timeout`   | Synchronous wait timed out; the run continues in the background             |

## Limits

- **Payload size:** up to 256 KiB per request.
- **Rate limit:** each webhook allows a short burst and then a steady trickle; sustained flooding returns `429`.

## Troubleshooting

**Getting `401 Unauthorized`?**

- Make sure the `Authorization` header is `Bearer <token>` and the token is a personal access token (starts with `stella_pat_`), not an OAuth or session token.

**Getting `403 Forbidden`?**

- The token must carry the `agent:write` scope. Recreate it in **Settings → Personal Access Tokens** if you forgot to select the scope.
- The token's user must be allowed to run the bound agent. Grant that user access to the agent, or use a token belonging to a user who already has it.

**Getting `404 Not Found`?**

- Double-check the channel ID in the URL. The webhook must exist and be of type Webhook.

**Getting `409 Conflict`?**

- The webhook or its bound agent is disabled. Enable both on their respective pages.

**Synchronous call returns `504`?**

- The agent took longer than the wait timeout. Use asynchronous mode (`?wait=false`) for long-running tasks, and check the run in the Web UI using the returned `session_id`.
