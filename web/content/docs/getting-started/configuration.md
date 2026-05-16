---
title: Configuration
---

All configuration is managed through the Web UI. Start the server with `stella server` and open [http://localhost:25678](http://localhost:25678) in your browser. Everything is stored in a single SQLite database at `~/.stella/stella.db` — there are no config files to edit.

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

You can also override the system prompt by placing a `SOUL.md` file in the agent's workspace at `~/.stella/workspaces/{agent-id}/`.

## Channels

Open the **Channels** page to connect messaging platforms. You can create multiple instances of the same platform (e.g. two Telegram bots for different agents).

Each channel instance can optionally be bound to a specific agent. If unbound, users can switch agents with the `/agent` command.

See the channel guides for setup instructions:

- [Telegram](/docs/channels/telegram)
- [QQ](/docs/channels/qq)
- [Feishu](/docs/channels/feishu)
- [WeChat](/docs/channels/weixin)

## Users

Users are created automatically when someone messages a connected channel. Each user gets isolated per-agent memory. You can manage users, roles, and permissions from the **Users** page in the Web UI.

## Runner Settings

The runner controls how the agent processes messages. You can configure these from the Web UI **Settings** page:

| Setting              | Default       | Description                                               |
| -------------------- | ------------- | --------------------------------------------------------- |
| Idle timeout         | 10 min        | Time before idle agent sessions are cleaned up            |
| Sub-agent timeout    | 15 min        | Maximum time for delegated sub-agent tasks                |
| Compaction threshold | 80,000 tokens | Auto-compress history when it exceeds this size           |
| Keep recent messages | 20            | Number of recent messages kept verbatim after compression |

## Heartbeat

Heartbeat lets Stella watch a file and act when something changes. Configure it from the Web UI **Settings** page:

- **Enabled** — turn heartbeat polling on or off
- **Interval** — how often to check (e.g. `10m`)
- **File** — path to the heartbeat file (e.g. `HEARTBEAT.md` in the agent workspace)

Heartbeat only runs in server mode (`stella server`). It uses the fast model to decide whether action is needed, keeping costs low.

## Directory Layout

All data lives under `~/.stella` (configurable via `STELLA_HOME`):

| Path                                           | Purpose                                             |
| ---------------------------------------------- | --------------------------------------------------- |
| `~/.stella/stella.db`                          | Database (config, memory, scheduler) — back this up |
| `~/.stella/workspaces/{agent-id}/`             | Per-agent workspace, skills, and overrides          |
| `~/.stella/workspaces/{agent-id}/SOUL.md`      | Optional agent personality override                 |
| `~/.stella/workspaces/{agent-id}/SYSTEM.md`    | Optional system prompt override                     |
| `~/.stella/workspaces/{agent-id}/HEARTBEAT.md` | Heartbeat instructions                              |
| `~/.stella/cache/`                             | Model cache (safe to delete)                        |

## Environment Variables

Only a small set of environment variables is recognized:

| Variable            | Description                                                                                                       |
| ------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `STELLA_HOME`       | Override the home directory (default `~/.stella`)                                                                 |
| `ANTHROPIC_API_KEY` | Fallback API key for Anthropic                                                                                    |
| `OPENAI_API_KEY`    | Fallback API key for OpenAI                                                                                       |
| `STELLA_VAULT_KEY`  | Master key for the [secret vault](/docs/guides/secrets-and-keys) — required for secrets, OAuth, and bearer tokens |

All other configuration is managed through the Web UI.
