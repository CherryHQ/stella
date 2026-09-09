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

## Conversational Settings

An Agent manager can opt one Agent into a limited subset of conversational
Settings tools in **Profile → Configuration → Advanced configuration**. Built-in
Stella starts enabled, including after an upgrade; other Agents start disabled until their manager opts in.
When enabled, these cold Code Mode tools are available only in a signed-in,
foreground one-to-one `main` or `chat` session. They remain unavailable in
groups, guests, webhooks, scheduler/task/delegate workers, and Agent-originated
`session_send`. Catalog visibility is not permission: every call rechecks the
durable Agent setting, direct human authority, and domain policy.

### Capability matrix

| Area                                | Tools or surface                                                                  | Scope                                                             |
| ----------------------------------- | --------------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| Agents and per-Agent tool overrides | `settings_agent_*`, `settings_agent_tool_*`                                       | Agents the caller may manage                                      |
| Library files                       | `settings_library_file_*`                                                         | user/user-agent, plus system scopes for authorized administrators |
| Skills and packages                 | `settings_skill_*`, Web UI, resource API                                          | complete files in the caller's writable scope                     |
| Providers and model defaults        | `settings_provider_*`, `settings_default_model_*`, `settings_embedding_setting_*` | administrator only                                                |

Resource management operates on complete files. Read the current resource before
an update or delete and use its current digest when the API exposes one. A
conflict means the bytes changed; read again before retrying. Copying creates an
independent package and does not establish an update link.

Runtime `skill_load` reads the selected turn view. It is separate from editing a
Skill or package. MCP declarations are edited as standalone files or package
files; their OAuth grants are separate. Delete removes the declaration. Use
Disconnect to revoke the matching locally stored grant, close its connection,
and block late callbacks or refreshes; remote provider revocation is not
guaranteed.

The execution summary records the actual files, content summaries, authorization,
preparation evidence, and selected resources admitted for a turn. It is not
rebuilt from files after the fact. A turn started before an edit keeps its
captured view; the next turn reads the new bytes.

A package or Skill disable blocks future selection but does not revoke OAuth or
immediately erase resource bytes. Stella does not online-GC resource bytes,
history, or installation caches. MCP connections are session-owned and close
with the session; catalog observations are not authority. The local backend
enforces its sandbox policy but cannot prove detached descendants stopped; the
`none` backend provides no reliable process isolation. Cleanup requires verified
process termination.

### Secrets and trust boundaries

No conversational Settings tool accepts an API key, bearer token, credential
reference, or another secret. Provider credentials, MCP bearer credentials, and
credential binding changes remain Web UI/API-only. OAuth grants are handled by
the connection flow and stored in the credential store, never in resource files.

Account, Users, Provisioning, Channels, Webhooks, OAuth connection
configuration, Agent workspace and sandbox settings, and credential changes
remain outside this capability. Existing OAuth and vault tools are separate
capabilities, not a path to bind secrets to resource declarations.

### Result and source bounds

- Resource lists return bounded metadata and may report `truncated`; they never return secret values.
- Library list uses `page_size` from 1 through 100 and returns `next_page_token` when another page exists.
- Skill/package create and update read bounded complete trees, including `SKILL.md` and optional resources; malformed or oversized files fail closed.
- Every Code Mode invocation, child result, and final result has a 1 MiB payload ceiling. Treat a bounded or truncated response as incomplete.

## Code Mode

Code Mode is the only tool path; there is no setting to enable it. Its production surface is Hot: `bash`, `memory_search`, `memory_read`, `skill_load`, and `view_image` when available stay directly callable, while `code` reaches the complete authorized catalog and keeps cold schemas outside provider context. Direct and child calls share authorization, hooks, auditing, redaction, sandbox, and tool execution.

The routing rule is deliberately narrow:

1. Directly exposed tools handle standalone work. Hot keeps `bash`, `memory_read`, `memory_search`, `skill_load`, and `view_image` directly callable. Use direct `bash` for standalone or potentially long-running shell, file, git, package, script, and process work; never wrap a standalone direct call in Code.
2. Code handles cold tools and short chains. Use it for a tool that is not exposed directly, or when intermediate results should stay between tools. Shell work inside Code uses `tools.invoke("bash", ...)`; the complete chain must fit Code's 30-second wall-clock budget.
3. Discovery only supplies missing information. Names exposed directly or documented by a loaded skill are exact. Search when the capability or name is unknown; describe directly when the exact name is known but its input schema is not. `tools.search(query, offset?)` returns up to 20 summaries, and a non-empty search with at most three total matches includes `inputSchema`. Describe a selected search result only when it omitted its schema. An empty query lists the catalog and pages expose `hasMore` / `nextOffset`; do not enumerate it as routine discovery.

`STELLA_EVAL_CODE_TOOL_SURFACE=hot|bash|only` is an internal evaluation hook for same-binary behavior comparisons. It is unsupported as an operator rollout setting; production Code Mode uses Hot, and invalid values stop startup.

`tools.invoke(name, args?)` returns a structured ToolValue. Use `tools.text(value)` for text blocks and `tools.json(value)` for JSON text, including caught `ToolInvocationError.value`. The VM has no ambient filesystem, process, network, timer, or module-import capability; `tools.invoke("bash", ...)` uses the same sandbox as direct `bash`. Keep large content in sandbox files and use documented path inputs such as Recally `content_path`, rather than copying bytes through JavaScript. Code has fixed execution and payload limits and is process-internal capability isolation, never a general user-code sandbox.

## Database tables

Core persisted configuration lives in PostgreSQL tables with domain-specific ownership:

| Table              | Purpose                                                                       |
| ------------------ | ----------------------------------------------------------------------------- |
| `app_setting`      | Deployment-wide key-value settings                                            |
| `agent`            | Agent definitions, model selection, system prompt, and workspace policy       |
| `provider`         | Provider type, base URL, models, enabled state, and global credentials        |
| `auth_user`        | User accounts and default-Agent preference                                    |
| `channel`          | Channel instances, configuration, enabled state, and optional dedicated Agent |
| `channel_agent`    | Per-channel chat or group Agent assignment                                    |
| `ctx_agent_memory` | Per-user-per-Agent identity, constraints, profile, and memory snapshot state  |

## Multi-agent setup

Each agent has:

- A global Provider + model selection, with an optional API-only key override
- A system prompt (personality/identity)
- A system-owned resource area and per-Agent resource roots selected by scope
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
- `allow_group` -- Accept messages from groups the bot was added to; defaults to `false`, which rejects all group messages
- `allow_dm` -- Accept private messages and account linking; defaults to `true`
- `allow_unlinked_dm` -- Allow persistent restricted guest private messages; defaults to `false`
- `guest_message_limit_per_minute`, `guest_max_per_channel`, `guest_retention_days` -- Guest resource limits
- `require_mention` -- Require a bot mention in group chats; defaults to `true`
- `enable_notify` -- Allow notify output for this channel

Channel access is enforced by Stella's trusted Authority-based domain services; notification targets are resolved from linked identities.

**Discord config fields:**

- `token` -- Bot token
- `allow_group` -- Master switch for server channels; defaults to `false`, which disables all guild messages but not direct messages
- `allow_all_guilds` -- Dangerous: skip the allowlist below and accept every server this bot joined; defaults to `false`. With `allow_group` on, `allow_all_guilds` off, and every allowlist empty, no guild message is served (fail closed)
- `allowed_guild_ids` -- Guild (server) IDs allowed to use the bot; defaults to empty
- `allowed_channel_ids` -- Channel IDs allowed to use the bot, matched against a thread's own ID or its parent channel ID; defaults to empty
- `allowed_user_ids` -- Discord user IDs allowed to use the bot in server channels; defaults to empty
- `allowed_role_ids` -- Discord role IDs allowed to use the bot in server channels, matched against the message author's guild roles; defaults to empty
- `allow_dm` -- Accept account linking and linked-user direct messages; defaults to `true`
- `allow_unlinked_dm` -- Allow persistent restricted guest direct messages on the channel-bound agent; defaults to `false` and requires `allow_dm`
- `guest_message_limit_per_minute` -- Per-guest message and command limit; defaults to `10`
- `guest_max_per_channel` -- Durable guest identity cap for one channel; defaults to `1000`
- `guest_retention_days` -- Inactivity period before the daily purge deletes a guest and its sessions; defaults to `30`
- `require_mention` -- Only process guild messages that mention the bot; defaults to `true`

Guest direct messages retain and compact conversation history but have no profile, reflection, tools, skills, files, workspace, plugins, or delegation. Guests can only use `/link`, `/help`, `/new`, `/compact`, and `/abort`; linking does not merge old guest history. Guest rate, count, and retention limits reduce abuse but enabling the feature still exposes model usage publicly, so warn about cost and security and use a dedicated guest-safe agent whose base prompt contains no secrets.

**QQ config fields:** `app_id`, `app_secret`, `enable_notify`

**Feishu config fields:** `app_id`, `app_secret`, `encrypt_key`, `verification_token`, `enable_notify`, `tenant_key`, `auto_provision`, `allow_group`, `allow_dm`, `allow_unlinked_dm`, `guest_message_limit_per_minute`, `guest_max_per_channel`, `guest_retention_days`, `require_mention`

Feishu `allow_group` is one fail-closed switch for every group the bot was added to and defaults to `false`. Direct messages default on, group mentions default required, and restricted guest direct messages default off. Guest sessions use the same isolation and resource limits described for Discord.

**DingTalk config fields:** `client_id`, `client_secret`, `allow_group`, `allow_dm`, `allow_unlinked_dm`, `guest_message_limit_per_minute`, `guest_max_per_channel`, `guest_retention_days`, `require_mention`

DingTalk uses Stream mode and requires no public callback URL. `allow_group` is one fail-closed switch for group conversations and defaults to `false`. Text messages, direct messages, group @mentions, account linking, and restricted guest DMs are supported. Notifications require a temporary session Webhook learned from a recent inbound message and stop working after restart or expiry until the user or group messages the bot again.

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

## Resource files and settings

Stella's runtime resource authority is the filesystem. Four typed roots hold
complete resources:

| Scope          | Meaning                                                                  |
| -------------- | ------------------------------------------------------------------------ |
| `system`       | deployment-wide files at `$STELLA_HOME/.agents/`                         |
| `system_agent` | files shared by users of one Agent at `agents/{agent}/.agents/`          |
| `user`         | one user's files across Agents at `users/{user}/.agents/`                |
| `user_agent`   | one user's files for one Agent at `users/{user}/agents/{agent}/.agents/` |

Project Skills live under the project's `.agents/skills/` and take precedence
only for Skill selection. Complete package selection uses the four roots in the
order `user_agent > user > system_agent > system`. A narrower package replaces
the broader package; it does not merge or inherit later source edits. A copied package has no parent link. Standalone
Skills and `mcp/<name>.json` declarations are independent files.

`settings.json` is the policy surface for a resource root. It contains only
`disabled`, administrator-only `forbidden`, and `disabled_tools`. A disabled or
invalid winner masks the inherited candidate. It does not contain endpoint
configuration, secret values, database IDs, revisions, or installation state.

Runtime captures selected bytes at turn admission. File edits affect the next
turn, while the current turn keeps its fixed Skill, CLI, environment, and MCP
view. Existing connections and unchanged CLI artifacts may be reused when the
captured identity still matches. Direct shell edits are supported but are not a
multi-file transaction.

MCP declarations keep public endpoint fields and opaque credential references
in files. OAuth grants, bearer tokens, refresh tokens, and client secrets stay
in the credential store. Deleting a declaration does not disconnect OAuth. Use
Disconnect to revoke the matching locally stored grant, close its connection,
and block late callbacks or refreshes; remote provider revocation is not
guaranteed.

The runtime does not online-GC resource bytes, history, or installation caches.
MCP connections are session-owned and close with the session. The local backend
enforces its sandbox policy but cannot prove detached descendants stopped; the
`none` backend provides no reliable process isolation. Cleanup requires an
explicit maintenance action after processes and descendants are proven stopped.
A TTL or guessed PID is not sufficient.

## Operator directory layout

All paths below are relative to `$STELLA_HOME` (`~/.stella` by default).

| Operator path                        | Purpose                                                         |
| ------------------------------------ | --------------------------------------------------------------- |
| `postgres/`                          | embedded PostgreSQL data, absent with `STELLA_DATABASE_URL`     |
| `pg-runtime/`                        | downloaded embedded PostgreSQL runtime                          |
| `cache/sandbox-tmp/`                 | Docker scratch directories; retain until cleanup is proven safe |
| `agents/{agent_id}/`                 | one Agent's durable Home and system-agent resources             |
| `users/{user_id}/agents/{agent_id}/` | one user's Agent Home and user-agent resources                  |
| `users/{principal}/data/`            | principal-shared data and uploaded assets                       |
| `users/{principal}/.mise-tools/`     | principal-shared CLI artifacts and cache                        |
| `runner-scratch/runner-*`            | disposable user-less-run scratch, never durable authority       |

`{principal}` is a user ID or `group-{group_id}`. Agents should use `$HOME` for
private workspace files and `$STELLA_ASSETS_DIR` for shared deliverables, never
operator paths. `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, and
`XDG_CACHE_HOME` are principal-shared CLI state, not Agent resource roots.

PostgreSQL owner rows authorize workspace access. `WorkspaceManager` creates
missing roots only for live owners and rejects symlinks, non-directories, unsafe
IDs, and replacement of trusted roots. Run restore and root cleanup while
Stella is stopped. Physical bytes remain after destructive owner deletion, but
future access fails owner validation.

## Skills and release bundles

Release Skills are immutable files in the release bundle. `local` and `none`
use `$STELLA_HOME/bundles/<revision>`; isolating backends project the matching
bundle read-only at their execution coordinate. This bundle is separate from
mutable `system`, `system_agent`, `user`, and `user_agent` resources.

Package and standalone Skill files pass the same frontmatter and size checks.
The model receives only the selected turn view and a disposable load path, not a
complete authority root or another user's files. Historical usage and changelog
evidence remain available for reflection, but are not an online byte collector.

Use `stellad system-bundle --help` for bundle commands. Docker images must be
built from the matching Stella release; the provider refuses a mismatched
bundle. During an upgrade, the migration publishes complete files to typed
roots, records source and target digests, and stops on a conflict. It does not
silently overwrite a different file tree or use old database declarations as a
runtime fallback.

## Environment variables

Provider credentials and base URLs are stored in explicit provider rows managed through the Web UI or API; they are not read from the server environment.

| Variable      | Purpose                                     |
| ------------- | ------------------------------------------- |
| `STELLA_HOME` | stella home directory (default `~/.stella`) |

Note: The old YAML-based environment variables (`STELLA_PROVIDER`, `STELLA_MODEL`, `STELLA_TELEGRAM_TOKEN`, etc.) are no longer supported. Use the Web UI or database directly.

## Defaults

On first run, Stella creates one enabled `stella` agent with an empty model and Stella's default system prompt. Provider and channel instances are explicit administrator configuration; built-in plugin capabilities are code-defined and do not require database rows.
