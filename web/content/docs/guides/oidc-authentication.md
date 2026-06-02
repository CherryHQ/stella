---
title: OIDC Authentication
---

Stella supports signing in through any OIDC-compatible identity provider — Zitadel, Keycloak, Authentik, Auth0, and others. When OIDC is configured, the login page shows a "Sign in" button that redirects your browser to the identity provider and back.

Stella also includes a **built-in local OIDC issuer** that is enabled by default when no external provider is configured. This lets you sign in with a local account without any additional setup.

## Built-in local OIDC issuer

When no external OIDC provider is configured (`OIDC_ISSUER_URL` is not set), Stella automatically enables a built-in local OIDC issuer. No environment variables are needed — a signing key is auto-generated and stored in the data directory.

The login page shows a **Sign in** button. You can also register a new account by clicking **Sign up** on the login page. Registration stays open as long as the built-in issuer is in use.

The first user to register automatically becomes an admin; everyone who registers after that gets the regular user role.

To restrict who can self-register, set `LOCAL_OIDC_ALLOWED_EMAIL_DOMAINS` to a comma-separated list of allowed email domains. Only addresses on those domains (or their subdomains) may register; leave it unset to allow any email.

```bash
LOCAL_OIDC_ALLOWED_EMAIL_DOMAINS=cicc.com.cn,example.com
```

The local issuer exposes standard OIDC endpoints under `/oidc/local`:

| Endpoint      | Path                                           |
| ------------- | ---------------------------------------------- |
| Discovery     | `/oidc/local/.well-known/openid-configuration` |
| JWKS          | `/oidc/local/jwks`                             |
| Authorization | `/oidc/local/authorize`                        |
| Token         | `/oidc/local/token`                            |
| Userinfo      | `/oidc/local/userinfo`                         |

### Security limitations

- Only **Authorization Code + PKCE** flow is supported.
- Redirect URIs are exact-match — no wildcards.
- The signing key is loaded at startup; key rotation requires a server restart.
- There is no dynamic client registration. Client config is static.
- Do not expose the local issuer publicly without TLS.

## External OIDC provider

### Quick start

Set these environment variables before starting the server:

```bash
OIDC_PROVIDER_NAME=Zitadel          # Display name shown on the login button
OIDC_ISSUER_URL=https://your-idp    # Identity provider's OIDC discovery URL
OIDC_CLIENT_ID=your-client-id
OIDC_REDIRECT_URL=https://your-stella-host/auth/callback/Zitadel
```

Then start the server normally:

```bash
stella server
```

The login page will show a **Sign in Zitadel** button. Clicking it starts the OIDC Authorization Code Flow with PKCE — no password is stored in Stella.

### Environment variables

| Variable             | Required | Description                                                          |
| -------------------- | -------- | -------------------------------------------------------------------- |
| `OIDC_PROVIDER_NAME` | Yes      | Display name shown on the login button                               |
| `OIDC_ISSUER_URL`    | Yes      | OIDC discovery URL of your identity provider                         |
| `OIDC_CLIENT_ID`     | Yes      | Client ID registered with your identity provider                     |
| `OIDC_REDIRECT_URL`  | Yes      | Callback URL: `https://your-host/auth/callback/{OIDC_PROVIDER_NAME}` |
| `OIDC_CLIENT_SECRET` | No       | Client secret; leave empty for public clients (PKCE only)            |
| `OIDC_SCOPES`        | No       | Comma-separated scopes (default: `openid,email,profile`)             |

When `OIDC_ISSUER_URL` is set, the external provider replaces the built-in local issuer on the login page.

## Identity provider setup

### Zitadel

#### 1. Create a project and application

1. In the Zitadel Console, go to **Projects** and create a new project (or use an existing one).
2. Inside the project, click **New** to add an application.
3. Choose **Web** as the application type.
4. Select **Code** as the authentication method. If you don't need a client secret, select **PKCE** instead.
5. Set the redirect URI to `https://your-stella-host/auth/callback/Zitadel`.
6. Copy the **Client ID** (and **Client Secret** if you chose Code flow) into the environment variables.
7. Set `OIDC_ISSUER_URL` to your Zitadel instance URL (e.g. `https://your-org.zitadel.cloud`).

#### 2. Configure token settings

By default, Zitadel does not include user profile claims (like `email`) in the ID token. Stella requires the `email` claim to identify users.

In the application settings, go to the **Token** tab and enable:

- **User Info inside ID Token** — this adds `email`, `email_verified`, `name`, and `picture` claims directly to the ID token.

Without this setting, login will fail with an "email claim missing" error.

#### 3. Enable user registration (optional)

If you want new users to be able to sign up through Zitadel's login page:

1. Go to your Zitadel instance **Settings** > **Login Behavior and Security**.
2. Enable **Self Registration**.

When enabled, Zitadel shows a "Register" link on its login page. Users who register through Zitadel are automatically created in Stella on their first login.

#### 4. Configure scopes (optional)

The default scopes (`openid,email,profile`) work for most setups. If you need Zitadel to recognize the application as an audience (required for some API integrations), add the Zitadel project audience scope:

```bash
OIDC_SCOPES=openid,email,profile,urn:zitadel:iam:org:project:id:zitadel:aud
```

#### Example configuration

```bash
OIDC_PROVIDER_NAME=Zitadel
OIDC_ISSUER_URL=https://your-org.zitadel.cloud
OIDC_CLIENT_ID=123456789012345678@my-project
OIDC_REDIRECT_URL=https://your-stella-host/auth/callback/Zitadel
OIDC_SCOPES=openid,email,profile,urn:zitadel:iam:org:project:id:zitadel:aud
```

### Other providers

Any OIDC-compliant provider works. The only requirement is that the identity provider returns `email` and `email_verified: true` in the ID token — Stella rejects logins where email verification cannot be confirmed.

## How accounts are linked

When you log in through OIDC for the first time, Stella looks up your account by the email address in the ID token:

- **Email matches an existing account** — Stella links your OIDC identity to that account. Your existing data (agents, conversations, vault secrets) is preserved.
- **No match** — Stella creates a new account for you. The first user to register is automatically assigned the admin role; subsequent users get the regular user role.

Admins can manage users and roles from **Settings > Users** in the web UI.

## Upgrading an existing installation

Stella automatically copies your existing users and channel identities into the new auth tables on first startup. No manual migration is needed.

If your existing usernames are not email addresses, you must update them before enabling OIDC so the auto-link works:

```bash
stella auth link-user --user-id <id> --email <your@email.com>
```

This updates the stored email for that user so the OIDC login can find and link the account automatically.

## Security notes

- Sessions are stored as SHA-256 hashes — the raw token never leaves the browser cookie.
- The OIDC state parameter is HMAC-signed using a key derived from `STELLA_VAULT_KEY` to prevent CSRF.
- `STELLA_VAULT_KEY` must be set before enabling OIDC.
