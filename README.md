<p align="center">
  <img src="avatar.png" width="200" alt="anna" />
</p>

# anna

**Your AI assistant that never forgets.**

Anna is a self-hosted AI assistant that runs on your machine and talks to you through your terminal, Telegram, QQ, Feishu, or WeChat. She keeps every conversation in a local SQLite database, compresses old context automatically so the LLM never hits its limit, and can recover the original detail whenever she needs it.

She supports multiple agents running simultaneously, each with their own personality, model, and provider. Multiple users are handled automatically -- each person gets isolated per-agent memory that persists across sessions.

She also schedules tasks, monitors files, and sends you notifications across channels without waiting for you to ask.

## Why anna

Most AI assistants lose your context. You hit the token limit, the old messages get truncated, and the assistant forgets what you were working on. Start a new chat, re-explain everything, repeat.

Anna solves this with LCM (Lossless Context Management). As conversations grow, older messages get compressed into summaries organized in a DAG. Summaries get condensed into higher-level summaries. But the originals stay in the database. The agent has tools to search its history and drill back into any summary to pull up the full text. You can talk to Anna for weeks and she'll still know what you said on day one.

Beyond memory, there are a few other things worth calling out.

Anna meets you where you are. Terminal, Telegram, QQ, Feishu, WeChat, all sharing the same session pool and memory. Chat from your laptop in the morning, pick it up on Telegram from your phone in the evening.

She does things on her own. Tell her "remind me every morning at 9am to check my email" and she will. Built-in scheduler, heartbeat file monitoring, push notifications across whatever channels you have connected.

Run multiple agents at once. A coding assistant, a writing partner, a daily planner -- each with its own model, provider, system prompt, and isolated workspace. Switch between them with `/agent` in Telegram or `--agent` on the CLI.

Multiple users out of the box. Users are auto-created from platform identity (Telegram user ID, QQ ID, etc). Each user gets per-agent memory stored in the database, so Anna remembers different things about different people.

And the whole thing is a Go CLI with a SQLite database. Your machine, your API keys, nothing leaves your network.

Extensibility now has two layers: JavaScript plugins for lightweight hooks and tools, and subprocess runtime plugins for replaceable built-in tools and channels.

## How it works

```
Users (Telegram / QQ / Feishu / WeChat / Terminal)
 |
 |  /agent to switch agents
 v
anna (single binary, your machine)
 |
 |- Agents (multiple, each with own model/provider/personality)
 |   |- Workspace (~/.anna/workspaces/{agent-id}/skills/)
 |   |- 3-layer system prompt (SYSTEM.md -> SOUL.md -> user memory)
 |   '- LCM Memory (DAG-based context compression)
 |
 |- Admin Panel (web UI for all configuration)
 |- Scheduler (jobs, reminders, heartbeat)
 |- Skills (extensible via skills.sh)
 '- Notifications (pushes results back to you)
 |
 v
LLM Provider (Anthropic / OpenAI / any compatible API)
```

## Memory: how LCM works

The memory system stores every message in SQLite and organizes summaries into a directed acyclic graph. When the conversation gets long, older messages are grouped and summarized into leaf nodes. Groups of leaf nodes get condensed into higher-level nodes. This happens automatically.

The agent carries a unified `memory` tool with four actions:
- `grep` -- search messages and summaries by keyword
- `describe` -- inspect a summary node's metadata and lineage
- `expand` -- drill into a summary to retrieve the source content
- `user_memory_update` -- update persistent per-user notes across sessions (write-only, injected into system prompt automatically)

When the context window fills up, Anna isn't working with truncated history. She's working with compressed summaries and can pull up specifics on demand. A conversation can be a thousand messages long and she'll still find what she needs.

## Multi-agent and multi-user

Anna supports running multiple agents simultaneously. Each agent has:

- Its own model and provider configuration
- An isolated workspace at `~/.anna/workspaces/{agent-id}/skills/`
- A system prompt defined in the DB (`settings_agents.system_prompt`), overridable by placing a `SOUL.md` in the workspace
- A 3-layer system prompt: basic system prompt (overridable by `SYSTEM.md`), then agent soul (overridable by `SOUL.md`), then per-user memory from the database

Users are auto-created from platform identity. Each user gets per-agent memory stored in the `ctx_agent_memory` table, which is injected into the system prompt and updated via the `user_memory_update` action on the `memory` tool. Anna remembers different things about different people, per agent.

In Telegram, use `/agent` to switch between agents. In DMs, your default agent is remembered. In groups, the agent is set per-group. On the CLI, use `anna chat --agent <name>`.

## Channels

Five channels, all sharing the same memory:

| Channel | Connection | Streaming | Groups |
|---------|-----------|-----------|--------|
| Terminal | Local TUI (Bubble Tea) | Token-by-token | n/a |
| Telegram | Long polling, no public IP | Draft API | Mention / always / disabled |
| QQ | WebSocket | Native Stream API | Mention support |
| Feishu | WebSocket, no public IP | Edit-in-place | Mention support |
| WeChat | Long polling (iLink Bot) | Non-streaming | DM only |

One bot per platform. Agent selection is handled via the `/agent` command rather than separate bots.

Every channel supports `/new`, `/compact`, `/model`, `/agent`, `/whoami`, model switching, access control, and image input.

Lark workspace automation is no longer built in as `feishu_*` tools. If you want those workflows, install a `lark-cli` skill yourself and use it with `lark-cli` for calendar, docs, tasks, sheets, drive, and other workspace actions.

## Scheduler

You don't write cron expressions by hand. You just tell Anna what you need.

"Check the weather in Beijing every morning at 8am" creates a recurring job. "Remind me at 2:30 PM to call the dentist" creates a one-shot timer that cleans up after it fires. Jobs persist across restarts.

There's also a heartbeat mode. Anna polls a markdown file on an interval, uses a cheap fast model to decide if anything needs attention, and only spins up the main model when there's real work. Results get pushed to whatever channels you have connected.

## Identity

Anna's identity system is DB-backed. No more markdown files to manage by hand.

- **Agent soul**: stored in `settings_agents.system_prompt`, overridable by placing a `SOUL.md` in the agent's workspace (`~/.anna/workspaces/{agent-id}/`)
- **System prompt**: base instructions overridable by `SYSTEM.md` in the workspace
- **User memory**: per-user per-agent notes stored in the `ctx_agent_memory` table, injected into the system prompt automatically

The 3-layer system prompt builds up as: base system prompt, then agent soul, then user memory. Anna updates user memory over time as she learns your name, timezone, and preferences.

## Providers and models

Works with Anthropic, OpenAI, and any OpenAI-compatible API (Perplexity, Together.ai, local models via Ollama, etc). Provider configuration is managed through the admin panel.

Environment variables `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` still work as fallbacks.

Three model tiers:

- `model_strong` for hard problems
- `model` for everyday use (the default)
- `model_fast` for cheap checks and gate decisions

The heartbeat system uses the fast model to decide "skip or run" and only calls the default model when there's actual work. Keeps costs down without you having to think about it.

## Skills

Anna connects to the [skills.sh](https://skills.sh) ecosystem:

```bash
anna skills search "web scraping"
anna skills install owner/repo@skill-name
anna skills list
anna skills remove skill-name
```

Search, install, and manage skills from the CLI or mid-conversation. Each agent has its own skills directory at `~/.anna/workspaces/{agent-id}/skills/`.

## Quick start

### Install

```bash
go install github.com/vaayne/anna@latest
```

Or grab a binary from [Releases](https://github.com/vaayne/anna/releases), or self-update with `anna upgrade`.

### Set up

```bash
anna --open
```

This opens a web admin panel in your browser where you can configure everything: providers, API keys, agents, channels (Telegram, QQ, Feishu, WeChat), users, scheduled jobs, and settings. All configuration is stored in `~/.anna/anna.db`. There are no YAML config files.

### Use

```bash
anna chat                   # Terminal chat (default agent)
anna chat --agent helper    # Terminal chat with a specific agent
anna                        # Start daemon (bots + scheduler)
anna --admin-port 8080      # Start daemon with admin panel
```

`anna chat` gives you a terminal conversation. `anna` (bare command) starts all your configured channels and the scheduler. Add `--admin-port` to expose the admin panel alongside the daemon for runtime configuration.

## CLI reference

```bash
anna --open                # Open web admin panel to configure anna
anna chat                  # Interactive terminal chat
anna chat --agent <name>   # Chat with a specific agent
anna chat --stream         # Pipe stdin, stream to stdout
anna                       # Start daemon (bots + scheduler)
anna --admin-port <port>   # Start daemon with admin panel
anna models list           # List available models
anna models set <p/m>      # Switch model (e.g. openai/gpt-4o)
anna models search <q>     # Search models
anna skills search <q>     # Search skills.sh
anna skills install <s>    # Install a skill
anna plugin list           # List configured JavaScript plugins
anna plugin runtime list   # Show runtime tool/channel bindings
anna plugin runtime bind tool read tool/read
anna version               # Print version
anna upgrade               # Self-update to latest release
```

## Documentation

| Document | Description |
|----------|------------|
| [Configuration](docs/content/docs/getting-started/configuration.md) | Full config reference, admin panel, defaults |
| [Deployment](docs/content/docs/getting-started/deployment.md) | Binary install, Docker, systemd, compose |
| [Architecture](docs/content/docs/core/architecture.md) | System design, packages, providers, tools |
| [Models](docs/content/docs/core/models.md) | Tiers, CLI commands, provider setup |
| [Memory System](docs/content/docs/core/memory-system.md) | LCM deep dive, DAG structure, retrieval tools |
| [Session Compaction](docs/content/docs/core/session-compaction.md) | How context compression works |
| [Telegram](docs/content/docs/channels/telegram.md) | Bot setup, streaming, groups, access control |
| [QQ Bot](docs/content/docs/channels/qq.md) | Bot setup, webhook, streaming |
| [Feishu Bot](docs/content/docs/channels/feishu.md) | Bot setup, WebSocket, streaming |
| [WeChat Bot](docs/content/docs/channels/weixin.md) | iLink Bot setup, QR login, DM |
| [Scheduler System](docs/content/docs/features/scheduler-system.md) | Scheduler system, heartbeat, persistence |
| [Plugin System](docs/content/docs/features/plugin-system.md) | JavaScript hooks plus subprocess tool/channel plugins |
| [Notification System](docs/content/docs/features/notification-system.md) | Dispatcher, backends, routing |

## Development

```bash
mise run build       # Build binary -> bin/anna
mise run test        # Run tests with -race
mise run lint        # golangci-lint
mise run format      # gofmt + go mod tidy
```

Or: `go build -o anna . && go test -race ./...`

## License

MIT
