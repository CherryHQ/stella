<p align="center">
  <img src="avatar.png" width="200" alt="stella" />
</p>

# Stella — An AI partner for people, teams, and everyday work

> **⚠️ Under Heavy Development** — Stella is not stable. APIs, config formats, and behavior may change without notice. Not recommended for production use.

Stella is not just a chat agent. She is a long-running AI partner you can bring into your company, home, or personal workflow: a teammate who remembers project context, a household manager who keeps routines moving, or a private assistant who knows how you work.

Stella is built as a multi-tenant, multi-user, multi-agent system. Each agent can have its own role, tools, skills, schedules, sandbox policy, and workspace. Each user gets dedicated memory with each agent, so the same writing partner, coding agent, or family assistant can understand different people without flattening everyone into one profile.

Deploy Stella where you want, use your own model API keys, and talk to her from Telegram, QQ, Feishu, WeChat, the Web UI, or the terminal. Conversations, secrets, memories, and workspaces stay under your control.

## Why use Stella

- **A partner, not another chat box.** Stella is designed for recurring relationships and long-running work: company teammate, family manager, personal assistant, research partner, or coding collaborator.
- **Multi-tenant and multi-user by design.** Stella supports many users from the same deployment. Memories are scoped per user per agent, so each person gets a dedicated relationship with each agent.
- **Many agents, many roles.** Create a coding agent, writing partner, operations assistant, family scheduler, or reading researcher. Each agent has its own personality, model, skills, tools, schedules, workspace, and sandbox policy.
- **Workspaces and sandboxes built in.** Agents can work with files and tools inside dedicated workspaces, with sandbox backends controlling execution boundaries and network access.
- **One presence across every channel.** Start in Telegram, continue in the Web UI, ask from the terminal, or receive notifications in WeChat, QQ, or Feishu. The right agent can meet each user where they already are.
- **Memory with receipts.** Stella uses Lossless Context Management (LCM) to compress old conversations while keeping the originals searchable. She can drill back into the exact context behind a memory when it matters.
- **Routines that keep running.** Schedule reminders, recurring jobs, reading digests, and background tasks that persist across restarts and notify people through connected channels.

## Quick start

```bash
# 1. Install
brew install CherryHQ/tap/stella

# 2. Set your API key
export ANTHROPIC_API_KEY="sk-ant-..."

# 3. Start the server
stella server

# 4. Open the Web UI at http://localhost:25678
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
stella server                           # Start server; Web UI at http://localhost:25678
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
