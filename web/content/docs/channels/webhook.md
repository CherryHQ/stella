---
title: Webhook
---

The webhook channel gives one configured automation its own HTTP endpoint. It is inbound-only: a request runs the channel's bound agent as one fixed owner. It does not use a personal access token (PAT), and a request can never choose a different user, agent, memory, or tool credentials.

## Before you start

You need a running Stella server, an enabled agent, and an administrator account. Choose the owner carefully: the endpoint runs with that user's context and the bound agent's tools.

## Create a generic webhook

1. In the Web UI, create a **Webhook** channel and bind it to an enabled agent.
2. Open the channel and select **Activate webhook endpoint**.
3. Select an active owner and the **Generic** provider.
4. Save the URL from the confirmation dialog. Stella shows it once only.

The URL is an unguessable bearer capability. Do not add an `Authorization` header or a PAT; Stella ignores it for webhook identity.

```bash
curl -X POST 'https://your-host/webhooks/stella_whk_…' \
  -H 'Content-Type: text/plain' \
  --data 'Deployment v1.4.2 finished. Summarize the changelog.'
```

The request body becomes the agent message. Treat every payload as untrusted external input, including JSON from systems you control.

## Connect GitHub

GitHub endpoints require both a capability URL and a separate GitHub signing secret.

1. Create or open a Webhook channel bound to a dedicated, least-privileged agent.
2. Select **Activate webhook endpoint**, choose the owner, then choose **GitHub**.
3. Enter the GitHub event and repository allowlists. Use only the events and `owner/repository` names this automation needs.
4. Save the one-time **Webhook URL** and **GitHub secret**.
5. In the repository, open **Settings → Webhooks → Add webhook**:
   - Set **Payload URL** to the Webhook URL.
   - Set **Content type** to `application/json`.
   - Set **Secret** to the GitHub secret.
   - Select only the events in the channel allowlist.
6. Send a test delivery and confirm it receives `202 Accepted`.

GitHub deliveries must include a valid `X-Hub-Signature-256`, event, and delivery ID. Stella verifies the signature over the original request body before it starts any agent work. GitHub requests are always asynchronous: `?wait=true` is rejected and agent output is never returned to GitHub.

## Rotate or revoke

Use **Rotate** when the URL or GitHub secret may have leaked. Rotation invalidates the old URL immediately and, for GitHub, issues a new signing secret. Update the external service before using the new endpoint.

Use **Revoke** to stop all requests immediately. You can then change the channel's bound agent or type and activate a new endpoint. Stella blocks those changes while an endpoint is active so a leaked URL cannot acquire a different identity.

Stella never shows an issued URL or GitHub secret again. If you lose either value, rotate it.

## Reply and session modes

Generic webhooks preserve the channel's reply and session settings:

| Mode                                      | Response                         |
| ----------------------------------------- | -------------------------------- |
| Asynchronous (default, or `?wait=false`)  | `202 Accepted` with a session ID |
| Synchronous (default on, or `?wait=true`) | `200 OK` with the agent output   |

A synchronous request can time out with `504` while its run continues. A persistent generic session accepts one run at a time; a busy session returns `429` with `Retry-After: 1`.

GitHub endpoints always return a bare `202 Accepted` after a valid accepted or duplicate delivery. Invalid signatures return `401`; signed events or repositories outside the allowlists return `202` without running the agent.

## Deduplication and retries

Stella keeps GitHub delivery IDs for **30 days**. A repeated delivery ID for the same endpoint returns `202` without another agent turn. After 30 days, a manual redelivery can run again.

If Stella rejects a delivery before the agent admits the run, it releases the claim and returns a retryable non-2xx response. GitHub can retry it. Once the agent has accepted a delivery, Stella retains the claim even if later work fails: replaying an agent after partial tool side effects is less safe than stopping automatic replay.

Write GitHub automations to be idempotent anyway. A delivery can be retried after the 30-day window, and external systems can deliver related events more than once.

## Limits and response codes

- Request body limit: 256 KiB.
- Each endpoint has a rate limit and at most 10 in-flight runs. Excess requests receive `429`.
- `404 Not Found` means the capability is malformed, unknown, revoked, disabled, or no longer has an active owner or agent. Stella intentionally does not reveal which.
- `503 Service Unavailable` means the agent could not admit the run; retry later.

## Least privilege

Create a separate webhook agent whenever possible. Give it only the tools, GitHub credentials, repositories, and instructions needed for this one automation. Select an owner with the same minimum access. A valid signature proves GitHub sent the payload; it does not make issue text, pull request content, or any other payload field trustworthy instructions.
