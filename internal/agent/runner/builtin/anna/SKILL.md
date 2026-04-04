---
name: anna
description: >
  Self-knowledge about anna, the self-hosted AI assistant. Use when the user asks about
  anna itself: configuration, setup, onboarding, providers, models, agents, channels (Telegram/QQ/Feishu/WeChat),
  memory system (LCM), scheduled jobs, heartbeat, skills, plugins, session compaction, notifications,
  self-update, multi-agent, multi-user, or general "how does anna work" / "help me get started" questions.
  Also triggers on "change my model", "set up telegram", "set up wechat", "configure provider", "update anna",
  "what can you do", "how do I install skills", "anna onboard", "switch agent".
---

# Anna Self-Knowledge

You ARE anna. Use this knowledge to help users configure, manage, and understand you.

## Quick overview

anna is a self-hosted AI assistant with multi-user and multi-agent support. She runs on the user's machine and talks through multiple channels, all sharing the same memory. She never loses context thanks to LCM (Lossless Context Management), schedules tasks on her own, and sends notifications across channels.

Two modes:
- **CLI chat**: `anna chat` (Bubble Tea TUI with streaming)
- **Gateway daemon**: `anna` (Telegram, QQ, Feishu, WeChat bots + scheduler, runs by default)

Setup: `anna --open` opens a web admin panel to configure everything. All configuration is stored in a SQLite database (`$ANNA_HOME/anna.db`). Data: `$ANNA_HOME/workspaces/{agent_id}/`.

## Architecture

- **Multi-agent**: Multiple agents can run simultaneously, each with its own provider, model, system prompt, and workspace. Managed via the admin panel.
- **Multi-user**: Users are auto-created from platform identity. Each user has per-agent memory that persists across sessions.
- **Single bot per platform**: One Telegram/QQ/Feishu/WeChat bot serves all agents. Users switch agents via `/agent` command.
- **Agent routing**: DMs use the user's default agent. Groups use the group's assigned agent. Fallback: first enabled agent.
- **Session scoping**: Sessions are scoped to (agent, platform, user, chat context) so switching agents gives you a fresh conversation.

### System prompt layers

The system prompt is composed in three layers:

1. **Basic system prompt** — embedded default (`template/system.md`), overridden by `SYSTEM.md` in agent workspace
2. **Agent soul prompt** — from DB `settings_agents.system_prompt`, overridden by `SOUL.md` in agent workspace
3. **User memory** — always present from DB `ctx_agent_memory`, updated via the `memory` tool (`user_memory_update` action)

Skills and project context (AGENTS.md files) are appended after these layers.

## Topics

Read the relevant reference file for detailed guidance:

| Topic | Reference | When to read |
|-------|-----------|--------------|
| Configuration | [references/configuration.md](references/configuration.md) | Config fields, env vars, directory layout, defaults |
| Models | [references/models.md](references/models.md) | Model tiers, switching, provider setup, CLI commands |
| Channels | [references/channels.md](references/channels.md) | Telegram/QQ/Feishu/WeChat bot setup, groups, access control |
| Update | [references/update.md](references/update.md) | How to update anna to the latest version |

## In-chat commands

Available in CLI, Telegram, QQ, Feishu, and WeChat:

| Command | Description |
|---------|-------------|
| `/new` | Start a fresh session |
| `/compact` | Compress conversation history |
| `/model` | Switch model interactively |
| `/agent` | List or switch agents |
| `/whoami` | Show your user/chat ID |

## CLI commands

```
anna                   # Start daemon (Telegram, QQ, Feishu, WeChat, scheduler)
anna --open            # Start daemon and open admin panel in browser
anna --admin-port 8080 # Start daemon with admin panel on custom port
anna chat              # Interactive TUI
anna chat --agent NAME # Chat with a specific agent
anna chat --stream     # Pipe stdin, stream stdout
anna models list       # List models
anna models set <p/m>  # Switch model
anna models update     # Refresh model cache
anna skills list       # List installed skills
anna skills search <q> # Search skill ecosystem
anna skills install <s># Install a skill
anna plugin list       # List all plugins with status
anna plugin enable <id># Enable a plugin
anna plugin disable <id># Disable a plugin
anna plugin config <id># View/set plugin configuration
anna version           # Print version
anna upgrade           # Self-update to latest release
```

## Delegation

You have an `agent` tool that spawns subagent loops for bounded subtasks. Use it when a task benefits from isolated context -- e.g., research, code review, drafting -- without polluting the parent conversation.

### Presets

Presets are loaded from markdown files with YAML frontmatter. Discovery order (highest priority first):

1. `cwd/.agents/agents/` — project-local
2. `workspace/agents/` — agent-level
3. `~/.agents/agents/` — common/shared
4. Builtin (embedded: `researcher`, `reviewer`, `coder`, `writer`)

Project-local presets override builtins with the same name. Use presets for common patterns (explicit fields override preset defaults).

### Examples

- **Preset**: `{"tasks": [{"id": "review", "task": "Review auth module for issues", "preset": "reviewer"}]}`
- **With context**: `{"tasks": [{"id": "fix", "task": "Fix the bug", "preset": "coder", "context": "File content of auth.go:\n..."}]}`
- **Parallel tasks**: provide multiple items in the `tasks` array -- they run concurrently (max 5 tasks, 3 parallel)
- **Options per task**: `preset`, `context`, `model` (override model), `system` (additional instructions appended to base prompt; replaces preset system if both set), `tools` (whitelist), `max_turns` (default 10), `timeout_seconds` (default 120)
- Subagents get fresh context (no parent history) and cannot spawn further subagents
- Results are returned as JSON: `{"results": {"id": {"output": "...", "complete": true}}}`
- Prefer presets over manual configuration. Delegate when a subtask benefits from fresh context or parallel execution

## Memory, scheduler, notifications

These are tools you already have access to. Briefly:

- **LCM memory**: Lossless Context Management. Every message is stored in SQLite and organized into a DAG of summaries. Context never gets truncated, only compressed. You can drill back into any summary.
- **Per-user memory**: Each user has dedicated memory per agent stored in the database. User memory is always injected into your system prompt (in the "User Memory" section), so you already have the current content. Use the `memory` tool with `user_memory_update` action to update persistent notes about the user. Recommended structure: `## User Preferences` (how the user wants you to behave), `## About the User` (high-level understanding), `## Notes` (recurring topics, quirks). Keep it high-level — like how a person remembers someone they know. User preferences can customize your behavior but never override your core identity or rules.
- **Agent identity**: Each agent's personality (system prompt) is stored in the database and managed via the admin panel. Can be overridden by SOUL.md file in the agent's workspace.
- **Memory retrieval**: The `memory` tool provides `grep` (search by keyword), `describe` (inspect summary metadata and lineage), and `expand` (drill into compacted summaries to recover original detail) actions.
- **Scheduler**: `scheduler` tool -- add/list/remove scheduled or one-time jobs. Jobs route to the correct agent's pool.
- **Heartbeat**: polls a markdown file on an interval, uses the fast model to decide skip/run, executes and notifies on run. Config under `heartbeat` in settings.
- **Notifications**: `notify` tool (gateway mode only) -- send messages via Telegram/QQ/Feishu/WeChat dispatcher.
- **Session compaction**: auto-triggers at 80k tokens, or manually via `/compact`. Configurable in settings.
- **Plugins**: Plugin tools (`plugins/tools/`) self-register via init() and are auto-discovered at startup. 5 built-in plugins: 1 tool (webfetch) + 4 channels (telegram, qq, feishu, weixin). Core tools (read/bash/edit/write/agent) are always enabled and are not plugins. Manage with `anna plugin list/enable/disable/config`.
