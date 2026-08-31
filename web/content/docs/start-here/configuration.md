---
title: Configuration
---

Most configuration is managed through the Web UI. Start the server with `stellad server` and open [http://localhost:25678](http://localhost:25678) in your browser. Everything is stored in PostgreSQL — either an embedded cluster managed under `~/.stella`, or an external server when you set `STELLA_DATABASE_URL`. If the embedded PostgreSQL runtime is not installed, run `stellad postgres download` once before starting the server. There are no config files to edit.

The home directory defaults to `~/.stella` and can be changed by setting the `STELLA_HOME` environment variable.

## Providers

Open the **Providers** page in the Web UI to add your AI provider credentials. Stella works with Anthropic, OpenAI, and any OpenAI-compatible API (Perplexity, Together.ai, local models via Ollama, etc.).

## Agents

Open the **Agents** page to create and configure agents. Each agent has:

- **Name** — display name shown in channels and the Web UI
- **Model** — the default model (in `provider/model` format, e.g. `anthropic/claude-sonnet-4-6`)
- **Strong model** — optional, for complex reasoning tasks (falls back to the default model)
- **Fast model** — optional, for quick checks and gate decisions (falls back to the default model)
- **System prompt** — custom personality and instructions
- **Sandbox settings** — network access policy for agent code execution
- **System settings tools** — off by default; an Agent manager can enable discovery only for that Agent's direct foreground one-to-one chats

You can also override the system prompt by placing a `SOUL.md` file in the agent's workspace at `~/.stella/agents/{agent-id}/`.

### Per-Agent Provider API keys

An administrator still owns each Provider's type, base URL, model catalog,
enabled state, and default API key. Enterprise provisioning can use the Agent
API to attach a different write-only API key to one Agent for a canonical
Provider ID:

```json
{
  "name": "Enterprise Coder",
  "model": "openai-main/gpt-4.1",
  "provider_credentials": [{ "provider_id": "openai-main", "api_key": "write-only" }]
}
```

- include `provider_credentials` when creating the Agent;
- use `PATCH /api/agents/{id}/provider-credentials/{providerId}` to set or
  rotate an override with `{ "api_key": "write-only" }`;
- use `DELETE` on the same resource to restore the Provider's global key;
- use the List and Get endpoints to read safe metadata. Keys are never returned.

The Agent override wins over the global key for every call that Agent makes
through the Provider. This includes image understanding when Vision selects the
same Provider. Assigned users consume the override when they use the Agent, but
only administrators and the Agent's creator can change it.

This API changes only the key. Provider endpoints, types, models, and enabled
state remain administrator-controlled. There is no per-Agent credential editor
in the Web UI yet.

## Manage selected settings in an Agent chat

Every Agent, including built-in **Stella**, starts with System settings tools
turned off. An Agent manager can enable them in **Profile → Configuration →
Advanced configuration**. The setting permits discovery only in that Agent's
signed-in, direct foreground one-to-one chats. It does not grant deployment,
domain, or administrator permissions. Group chats, guest chats, webhooks,
scheduled and delegated work, and `session_send` cannot use this capability.
Every call rechecks the saved Agent setting and your normal permissions.

An enabled Agent can manage the Agents you are allowed to use or manage, their per-Agent
tool overrides, and authorized personal or Agent-scoped Library files, managed
Skills, and MCP registrations. Administrators also get Provider metadata,
default-model and embedding settings, plugin enable/disable, and system-scoped
Library, Skill, and MCP resources. A target Agent is always checked separately.

| Settings area            | Available actions                                                                                                                               | Access                                                                                        |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| Agents                   | `settings_agent_list`, `settings_agent_get`, `settings_agent_create`, `settings_agent_update`, `settings_agent_delete`                          | Your normal Agent permissions. Workspace, sandbox, assignments, and credentials are excluded. |
| Per-Agent tool overrides | `settings_agent_tool_list`, `settings_agent_tool_update`, `settings_agent_tool_delete`                                                          | An Agent you may manage. Delete restores the normal tool decision.                            |
| Library files            | `settings_library_file_list`, `settings_library_file_get`, `settings_library_file_upload`, `settings_library_file_delete`                       | Authorized `user`/`user_agent` scopes; administrators also `system`/`system_agent`.           |
| Managed Skills           | `settings_skill_list`, `settings_skill_get`, `settings_skill_create`, `settings_skill_update`, `settings_skill_delete`                          | The same authorized scopes. This is separate from loading an installed Skill.                 |
| Providers                | `settings_provider_list`, `settings_provider_get`, `settings_provider_create`, `settings_provider_update`, `settings_provider_delete`           | Administrator only; results are redacted.                                                     |
| Default models           | `settings_default_model_get`, `settings_default_model_update`                                                                                   | Administrator only.                                                                           |
| Embedding settings       | `settings_embedding_setting_get`, `settings_embedding_setting_update`                                                                           | Administrator only.                                                                           |
| Plugins                  | `settings_plugin_list`, `settings_plugin_enable`, `settings_plugin_disable`                                                                     | Administrator only; a plugin uses its `kind` and `name`, not arbitrary configuration.         |
| MCP registrations        | `settings_mcp_server_list`, `settings_mcp_server_get`, `settings_mcp_server_create`, `settings_mcp_server_update`, `settings_mcp_server_delete` | The same authorized scopes as Library and Skills.                                             |

For an existing resource, Stella first reads its current `version`; update and
delete requests use that opaque version. If a resource changed, Stella must read
it again before deciding what to do. New Agents, Library uploads, managed
Skills, Providers, and MCP registrations return their server-selected ID and
current version.

Credentials are deliberately excluded from chat configuration. Provider and
per-Agent API keys, MCP bearer tokens, and every credential binding change
remain in the Web UI or API. A Provider created in chat has no key. A Provider
that already has a key cannot move to a different endpoint origin in chat. A
chat-created MCP registration is no-auth; a bearer-backed registration can
receive limited safe metadata changes but cannot move its endpoint origin, scope,
or owner there.

Results are bounded: Agent, Provider, Plugin, and MCP lists return at most 50
entries and report when more exist. Library listing uses pages of 1–100 entries;
Library results never return raw file bytes, and managed Skill results never
return file contents. Account, Users, Provisioning, Channels, Webhooks,
arbitrary plugin configuration, Agent workspace/sandbox settings, and
credential changes still require the Web UI or API.

## Channels

Open the **Channels** page to connect messaging platforms. You can create multiple instances of the same platform (e.g. two Telegram bots for different agents).

Each channel instance can optionally be bound to a specific agent in the Web UI.

See the channel guides for setup instructions:

- [Telegram](/docs/channels/telegram)
- [Discord](/docs/channels/discord)
- [QQ](/docs/channels/qq)
- [Feishu](/docs/channels/feishu)
- [DingTalk](/docs/channels/dingtalk)
- [WeChat](/docs/channels/weixin)

## Authentication

By default, you sign in to the Web UI with the username and password you created during setup.

To use an external identity provider (Zitadel, Keycloak, Auth0, or any OIDC-compatible service), configure OIDC via environment variables. See the [OIDC Authentication guide](/docs/guides/oidc-authentication) for setup instructions.

## Users

Users are created automatically when someone messages a connected channel. Each user gets isolated per-agent memory. You can manage users, roles, and permissions from the **Users** page in the Web UI.

## Runner Settings

The runner controls how the agent processes messages. You can configure these from the Web UI **Settings** page:

| Setting                 | Default       | Description                                                          |
| ----------------------- | ------------- | -------------------------------------------------------------------- |
| Idle timeout            | 10 min        | Time before idle agent sessions are cleaned up                       |
| Focused session timeout | 15 min        | Maximum time for a synchronous `session_create` / `session_send` run |
| Compaction threshold    | 80,000 tokens | Auto-compress history when it exceeds this size                      |
| Keep recent messages    | 20            | Number of recent messages kept verbatim after compression            |

## Directory Layout

All data lives under `~/.stella` (configurable via `STELLA_HOME`):

| Path                                    | Purpose                                                                                                                             |
| --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `~/.stella/postgres/`                   | Embedded PostgreSQL data (config, memory, scheduler) — back this up. Absent when `STELLA_DATABASE_URL` points at an external server |
| `~/.stella/pg-runtime/`                 | Downloaded embedded PostgreSQL runtime; recreate with `stellad postgres download` if deleted                                        |
| `~/.stella/agents/{agent-id}/`          | Per-agent workspace, skills, and overrides                                                                                          |
| `~/.stella/agents/{agent-id}/SOUL.md`   | Optional agent personality override                                                                                                 |
| `~/.stella/agents/{agent-id}/SYSTEM.md` | Optional system prompt override                                                                                                     |
| `~/.stella/cache/`                      | Model cache (safe to delete)                                                                                                        |

## Environment Variables

Only a small set of environment variables is recognized:

| Variable                      | Description                                                                                                       |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `STELLA_HOME`                 | Override the home directory (default `~/.stella`)                                                                 |
| `STELLA_DATABASE_URL`         | Use an external PostgreSQL database instead of the embedded cluster                                               |
| `STELLA_BLOB_S3_ENDPOINT`     | Optional S3-compatible endpoint for immutable BlobStore data                                                      |
| `STELLA_BLOB_S3_BUCKET`       | Bucket for immutable BlobStore data; set with endpoint/access/secret or leave all unset                           |
| `STELLA_BLOB_S3_ACCESS_KEY`   | Access key for immutable BlobStore data                                                                           |
| `STELLA_BLOB_S3_SECRET_KEY`   | Secret key for immutable BlobStore data                                                                           |
| `STELLA_BLOB_S3_REGION`       | Optional S3 region                                                                                                |
| `STELLA_BLOB_S3_USE_SSL`      | Use HTTPS for S3-compatible storage; defaults to `true`                                                           |
| `STELLA_VAULT_KEY`            | Master key for the [secret vault](/docs/guides/secrets-and-keys) — required for secrets, OAuth, and bearer tokens |
| `STELLA_SANDBOX_BACKEND`      | Sandbox backend: `docker`, `local` (default), or `none`                                                           |
| `STELLA_DOCKER_RUNTIME`       | Optional registered OCI runtime for Docker sandboxes, such as gVisor's `runsc`; unavailable values fail preflight |
| `STELLA_REFLECT_CURATOR_MODE` | Lifecycle curator: `armed` (default) or non-mutating emergency-stop mode `shadow`                                 |

Structured Reflect is the only writer. Curator mode is read at server startup, so restart Stella after changing it. Invalid curator modes stop startup. See [Deployment](/docs/start-here/deployment#structured-reflect-and-curator) for operational checks and [Memory internals](/docs/development/memory-internals#structured-reflect-and-curator) for the detailed mechanism.

## Code Mode

Code Mode is how every session reaches its tools; there is nothing to enable. The provider keeps a small hot set directly callable: `bash`, `memory_search`, `memory_read`, `skill_load`, and `view_image` when available. It also sees `code` whenever at least one non-bash tool is admitted. Code can search and invoke the complete authorized catalog, including those hot tools, while cold Stella, MCP, and plugin schemas stay out of the provider context. Direct and Code-child calls share authorization, hooks, audit, redaction, sandbox, and tool lifecycle.

Inside Code, `tools.search(query, offset?)` returns up to 20 tool summaries. An empty query lists the catalog; each returned page carries `hasMore` and `nextOffset`. Use `tools.describe(name)` for the exact schema and `tools.invoke(name, args?)` to call it. Child results are structured values: `tools.text(value)` joins their text blocks, while `tools.json(value)` parses JSON text. The same helpers accept a caught `ToolInvocationError.value`. Keep large content in sandbox files and use documented path inputs such as Recally `content_path`; moving a file through JavaScript wastes payload and model context.

Code Mode has fixed limits: 100 KiB source, 30 seconds wall time (or an earlier turn deadline), 64 MiB VM memory, 1,024 stack slots, 64 child calls, 256 log entries/256 KiB logs, and 1 MiB for invocation, child-result, and final-result payloads. The JavaScript runtime has no ambient filesystem, process, network, timer, or module-import capability; shell and file operations inside an orchestration use `tools.invoke("bash", ...)`. This is in-process capability isolation, not a general-purpose sandbox for user-supplied code. Do not expose it as a user code-execution feature.

See the [Sandbox guide](/docs/guides/sandbox) to choose a backend and optional OCI runtime. Custom deployment details are documented separately in that guide.

All other configuration is managed through the Web UI.
