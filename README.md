<p align="center">
  <img src="avatar.png" width="200" alt="stella" />
</p>

# stella — Your AI assistant that never forgets

> **⚠️ Under Heavy Development** — Stella is not stable. APIs, config formats, and behavior may change without notice. Not recommended for production use.

Stella is a self-hosted AI assistant that runs on your machine and talks to you through Telegram, QQ, Feishu, WeChat, or your terminal. She remembers every conversation — even weeks later — because she compresses old context automatically and can pull up the details whenever she needs them. Your data stays local in a SQLite database. Your API keys, your machine, nothing leaves your network.

## What you can do with Stella

- **Chat from anywhere.** Talk to Stella from Telegram, QQ, Feishu, WeChat, or the terminal — all sharing the same memory. Start a conversation on your laptop, pick it up on your phone.
- **Never lose context.** Stella uses Lossless Context Management (LCM) to compress old messages into summaries while keeping the originals. She can search her history and drill into any summary to recall exactly what you said, even thousands of messages later.
- **Run multiple agents.** Set up a coding assistant, a writing partner, and a daily planner — each with its own model, personality, and memory. Switch between them with `/agent` in Telegram or `--agent` on the CLI.
- **Schedule tasks and reminders.** Tell Stella "remind me every morning at 9am to check my email" and she will. Jobs persist across restarts.
- **Extend with skills.** Browse and install community skills from [skills.sh](https://skills.sh) to teach Stella new abilities — web scraping, file monitoring, workspace automation, and more.

## Quick start

```bash
# 1. Install
brew install CherryHQ/tap/stella

# 2. Set your API key
export ANTHROPIC_API_KEY="sk-ant-..."

# 3. Start the server
stella server

# 4. Open the admin panel at http://localhost:25678
#    Add your provider and API key under Providers

# 5. Open Chat and start talking
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
stella server                           # Start server; admin panel at http://localhost:25678
stella server --port 8080               # Custom port
stella skill search <query>             # Search skills.sh
stella skill install <name>             # Install a skill
stella skill list                       # List installed skills
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

MIT
