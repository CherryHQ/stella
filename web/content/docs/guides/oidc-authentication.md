---
title: OIDC Authentication
---

Stella supports signing in through any OIDC-compatible identity provider — Zitadel, Keycloak, Authentik, Auth0, and others. When OIDC is configured, the login page shows a "Sign in" button that redirects your browser to the identity provider and back.

## Quick start

Set these environment variables before starting the server:

```bash
OIDC_PROVIDER_NAME=Zitadel          # Display name shown on the login button
OIDC_ISSUER_URL=https://your-idp    # Identity provider's OIDC discovery URL
OIDC_CLIENT_ID=your-client-id
OIDC_CLIENT_SECRET=your-client-secret
OIDC_REDIRECT_URL=https://your-stella-host/auth/callback/Zitadel
```

Then start the server normally:

```bash
stella server
```

The login page will show a **Sign in Zitadel** button. Clicking it starts the OIDC Authorization Code Flow with PKCE — no password is stored in Stella.

## Environment variables

| Variable              | Required | Description                                                                |
| --------------------- | -------- | -------------------------------------------------------------------------- |
| `OIDC_ISSUER_URL`     | Yes      | OIDC discovery URL of your identity provider                               |
| `OIDC_CLIENT_ID`      | Yes      | Client ID registered with your identity provider                           |
| `OIDC_CLIENT_SECRET`  | Yes      | Client secret                                                              |
| `OIDC_REDIRECT_URL`   | Yes      | Callback URL: `https://your-host/auth/callback/{OIDC_PROVIDER_NAME}`       |
| `OIDC_PROVIDER_NAME`  | Yes      | Display name shown on the login button                                     |
| `OIDC_SCOPES`         | No       | Comma-separated scopes (default: `openid,email,profile`)                   |
| `OIDC_ORG_ID_CLAIM`   | No       | JWT claim that carries the organization ID (e.g. `urn:zitadel:iam:org:id`) |
| `OIDC_ORG_NAME_CLAIM` | No       | JWT claim that carries the organization name                               |

OIDC is entirely opt-in. When `OIDC_ISSUER_URL` is not set, the server starts normally and the username/password login form is shown instead.

## Identity provider setup

### Zitadel

1. Create a project in your Zitadel instance.
2. Add a **Web** application. Choose **Code** flow and enable **PKCE**.
3. Set the redirect URI to `https://your-stella-host/auth/callback/Zitadel`.
4. Copy the **Client ID** and **Client Secret** into the environment variables above.
5. Set `OIDC_ISSUER_URL` to your Zitadel domain (e.g. `https://your-org.zitadel.cloud`).

To use Zitadel's organization claims for multi-tenant access control, also set:

```bash
OIDC_ORG_ID_CLAIM=urn:zitadel:iam:org:id
OIDC_ORG_NAME_CLAIM=urn:zitadel:iam:org:name
```

### Other providers

Any OIDC-compliant provider works. The only requirement is that the identity provider returns `email_verified: true` in the ID token — Stella rejects logins where email verification cannot be confirmed.

## How accounts are linked

When you log in through OIDC for the first time, Stella looks up your account by the email address in the ID token:

- **Email matches an existing account** — Stella links your OIDC identity to that account. Your existing data (agents, conversations, vault secrets) is preserved.
- **No match** — Stella creates a new account for you.

This auto-linking works when your Stella username was set to your email address. If it was not, see the upgrade section below.

## Upgrading an existing installation

Stella automatically copies your existing users and channel identities into the new auth tables on first startup. No manual migration is needed.

If your existing usernames are not email addresses, you must update them before enabling OIDC so the auto-link works. Run this command on the server before setting `OIDC_ISSUER_URL`:

```bash
stella auth link-user --user-id <id> --email <your@email.com>
```

This updates the stored email for that user so the OIDC login can find and link the account automatically.

## Security notes

- Sessions are stored as SHA-256 hashes — the raw token never leaves the browser cookie.
- The OIDC state parameter is HMAC-signed using a key derived from `STELLA_VAULT_KEY` to prevent CSRF.
- `STELLA_VAULT_KEY` must be set before enabling OIDC.
