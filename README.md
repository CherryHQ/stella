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

Extensibility uses a unified subprocess plugin model: all built-in tools and channels are plugins that can be replaced or extended without recompiling.

## How it works

```
Users (Telegram / QQ / Feishu / WeChat / Terminal)
 |
 |  /agent to switch agents
 v
anna (single binary, your machine)
 |
 |- Agents (multiple, each with own model/provider/personality)
 |   |- Workspace (~/.anna/workspaces/{agent-id}/.agents/skills/)
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
- An isolated workspace at `~/.anna/workspaces/{agent-id}/.agents/skills/`
- A system prompt defined in the DB (`settings_agents.system_prompt`), overridable by placing a `SOUL.md` in the workspace
- A 3-layer system prompt: basic system prompt (overridable by `SYSTEM.md`), then agent soul (overridable by `SOUL.md`), then per-user memory from the database

Users are auto-created from platform identity. Each user gets per-agent memory stored in the `ctx_agent_memory` table, which is injected into the system prompt and updated via the `user_memory_update` action on the `memory` tool. Anna remembers different things about different people, per agent.

In Telegram, use `/agent` to switch between agents. In DMs, your default agent is remembered. In groups, the agent is set per-group. On the CLI, use `anna --agent <name>`.

## Channels

Five channels, all sharing the same memory:

| Channel | Connection | Streaming | Groups |
|---------|-----------|-----------|--------|
| Terminal | Local TUI (Bubble Tea) | Token-by-token | n/a |
| Telegram | Long polling, no public IP | Draft API | Mention / always / disabled |
| QQ | WebSocket | Native Stream API | Mention support |
| Feishu | WebSocket, no public IP | Edit-in-place | Mention support |
| WeChat | Long polling (iLink Bot) | Non-streaming | DM only |

You can run multiple bot instances for the same platform. Leave a channel unbound to let users switch agents with `/agent`, or bind a channel instance to a dedicated agent so that bot always routes to that agent.

Every channel supports `/new`, `/compact`, `/abort`, `/model`, `/agent`, `/whoami`, model switching, access control, and image input. Channel messages are processed one-at-a-time per session, so later messages wait for the current turn to finish or be aborted.

Lark workspace automation is no longer built in as `feishu_*` tools. Instead, anna now models `mise`, `tap-web`, `gh`, and `lark-cli` as plugin-managed CLI integrations. `tap-web` and `lark` ship as generated builtin system skills, while `gh` and `lark-cli` also own their OAuth config and injected runtime env. Their binaries resolve directly from Anna-managed `PATH` entries rooted at `$ANNA_HOME/bin`. Use the builtin `lark` skill with `lark-cli` for calendar, docs, tasks, sheets, drive, and other workspace actions.

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

Search, install, and manage skills from the CLI or mid-conversation. Each agent has its own skills directory at `~/.anna/workspaces/{agent-id}/.agents/skills/`.

## Security and Sandboxing

Anna uses Docker for local agent code execution on all platforms (Linux, macOS, Windows). Docker is required; Anna fails closed when the Docker daemon is unavailable. The `bash`, `read`, `write`, and `edit` tools run inside a Docker container that isolates each session:

- Each agent session gets its own ephemeral container
- File modifications don't affect the underlying source workspace
- Path traversal outside the sandbox is blocked
- Network access is disabled by default

Per-agent network policy can be configured through the admin panel:

| Mode | Description |
|------|-------------|
| `disabled` | No outbound network (default) |
| `allow_all` | Unrestricted outbound access |

Runner startup fails closed when Docker is unavailable. Remote MCP servers are a separate trust boundary: local MCP stdio transport is runtime-mediated via `Session.StartProcess`, while remote MCP HTTP/SSE transport is not currently covered by the local sandbox boundary.

See [Architecture](/docs/core/architecture) for the session interface, execution-time mediation details, and the explicit MCP transport exception.

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
anna                         # Start daemon (bots + scheduler)
anna --port 8080             # Start daemon with admin panel
anna --host 0.0.0.0 --port 8080  # Bind admin panel to all interfaces
```

`anna` (bare command) starts all your configured channels and the scheduler. Add `--port` to expose the admin panel alongside the daemon for runtime configuration. `HOST` and `PORT` environment variables are also supported.

## CLI reference

```bash
anna --open                        # Open web admin panel to configure anna
anna                               # Start daemon (bots + scheduler)
anna --port <port>                 # Start daemon with admin panel
anna --host <host> --port <port>   # Bind admin panel to a specific host/interface
anna models list           # List available models
anna models set <p/m>      # Switch model (e.g. openai/gpt-4o)
anna models search <q>     # Search models
anna skills search <q>     # Search skills.sh
anna skills install <s>    # Install a skill
anna plugin list           # List all plugins with status
anna plugin add <path>     # Install a plugin
anna plugin remove <name>  # Remove an installed plugin
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
| [Recally](docs/content/docs/features/recally.md) | Reading assistant for saved articles, RSS feeds, and daily digests |
| [Plugin System](docs/content/docs/features/plugin-system.md) | Unified subprocess plugin model for tools and channels |
| [Notification System](docs/content/docs/features/notification-system.md) | Dispatcher, backends, routing |

## Development

```bash
mise run build             # Build binary -> bin/anna (runs pre-build deps sync)
mise run deps:sync         # Sync embedded third-party tools + generated system skills
mise run test              # Run tests
mise run format            # golangci-lint run --fix
mise run release:check     # Validate GoReleaser config
mise run release:snapshot  # Build a host-only snapshot artifact
```

If you bypass `mise`, run `go run ./cmd/builddeps sync --skills --tools` before `go build` so embedded binaries and generated system skills are up to date.

## License

MIT
