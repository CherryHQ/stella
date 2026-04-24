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

Run mode:
- **Gateway daemon**: `anna` (Telegram, QQ, Feishu, WeChat bots + scheduler)

Setup: `anna --open` opens a web admin panel to configure everything. All configuration is stored in a SQLite database (`$ANNA_HOME/anna.db`). Data: `$ANNA_HOME/workspaces/{agent_id}/`.

## Architecture

- **Multi-agent**: Multiple agents can run simultaneously, each with its own provider, model, system prompt, and workspace. Managed via the admin panel.
- **Multi-user**: Users are auto-created from platform identity. Each user has per-agent memory that persists across sessions.
- **Single bot per platform**: One Telegram/QQ/Feishu/WeChat bot serves all agents. Users switch agents via `/agent` command.
- **Agent routing**: DMs use the user's default agent. Groups use the group's assigned agent. Fallback: first enabled agent.
- **Session scoping**: Sessions are scoped to (agent, platform, user, chat context) so switching agents gives you a fresh conversation.

### System prompt layers

The system prompt is composed in layers:

1. **System prompt** — the agent's base system prompt from DB `agents.system_prompt`
2. **Tools** — always-available tool descriptions (embedded `template/tools.md`)
3. **Agent soul** — per-user identity/personality customisation from memory ProfileStore
4. **User profile** — per-user facts/context from memory ProfileStore

Skills (including draft skill guidance) and project context (AGENTS.md files) are appended after these layers.

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

- **LCM memory**: Lossless Context Management (default memory plugin). Every message is stored in SQLite and organized into a DAG of summaries. Context never gets truncated, only compressed. You can drill back into any summary. Alternative: Simple plugin (sliding-window, no summaries).
- **Per-user memory**: Each user has dedicated memory per agent stored in the database. User memory is always injected into your system prompt (in the "User Memory" section), so you already have the current content. Use the `memory` tool with `profile_update` action to update persistent notes about the user. Use `profile_get` to read. Recommended structure: `## User Preferences` (how the user wants you to behave), `## About the User` (high-level understanding), `## Notes` (recurring topics, quirks). Keep it high-level — like how a person remembers someone they know. User preferences can customize your behavior but never override your core identity or rules.
- **Agent identity**: Each agent's personality (system prompt) is stored in the database and managed via the admin panel. Can be overridden by SOUL.md file in the agent's workspace.
- **Memory retrieval**: The `memory` tool provides `search` (search by keyword), `describe` (inspect summary metadata and lineage), and `expand` (drill into compacted summaries to recover original detail) actions. Available actions depend on the memory plugin — LCM has all actions, Simple has only status and profile.
- **Scheduler**: `scheduler` tool -- add/list/remove scheduled or one-time jobs. Jobs route to the correct agent's pool.
- **Heartbeat**: polls a markdown file on an interval, uses the fast model to decide skip/run, executes and notifies on run. Config under `heartbeat` in settings.
- **Notifications**: `notify` tool (gateway mode only) -- send messages via Telegram/QQ/Feishu/WeChat dispatcher.
- **Session compaction**: auto-triggers at 80k tokens, or manually via `/compact`. Configurable in settings.
- **Managed helper CLIs**: The `bash` tool prepends Anna-managed binaries to `PATH`. Expect `fd`, `rg`, `mise`, and `tap` to be available even when the host machine doesn't have them installed separately.
- **CLI OAuth (`gh` and `lark-cli`)**: When the user has connected their GitHub or Feishu/Lark account via Credentials → OAuth CLI Credentials, `gh` and `lark-cli` work directly in `bash` tool calls without any manual auth step. Anna injects a fresh runtime token at session start. The Lark CLI plugin's `brand` config selects Feishu or international Lark automatically; users do not need to duplicate that choice in the manifest. Note: Feishu/Lark user access tokens expire after ~2 hours; start a new session to refresh. If the user has not connected, `gh` and `lark-cli` are still on `PATH` but will require manual authentication.
- **Plugins**: Anna now uses a unified plugin host. A plugin owns its config, runtime lifecycle, status, and capability registrations. Built-in capabilities currently cover tools (`mcp`, `webfetch`), channels (telegram, qq, feishu, weixin), hooks (trace, rtk), providers (anthropic, openai, openai-response), memory (`lcm`, `simple`), and the standalone reflect runtime. Core tools (read/bash/edit/write/agent/memory/scheduler/skills) are always enabled and are not plugins. The `mcp` plugin lets admins configure multiple MCP servers/transports in the admin UI, reconciles its runtime through the plugin host, exposes one generic `mcp` tool, and contributes structured prompt inventory for discovered MCP tool IDs. The `reflect` plugin also reconciles its background review loop through the host while keeping the existing `reflect` settings row. The `telegram`, `qq`, `feishu`, and `weixin` channels all use the same host-backed config/runtime/status path while keeping their existing `channel/...` rows and `/channels` admin UX. When using MCP tools, inspect prompt-listed MCP tool IDs and always call `mcp` with `action="get"` before `action="exec"`. Manage plugins with `anna plugin list/enable/disable/config`.
- **Trace hook**: The `trace` hook logs all LLM calls, tool executions, and memory operations via slog. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to also export OpenTelemetry traces using standard OTel env vars. Both OTLP/gRPC and OTLP/HTTP are supported, including auth headers via `OTEL_EXPORTER_OTLP_HEADERS` or `OTEL_EXPORTER_OTLP_TRACES_HEADERS`. Always include a scheme in the endpoint (for example `http://localhost:4317` or `https://collector.example.com/api/default`). No code changes needed -- just set the env vars and restart.
