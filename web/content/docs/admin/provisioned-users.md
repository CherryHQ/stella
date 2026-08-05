---
title: Provision users through an enterprise integration
description: Create and operate passwordless, API-only users with a restricted provisioning token.
---

Use this API when your identity or employee system needs to create Stella users without giving that system administrator access.

## Create a provisioning token

Sign in to the Web UI as an administrator, then create a token from your interactive admin session:

```sh
curl -X POST https://stella.example/api/admin/provisioning-tokens \
  -H 'Content-Type: application/json' \
  -b 'stella_session=…' \
  -d '{"name":"hr-provisioner"}'
```

Store the returned `stella_prv_…` value in your integration's secret manager. Stella shows it once. It defaults to a 90-day expiry and cannot live longer than 365 days. Keep two tokens briefly during a planned rotation, verify the new integration deployment, then revoke the old token.

Provisioning tokens can only call the provisioned-user API. They cannot sign in to the Web UI, manage ordinary accounts, or mint more provisioning tokens.

## Provision and rotate a user

Create a user with an immutable identifier from your directory. The response includes one passwordless `stella_pat_…` bearer token exactly once.

```sh
curl -X POST https://stella.example/api/provisioned-users \
  -H 'Authorization: Bearer stella_prv_…' \
  -H 'Content-Type: application/json' \
  -d '{"external_id":"employee-42","email":"ada@example.com","name":"Ada","token_name":"hr-sync"}'
```

Use `GET /api/provisioned-users` and `GET /api/provisioned-users/{id}` for safe metadata only. To replace a lost or exposed user token, call `POST /api/provisioned-users/{id}/rotate-token`; it revokes the prior provisioning-issued token before creating one replacement. It never revokes personal tokens that the user created independently.

All user tokens expire by default after 90 days; `expires_at` may shorten or extend that interval up to 365 days. There is no never-expiring option.

## Recover and respond to incidents

If `external_id` already belongs to a managed user, creation returns `409` with safe existing user and token metadata, never the old token. Treat this as a retry/reconciliation result: fetch that resource and rotate its token if the earlier response was lost. An email collision with an unmanaged account also returns `409`, but deliberately reveals nothing about that account.

If an integration or user token leaks, rotate it immediately. If the person must lose access, call `POST /api/provisioned-users/{id}/deactivate`. Deactivation uses Stella's normal account lockdown, revoking sessions and every personal access token; it cannot be undone through this API. A provisioned user promoted to administrator is read-only to the provisioner, so investigate and use an interactive admin session instead.
