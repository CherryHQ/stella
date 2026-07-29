---
title: OIDC Authentication
---

Stella supports three login paths:

1. **Local password** — Stella stores a bcrypt password and signs users in directly.
2. **External OIDC** — one standard OpenID Connect identity provider such as Zitadel, Keycloak, Authentik, Auth0, Okta, or Azure AD.
3. **OAuth login providers** — multiple provider buttons such as Feishu, Google, GitHub, or custom OAuth providers.

When external login is configured, the login page shows provider buttons that redirect your browser to the provider and back.

## Local password login

When no external OIDC provider is configured (`OIDC_ISSUER_URL` is not set), Stella enables local email/password login. No OIDC issuer endpoints are exposed for local login; credential submission happens through Stella's JSON API and creates a Stella session directly.

The first user to register automatically becomes an admin. After that bootstrap account exists, local self-registration is closed by default. Set `LOCAL_PASSWORD_ALLOW_REGISTRATION=true` only when you want additional users to create their own local accounts.

To reduce accidental self-registration when you enable it, set `LOCAL_PASSWORD_ALLOWED_EMAIL_DOMAINS` to a comma-separated list of allowed email domains. Only addresses on those domains (or their subdomains) may register; leave it unset to allow any email. This does **not** verify mailbox ownership — use an external OIDC/OAuth provider for a real security boundary.

```bash
LOCAL_PASSWORD_ALLOW_REGISTRATION=true
LOCAL_PASSWORD_ALLOWED_EMAIL_DOMAINS=cicc.com.cn,example.com
```

The old `LOCAL_OIDC_ALLOW_REGISTRATION` and `LOCAL_OIDC_ALLOWED_EMAIL_DOMAINS` names still work for compatibility, but new deployments should use the `LOCAL_PASSWORD_*` names.

### Security limitations

- Local password login is protected by the password only.
- `LOCAL_PASSWORD_ALLOWED_EMAIL_DOMAINS` only checks the submitted email string during registration. It is not email verification and does not affect existing-user login.
- For production access control, prefer Feishu tenant allowlisting, Google/GitHub verified email allowlisting, or an external OIDC provider.
- If Stella runs behind a reverse proxy and you want login rate limits to use the original client IP, set `STELLA_TRUSTED_PROXIES` to the proxy IPs or CIDR ranges. Without it, Stella ignores `X-Forwarded-For` and `X-Real-IP` for authentication rate limiting.

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
stellad server
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

When `OIDC_ISSUER_URL` is set, the external OIDC provider replaces local password login on the login page. Any configured OAuth providers are still shown alongside it.

## OAuth login providers

Use OAuth login when the provider is not a standard OIDC issuer, or when you want multiple login buttons on the same Stella instance. Stella includes presets for `github`, `google`, and `feishu`; custom providers can be added by supplying their authorization, token, and userinfo URLs.

Enable providers with a comma-separated list:

```bash
AUTH_OAUTH_PROVIDERS=google,github,feishu
```

Each provider uses an uppercase provider ID in its environment variables:

```bash
AUTH_OAUTH_GOOGLE_CLIENT_ID=your-google-client-id
AUTH_OAUTH_GOOGLE_CLIENT_SECRET=your-google-client-secret
AUTH_OAUTH_GOOGLE_ALLOWED_EMAIL_DOMAINS=example.com

AUTH_OAUTH_GITHUB_CLIENT_ID=your-github-client-id
AUTH_OAUTH_GITHUB_CLIENT_SECRET=your-github-client-secret
AUTH_OAUTH_GITHUB_ALLOWED_EMAIL_DOMAINS=example.com

AUTH_OAUTH_FEISHU_CLIENT_ID=cli_xxx
AUTH_OAUTH_FEISHU_CLIENT_SECRET=your-feishu-app-secret
AUTH_OAUTH_FEISHU_ALLOWED_TENANT_KEYS=tenant_key_from_feishu
```

If `STELLA_BASE_URL` is set, Stella derives callback URLs automatically as `https://your-host/auth/callback/{provider}`. You can override each callback with `AUTH_OAUTH_{PROVIDER}_REDIRECT_URL`.

### OAuth environment variables

| Variable                                       | Required | Description                                                             |
| ---------------------------------------------- | -------- | ----------------------------------------------------------------------- |
| `AUTH_OAUTH_PROVIDERS`                         | Yes      | Comma-separated provider IDs, for example `google,github,feishu`        |
| `AUTH_OAUTH_{PROVIDER}_CLIENT_ID`              | Yes      | OAuth client ID / app ID                                                |
| `AUTH_OAUTH_{PROVIDER}_CLIENT_SECRET`          | Yes      | OAuth client secret / app secret                                        |
| `AUTH_OAUTH_{PROVIDER}_REDIRECT_URL`           | No       | Callback URL; defaults to `STELLA_BASE_URL/auth/callback/{provider}`    |
| `AUTH_OAUTH_{PROVIDER}_SCOPES`                 | No       | Space- or comma-separated scopes; built-in providers have safe defaults |
| `AUTH_OAUTH_{PROVIDER}_ALLOWED_EMAIL_DOMAINS`  | Yes\*    | Allowed verified email domains; exact domain or subdomain match         |
| `AUTH_OAUTH_{PROVIDER}_ALLOWED_TENANT_KEYS`    | Yes\*    | Allowed tenant keys; required for Feishu                                |
| `AUTH_OAUTH_{PROVIDER}_REQUIRE_EMAIL_VERIFIED` | No       | For generic OAuth providers, require `email_verified`; default: `true`  |

`ALLOWED_EMAIL_DOMAINS` or a provider-supported tenant allowlist is required for every OAuth provider. Google and GitHub use `ALLOWED_EMAIL_DOMAINS`; Feishu specifically requires `ALLOWED_TENANT_KEYS`. This prevents accidentally opening your Stella instance to any account from that provider.

### Feishu login

For Feishu, create a web application in the Feishu Open Platform and add this callback URL in **Security Settings**:

```text
https://your-stella-host/auth/callback/feishu
```

Recommended configuration:

```bash
STELLA_BASE_URL=https://your-stella-host
AUTH_OAUTH_PROVIDERS=feishu
AUTH_OAUTH_FEISHU_CLIENT_ID=cli_xxx
AUTH_OAUTH_FEISHU_CLIENT_SECRET=your-feishu-app-secret
AUTH_OAUTH_FEISHU_ALLOWED_TENANT_KEYS=your_tenant_key
```

Stella requests `contact:user.email:readonly` by default so it can fetch the user's email from Feishu. Feishu user email fields are directory data, not a live mailbox verification proof, so `AUTH_OAUTH_FEISHU_ALLOWED_TENANT_KEYS` is required. If Feishu does not return an email, Stella creates the account with a stable internal email like `union_id@tenant_key.feishu.local`. Email-domain allowlisting is useful as an extra filter, but if you enable it, Feishu must return a matching email.

#### Feishu workspace tools

Login requests only the identity scope (`contact:user.email:readonly`), so the login URL stays small and authentication is a single, fast consent. It does **not** grant access to Feishu tools.

The built-in lark-cli is configured from the current Agent's Feishu/Lark Channel app and uses its own per-employee native device authorization. It does not use the login token or the Feishu OAuth credential card. See [Feishu Bot](../channels/feishu#lark-workspace-automation).

Generic Feishu/Lark OAuth providers remain available for other manifest tools that explicitly require them; connect those providers separately from the **Credentials** page. See [OAuth Connections](./oauth-connections).

### Custom OAuth provider

For a non-preset provider, provide the endpoints explicitly:

```bash
AUTH_OAUTH_PROVIDERS=acme
AUTH_OAUTH_ACME_CLIENT_ID=client-id
AUTH_OAUTH_ACME_CLIENT_SECRET=client-secret
AUTH_OAUTH_ACME_AUTH_URL=https://idp.example/oauth/authorize
AUTH_OAUTH_ACME_TOKEN_URL=https://idp.example/oauth/token
AUTH_OAUTH_ACME_USERINFO_URL=https://idp.example/oauth/userinfo
AUTH_OAUTH_ACME_ALLOWED_EMAIL_DOMAINS=example.com
```

Custom OAuth providers must return `email_verified: true` from the userinfo endpoint by default. If your provider is trusted but does not expose this claim, set `AUTH_OAUTH_{PROVIDER}_REQUIRE_EMAIL_VERIFIED=false`; Stella will still require the email domain allowlist.

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

When you log in through OIDC or OAuth, Stella links the account by the provider and subject identifier returned by that provider. Stella does not silently link a new external identity to an existing account just because the email address matches.

- **Provider subject already linked** — Stella signs you in to that account. Your existing data (agents, conversations, vault secrets) is preserved.
- **Provider subject not linked** — Stella creates a new account for you. The first user to register is automatically assigned the admin role; subsequent users get the regular user role.

Admins can manage users, roles, and explicit login identity links from **Settings > Users** in the web UI.

## Upgrading an existing installation

Stella automatically copies your existing users and channel identities into the new auth tables on first startup. No manual migration is needed.

To attach an external login to an existing account, create an explicit login identity link from **Settings > Users**. A matching email alone is not enough to link accounts automatically.

## Security notes

- Sessions are stored as SHA-256 hashes — the raw token never leaves the browser cookie.
- The OIDC state parameter is HMAC-signed using a key derived from `STELLA_VAULT_KEY` to prevent CSRF.
- `STELLA_VAULT_KEY` must be set before enabling OIDC.
