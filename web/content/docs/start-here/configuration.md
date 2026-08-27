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

| Setting                 | Default       | Description                                               |
| ----------------------- | ------------- | --------------------------------------------------------- |
| Idle timeout            | 10 min        | Time before idle agent sessions are cleaned up            |
| Focused session timeout | 15 min        | Maximum time for a synchronous `session.create/send` run  |
| Compaction threshold    | 80,000 tokens | Auto-compress history when it exceeds this size           |
| Keep recent messages    | 20            | Number of recent messages kept verbatim after compression |

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

## Stella settings tool

Stella exposes a read-only `stella_settings` tool only in its direct one-to-one sessions. It provides a catalog and authorized list/get operations for Agent metadata. Ordinary Agents and group/guest sessions do not receive this tool.

Settings mutations remain a Web UI operation in this phase. The tool deliberately does not expose secrets, opaque sandbox configuration, provider credentials, or deployment-wide settings.

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
| `STELLA_AGENT_TOOL_MODE`      | Agent tool strategy: `native` (default) or opt-in `code`; invalid values stop startup                             |
| `STELLA_REFLECT_CURATOR_MODE` | Lifecycle curator: `armed` (default) or non-mutating emergency-stop mode `shadow`                                 |

Structured Reflect is the only writer. Curator mode is read at server startup, so restart Stella after changing it. Invalid curator modes stop startup. See [Deployment](/docs/start-here/deployment#structured-reflect-and-curator) for operational checks and [Memory internals](/docs/development/memory-internals#structured-reflect-and-curator) for the detailed mechanism.

## Code tool mode

Set `STELLA_AGENT_TOOL_MODE=code` before starting `stellad server` to opt an installation into Code Mode. The provider sees native `bash` for shell and file work plus one `code` tool; schemas for Stella, MCP, and other specialized tools stay behind `code` until the model searches or describes them. `bash` is never exposed through `tools.search`, `tools.describe`, or `tools.invoke`. Authorization, hooks, audit, redaction, and tool lifecycle stay on the Stella side. Set `native` or remove the variable to restore the complete native tool catalog.

Code Mode has fixed limits: 100 KiB source, 30 seconds wall time (or an earlier turn deadline), 64 MiB VM memory, 1,024 stack slots, 64 child calls, 256 log entries/256 KiB logs, and 1 MiB for invocation, child-result, and final-result payloads. This is in-process capability isolation, not a general-purpose sandbox for user-supplied code. Do not expose it as a user code-execution feature.

For a controlled native/code Harbor evaluation, use `mise run eval:loop -- --tool-mode native` or `code`. The runner verifies the active mode from `/api/status`; changing the environment after the server starts does not change an existing testbed.

See the [Sandbox guide](/docs/guides/sandbox) to choose a backend and optional OCI runtime. Custom deployment details are documented separately in that guide.

All other configuration is managed through the Web UI.
