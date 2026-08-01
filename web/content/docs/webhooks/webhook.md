---
title: Webhooks
---

A webhook is a personal HTTP invocation capability for one agent. It is not a chat channel. Every authenticated user manages only their own webhooks, including administrators.

## Create a webhook

1. Open **Settings → Webhooks**.
2. Create a webhook with a name and an agent you can use.
3. Copy the URL shown after creation. Stella never shows that URL again.

The URL is the credential. Send a `POST` request to it; do not add an `Authorization` header.

```bash
curl -X POST https://your-host/webhooks/stella_whk_your_capability \
  --data 'Summarize the deployment that just finished.'
```

A webhook always runs as its owner with its bound agent. Stella checks that the user and agent are active and that the user can still use that agent each time it admits a request. Disabled webhooks, removed access, disabled users, and disabled agents reject later requests with an opaque `404`.

## Invocation options

Options apply to one request and are never saved on the webhook:

- `?wait=true|false` waits for a reply when true. The default is `false`.
- `?session_mode=ephemeral|persistent` selects a fresh or continuing session. The default is `ephemeral`.

Duplicate or invalid options return `400`. A persistent session key is unique to the webhook.

## Receive GitHub events

GitHub is a caller of the same generic Webhook capability; no GitHub integration is required.

1. Create a Webhook in **Settings → Webhooks** and copy its one-time URL.
2. In your GitHub repository, open **Settings → Webhooks → Add webhook**.
3. Paste the Webhook URL into **Payload URL**, select **application/json**, and leave **Secret** empty.
4. Choose the GitHub events your Agent should receive, then save.

Stella authorizes the request solely by possession of the unguessable capability URL; it does not authenticate GitHub as the source or verify `X-Hub-Signature-256`. Do not configure a GitHub secret for this endpoint. The default invocation is asynchronous and uses an ephemeral session: GitHub receives `202` while the Agent runs, and the reply is not sent back to GitHub.

GitHub's initial `ping` and every selected event invoke the Agent. GitHub can also redeliver an event. Stella does not deduplicate deliveries, including by `X-GitHub-Delivery`; make Agents that cause side effects idempotent.

## Manage the capability

- **Edit** changes its name, binding, enabled state, and server timeout ceilings.
- **Rotate** requires the current etag, immediately invalidates the old URL, and returns a new URL once.
- **Delete** revokes the resource and invalidates its URL.

Stable reads never expose the URL or credential material. If you lose a URL, rotate the webhook.

## Limits and errors

Webhook bodies are limited to 256 KiB and synchronous reply text to 1 MiB; asynchronous calls do not retain Agent output. Stella limits request admission and concurrent runs per webhook. A synchronous request uses the webhook's wait timeout (default 60 seconds, maximum 600); every run uses its run timeout (default 300 seconds, maximum 3600).

| Code  | Meaning                                                                 |
| ----- | ----------------------------------------------------------------------- |
| `200` | Synchronous reply completed                                             |
| `202` | Asynchronous invocation accepted; the Agent continues in the background |
| `400` | Invalid options or request body                                         |
| `404` | Invalid, rotated, revoked, disabled, or no-longer-authorized webhook    |
| `408` | Request body was not received before the read deadline                  |
| `413` | Body exceeds 256 KiB                                                    |
| `429` | Admission, rate, or run limit reached                                   |
| `502` | The admitted Agent run failed                                           |
| `503` | The Agent runtime is unavailable                                        |
| `504` | Synchronous reply did not finish before the wait timeout                |
