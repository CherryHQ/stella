## Webhooks

A webhook is a personal HTTP invocation capability, not a chat channel. Any authenticated user can create, edit, rotate, and delete only their own webhook; administrator status never grants access to another user's resource.

Create it in **Settings → Webhooks** with a name and an Agent the user may currently use. The URL shown by Create or Rotate is a one-time secret. Stable reads never return it. The resource stores only server timeout ceilings; callers choose `?wait=true|false&session_mode=ephemeral|persistent` for each request (defaults: `false`, `ephemeral`).

Callers `POST` the raw message body to `https://your-host/webhooks/<capability>`. The capability URL is the only credential; do not send `Authorization`. On every admission Stella verifies the webhook is enabled, its owner and Agent are active, and the owner can still use the Agent. Invalid, revoked, disabled, or unauthorized capabilities return opaque `404`.

Rotate when a URL is lost or leaked. It requires the latest opaque etag, invalidates the old URL immediately, and returns a new one-time URL. Delete revokes the webhook.
