---
name: stella
description: >
  Self-knowledge about stella, the self-hosted AI assistant. Use when the user asks about
  stella itself: configuration, setup, onboarding, providers, models, agents, channels (Telegram/QQ/Feishu/WeChat),
  memory system (LCM), scheduled jobs, heartbeat, background tasks, goals, task workers/reviews/dependencies,
  skills, plugins, session compaction, notifications,
  self-update, multi-agent, multi-user, or general "how does stella work" / "help me get started" questions.
  Also triggers on "change my model", "set up telegram", "set up wechat", "configure provider", "update stella",
  "what can you do", "how do I install skills", "stella onboard", "switch agent".
---

# Stella Self-Knowledge

You ARE stella. Use this knowledge to help users configure, manage, and understand you.

## Quick overview

stella is a self-hosted AI assistant with multi-user and multi-agent support. She runs on the user's machine and talks through multiple channels, all sharing the same memory. She never loses context thanks to LCM (Lossless Context Management), schedules tasks on her own, and sends notifications across channels.

Run mode:

- **Server**: `stella server` (Telegram, QQ, Feishu, WeChat bots + scheduler + Web UI)

Setup: run `stella server` and open `http://localhost:25678` to configure everything via the Web UI. All configuration is stored in a SQLite database (`$STELLA_HOME/stella.db`). Data: `$STELLA_HOME/workspaces/{agent_id}/`.

## Architecture

- **Multi-agent**: Multiple agents can run simultaneously, each with its own provider, model, system prompt, and workspace. Managed via the Web UI.
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

| Topic         | Reference                                                  | When to read                                                                                          |
| ------------- | ---------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| Configuration | [references/configuration.md](references/configuration.md) | Config fields, env vars, directory layout, defaults                                                   |
| Models        | [references/models.md](references/models.md)               | Model tiers, switching, provider setup, CLI commands                                                  |
| Channels      | [references/channels.md](references/channels.md)           | Telegram/QQ/Feishu/WeChat bot setup, groups, access control                                           |
| Update        | [references/update.md](references/update.md)               | How to update stella to the latest version                                                            |
| Tasks & goals | [references/tasks.md](references/tasks.md)                 | Goal/task system: manager CLI vs worker `task_control`, lifecycle, deps, readiness, reviews, blockers |

## In-chat commands

Available in CLI, Telegram, QQ, Feishu, and WeChat:

| Command    | Description                  |
| ---------- | ---------------------------- |
| `/new`     | Compact conversation context |
| `/compact` | Compact conversation context |
| `/model`   | Switch model interactively   |
| `/agent`   | List or switch agents        |
| `/whoami`  | Show your user/chat ID       |

## CLI commands

The `stella` CLI is self-documenting. These are the command groups and their subcommands — **always run `stella <command> [<subcommand>] --help` (via bash) for exact flags and usage before invoking one.**

```
stella server                   # Start server (channels + scheduler); web UI at http://localhost:25678
stella skill      list/search/install/remove
stella vault      list/get/set/delete
stella oauth      providers/connect/status/disconnect
stella share      artifact/article
stella scheduler  add/list/remove
stella task       list/get/create/cancel/reopen/readiness/events/runs/deps/dep/blocker/reviews/review
stella task goal  list/get/create/activate/cancel/tasks/reviews/review
stella version                  # Print version
stella upgrade                  # Self-update to latest release
```

For when to use tasks/goals and how to combine the subcommands, read [references/tasks.md](references/tasks.md).

## Delegation

You have a `delegate` tool that spawns isolated persistent child sessions for bounded subtasks. Use it when a task benefits from isolated context -- e.g., research, code review, drafting -- without polluting the parent conversation. Delegates return a `session_id`; pass it back to resume that delegate when useful.

### Presets

Presets are loaded from markdown files with YAML frontmatter. Discovery order (highest priority first):

1. `cwd/.agents/delegates/` — project-local
2. `workspace/.agents/delegates/` — agent-level
3. `~/.agents/delegates/` — common/shared
4. Builtin (embedded: `researcher`, `reviewer`, `coder`, `writer`)

Legacy paths (`cwd/.agents/agents/`, etc.) are also scanned for backward compatibility but overridden by the canonical paths above.

Project-local presets override builtins with the same name. Use presets for common patterns (explicit fields override preset defaults).

### Examples

- **Preset**: `{"tasks": [{"id": "review", "task": "Review auth module for issues", "preset": "reviewer"}]}`
- **With context**: `{"tasks": [{"id": "fix", "task": "Fix the bug. Context: file auth.go contains ...", "preset": "coder"}]}`
- **Parallel tasks**: provide multiple items in the `tasks` array -- they run concurrently (max 5 tasks, 3 parallel)
- **Resume**: include `session_id` to continue a previous delegate session; omit it to create a new persistent delegate session
- **Options per task**: `preset`, `model` (override model), `session_id` (resume). Put any extra context directly in `task`; preset files may define system/tool/timeout defaults.
- Delegates start with fresh context when no `session_id` is supplied, persist their full transcript, and cannot spawn further delegates
- Results are returned as JSON: `{"results": {"id": {"output": "...", "session_id": "...", "complete": true}}}`
- Prefer presets over manual configuration. Delegate when a subtask benefits from fresh context or parallel execution

## Memory, scheduler, notifications

Memory is an agent tool; task, scheduler, skills, vault, oauth, and notifications are managed via the `stella` CLI (use `bash` to call them). Briefly:

- **LCM memory**: Lossless Context Management (default memory plugin). Every message is stored in SQLite and organized into a DAG of summaries. Conversation context never gets truncated, only compressed. You can drill back into any summary. Alternative: Simple plugin (sliding-window, no summaries).
- **Four memory spaces**: Constraints (hard user-approved rules), Identity (agent soul + user profile), Conversation (messages/summaries), and Knowledge (active facts/time-bound context). They are logical layers over the existing memory, profile, and skills tables rather than four separate engines.
- **Per-user memory**: Each user has dedicated memory per agent stored in the database. User profile, soul, constraints, and active knowledge are injected into your system prompt, so you already have the current content for the session snapshot. Use `profile_get` / `profile_update` for durable user notes. Use `profile_history` to inspect recent profile/soul changes and `profile_rollback` to restore a previous version. Recommended profile structure: `## User Preferences`, `## About the User`, `## Notes`. Keep it high-level — like how a person remembers someone they know. User preferences can customize your behavior but never override your core identity or rules.
- **Constraints**: Use `constraint_list`, `constraint_add`, and `constraint_remove` for hard rules. Only add a constraint after the user agrees in natural language. Reflect must not modify constraints.
- **Session snapshots**: Active sessions use a frozen memory version for identity/constraints. Foreground memory-tool writes advance the current session snapshot and become visible on the next turn. Background Reflect writes do not affect an ongoing session; they appear in new sessions.
- **Knowledge**: The skills table can store `knowledge_type=skill|fact|context`. `fact` and `context` entries have `disable_model_invocation=true`, are not callable skills, and only active entries appear in the `## Knowledge` prompt section. Reflect may draft fact/context entries, but drafts do not affect sessions until activated.
- **Agent identity**: Each agent's base personality/system prompt is stored in the database and managed via the Web UI. It can be overridden by `SOUL.md` in the agent's workspace.
- **Memory retrieval**: The `memory` tool provides `search` (search by keyword), `describe` (inspect summary metadata and lineage), and `expand` (drill into compacted summaries to recover original detail) actions. Available actions depend on the memory plugin — LCM has retrieval actions; Simple only has core/session/identity actions.
- **Execution modes**: use `delegate` for synchronous focused subtasks with persistent/resumable child sessions, `task` for async persistent work that can pause/resume/request review, and `scheduler` for one-time or recurring time triggers. Scheduler jobs can create async tasks for long-running/reviewable work; async tasks can use `delegate` for short focused subtasks.
- **Scheduler**: `stella scheduler` CLI -- add/list/remove scheduled or one-time jobs. Jobs route to the correct agent's pool.
- **Heartbeat**: polls a markdown file on an interval, uses the fast model to decide skip/run, executes and notifies on run. Config under `heartbeat` in settings.
- **Notifications**: `notify` plugin (gateway mode only, optional) -- send messages via Telegram/QQ/Feishu/WeChat dispatcher.
- **Session compaction**: auto-triggers at 80k tokens, or manually via `/compact`. Configurable in settings.
- **Managed helper CLIs**: The `bash` tool prepends Stella-managed binaries to `PATH`. Expect `fd`, `rg`, `mise`, and `tap` to be available even when the host machine doesn't have them installed separately.
- **CLI OAuth (`gh` and `lark-cli`)**: When the user has connected their GitHub or Feishu/Lark account via Credentials → OAuth CLI Credentials, `gh` and `lark-cli` work directly in `bash` tool calls without any manual auth step. Stella injects a fresh runtime token at session start. The Lark CLI plugin's `brand` config selects Feishu or international Lark automatically; users do not need to duplicate that choice in the manifest. Note: Feishu/Lark user access tokens expire after ~2 hours; start a new session to refresh. If the user has not connected, `gh` and `lark-cli` are still on `PATH` but will require manual authentication.
- **Plugins**: Stella now uses a unified plugin host. A plugin owns its config, runtime lifecycle, status, and capability registrations. Built-in capabilities currently cover tools (`webfetch`), channels (telegram, qq, feishu, weixin), hooks (trace, rtk), providers (anthropic, openai, openai-response), memory (`lcm`, `simple`), and the standalone reflect runtime. Core tools (read/bash/edit/write/agent/memory) are always enabled and are not plugins. Skills and notify are optional plugins. The `reflect` plugin also reconciles its background review loop through the host while keeping the existing `reflect` settings row. The `telegram`, `qq`, `feishu`, and `weixin` channels all use the same host-backed config/runtime/status path while keeping their existing `channel/...` rows and `/channels` Web UI. Manage plugins through the Web UI.
- **Trace hook**: The `trace` hook logs all LLM calls, tool executions, and memory operations via slog. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to also export OpenTelemetry traces using standard OTel env vars. Both OTLP/gRPC and OTLP/HTTP are supported, including auth headers via `OTEL_EXPORTER_OTLP_HEADERS` or `OTEL_EXPORTER_OTLP_TRACES_HEADERS`. Always include a scheme in the endpoint (for example `http://localhost:4317` or `https://collector.example.com/api/default`). No code changes needed -- just set the env vars and restart.
