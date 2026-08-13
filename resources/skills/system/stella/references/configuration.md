# Configuration reference

All configuration is stored in PostgreSQL. Stella can run an embedded PostgreSQL cluster whose data directory lives under `$STELLA_HOME`; run `stellad postgres download` first if the runtime is not installed. Set `STELLA_DATABASE_URL` to point at an external PostgreSQL server instead.

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
| `settings_users`          | User accounts with default-agent preference                                                             |
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
- `allowed_chat_ids` -- Comma-separated trusted numeric group IDs; empty rejects all group messages
- `allow_dm` -- Accept private messages and account linking; defaults to `true`
- `allow_unlinked_dm` -- Allow persistent restricted guest private messages; defaults to `false`
- `guest_message_limit_per_minute`, `guest_max_per_channel`, `guest_retention_days` -- Guest resource limits
- `require_mention` -- Require a bot mention in allowed groups; defaults to `true`
- `enable_notify` -- Allow notify output for this channel

Channel access is enforced by Stella's trusted Authority-based domain services; notification targets are resolved from linked identities.

**Discord config fields:**

- `token` -- Bot token
- `allowed_guild_ids` -- Comma-separated trusted server IDs; empty disables all guild messages but not direct messages
- `allow_dm` -- Accept account linking and linked-user direct messages; defaults to `true`
- `allow_unlinked_dm` -- Allow persistent restricted guest direct messages on the channel-bound agent; defaults to `false` and requires `allow_dm`
- `guest_message_limit_per_minute` -- Per-guest message and command limit; defaults to `10`
- `guest_max_per_channel` -- Durable guest identity cap for one channel; defaults to `1000`
- `guest_retention_days` -- Inactivity period before the daily purge deletes a guest and its sessions; defaults to `30`
- `require_mention` -- Only process guild messages that mention the bot; defaults to `true`

Guest direct messages retain and compact conversation history but have no profile, reflection, tools, skills, files, workspace, plugins, or delegation. Guests can only use `/link`, `/help`, `/new`, `/compact`, and `/abort`; linking does not merge old guest history. Guest rate, count, and retention limits reduce abuse but enabling the feature still exposes model usage publicly, so warn about cost and security and use a dedicated guest-safe agent whose base prompt contains no secrets.

**QQ config fields:** `app_id`, `app_secret`, `enable_notify`

**Feishu config fields:** `app_id`, `app_secret`, `encrypt_key`, `verification_token`, `enable_notify`, `tenant_key`, `auto_provision`, `allowed_chat_ids`, `allow_dm`, `allow_unlinked_dm`, `guest_message_limit_per_minute`, `guest_max_per_channel`, `guest_retention_days`, `require_mention`

Feishu `allowed_chat_ids` is a comma-separated, fail-closed group `chat_id` allowlist. Direct messages default on, group mentions default required, and restricted guest direct messages default off. Guest sessions use the same isolation and resource limits described for Discord.

**DingTalk config fields:** `client_id`, `client_secret`, `allowed_conversation_ids`, `allow_dm`, `allow_unlinked_dm`, `guest_message_limit_per_minute`, `guest_max_per_channel`, `guest_retention_days`, `require_mention`

DingTalk uses Stream mode and requires no public callback URL. `allowed_conversation_ids` is a comma-separated, fail-closed group allowlist. Text messages, direct messages, group @mentions, account linking, and restricted guest DMs are supported. Notifications require a temporary session Webhook learned from a recent inbound message and stop working after restart or expiry until the user or group messages the bot again.

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
| `pg-runtime/`                               | Downloaded embedded PostgreSQL runtime; recreate with `stellad postgres download`                               |
| `cache/sandbox-tmp/`                        | Docker sandbox temporary directories; scratch, removed when stale                                               |
| `.agents/db-skills/`                        | Local compatibility coordinate for the narrow system Skill root; PostgreSQL-derived materialization             |
| `agents/{agent_id}/`                        | User-independent agent definition and administrator-managed compatibility area                                  |
| `agents/{agent_id}/.agents/skills/`         | Local compatibility coordinate for the narrow system-Agent Skill root; PostgreSQL-derived materialization       |
| `users/{user_id}/agents/{agent_id}/`        | This user's per-principal Agent Home; sandbox `$HOME` and initial working directory                             |
| `users/group-{group_id}/agents/{agent_id}/` | This channel group's per-principal Agent Home; sandbox `$HOME` and initial working directory                    |
| `users/{principal}/data/`                   | User or group Principal Home: shared principal data and uploads                                                 |
| `runner-scratch/runner-*`                   | Disposable user-less-run workspace; never durable Home authority                                                |
| `users/{principal}/data/assets/`            | Uploaded assets; inside the sandbox, use `$STELLA_ASSETS_DIR` rather than an operator path                      |
| `users/{principal}/.mise-tools/`            | Managed per-user or per-group toolchain; shared by that principal's agents                                      |

## Skills and release bundles

Release-provided builtins are immutable `builtin:<name>` entries from `resources.Registry`. Their only authority is the content-addressed release bundle. Native `local` and `none` execution installs the exact bundle at `$STELLA_HOME/bundles/<revision>`; isolating execution sees that bundle read-only at `/opt/stella/skills/builtin`. `/opt` is only an execution coordinate. Helper executable modes are preserved.

Project Skills remain ordinary files in durable Agent/project working trees. PostgreSQL remains the authority for mutable `system`, `system_agent`, `user`, and `user_agent` records; their on-disk materializations are derived caches. The Home filesystem authority cutover is planned and not active. `system:<name>` is a mutable administrator-installed global Skill, `system_agent:<name>` is a mutable Agent-bound administrator Skill, and neither is a release builtin.

Skills are enabled per Agent by default. An administrator or durable Agent creator changes one shared setting. Stella selects the precedence winner before applying that policy, so disabling it never reveals a lower same-name Skill. Activation is independent of content-edit permission and `disable_model_invocation`. An admitted turn keeps its snapshot; the next turn sees a committed change. Legacy non-empty arrays diagnose all-enabled, and dangling disabled references are inert until explicitly cleared.

For an exact operator command syntax, run `stellad system-bundle --help`. Docker sandbox images bake and label the matching bundle revision, never fall back to host builtins, and Docker provider preflight prevents a runner session from starting if their revision differs from the binary. Developers rebuild the local image with `mise run sandbox:docker:build`; rebuild custom images from the matching Stella revision.

Before upgrading, use the old working binary to import each custom Skill root under legacy `$STELLA_HOME/.agents/skills` as a global (`system`) Skill through **Settings → Skills** on older releases or **Admin Console → Deployment resources → Global Skills** on newer releases. Back up, verify, and remove other residual paths. Current-manifest paths are inert even if their contents or modes differ; every other Skill root or residual path blocks startup without mutation.

`{principal}` is a user ID or `group-{group_id}`. These are deterministic paths
under the single POSIX `STELLA_HOME`, not registry locators. Agents should use their sandbox variables and ordinary relative paths:
`$HOME` for their workspace and `$STELLA_ASSETS_DIR` for uploaded assets. Persistent
XDG state is stored under the principal's `data/` tree; it is not an agent
workspace.

PostgreSQL owner rows authorize workspace access. The sole production
`WorkspaceManager` creates a missing root for live owners and rejects symlinks,
non-directories, unsafe IDs, and replacement of the trusted root. The filesystem
owns the bytes; back it up with PostgreSQL. Any entry at `agents/{id}` reserves
that global Agent ID. Run restore and root cleanup while Stella is stopped.

An explicit destructive user, group, or Agent delete fences execution before the
database transaction removes the owner. Physical bytes and inodes remain, while
subsequent workspace access fails owner validation.
Removing an assignment or member, archiving a Session, and uninstalling Helm do not
delete workspace bytes. Do not manually clean workspace roots while Stella is running.
Multi-replica, Kubernetes, and S3 authority require a future redesign.

`runner-scratch/` is trusted host-owned structural state. Normal close and
construction failure clean each disposable child best-effort; crash or trusted
host tampering may leave children. Isolating providers mount only the exact child.
Clean leftovers only while Stella is stopped or affected consumers are fenced.

## Environment variables

Provider credentials and base URLs are stored in explicit provider rows managed through the Web UI or API; they are not read from the server environment.

| Variable      | Purpose                                     |
| ------------- | ------------------------------------------- |
| `STELLA_HOME` | stella home directory (default `~/.stella`) |

Note: The old YAML-based environment variables (`STELLA_PROVIDER`, `STELLA_MODEL`, `STELLA_TELEGRAM_TOKEN`, etc.) are no longer supported. Use the Web UI or database directly.

## Defaults

On first run, Stella creates one enabled `stella` agent with an empty model and Stella's default system prompt. Provider and channel instances are explicit administrator configuration; built-in plugin capabilities are code-defined and do not require database rows.
