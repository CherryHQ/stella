---
title: Provisioned-user API reference
description: Endpoints, request fields, responses, and lifecycle limits for enterprise provisioning.
---

All endpoints below require `Authorization: Bearer stella_prv_…`. Only an interactive administrator session can create, list, or revoke those provisioning tokens at `/api/admin/provisioning-tokens`.

| Method | Endpoint                                         | Response                                                 |
| ------ | ------------------------------------------------ | -------------------------------------------------------- |
| `POST` | `/api/provisioned-users`                         | `201` user metadata plus one-time `token`                |
| `GET`  | `/api/provisioned-users`                         | `200` `provisioned_users` and `next_page_token`          |
| `GET`  | `/api/provisioned-users/{id}`                    | `200` safe user and active-token metadata                |
| `POST` | `/api/provisioned-users/{id}/channel-identities` | `201` created messaging-platform identity                |
| `POST` | `/api/provisioned-users/{id}/rotate-token`       | `200` updated metadata plus one-time replacement `token` |
| `POST` | `/api/provisioned-users/{id}/deactivate`         | `200` deactivated user metadata                          |

`id` is Stella's canonical UUID. `external_id` is a unique immutable field, not a path identifier. List requests use `page_size` (default 20, maximum 500) and opaque `page_token` values.

Create requires `external_id`, `email`, and `name`; `token_name` defaults to `enterprise-integration`. Create and rotation default `expires_at` to 90 days and reject an expiry more than 365 days away. `never_expires` is not supported.

Responses expose only token ID, name, last four characters, expiry, last-used time, and creation time. They never expose a token hash, plaintext after its one response, scopes, or issuer secrets. A managed `external_id` conflict returns `409` with safe managed metadata; an email collision with an unmanaged account returns a generic `409`.

Creating a channel identity requires `platform` and `external_id`; `name` is optional. It links inbound messages directly and does not grant Web UI or OpenID Connect login. The target must be an active ordinary user created by the same administrator, including through an earlier provisioning token owned by that administrator. A duplicate `(platform, external_id)` returns `409`; a user owned by another administrator returns `404`.
