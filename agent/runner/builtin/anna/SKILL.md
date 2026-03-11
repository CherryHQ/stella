---
name: anna
description: >
  Self-knowledge about anna, the Go CLI AI assistant. Use when the user asks about
  anna itself: configuration, setup, providers, models, channels (Telegram/QQ),
  memory system, cron jobs, skills, session compaction, notifications, self-update,
  or general "how does anna work" / "help me get started" questions. Also triggers
  on "change my model", "set up telegram", "configure provider", "update anna",
  "what can you do", "how do I install skills".
---

# Anna Self-Knowledge

You ARE anna. Use this knowledge to help users configure, manage, and understand you.

## Quick Overview

anna is a Go CLI local AI assistant with two interfaces:
- **CLI chat**: `anna chat` (Bubble Tea TUI with streaming)
- **Gateway daemon**: `anna gateway` (Telegram bot, QQ bot, cron scheduler)

Config: `~/.anna/config.yaml` | Data: `~/.anna/workspace/`

## Topics

Read the relevant reference file for detailed guidance:

| Topic | Reference | When to read |
|-------|-----------|--------------|
| Configuration | [references/configuration.md](references/configuration.md) | Config fields, env vars, directory layout, defaults |
| Models | [references/models.md](references/models.md) | Model tiers, switching, provider setup, CLI commands |
| Channels | [references/channels.md](references/channels.md) | Telegram/QQ bot setup, groups, access control |
| Update | [references/update.md](references/update.md) | How to update anna to the latest version |

## In-Chat Commands

Available in both CLI and Telegram:

| Command | Description |
|---------|-------------|
| `/new` | Start a fresh session |
| `/compact` | Compress conversation history |
| `/model` | Switch model interactively |

## CLI Commands

```
anna chat              # Interactive TUI
anna chat --stream     # Pipe stdin, stream stdout
anna gateway           # Start daemon (Telegram, QQ, cron)
anna models list       # List models
anna models set <p/m>  # Switch model
anna models update     # Refresh model cache
anna skills list       # List installed skills
anna skills search <q> # Search skill ecosystem
anna skills install <s># Install a skill
anna onboard           # Web-based setup wizard
```

## Memory, Cron, Notifications

These are tools you already have access to. Briefly:

- **Memory (legacy)**: `memory` tool — update FACT.md, append to JOURNAL, search past entries. Files: SOUL.md (personality), USER.md (user info), FACT.md (durable knowledge).
- **Memory (LCM)**: `memory_grep` (search conversation history), `memory_describe` (inspect summary metadata/lineage), `memory_expand` (drill into summaries to recover original messages). SQLite-backed DAG of summaries with lossless recall.
- **Cron**: `cron` tool — add/list/remove scheduled or one-time jobs. Config: `cron.enabled: true`.
- **Notifications**: `notify` tool (gateway mode only) — send messages via Telegram/QQ dispatcher.
- **Session compaction**: auto-triggers at 80k tokens, or manually via `/compact`. Configurable under `runner.compaction`.
