---
title: Architecture
---

## System Overview

stella is structured as a set of loosely coupled packages wired together in `main.go`. The system supports multiple users and multiple agents, with routing handled per-message. The core flow:

1. A **channel** (CLI, Telegram, QQ, Feishu, or WeChat) receives user input
2. The channel **resolves the user** (upsert by external ID + platform) and **resolves the agent** (DM default, group binding, or fallback)
3. The **PoolManager** looks up (or creates) the agent's **Pool** by agent ID
4. The **Pool** manages sessions and dispatches to a **Runner**
5. The **Go runner** calls LLM providers via `internal/ai/`, executing tools in a loop
6. Responses stream back through the channel to the user

```
Channel (CLI / Telegram / QQ / Feishu / WeChat)
    |
    v
Resolve user (identity.go)  -->  Resolve agent (identity.go)
    |
    v
PoolManager.Get(agentID)  -->  Pool (sessions + runner lifecycle)
    |
    v
Go Runner (agent loop + tools)
    |
    v
LLM Provider (Anthropic / OpenAI / OpenAI-compatible)
```

Session keys are scoped per agent: `{agentID}:{platform}:{userID}:{context}`, ensuring that the same user talking to different agents gets independent conversation histories.

## Package Layout

```
cmd/stella/              Entry point, CLI commands, service wiring
internal/
  config/              Store interface, DBStore (SQLite), Snapshot, types
  ai/                  Message/Content types, Model, Provider interface, streaming events
  agent/               PoolManager, Pool, Session, workspace setup, runner factory
    engine/            Agent loop engine (multi-turn tool execution)
    runner/            GoRunner, system prompt builder, skill loading
  channel/             Channel interface, identity resolution, slash commands, notify
    cli/               Bubble Tea TUI
    telegram/          Telegram bot
    qq/                QQ bot
    feishu/            Feishu bot
  admin/               HTTP API + embedded SPA (templ + Alpine.js + daisyUI)
  auth/                RBAC/ABAC policy engine, sessions, sandbox
  db/                  SQLite, Atlas migrations, sqlc queries
  scheduler/           gocron service, heartbeat (skill via stella scheduler CLI)
  skills/              Skills tool (search/install/list/remove via skills.sh)
pkg/
  memory/              Memory Provider interface, types, Summarizer, tool auto-generation, test helpers
  tools/               Tool interface, registry, built-in tools (read, bash, write, edit, agent)
plugins/
  memory/              Memory plugin registry + implementations
    lcm/               Lossless Context Management (default) — DAG summaries, compaction, search
    simple/            Sliding-window memory — last N messages, no summaries
  tools/               Plugin tool registry + plugin tools (mcp, webfetch)
  hooks/               Plugin hook registry + plugin hooks (rtk)
  channels/            Channel plugins (telegram, qq, feishu, weixin)
  providers/           Provider plugin registry + LLM adapters (anthropic, openai, openai-response)
```

## Configuration

Configuration is stored in SQLite and accessed through the `config.Store` interface. There is no YAML config file; all settings (providers, agents, channels, scheduler) are managed via the admin API or database.

- **Store** (`config.Store`) -- Interface for reading and writing providers, agents, channels, users, and chat-agent bindings. Implemented by `DBStore`.
- **DBStore** (`config.DBStore`) -- SQLite-backed implementation using sqlc-generated queries.
- **Snapshot** (`config.Snapshot`) -- Read-only view of configuration for a single agent. Assembled from the Store at pool creation time. Contains resolved provider credentials, model names, workspace path, system prompt, and runner settings. Passed to the runner factory and tools that need per-agent config.

## Multi-User Multi-Agent Routing

Each incoming message goes through a two-step resolution before reaching the agent loop:

1. **User resolution** (`channel.ResolveUser`) -- Upserts the sender by external platform ID, returning a `config.User` record with a stable internal user ID.
2. **Agent resolution** (`channel.ResolveAgent`) -- Determines which agent handles this message:
   - In DMs, the user's `default_agent_id` is used.
   - In group chats, a `chat_agents` binding maps `(platform, chat_id)` to an agent.
   - If neither is set, the first enabled agent is used as fallback.

The resolved user and agent are bundled into a `ResolvedChat` struct that threads through all handler and command paths. This struct holds the target `Pool`, the `User`, the `AgentID`, and the `SessionKey`.

The `PoolManager` maintains a `map[agentID]*Pool` and lazily creates pools on first access. Each pool is configured with its agent's `Snapshot` (model, credentials, workspace, system prompt) via the runner factory.

### Agent Switching

The `/agent` slash command (handled by `AgentCommander`) lets users list enabled agents and switch the active agent for their DM or group chat. In DMs this updates `default_agent_id`; in groups it updates the `chat_agents` binding. `/model` remains per-session within the current agent.

## Providers

LLM providers are plugin-based. Three built-in providers ship with Stella:

| Provider          | API                  | Use Case                                                   |
| ----------------- | -------------------- | ---------------------------------------------------------- |
| `anthropic`       | Messages API         | Claude models                                              |
| `openai`          | Chat Completions API | GPT models                                                 |
| `openai-response` | Responses API        | OpenAI-compatible services (Perplexity, Together.ai, etc.) |

Each provider implements the `ai.ProviderAdapter` interface for streaming responses and optionally `ai.ModelLister` for model discovery. All providers support multimodal input (text + images) via the `ImageContent` type, converting to their native image format (base64 blocks for Anthropic, data URI image_url for OpenAI).

Providers live in `plugins/providers/` and self-register via `init()`. Adding a new provider requires creating a package under `plugins/providers/` -- no other wiring code is needed. See [plugin-system](/docs/features/plugin-system) for details.

## Tools

The Go runner injects tools into LLM calls. Tools follow a common interface defined in `pkg/tools/`. The `tools.Definition` type is a type alias for `ai.ToolDefinition`, keeping domain packages decoupled:

```go
type Tool interface {
    Definition() tools.Definition
    Execute(ctx context.Context, args map[string]any) (string, error)
}
```

### Built-in Tools (always available)

| Tool    | Description                                   |
| ------- | --------------------------------------------- |
| `read`  | Read file contents with UTF-8 safe truncation |
| `bash`  | Execute shell commands                        |
| `write` | Create/overwrite files atomically             |
| `edit`  | Edit file sections preserving context         |
| `agent` | Spawn subagent loops for bounded subtasks     |

### Plugin Tools (toggleable via admin)

| Tool       | Description                                                      |
| ---------- | ---------------------------------------------------------------- |
| `mcp`      | Proxy configured MCP servers through one generic Stella MCP tool |
| `webfetch` | Fetch web page contents                                          |

The core local-workspace tools run through a Docker sandbox backend. The `bash` tool executes via `Session.Exec`; the `read`, `write`, and `edit` tools use `Session.ResolvePath` to obtain the host path and then call `os.*` directly. Runner startup fails closed when Docker is unavailable.

### Sandbox Architecture

The sandbox system uses Docker for process and filesystem isolation:

- **Session**: A per-run Docker container created when the runner starts, torn down on close.
- **Workspace root**: The agent workspace directory mounted into the container.
- **Working Directory**: Logical working directory inside the container, resolved via `Session.WorkingDir`.

All core tools share the same container session per runner:

```
┌─────────────────────────────────────────────────────────────┐
│                     Go Runner                               │
│  ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐           │
│  │  bash   │ │  read   │ │  write  │ │  edit   │           │
│  └────┬────┘ └────┬────┘ └────┬────┘ └────┬────┘           │
│       │           └───────────┘                             │
│       │ Exec              ResolvePath + os.*                │
│       ▼                                                     │
│  ┌──────────────────┐                                       │
│  │  sandbox.Session │                                       │
│  │  (Docker)        │                                       │
│  └──────────────────┘                                       │
└─────────────────────────────────────────────────────────────┘
```

### Platform Requirements

Docker is the only backend and is required on all platforms (Linux, macOS, Windows). The Docker daemon must be running and reachable. Stella contacts the Docker daemon at session-create time and fails closed if it is unavailable. There is no `auto`, `boxsh`, or `Relaxed` mode.

### Network Policy Configuration

Per-agent sandbox network policy is configured via the admin API or database:

| Mode        | Description                          | Use Case                              |
| ----------- | ------------------------------------ | ------------------------------------- |
| `disabled`  | No outbound network access (default) | Maximum security for untrusted code   |
| `allow_all` | Unrestricted outbound access         | Trusted agents requiring full network |

Stella validates the network mode at session-create time and fails closed if the Docker backend cannot enforce it.

### Failure Behavior

Runner startup fails closed when:

- Docker daemon is unavailable or unreachable
- Network policy configuration is invalid
- Network policy is valid but not supported by the Docker backend

This ensures that sandboxed execution is either fully functional or does not run at all, preventing silent security downgrades.

### Explicit Exception Boundary

Sandbox guarantees apply to local execution paths owned by Stella. Remote MCP transports are currently treated as a separate trust boundary:

- local MCP stdio spawning uses `Session.StartProcess`, mediated through the active runner session
- remote MCP HTTP/SSE/StreamableHTTP dialing is not currently mediated by `ToolRuntime`
- that exception is explicit, observable, and logged as `runtime.exception_path` with `exception_id=EX-009`

Plugin tools live in `plugins/tools/` and self-register via `init()`. Adding a new plugin tool requires no changes to the wiring code beyond a blank import. See [plugin-system](/docs/features/plugin-system) for the full plugin architecture.

### Agent Tool

The `agent` tool enables the agent to spawn child agent loops with isolated context. This is useful for focused subtasks (research, code review, drafting) that benefit from fresh context without polluting the parent conversation.

- Each child gets a fresh message history containing only the task description
- Multiple tasks run in parallel via goroutines with configurable concurrency
- The `agent` tool is excluded from children to prevent recursion
- Child output is truncated to ~4096 tokens to avoid bloating the parent context
- Supports presets loaded from markdown files with YAML frontmatter
- Per-task options: `preset`, `context`, `model` (override), `system` (additional instructions), `tools` (whitelist), `max_turns` (default 10), `timeout_seconds` (default 120)

### Builtin Shared Tools

| Tool        | Condition                         | Description                                                         |
| ----------- | --------------------------------- | ------------------------------------------------------------------- |
| `memory`    | Always                            | Auto-generated memory tool (actions adapt to provider capabilities) |
| `skills`    | Always                            | Skill management (search/install/list/remove from skills.sh)        |
| `scheduler` | Always                            | Schedule tasks (add/list/remove jobs)                               |
| `notify`    | Gateway mode + channel configured | Send notifications via dispatcher                                   |

The memory tool is auto-generated by `memory.BuildTool(provider)`, which inspects the provider's capabilities and produces a tool with matching actions. With the LCM provider: `status`, `search`, `describe`, `expand`, `profile_get`, `profile_update`. With the Simple provider: `status`, `profile_get`, `profile_update`. Per-user notes are managed via `profile_get`/`profile_update` and injected into the system prompt at session start.

## Session Lifecycle

1. Channel resolves user and agent, producing a `ResolvedChat`
2. `ResolvedChat.Pool.Chat(ctx, sessionKey, message)` is called -- message is `string` (text) or `[]ContentBlock` (multimodal)
3. Pool finds or creates a session using the scoped key `{agentID}:{platform}:{userID}:{context}`
4. Pool acquires or creates a runner for the session, configured with the agent's Snapshot
5. Runner streams events back through a channel
6. On idle timeout, runners are reaped; sessions persist to SQLite via `memory.Provider`

See [session-compaction.md](/docs/core/session-compaction) for history management.

## Channel Interface

All messaging platforms implement the `channel.Channel` interface:

```go
type Channel interface {
    Name() string
    Start(ctx context.Context) error
    Stop()
    Notify(ctx context.Context, n Notification) error
}
```

Shared command logic for `/new`, `/compact`, `/abort`, and `/whoami` lives in the channel coordination layer, which each channel delegates to for the core logic. `/model` and `/agent` remain per-channel because they require platform-specific UI (Telegram uses inline keyboards, QQ, Feishu, and WeChat use text lists, CLI uses a TUI picker). Chat turns are serialized per resolved Stella session so overlapping channel messages cannot race the same session history; `/abort` cancels the currently running turn for that session.

## Admin API

The `server/` package provides an HTTP API and embedded SPA for managing the system. Endpoints cover CRUD operations for providers, agents, channels, users, sessions, scheduler jobs, and global settings. The server reads and writes through `config.Store`, giving operators a web interface for configuration that was previously done via YAML files.

## Notification Flow

```
Agent notify tool      --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
Scheduler job result   --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
```

The dispatcher is created early in setup, but backends are registered later when gateway services start. The PoolManager wires per-agent notification tool injection through the `BuiltinToolsFactory`, keeping notifications in the always-on builtin tool set while external tools remain plugin-managed. See [notification-system.md](/docs/features/notification-system) for details.
