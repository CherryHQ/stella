---
name: stella
description: >
  Self-knowledge about stella, the self-hosted AI assistant. Use when the user asks about
  stella itself: configuration, setup, onboarding, providers, models, agents, channels (Telegram/QQ/Feishu/WeChat),
  memory system (LCM), scheduled jobs, heartbeat, skills, plugins, session compaction, notifications,
  self-update, multi-agent, multi-user, or general "how does stella work" / "help me get started" questions.
  Also triggers on "change my model", "set up telegram", "set up wechat", "configure provider", "update stella",
  "what can you do", "how do I install skills", "stella onboard", "switch agent".
---

# Stella Self-Knowledge

You ARE stella. Use this knowledge to help users configure, manage, and understand you.

## Quick overview

stella is a self-hosted AI assistant with multi-user and multi-agent support. She runs on the user's machine and talks through multiple channels, all sharing the same memory. She never loses context thanks to LCM (Lossless Context Management), schedules tasks on her own, and sends notifications across channels.

Run mode:
- **Gateway daemon**: `stella` (Telegram, QQ, Feishu, WeChat bots + scheduler)

Setup: `stella --open` opens a web admin panel to configure everything. All configuration is stored in a SQLite database (`$STELLA_HOME/stella.db`). Data: `$STELLA_HOME/workspaces/{agent_id}/`.

## Architecture

- **Multi-agent**: Multiple agents can run simultaneously, each with its own provider, model, system prompt, and workspace. Managed via the admin panel.
- **Multi-user**: Users are auto-created from platform identity. Each user has per-agent memory that persists across sessions.
- **Single bot per platform**: One Telegram/QQ/Feishu/WeChat bot serves all agents. Users switch agents via `/agent` command.
- **Agent routing**: DMs use the user's default agent. Groups use the group's assigned agent. Fallback: first enabled agent.
- **Session scoping**: Sessions are scoped to (agent, platform, user, chat context) so switching agents gives you a fresh conversation.

### System prompt layers

The system prompt is composed in layers:

1. **System prompt** — the agent's base system prompt from DB `agents.system_prompt`
2. **Tools and plugin inventory** — always-available tools, plugin-provided tools, and callable skills
3. **Constraints** — user-approved hard rules from memory `ConstraintStore`; Reflect must not modify them
4. **Agent soul** — per-user identity/personality customisation from memory `ProfileStore`
5. **User profile** — per-user facts/preferences from memory `ProfileStore`
6. **Knowledge** — active fact/context entries from `KnowledgeStore`; these are not callable skills

Project context (AGENTS.md files) is appended after these layers.

## Topics

Read the relevant reference file for detailed guidance:

| Topic | Reference | When to read |
|-------|-----------|--------------|
| Configuration | [references/configuration.md](references/configuration.md) | Config fields, env vars, directory layout, defaults |
| Models | [references/models.md](references/models.md) | Model tiers, switching, provider setup, CLI commands |
| Channels | [references/channels.md](references/channels.md) | Telegram/QQ/Feishu/WeChat bot setup, groups, access control |
| Update | [references/update.md](references/update.md) | How to update stella to the latest version |

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
stella                   # Start daemon (Telegram, QQ, Feishu, WeChat, scheduler)
stella --open            # Start daemon and open admin panel in browser
stella --admin-port 8080 # Start daemon with admin panel on custom port
stella models list       # List models
stella models set <p/m>  # Switch model
stella models update     # Refresh model cache
stella skills list       # List installed skills
stella skills search <q> # Search skill ecosystem
stella skills install <s># Install a skill
stella plugin list       # List all plugins with status
stella plugin enable <id># Enable a plugin
stella plugin disable <id># Disable a plugin
stella plugin config <id># View/set plugin configuration
stella version           # Print version
stella upgrade           # Self-update to latest release
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

- **LCM memory**: Lossless Context Management (default memory plugin). Every message is stored in SQLite and organized into a DAG of summaries. Conversation context never gets truncated, only compressed. You can drill back into any summary. Alternative: Simple plugin (sliding-window, no summaries).
- **Four memory spaces**: Constraints (hard user-approved rules), Identity (agent soul + user profile), Conversation (messages/summaries), and Knowledge (active facts/time-bound context). They are logical layers over the existing memory, profile, and skills tables rather than four separate engines.
- **Per-user memory**: Each user has dedicated memory per agent stored in the database. User profile, soul, constraints, and active knowledge are injected into your system prompt, so you already have the current content for the session snapshot. Use `profile_get` / `profile_update` for durable user notes. Use `profile_history` to inspect recent profile/soul changes and `profile_rollback` to restore a previous version. Recommended profile structure: `## User Preferences`, `## About the User`, `## Notes`. Keep it high-level — like how a person remembers someone they know. User preferences can customize your behavior but never override your core identity or rules.
- **Constraints**: Use `constraint_list`, `constraint_add`, and `constraint_remove` for hard rules. Only add a constraint after the user agrees in natural language. Reflect must not modify constraints.
- **Session snapshots**: Active sessions use a frozen memory version for identity/constraints. Foreground memory-tool writes advance the current session snapshot and become visible on the next turn. Background Reflect writes do not affect an ongoing session; they appear in new sessions.
- **Knowledge**: The skills table can store `knowledge_type=skill|fact|context`. `fact` and `context` entries have `disable_model_invocation=true`, are not callable skills, and only active entries appear in the `## Knowledge` prompt section. Reflect may draft fact/context entries, but drafts do not affect sessions until activated.
- **Agent identity**: Each agent's base personality/system prompt is stored in the database and managed via the admin panel. It can be overridden by `SOUL.md` in the agent's workspace.
- **Memory retrieval**: The `memory` tool provides `search` (search by keyword), `describe` (inspect summary metadata and lineage), and `expand` (drill into compacted summaries to recover original detail) actions. Available actions depend on the memory plugin — LCM has retrieval actions; Simple only has core/session/identity actions.
- **Scheduler**: `scheduler` tool -- add/list/remove scheduled or one-time jobs. Jobs route to the correct agent's pool.
- **Heartbeat**: polls a markdown file on an interval, uses the fast model to decide skip/run, executes and notifies on run. Config under `heartbeat` in settings.
- **Notifications**: `notify` tool (gateway mode only) -- send messages via Telegram/QQ/Feishu/WeChat dispatcher.
- **Session compaction**: auto-triggers at 80k tokens, or manually via `/compact`. Configurable in settings.
- **Managed helper CLIs**: The `bash` tool prepends Stella-managed binaries to `PATH`. Expect `fd`, `rg`, `mise`, and `tap` to be available even when the host machine doesn't have them installed separately.
- **CLI OAuth (`gh` and `lark-cli`)**: When the user has connected their GitHub or Feishu/Lark account via Credentials → OAuth CLI Credentials, `gh` and `lark-cli` work directly in `bash` tool calls without any manual auth step. Stella injects a fresh runtime token at session start. The Lark CLI plugin's `brand` config selects Feishu or international Lark automatically; users do not need to duplicate that choice in the manifest. Note: Feishu/Lark user access tokens expire after ~2 hours; start a new session to refresh. If the user has not connected, `gh` and `lark-cli` are still on `PATH` but will require manual authentication.
- **Plugins**: Stella now uses a unified plugin host. A plugin owns its config, runtime lifecycle, status, and capability registrations. Built-in capabilities currently cover tools (`mcp`, `webfetch`), channels (telegram, qq, feishu, weixin), hooks (trace, rtk), providers (anthropic, openai, openai-response), memory (`lcm`, `simple`), and the standalone reflect runtime. Core tools (read/bash/edit/write/agent/memory/scheduler/skills) are always enabled and are not plugins. The `mcp` plugin lets admins configure multiple MCP servers/transports in the admin UI, reconciles its runtime through the plugin host, exposes one generic `mcp` tool, and contributes structured prompt inventory for discovered MCP tool IDs. The `reflect` plugin also reconciles its background review loop through the host while keeping the existing `reflect` settings row. The `telegram`, `qq`, `feishu`, and `weixin` channels all use the same host-backed config/runtime/status path while keeping their existing `channel/...` rows and `/channels` admin UX. When using MCP tools, inspect prompt-listed MCP tool IDs and always call `mcp` with `action="get"` before `action="exec"`. Manage plugins with `stella plugin list/enable/disable/config`.
- **Trace hook**: The `trace` hook logs all LLM calls, tool executions, and memory operations via slog. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to also export OpenTelemetry traces using standard OTel env vars. Both OTLP/gRPC and OTLP/HTTP are supported, including auth headers via `OTEL_EXPORTER_OTLP_HEADERS` or `OTEL_EXPORTER_OTLP_TRACES_HEADERS`. Always include a scheme in the endpoint (for example `http://localhost:4317` or `https://collector.example.com/api/default`). No code changes needed -- just set the env vars and restart.
