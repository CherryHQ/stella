# anna

A minimal Go CLI that acts as a local AI assistant. Uses a native Go runner that calls LLM providers (Anthropic, OpenAI, OpenAI-compatible) directly.

Two interfaces: **interactive CLI chat** and **gateway daemon** (Telegram bot, QQ bot, Feishu bot).

## Features

- Native Go runner calling LLM providers directly (Anthropic, OpenAI, OpenAI-compatible)
- Interactive CLI chat with Bubble Tea TUI and streaming responses
- Telegram bot via long polling (no webhook, no public IP needed)
  - Streaming drafts (Bot API 9.3+) for smooth animated responses
  - Image input support (send photos for vision-based analysis)
  - Group support with configurable `group_mode` (mention/always/disabled)
  - Access control via `allowed_ids`
- QQ bot via webhook (HTTP callbacks)
  - Native Stream API for progressive response delivery
  - C2C (private) and group @mention support
  - Sandbox mode for testing
- Feishu bot via WebSocket (no webhook, no public IP needed)
  - Edit-in-place streaming for progressive response delivery
  - Private (p2p) and group @mention support
  - Access control via `allowed_ids`
- Notification system with multi-backend dispatcher
- Heartbeat polling with fast-model gating and proactive notifications
- Model management CLI (`anna models list/update/set/search`)
- Tiered model config (strong/worker/fast) with runtime model switching
- Per-chat session management with persistent history (JSONL)
- Session compaction with LLM-generated summaries
- Scheduled tasks via cron with persistent job storage
- Skill management (search, install, list, remove from [skills.sh](https://skills.sh) ecosystem)
- Persistent memory (facts + journal)
- Idle runner auto-reaping (configurable timeout)
- Graceful shutdown on SIGINT/SIGTERM

## Prerequisites

- Go 1.24+
- An API key for at least one LLM provider (Anthropic, OpenAI)
- (Optional) [mise](https://mise.jdx.dev/) for task automation

## Install

```bash
go install github.com/vaayne/anna@latest
```

Or build from source:

```bash
git clone https://github.com/vaayne/anna.git
cd anna
go build -o anna .
```

## Quick Start

### CLI Chat

```bash
anna chat            # Interactive TUI
anna chat --stream   # Pipe prompt via stdin, stream to stdout
```

### Gateway (Daemon)

```bash
anna gateway
```

Starts all configured services (Telegram bot, QQ bot, Feishu bot, cron scheduler). Services are activated based on config.

### Model Management

```bash
anna models             # List available models (alias for list)
anna models list        # List all models grouped by provider
anna models update      # Fetch models from provider APIs and update cache
anna models current     # Show active provider/model
anna models set <p/m>   # Switch model (e.g. anna models set openai/gpt-4o)
anna models search <q>  # Search models by name
```

### Skill Management

```bash
anna skills              # List installed skills (alias for list)
anna skills list         # List installed skills grouped by source
anna skills list --json  # List as JSON
anna skills search <q>   # Search skills.sh ecosystem
anna skills install <s>  # Install (e.g. anna skills install owner/repo@skill-name)
anna skills remove <n>   # Remove an installed skill
```

## Configuration

Config file: `~/.anna/config.yaml` -- see [docs/configuration.md](docs/configuration.md) for full reference.

The config directory defaults to `~/.anna` and can be changed via the `ANNA_HOME` environment variable.

Minimal example to get started:

```yaml
providers:
  anthropic:
    api_key: "sk-..."

provider: anthropic
model: claude-sonnet-4-6

heartbeat:
  enabled: true
  every: 10m
  file: "HEARTBEAT.md"
```

Heartbeat runs in `anna gateway` and uses the fast model for the `skip`/`run` check before forwarding `run` cases into the reserved heartbeat session.

Or use environment variables:

```bash
export ANTHROPIC_API_KEY="sk-..."
anna chat
```

## Architecture

```
                        anna
  +-----------+      +----------------+
  | CLI Chat  |----->|                |
  +-----------+      |     Pool       |   LLM Providers
                     | (sessions +   |<--> Anthropic / OpenAI
  +-----------+      |  Go runner)   |   HTTP API
  | Telegram  |----->|                |
  | LongPoll  |      +-------+--------+
  +-----------+              |
  +-----------+       +------v---------+
  | QQ Bot    |----->|  Dispatcher   |
  | Webhook   |      | (notify tool) |--> Telegram
  +-----------+      |               |--> QQ
  +-----------+      |               |--> Feishu
  | Feishu    |----->|               |
  | WebSocket |      +-------^--------+
  +-----------+              |
  +-----------+              |
  |   Cron    |--------------+
  +-----------+
```

```
main.go                             Entry point, CLI commands, service wiring
config.go                           Config types, YAML loading, env var overrides
models.go                           Model cache, discovery, CLI model commands
agent/pool.go                       Session management, runner lifecycle
agent/session.go                    Per-chat session state
agent/store/                        Session persistence (JSONL file store)
agent/runner/                       Runner interface, GoRunner, RPC protocol
agent/runner/prompt.go              System prompt builder
agent/engine/                       Agent loop engine, tool execution, loop events
agent/tool/                         Built-in tools (read, bash, write, edit, truncate)
channel/notifier.go                 Notification dispatcher (multi-backend)
channel/notify_tool.go              Agent notify tool
channel/telegram/                   Telegram bot + streaming + notification backend
channel/qq/                         QQ bot + webhook + streaming + notification backend
channel/feishu/                     Feishu bot + WebSocket + streaming + notification backend
channel/cli/                        Interactive terminal chat (Bubble Tea TUI)
cron/                               Scheduled jobs (gocron/v2)
memory/                             Persistent memory (facts + journal)
ai/providers/                       LLM provider implementations (Anthropic, OpenAI)
ai/types/                           Shared types (Model, Message, ToolDefinition, events)
ai/stream/                          Streaming abstractions
```

## Documentation

| Document | Description |
|----------|-------------|
| [Deployment](docs/deployment.md) | Binary install, Docker, systemd, compose |
| [Configuration](docs/configuration.md) | Full config reference, env vars, defaults |
| [Architecture](docs/architecture.md) | System design, packages, providers, tools |
| [Telegram](docs/telegram.md) | Bot setup, streaming, groups, access control |
| [QQ Bot](docs/qq.md) | Bot setup, webhook, streaming, access control |
| [Feishu Bot](docs/feishu.md) | Bot setup, WebSocket, streaming, access control |
| [Models](docs/models.md) | Tiers, CLI commands, provider setup, caching |
| [Memory System](docs/memory-system.md) | Facts + journal, tool interface |
| [Cron System](docs/cron-system.md) | Scheduled tasks, job persistence |
| [Session Compaction](docs/session-compaction.md) | History compaction, token management |
| [Notification System](docs/notification-system.md) | Dispatcher, backends, agent tool |

## Development

Uses [mise](https://mise.jdx.dev/) for task automation:

```bash
mise run build          # Build binary -> bin/anna
mise run test           # Run tests with race detection
mise run lint           # go vet
mise run format         # gofmt + go mod tidy
mise run run:chat       # Build + run CLI chat
mise run run:stream     # Build + run streaming chat
mise run run:gateway    # Build + run gateway daemon
mise run clean          # Remove build artifacts
```

Or with plain Go:

```bash
go build -o anna .
go test -race ./...
```

## License

MIT
