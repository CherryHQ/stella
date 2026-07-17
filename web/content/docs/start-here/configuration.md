---
title: Configuration
---

All configuration is managed through the Web UI. Start the server with `stellad server` and open [http://localhost:25678](http://localhost:25678) in your browser. Everything is stored in PostgreSQL — either an embedded cluster managed under `~/.stella`, or an external server when you set `STELLA_DATABASE_URL`. If the embedded PostgreSQL runtime is not installed, run `stellad postgres download-runtime` once before starting the server. There are no config files to edit.

The home directory defaults to `~/.stella` and can be changed by setting the `STELLA_HOME` environment variable.

## Providers

Open the **Providers** page in the Web UI to add your AI provider credentials. Stella works with Anthropic, OpenAI, and any OpenAI-compatible API (Perplexity, Together.ai, local models via Ollama, etc.).

Environment variables `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` are supported as fallbacks when the Web UI credentials are empty.

## Agents

Open the **Agents** page to create and configure agents. Each agent has:

- **Name** — display name shown in channels and the Web UI
- **Model** — the default model (in `provider/model` format, e.g. `anthropic/claude-sonnet-4-6`)
- **Strong model** — optional, for complex reasoning tasks (falls back to the default model)
- **Fast model** — optional, for quick checks and gate decisions (falls back to the default model)
- **System prompt** — custom personality and instructions
- **Sandbox settings** — network access policy for agent code execution

You can also override the system prompt by placing a `SOUL.md` file in the agent's workspace at `~/.stella/agents/{agent-id}/`.

## Channels

Open the **Channels** page to connect messaging platforms. You can create multiple instances of the same platform (e.g. two Telegram bots for different agents).

Each channel instance can optionally be bound to a specific agent. If unbound, users can switch agents with the `/agent` command.

See the channel guides for setup instructions:

- [Telegram](/docs/channels/telegram)
- [QQ](/docs/channels/qq)
- [Feishu](/docs/channels/feishu)
- [WeChat](/docs/channels/weixin)

## Authentication

By default, you sign in to the Web UI with the username and password you created during setup.

To use an external identity provider (Zitadel, Keycloak, Auth0, or any OIDC-compatible service), configure OIDC via environment variables. See the [OIDC Authentication guide](/docs/guides/oidc-authentication) for setup instructions.

## Users

Users are created automatically when someone messages a connected channel. Each user gets isolated per-agent memory. You can manage users, roles, and permissions from the **Users** page in the Web UI.

## Runner Settings

The runner controls how the agent processes messages. You can configure these from the Web UI **Settings** page:

| Setting              | Default       | Description                                               |
| -------------------- | ------------- | --------------------------------------------------------- |
| Idle timeout         | 10 min        | Time before idle agent sessions are cleaned up            |
| Delegate timeout     | 15 min        | Maximum time for delegated tasks                          |
| Compaction threshold | 80,000 tokens | Auto-compress history when it exceeds this size           |
| Keep recent messages | 20            | Number of recent messages kept verbatim after compression |

## Directory Layout

All data lives under `~/.stella` (configurable via `STELLA_HOME`):

| Path                                    | Purpose                                                                                                                             |
| --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `~/.stella/postgres/`                   | Embedded PostgreSQL data (config, memory, scheduler) — back this up. Absent when `STELLA_DATABASE_URL` points at an external server |
| `~/.stella/pg-runtime/`                 | Downloaded embedded PostgreSQL runtime; recreate with `stellad postgres download-runtime` if deleted                                |
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
| `STELLA_BLOB_S3_ENDPOINT`     | Optional S3-compatible endpoint for the durable user-asset mirror                                                 |
| `STELLA_BLOB_S3_BUCKET`       | Bucket for mirrored user-uploaded assets; set with endpoint/access/secret or leave all unset                      |
| `STELLA_BLOB_S3_ACCESS_KEY`   | Access key for the asset mirror                                                                                   |
| `STELLA_BLOB_S3_SECRET_KEY`   | Secret key for the asset mirror                                                                                   |
| `STELLA_BLOB_S3_REGION`       | Optional S3 region                                                                                                |
| `STELLA_BLOB_S3_USE_SSL`      | Use HTTPS for S3-compatible storage; defaults to `true`                                                           |
| `ANTHROPIC_API_KEY`           | Fallback API key for Anthropic                                                                                    |
| `OPENAI_API_KEY`              | Fallback API key for OpenAI                                                                                       |
| `STELLA_VAULT_KEY`            | Master key for the [secret vault](/docs/guides/secrets-and-keys) — required for secrets, OAuth, and bearer tokens |
| `STELLA_DOCKER_SANDBOX_MODE`  | Required only for the `docker` sandbox backend: `host`, `bind`, or `volume`                                       |
| `STELLA_HOME_HOST`            | Host-side path for `STELLA_HOME`; required only when `STELLA_DOCKER_SANDBOX_MODE=bind`                            |
| `STELLA_HOME_VOLUME`          | Docker named volume for `STELLA_HOME`; required only when `STELLA_DOCKER_SANDBOX_MODE=volume`                     |
| `STELLA_REFLECT_MODE`         | Reflect writer: `legacy` (default and rollback target) or `structured`                                            |
| `STELLA_REFLECT_CURATOR_MODE` | Lifecycle curator: `shadow` (default and rollback target) or `armed`                                              |

The Reflect mode variables are read at server startup, so restart Stella after changing them. Invalid writer or curator modes stop startup. See [Deployment](/docs/start-here/deployment#roll-out-structured-reflect) for the activation and rollback procedure and [Memory internals](/docs/development/memory-internals#structured-reflect-and-curator-rollout) for the detailed mechanism.

See the [Sandbox guide](/docs/guides/sandbox) for how to choose a sandbox backend and configure Docker sandbox modes.

All other configuration is managed through the Web UI.
