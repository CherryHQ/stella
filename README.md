<p align="center">
  <img src="avatar.png" width="200" alt="anna" />
</p>

# anna

**Your AI assistant that never forgets.**

Anna is a self-hosted AI assistant that runs on your machine and talks to you through your terminal, Telegram, QQ, or Feishu. She keeps every conversation in a local SQLite database, compresses old context automatically so the LLM never hits its limit, and can recover the original detail whenever she needs it.

She also schedules tasks, monitors files, and sends you notifications across channels without waiting for you to ask.

## Why anna

Most AI assistants lose your context. You hit the token limit, the old messages get truncated, and the assistant forgets what you were working on. Start a new chat, re-explain everything, repeat.

Anna solves this with LCM (Lossless Context Management). As conversations grow, older messages get compressed into summaries organized in a DAG. Summaries get condensed into higher-level summaries. But the originals stay in the database. The agent has tools to search its history and drill back into any summary to pull up the full text. You can talk to Anna for weeks and she'll still know what you said on day one.

Beyond memory, there are a few other things worth calling out.

Anna meets you where you are. Terminal, Telegram, QQ, Feishu, all sharing the same session pool and memory. Chat from your laptop in the morning, pick it up on Telegram from your phone in the evening.

She does things on her own. Tell her "remind me every morning at 9am to check my email" and she will. Built-in scheduler, heartbeat file monitoring, push notifications across whatever channels you have connected.

Two markdown files define the relationship. `SOUL.md` describes her personality, `USER.md` stores your preferences. She can edit both. Over time she learns your name, timezone, how you like things. Per-project overrides if you need them.

And the whole thing is a single Go binary with a SQLite database. Your machine, your API keys, nothing leaves your network.

## How it works

```
You
 |
 |  Talk from anywhere
 v
Terminal  /  Telegram  /  QQ  /  Feishu
 |
 v
anna (single binary, your machine)
 |
 ├── LCM Memory (SQLite, DAG-based context compression)
 ├── Scheduler (jobs, reminders, heartbeat)
 ├── Skills (extensible via skills.sh)
 └── Notifications (pushes results back to you)
 |
 v
LLM Provider (Anthropic / OpenAI / any compatible API)
```

## Memory: how LCM works

The memory system stores every message in SQLite and organizes summaries into a directed acyclic graph. When the conversation gets long, older messages are grouped and summarized into leaf nodes. Groups of leaf nodes get condensed into higher-level nodes. This happens automatically.

The agent carries three retrieval tools:
- `memory_grep` searches messages and summaries by keyword
- `memory_describe` inspects a summary node's metadata and lineage
- `memory_expand` drills into a summary to retrieve the source content

When the context window fills up, Anna isn't working with truncated history. She's working with compressed summaries and can pull up specifics on demand. A conversation can be a thousand messages long and she'll still find what she needs.

## Channels

Four channels, all sharing the same memory:

| Channel | Connection | Streaming | Groups |
|---------|-----------|-----------|--------|
| Terminal | Local TUI (Bubble Tea) | Token-by-token | n/a |
| Telegram | Long polling, no public IP | Draft API | Mention / always / disabled |
| QQ | WebSocket | Native Stream API | Mention support |
| Feishu | WebSocket, no public IP | Edit-in-place | Mention support |

Every channel supports `/new`, `/compact`, `/model`, `/whoami`, model switching, access control, and image input.

## Scheduler

You don't write cron expressions by hand. You just tell Anna what you need.

"Check the weather in Beijing every morning at 8am" creates a recurring job. "Remind me at 2:30 PM to call the dentist" creates a one-shot timer that cleans up after it fires. Jobs persist across restarts.

There's also a heartbeat mode. Anna polls a markdown file on an interval, uses a cheap fast model to decide if anything needs attention, and only spins up the main model when there's real work. Results get pushed to whatever channels you have connected.

## Identity

Two files in `$ANNA_HOME/workspace/` (`~/.anna/workspace` by default):

- `SOUL.md` defines how Anna communicates: personality, tone, values
- `USER.md` stores things about you: name, timezone, preferences, context

Anna can edit both. She picks up things you mention and writes them down for next time. You can set per-project overrides with `.agents/SOUL.md` and `.agents/USER.md` in any repo.

## Providers and models

Works with Anthropic, OpenAI, and any OpenAI-compatible API (Perplexity, Together.ai, local models via Ollama, etc).

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

Search, install, and manage skills from the CLI or mid-conversation.

## Quick start

### Install

```bash
go install github.com/vaayne/anna@latest
```

Or grab a binary from [Releases](https://github.com/vaayne/anna/releases), or self-update with `anna upgrade`.

### Set up

```bash
anna onboard
```

This opens a web UI in your browser where you can configure everything: API keys, providers, models, channels (Telegram, QQ, Feishu), and scheduled jobs. No need to edit config files by hand.

If you prefer YAML, the config lives at `$ANNA_HOME/config.yaml` (`~/.anna` by default). See [Configuration](docs/content/docs/getting-started/configuration.md) for the full reference.

### Use

```bash
anna chat            # Terminal chat
anna gateway         # Start daemon (bots + scheduler)
```

`anna chat` gives you a terminal conversation. `anna gateway` starts all your configured channels and the scheduler in the background.

## CLI reference

```bash
anna onboard           # Open web UI to configure anna
anna chat              # Interactive terminal chat
anna chat --stream     # Pipe stdin, stream to stdout
anna gateway           # Start daemon (bots + scheduler)
anna models list       # List available models
anna models set <p/m>  # Switch model (e.g. openai/gpt-4o)
anna models search <q> # Search models
anna skills search <q> # Search skills.sh
anna skills install <s># Install a skill
anna version           # Print version
anna upgrade           # Self-update to latest release
```

## Documentation

| Document | Description |
|----------|------------|
| [Configuration](docs/content/docs/getting-started/configuration.md) | Full config reference, env vars, defaults |
| [Deployment](docs/content/docs/getting-started/deployment.md) | Binary install, Docker, systemd, compose |
| [Architecture](docs/content/docs/core/architecture.md) | System design, packages, providers, tools |
| [Models](docs/content/docs/core/models.md) | Tiers, CLI commands, provider setup |
| [Memory System](docs/content/docs/core/memory-system.md) | LCM deep dive, DAG structure, retrieval tools |
| [Session Compaction](docs/content/docs/core/session-compaction.md) | How context compression works |
| [Telegram](docs/content/docs/channels/telegram.md) | Bot setup, streaming, groups, access control |
| [QQ Bot](docs/content/docs/channels/qq.md) | Bot setup, webhook, streaming |
| [Feishu Bot](docs/content/docs/channels/feishu.md) | Bot setup, WebSocket, streaming |
| [Scheduler System](docs/content/docs/features/scheduler-system.md) | Scheduler system, heartbeat, persistence |
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
