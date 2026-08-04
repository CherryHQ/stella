# Configuration reference

All configuration is stored in PostgreSQL. Stella can run an embedded PostgreSQL cluster whose data directory lives under `$STELLA_HOME`; run `stellad postgres download-runtime` first if the runtime is not installed. Set `STELLA_DATABASE_URL` to point at an external PostgreSQL server instead.

The easiest way to configure stella is to run `stellad server` and open `http://localhost:25678`. Use `--port` to change the port.

## Quick start

1. Run `stellad server` and open `http://localhost:25678`
2. Add a provider (e.g., "anthropic" with your API key)
3. Create or edit an agent (set provider, model, system prompt)
4. Configure channels (Telegram token, etc.)
5. Restart: `stellad server`

On first run, Stella creates an enabled `stella` agent without a provider or model. Add a provider and choose its model for Stella in the Web UI before chatting.

## Database tables

All config lives in normalized PostgreSQL tables:

| Table                     | Purpose                                                                                                 |
| ------------------------- | ------------------------------------------------------------------------------------------------------- |
| `settings`                | Key-value JSON settings (runner, scheduler, plugins)                                                    |
| `settings_agents`         | Agent definitions (provider, model, system prompt, workspace)                                           |
| `settings_plugins`        | Unified plugin table (tools, channels, hooks, providers). Provider credentials stored in `config` JSON. |
| `settings_users`          | Auto-created platform users with default agent preference                                               |
| `settings_channel_agents` | Per-group agent assignment                                                                              |
| `ctx_agent_memory`        | Per-user-per-agent persistent notes                                                                     |

## Multi-agent setup

Each agent has:

- A global Provider + model selection, with an optional API-only key override
- A system prompt (personality/identity)
- A user-independent definition and administrator-managed skills area
- A separate sandbox workspace for each user or channel group

Inside a sandbox, `$HOME` is that principal's per-agent workspace, not the
operator's `$STELLA_HOME/agents/{agent_id}` directory.

Create agents via the Web UI or directly in the database.

Provider type, base URL, models, enabled state, and default key are global
administrator configuration. Enterprise provisioning may set a write-only key
override through `POST /api/agents` or the Agent Provider credential
subresource. Override precedence is Agent key, then global Provider key;
deleting the override restores fallback. Safe metadata follows Agent Read, while
only administrators and the persisted Agent creator may mutate it. The same key
resolution applies to every host-side Agent model call, including Vision when it
uses that Provider. Do not place overrides in sandbox environment variables.

## Channel configuration

Channels are stored in the `channel` table. Each row is a channel instance with an `id`, platform `type`, optional dedicated `agent_id`, enabled flag, and JSON config. Stella does not create channel instances on startup; configure them via the Web UI.

**Telegram config fields:**

- `token` -- Bot token
- `channel_id` -- Broadcast channel ID or @username
- `enable_notify` -- Allow notify tool for this channel

Channel access is enforced by Stella's trusted Authority-based domain services; notification targets are resolved from linked identities.

**QQ config fields:** `app_id`, `app_secret`, `enable_notify`

**Feishu config fields:** `app_id`, `app_secret`, `encrypt_key`, `verification_token`, `enable_notify`

Feishu is a chat channel only.

## Login providers

Stella supports local password login, one external OIDC provider, and multiple OAuth login providers.

Local password login is enabled when `OIDC_ISSUER_URL` is not set. The first local registrant bootstraps the admin account; after that, local self-registration is closed unless `LOCAL_PASSWORD_ALLOW_REGISTRATION=true` is set. `LOCAL_PASSWORD_ALLOWED_EMAIL_DOMAINS` optionally restricts self-registration by submitted email domain; it does not verify mailbox ownership and does not affect existing-user login. The old `LOCAL_OIDC_*` names are compatibility fallbacks only. `STELLA_TRUSTED_PROXIES` is a comma-separated list of proxy IPs/CIDRs whose `X-Forwarded-For`/`X-Real-IP` headers may be used for authentication rate limiting.

Standard external OIDC login uses `OIDC_*` env vars (`OIDC_PROVIDER_NAME`, `OIDC_ISSUER_URL`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_REDIRECT_URL`, `OIDC_SCOPES`). Setting `OIDC_ISSUER_URL` replaces local password login on the login page.

OAuth login supports multiple providers through env vars:

```bash
AUTH_OAUTH_PROVIDERS=google,github,feishu
AUTH_OAUTH_FEISHU_CLIENT_ID=cli_xxx
AUTH_OAUTH_FEISHU_CLIENT_SECRET=...
AUTH_OAUTH_FEISHU_ALLOWED_TENANT_KEYS=tenant_key
```

Built-in OAuth provider IDs: `google`, `github`, `feishu`. Google uses OIDC discovery and verified ID-token email, and must be restricted with `AUTH_OAUTH_GOOGLE_ALLOWED_EMAIL_DOMAINS`; tenant keys are not supported for Google login. Every OAuth provider must set either `AUTH_OAUTH_{PROVIDER}_ALLOWED_EMAIL_DOMAINS` or a provider-supported tenant allowlist; Feishu requires tenant keys because Feishu email fields are directory data, not live mailbox verification. Generic OAuth providers require `email_verified: true` by default; set `AUTH_OAUTH_{PROVIDER}_REQUIRE_EMAIL_VERIFIED=false` only for trusted providers that do not expose that claim. If Feishu does not return an email, Stella uses a stable internal email like `union_id@tenant_key.feishu.local`; configuring `AUTH_OAUTH_FEISHU_ALLOWED_EMAIL_DOMAINS` makes a real matching Feishu email required.

## Settings (key-value)

Global settings are stored in the `settings` table as JSON values:

| Key         | Purpose                                           |
| ----------- | ------------------------------------------------- |
| `runner`    | Idle timeout, delegate timeout, compaction config |
| `scheduler` | Scheduler enabled flag, data directory            |
| `plugins`   | Array of plugin configs (path + optional config)  |

## Directory layout

All paths are relative to `$STELLA_HOME` (`~/.stella` by default).

| Operator path                               | Purpose                                                                                                         |
| ------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `postgres/`                                 | Embedded PostgreSQL data directory (all config; absent when `STELLA_DATABASE_URL` points at an external server) |
| `pg-runtime/`                               | Downloaded embedded PostgreSQL runtime; recreate with `stellad postgres download-runtime`                       |
| `cache/models.json`                         | Cached model list (safe to delete)                                                                              |
| `agents/{agent_id}/`                        | User-independent agent definition and administrator-managed area                                                |
| `agents/{agent_id}/.agents/skills/`         | Administrator-managed, agent-bound skills                                                                       |
| `users/{user_id}/agents/{agent_id}/`        | This user's sandbox workspace for this agent; sandbox `$HOME` and initial working directory                     |
| `users/group-{group_id}/agents/{agent_id}/` | This channel group's sandbox workspace for this agent; sandbox `$HOME` and initial working directory            |
| `users/{principal}/data/`                   | Shared principal data and uploads; persistent user data lives here                                              |
| `users/{principal}/data/assets/`            | Uploaded assets; inside the sandbox, use `$STELLA_ASSETS_DIR` rather than an operator path                      |
| `users/{principal}/.mise-tools/`            | Managed per-user or per-group toolchain; shared by that principal's agents                                      |

`{principal}` is a user ID or `group-{group_id}`. These are operator filesystem
paths. Agents should use their sandbox variables and ordinary relative paths:
`$HOME` for their workspace and `$STELLA_ASSETS_DIR` for uploaded assets. Persistent
XDG state is stored under the principal's `data/` tree; it is not an agent
workspace.

## Environment variables

Provider credentials and base URLs are stored in explicit provider rows managed through the Web UI or API; they are not read from the server environment.

| Variable      | Purpose                                     |
| ------------- | ------------------------------------------- |
| `STELLA_HOME` | stella home directory (default `~/.stella`) |

Note: The old YAML-based environment variables (`STELLA_PROVIDER`, `STELLA_MODEL`, `STELLA_TELEGRAM_TOKEN`, etc.) are no longer supported. Use the Web UI or database directly.

## Defaults

On first run, Stella creates one enabled `stella` agent with an empty model and Stella's default system prompt. Provider and channel instances are explicit administrator configuration; built-in plugin capabilities are code-defined and do not require database rows.
