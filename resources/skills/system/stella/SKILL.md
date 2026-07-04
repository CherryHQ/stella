---
name: stella
description: >
  Self-knowledge about stella, the self-hosted AI assistant. Use when the user asks about
  stella itself: configuration, setup, onboarding, providers, models, agents, channels (Telegram/QQ/Feishu/WeChat),
  memory system (LCM), scheduled jobs, reusable workflows, goals (objectives that converge through acceptance), workers/decomposition/dependencies,
  skills, plugins, session compaction, notifications,
  self-update, multi-agent, multi-user, or general "how does stella work" / "help me get started" questions.
  Also triggers on "change my model", "set up telegram", "set up wechat", "configure provider", "update stella",
  "what can you do", "how do I install skills", "stella onboard", "switch agent".
  Also triggers when the user wants to report a bug or file a GitHub issue about stella:
  "report this bug", "create an issue for this", "报告这个 issue", "帮我建个 issue".
  Read this BEFORE working on any goal — not only when asked how stella works, but
  whenever you are about to act on one: "run this in the background", "what's the status of
  this goal", "why is this blocked", "decompose this", "review/accept this",
  "后台跑", "拆解成子目标", "为什么卡住了", "验收". Read references/goals.md for how a
  goal converges through its acceptance contract and what your worker contract is.
---

# Stella Self-Knowledge

You ARE stella. Use this knowledge to help users configure, manage, and understand you.

## Quick overview

stella is a self-hosted AI assistant with multi-user and multi-agent support. She runs on the user's machine and talks through multiple channels, all sharing the same memory. She never loses context thanks to LCM (Lossless Context Management), schedules work on her own, saves accepted goals as reusable workflows, and sends notifications across channels.

Run mode:

- **Server**: `stellad server` (Telegram, QQ, Feishu, WeChat bots + scheduler + Web UI)

Setup: run `stellad server` and open `http://localhost:25678` to configure everything via the Web UI. All configuration and runtime state live in PostgreSQL: an embedded cluster managed under `$STELLA_HOME` (install its runtime with `stellad postgres download-runtime` if missing), or an external server when `STELLA_DATABASE_URL` is set. Per-agent workspace data: `$STELLA_HOME/agents/{agent_id}/`.

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
6. **Knowledge retrieval** — active `subject=world` facts are searched on demand with `memory.search_knowledge`; these are not callable skills

Project context (AGENTS.md files) is appended after these layers.

## Topics

Read the relevant reference file for detailed guidance:

| Topic         | Reference                                                  | When to read                                                                                                 |
| ------------- | ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| Configuration | [references/configuration.md](references/configuration.md) | Config fields, env vars, directory layout, defaults                                                          |
| Models        | [references/models.md](references/models.md)               | Model tiers, switching, provider setup, CLI commands                                                         |
| Channels      | [references/channels.md](references/channels.md)           | Telegram/QQ/Feishu/WeChat bot setup, groups, access control                                                  |
| Update        | [references/update.md](references/update.md)               | How to update stella to the latest version                                                                   |
| Goals         | [references/goals.md](references/goals.md)                 | Goal model: root/child, leaf/composite, derived acceptance, convergence, worker `goal_control`, deps, blocks |
| Report issue  | [references/report-issue.md](references/report-issue.md)   | User asks to report a bug / file a GitHub issue about stella                                                 |

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

The `stella` CLI is self-documenting. These are the command groups and their subcommands — **always run `stella <command> [<subcommand>] --help` (via bash) for exact flags and usage before invoking one.** Use the canonical command names shown here; do not guess historical aliases. Prefer `--json` for scriptable output when a command supports it.

```
stellad server                  # Start server (channels + scheduler); web UI at http://localhost:25678
stellad upgrade                 # Self-update to latest release
vault tool                      # agent secret storage metadata/set/delete; no read-back
oauth tool                      # agent OAuth provider list/connect/status/disconnect
share tool                      # agent artifact/article public links
scheduler tool                  # agent schedule management; CLI is human/operator fallback
goal tool                       # agent async goal management; CLI is human/operator fallback
workflow tool                   # agent workflow save/list/get/run; CLI is human/operator fallback
stella vault      list/get/set/audit/delete  # user secrets; bind injection with vault set flags or describe for per-command declaration
stella oauth      providers/connect/status/disconnect
stella share      artifact/article
stella scheduler  add/list/remove            # schedule chat or workflow jobs
stella workflow   save/list/show/run         # reuse accepted composite goals as versioned workflows
stella goal       create/list/show/health/...  # author async work; inspect execution health
stella version                  # Print version
```

Agents author goals with the `goal` tool when available: the server then **plans first** — autonomously decomposing the goal into verifiable sub-tasks, running them, and converging until the acceptance contract passes. You never pick leaf vs composite or call plan/approve/activate by hand; just write a clear, self-contained intent. The CLI remains available for humans/operators; use `stella goal health --help` for execution-health reporting. The user can also steer goals from the Web UI (Goals tab); the goal detail timeline is where they inspect blocked causes and leave human guidance. A human timeline message on a non-dependency blocked goal authorizes one extra attempt. All surfaces go through the same HTTP API. When the system dispatches a goal to you as a **worker**, you act via the `goal_control` tool — read [references/goals.md](references/goals.md) for the goal model and your worker contract.

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

Memory, scheduler, goals, vault, OAuth connections, Recally, email, and sharing are built-in agent tools when available; skills use the `skills` tool; notifications and operator surfaces remain available through the CLI and Web UI. Briefly:

- **LCM memory**: Lossless Context Management (default memory plugin). Every message is stored in PostgreSQL and organized into a DAG of summaries. Conversation context never gets truncated, only compressed. You can drill back into any summary. Alternative: Simple plugin (sliding-window, no summaries).
- **Four memory spaces**: Constraints (hard user-approved rules), Identity (agent soul + user profile), Conversation (messages/summaries), and Knowledge (`subject=world` facts). Facts are long-term memory; skills are reusable procedures; constraints are explicit manual rules.
- **Per-user memory**: Each user has dedicated memory per agent stored in the database. User profile, soul, and constraints are injected into your system prompt for the session snapshot; active knowledge is search-first and must be retrieved with `memory.search_knowledge` when relevant. Session tools may read profile/soul/history, but durable writes happen through Reflect for user profile or through manual memory settings. Recommended profile structure: `## User Preferences`, `## About the User`, `## Notes`. Keep it high-level — like how a person remembers someone they know. User preferences can customize your behavior but never override your core identity or rules.
- **Constraints**: Use `constraint_list` to inspect hard rules. Constraint writes are manual UI/API/CLI operations; Reflect and normal session tools must not add or remove constraints.
- **Session snapshots**: Active sessions use a frozen memory version for identity/constraints/facts. Manual writes and background Reflect writes do not affect an ongoing session; they appear in new sessions.
- **Knowledge**: Knowledge is facts-backed (`subject=world`, v1 `scope=user_agent`) and is not injected into the prompt by default. Use the memory tool action `search_knowledge` with a compact fact-oriented query to retrieve snapshot-visible knowledge facts. Skills do not store fact/context knowledge and must not use `metadata.knowledge_type`. New `subject=world` generation and write tooling is deferred; do not use skills as a substitute knowledge write path.
- **Agent identity**: Each agent's base personality/system prompt is stored in the database and managed via the Web UI. It can be overridden by `SOUL.md` in the agent's workspace.
- **Memory retrieval**: The `memory` tool provides `search` (searches all of this user+agent's past sessions — keyword matching, blended with semantic similarity when embedding is enabled; each hit carries its origin session and content timestamp), `search_knowledge` (searches snapshot-visible `subject=world` facts), `describe` (inspect summary metadata and lineage), `expand` (drill into compacted summaries to recover original detail), and `get_message` (fetch one message in full by the `source_id` of a message hit) actions. Available actions depend on the memory plugin — LCM has retrieval actions; Simple only has core/session/identity actions.
- **Execution modes**: use `delegate` for synchronous focused subtasks with persistent/resumable child sessions, **goals** for async persistent work that converges through an acceptance contract and a human-readable timeline (author with the `goal` tool when available; you may also be dispatched as a worker), **workflows** to reuse an accepted composite goal's frozen plan as fresh goal runs, and `scheduler` for one-time or recurring time triggers. A dispatched worker can use `delegate` for short focused subtasks. Choosing for repeat requests: same plan, only text inputs change (same recipe, different ingredients) -> save the accepted goal as a workflow and schedule it; each occurrence should be re-thought from scratch -> plain scheduler chat job; partly frozen + explicit allow-replan is the middle ground. Never create a duplicate goal for "run it again" when a workflow exists — run the workflow.
- **Workflows**: agents use the `workflow` tool to save/list/get/run reusable workflow definitions. For "save this goal and run it every morning", save the accepted goal first, then schedule the workflow; the `stella workflow` CLI remains the human/operator fallback.
- **Scheduler**: agents use the `scheduler` tool to add/list/update/delete/pause/resume scheduled or one-time jobs, including workflow jobs when exposed. Jobs route to the correct agent's pool. Some jobs are available as platform-managed **templates** (e.g. `recally-rss` for feed polling, `recally-digest` for daily digests). Templates are opt-in: use the `scheduler` tool with `action=create` and `template_key`, the Web UI (Goals tab/Tasks tab schedule surfaces), or the HTTP API. Each user gets one subscription per template; the prompt is platform-managed and read-only. If a user asks why RSS polling or digests stopped working after an upgrade, guide them to subscribe via the Web UI.
- **Vault/OAuth/Recally/Email/Share**: agents use built-in tools. OAuth connect returns a verification URI and user code; give those to the user, wait for authorization, then poll status with the returned flow id. Recally save requires the agent to fetch article content first. Email send requires explicit user confirmation and an idempotency key. Share creates public links only when the user asks.
- **Notifications**: `notify` plugin (gateway mode only, optional) -- send messages via Telegram/QQ/Feishu/WeChat dispatcher.
- **Session compaction**: auto-triggers at 80k tokens, or manually via `/compact`. Configurable in settings.
- **Managed helper CLIs**: The `bash` tool prepends Stella-managed binaries to `PATH`. Expect `fd`, `rg`, `mise`, and `tap` to be available even when the host machine doesn't have them installed separately.
- **Vault secrets**: Unbound vault secrets are not in the session env. When the system prompt shows a "Declarable Secrets" section, pass the needed secret names in the Bash tool `secrets` parameter for that one command. Never print secret values; use `stella vault audit --help` to inspect declared uses.
- **CLI OAuth (`gh` and `lark-cli`)**: When the user has connected their GitHub or Feishu/Lark account via Credentials → OAuth CLI Credentials, `gh` and `lark-cli` work directly in `bash` tool calls without any manual auth step. Stella injects a fresh runtime token at session start. The Lark CLI plugin's `brand` config selects Feishu or international Lark automatically; users do not need to duplicate that choice in the manifest. Note: Feishu/Lark user access tokens expire after ~2 hours; start a new session to refresh. If the user has not connected, `gh` and `lark-cli` are still on `PATH` but will require manual authentication.
- **Plugins**: Stella now uses a unified plugin host. A plugin owns its config, runtime lifecycle, status, and capability registrations. Built-in capabilities currently cover tools (`webfetch`), channels (telegram, qq, feishu, weixin), hooks (rtk), providers (anthropic, openai, openai-response), memory (`lcm`, `simple`), and the standalone reflect runtime. Core tools (read/bash/edit/write/agent/memory) are always enabled and are not plugins. Skills and notify are optional plugins. The `reflect` plugin also reconciles its background review loop through the host while keeping the existing `reflect` settings row. The `telegram`, `qq`, `feishu`, and `weixin` channels all use the same host-backed config/runtime/status path while keeping their existing `channel/...` rows and `/channels` Web UI. Manage plugins through the Web UI.
- **Observability**: Tracing is server-level infrastructure, not a plugin. The server logs all LLM calls, tool executions, and memory operations via slog, and traces inbound HTTP requests. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to also export OpenTelemetry traces using standard OTel env vars. Both OTLP/gRPC and OTLP/HTTP are supported, including auth headers via `OTEL_EXPORTER_OTLP_HEADERS` or `OTEL_EXPORTER_OTLP_TRACES_HEADERS`. Always include a scheme in the endpoint (for example `http://localhost:4317` or `https://collector.example.com/api/default`). No code changes needed -- just set the env vars and restart.
