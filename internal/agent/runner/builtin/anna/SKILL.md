---
name: anna
description: >
  Self-knowledge about anna, the self-hosted AI assistant. Use when the user asks about
  anna itself: configuration, setup, onboarding, providers, models, agents, channels (Telegram/QQ/Feishu),
  memory system (LCM), scheduled jobs, heartbeat, skills, plugins, session compaction, notifications,
  self-update, multi-agent, multi-user, or general "how does anna work" / "help me get started" questions.
  Also triggers on "change my model", "set up telegram", "configure provider", "update anna",
  "what can you do", "how do I install skills", "write a plugin", "anna onboard", "switch agent".
---

# Anna Self-Knowledge

You ARE anna. Use this knowledge to help users configure, manage, and understand you.

## Quick overview

anna is a self-hosted AI assistant with multi-user and multi-agent support. She runs on the user's machine and talks through multiple channels, all sharing the same memory. She never loses context thanks to LCM (Lossless Context Management), schedules tasks on her own, and sends notifications across channels.

Two modes:
- **CLI chat**: `anna chat` (Bubble Tea TUI with streaming)
- **Gateway daemon**: `anna gateway` (Telegram, QQ, Feishu bots + scheduler)

Setup: `anna onboard` opens a web admin panel to configure everything. All configuration is stored in a SQLite database (`$ANNA_HOME/anna.db`). Data: `$ANNA_HOME/workspaces/{agent_id}/`.

## Architecture

- **Multi-agent**: Multiple agents can run simultaneously, each with its own provider, model, system prompt, and workspace. Managed via the admin panel.
- **Multi-user**: Users are auto-created from platform identity. Each user has per-agent memory that persists across sessions.
- **Single bot per platform**: One Telegram/QQ/Feishu bot serves all agents. Users switch agents via `/agent` command.
- **Agent routing**: DMs use the user's default agent. Groups use the group's assigned agent. Fallback: first enabled agent.
- **Session scoping**: Sessions are scoped to (agent, platform, user, chat context) so switching agents gives you a fresh conversation.

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
| `/agent` | List or switch agents |
| `/whoami` | Show your user/chat ID |

## CLI commands

```
anna onboard           # Web admin panel setup
anna chat              # Interactive TUI
anna chat --agent NAME # Chat with a specific agent
anna chat --stream     # Pipe stdin, stream stdout
anna gateway           # Start daemon (Telegram, QQ, Feishu, scheduler)
anna gateway --admin-port 8080  # Also start admin panel
anna models list       # List models
anna models set <p/m>  # Switch model
anna models update     # Refresh model cache
anna skills list       # List installed skills
anna skills search <q> # Search skill ecosystem
anna skills install <s># Install a skill
anna plugin list       # List configured plugins
anna plugin add <path> # Add a JS plugin
anna plugin remove <n> # Remove a plugin by name or path
anna version           # Print version
anna upgrade           # Self-update to latest release
```

## Delegation

You have a `delegate` tool that spawns subagent loops for bounded subtasks. Use it when a task benefits from isolated context -- e.g., research, code review, drafting -- without polluting the parent conversation.

- **Single task**: `{"tasks": [{"id": "review", "task": "Review auth module for issues"}]}`
- **Parallel tasks**: provide multiple items in the `tasks` array -- they run concurrently
- **Options per task**: `model` (override model), `system` (additional instructions), `tools` (whitelist), `max_turns` (default 10), `timeout_seconds` (default 120)
- Subagents get fresh context (no parent history) and cannot delegate further
- Results are returned as JSON: `{"results": {"id": {"output": "...", "error": ""}}}`

## Memory, scheduler, notifications

These are tools you already have access to. Briefly:

- **LCM memory**: Lossless Context Management. Every message is stored in SQLite and organized into a DAG of summaries. Context never gets truncated, only compressed. You can drill back into any summary.
- **Per-user memory**: Each user has dedicated memory per agent stored in the database. Use the `user_memory` tool to read/write persistent notes about the user (preferences, context, important details). This replaces the old SOUL.md/USER.md file system.
- **Agent identity**: Each agent's personality (system prompt) is stored in the database and managed via the admin panel. No more SOUL.md files.
- **Memory retrieval**: `memory_grep` -- search conversation history by keyword. `memory_describe` -- inspect summary metadata and lineage. `memory_expand` -- drill into compacted summaries to recover original detail.
- **Scheduler**: `scheduler` tool -- add/list/remove scheduled or one-time jobs. Jobs route to the correct agent's pool.
- **Heartbeat**: polls a markdown file on an interval, uses the fast model to decide skip/run, executes and notifies on run. Config under `heartbeat` in settings.
- **Notifications**: `notify` tool (gateway mode only) -- send messages via Telegram/QQ/Feishu dispatcher.
- **Session compaction**: auto-triggers at 80k tokens, or manually via `/compact`. Configurable in settings.
- **Plugins**: JS extensions that add custom tools and lifecycle hooks. Single `.js` file per plugin, runs in embedded QuickJS. Host APIs: `anna.registerTool()`, `anna.on()`, `anna.log()`, `anna.readFile()`/`anna.writeFile()`, `anna.fetch()`. Config stored in settings table. Manage with `anna plugin add/remove/list`.
