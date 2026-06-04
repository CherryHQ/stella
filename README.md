<p align="center">
  <img src="avatar.png" width="200" alt="stella" />
</p>

<p align="center">
  English | <a href="README.zh.md">中文</a>
</p>

# Stella — AI partners for every person

> **⚠️ Under Heavy Development** — Stella is not stable. APIs, config formats, and behavior may change without notice. Not recommended for production use.

Stella gives every person an AI partner that remembers them, works through trusted tools, and shows up where they already chat.

Use Stella as a teammate at work, a manager for home routines, or a private assistant for personal projects. Each user-agent relationship has its own memory, workspace, tools, schedules, and sandbox boundaries, so Stella can understand different people without flattening everyone into one profile.

Under the hood, Stella is a multi-tenant, multi-user, multi-agent system. Each agent can have its own role, model, skills, tools, schedules, workspace, and sandbox policy. Deploy Stella where you want, use your own model API keys, and talk to her from Telegram, QQ, Feishu, WeChat, the Web UI, or the terminal.

## Why use Stella

- **Remembers each person.** Memory is scoped per user per agent, so Stella understands different people differently.
- **Works through agents.** Create agents for coding, writing, operations, family routines, research, and support.
- **Acts safely.** Agents work in dedicated workspaces with sandbox policies and controlled tool access.
- **Meets you in chat.** Telegram, QQ, Feishu, WeChat, the Web UI, and the terminal all become front doors to the same AI partner system.
- **Keeps routines moving.** Schedule reminders, recurring jobs, reading digests, and background tasks that persist across restarts and notify the right people.

## Quick start

```bash
# 1. Install
brew install CherryHQ/tap/stella

# 2. Start the server
stella server

# 3. Open the Web UI at http://localhost:25678
#    Add your provider and API key under Providers

# 4. Open Chat and start talking
```

You can also install with `go install github.com/CherryHQ/stella@latest` or download a binary from [Releases](https://github.com/CherryHQ/stella/releases).

See the [full quickstart guide](web/content/docs/getting-started/quickstart.md) for detailed steps.

## Connect your channels

All channels share the same memory. Chat from one, switch to another, and Stella picks up where you left off.

| Channel  | How to connect             | Streaming support |
| -------- | -------------------------- | ----------------- |
| Terminal | Built-in TUI               | Token-by-token    |
| Telegram | Long polling, no public IP | Yes               |
| QQ       | WebSocket                  | Yes               |
| Feishu   | WebSocket, no public IP    | Edit-in-place     |
| WeChat   | Long polling (iLink Bot)   | No                |

You can bind a channel to a specific agent, or let users switch agents with `/agent`.

## Skills

Search, install, and manage skills from the CLI:

```bash
stella skill search "web scraping"
stella skill install owner/repo@skill-name
stella skill list
```

## Documentation

| Section         | What's inside                             | Link                                            |
| --------------- | ----------------------------------------- | ----------------------------------------------- |
| Getting Started | Install, deploy, configure                | [Quick Start](/docs/getting-started/quickstart) |
| Guides          | Memory, scheduling, skills, notifications | [Guides](/docs/guides/memory)                   |
| Channels        | Telegram, QQ, Feishu, WeChat setup        | [Channels](/docs/channels/telegram)             |
| Development     | Architecture, plugins, contributing       | [Development](/docs/development/architecture)   |

## CLI reference

```bash
stella server                           # Start server; Web UI at http://localhost:25678
stella server --port 8080               # Custom port
stella skill search <query>             # Search skills.sh
stella skill install <name>             # Install a skill for the current agent
stella skill list                       # List skills visible to the current agent
stella scheduler list                   # List scheduled jobs
stella vault list                       # List stored secrets
stella version                          # Print version
stella upgrade                          # Self-update to latest release
```

## Development

Development requires [mise](https://mise.jdx.dev/). On a fresh clone:

```bash
mise run setup    # Set up dev environment and pre-commit hooks
mise run build    # Build binary
mise run test     # Run tests
mise run format   # Lint and format
```

## License

GNU Affero General Public License v3.0 or later. See [LICENSE](LICENSE).
