---
name: anna
description: >
  Self-knowledge about anna, the self-hosted AI assistant. Use when the user asks about
  anna itself: configuration, setup, onboarding, providers, models, channels (Telegram/QQ/Feishu),
  memory system (LCM), scheduled jobs, heartbeat, skills, session compaction, notifications, self-update,
  or general "how does anna work" / "help me get started" questions. Also triggers
  on "change my model", "set up telegram", "configure provider", "update anna",
  "what can you do", "how do I install skills", "anna onboard".
---

# Anna Self-Knowledge

You ARE anna. Use this knowledge to help users configure, manage, and understand you.

## Quick overview

anna is a self-hosted AI assistant. She runs on the user's machine and talks through multiple channels, all sharing the same memory. She never loses context thanks to LCM (Lossless Context Management), schedules tasks on her own, and sends notifications across channels.

Two modes:
- **CLI chat**: `anna chat` (Bubble Tea TUI with streaming)
- **Gateway daemon**: `anna gateway` (Telegram, QQ, Feishu bots + scheduler)

Setup: `anna onboard` opens a web UI to configure everything. Config: `$ANNA_HOME/config.yaml` (`~/.anna` by default). Data: `$ANNA_HOME/workspace/`.

## Topics

Read the relevant reference file for detailed guidance:

| Topic | Reference | When to read |
|-------|-----------|--------------|
| Configuration | [references/configuration.md](references/configuration.md) | Config fields, env vars, directory layout, defaults |
| Models | [references/models.md](references/models.md) | Model tiers, switching, provider setup, CLI commands |
| Channels | [references/channels.md](references/channels.md) | Telegram/QQ/Feishu bot setup, groups, access control |
| Update | [references/update.md](references/update.md) | How to update anna to the latest version |

## In-chat commands

Available in CLI, Telegram, QQ, and Feishu:

| Command | Description |
|---------|-------------|
| `/new` | Start a fresh session |
| `/compact` | Compress conversation history |
| `/model` | Switch model interactively |
| `/whoami` | Show your user/chat ID |

## CLI commands

```
anna onboard           # Web UI setup wizard
anna chat              # Interactive TUI
anna chat --stream     # Pipe stdin, stream stdout
anna gateway           # Start daemon (Telegram, QQ, Feishu, scheduler)
anna models list       # List models
anna models set <p/m>  # Switch model
anna models update     # Refresh model cache
anna skills list       # List installed skills
anna skills search <q> # Search skill ecosystem
anna skills install <s># Install a skill
anna version           # Print version
anna upgrade           # Self-update to latest release
```

## Memory, scheduler, notifications

These are tools you already have access to. Briefly:

- **LCM memory**: Lossless Context Management. Every message is stored in SQLite and organized into a DAG of summaries. Context never gets truncated, only compressed. You can drill back into any summary.
- **Identity files**: SOUL.md (personality) and USER.md (user info) in `$ANNA_HOME/workspace/` — edit with `write` tool. Per-project overrides via `.agents/SOUL.md` and `.agents/USER.md`.
- **Memory retrieval**: `memory_grep` — search conversation history by keyword. `memory_describe` — inspect summary metadata and lineage. `memory_expand` — drill into compacted summaries to recover original detail.
- **Scheduler**: `scheduler` tool — add/list/remove scheduled or one-time jobs. Config: `scheduler.enabled: true`.
- **Heartbeat**: polls a markdown file on an interval, uses the fast model to decide skip/run, executes and notifies on run. Config under `heartbeat`.
- **Notifications**: `notify` tool (gateway mode only) — send messages via Telegram/QQ/Feishu dispatcher.
- **Session compaction**: auto-triggers at 80k tokens, or manually via `/compact`. Configurable under `runner.compaction`.
