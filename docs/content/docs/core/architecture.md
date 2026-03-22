---
title: Architecture
---

## System Overview

anna is structured as a set of loosely coupled packages wired together in `main.go`. The system supports multiple users and multiple agents, with routing handled per-message. The core flow:

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
cmd/anna/                             Entry point, CLI commands, service wiring

internal/
  config/
    store.go                          Store interface (DB-backed config CRUD)
    dbstore.go                        DBStore implementation (SQLite-backed)
    snapshot.go                       Read-only config snapshot per agent
    types.go                          Provider, Agent, Channel, User types

  ai/
    message.go                        Message, Content types
    model.go                          Model, ModelCost, Context types
    options.go                        RequestOptions
    events.go                         StreamEvent types
    provider.go                       Provider interface, registry, event stream
    transform.go                      Message format conversions
    providers/
      anthropic/                      Anthropic provider (Messages API)
      openai/                         OpenAI provider (Chat Completions API)
      openai-response/                OpenAI-compatible provider (Responses API)
      register_builtins.go            Auto-register all built-in providers

  agent/
    pool_manager.go                   PoolManager (map[agentID]*Pool, lazy creation)
    pool.go                           Session pool, Chat(), runner lifecycle
    pool_options.go                   PoolOption, ChatOption, With* funcs
    pool_reaper.go                    Idle/dead runner reaping
    pool_compaction.go                Session compaction orchestration
    session.go                        Per-chat session state, BuildSessionKey()
    workspace.go                      Per-agent workspace setup (dirs, identity files)
    factory.go                        Per-agent runner factory (Snapshot -> GoRunner)
    engine/
      engine.go                       Agent loop engine (multi-turn tool execution)
      continue.go                     Resume agent loop from existing history
      types.go                        LoopConfig, ToolSet, ToolFunc
      events.go                       Loop event types (AgentStarted, AssistantDelta, etc.)
      tool_execution.go               Tool call dispatch with callbacks
    runner/
      runner.go                       Runner interface, RPC types, event helpers
      gorunner.go                     GoRunner: native LLM provider calls
      prompt.go                       System prompt builder (memory, tools, context)
      skill.go                        Skill loading from agent workspace
      stream_proxy.go                 Stream proxy utilities
    tool/                             Built-in tools
      tool.go                         Tool interface and registry
      read.go                         Read file contents
      bash.go                         Execute shell commands
      write.go                        Create/overwrite files
      edit.go                         Edit file sections
      delegate.go                     Subagent delegation (parallel subtasks)
      truncate.go                     Truncate large outputs to temp files
      webfetch.go                     Fetch web page contents

  channel/
    model.go                          Channel interface, model list/switch types
    command.go                        Shared HandleCommand (/new, /compact, /model, /agent, /whoami)
    identity.go                       ResolveUser, ResolveAgent, ChatContext
    agent_command.go                  AgentCommander (list/switch agents)
    resolved.go                       ResolvedChat type (Pool + User + AgentID + SessionKey)
    util.go                           Shared utilities (SplitMessage, FormatDuration)
    notifier.go                       Notification dispatcher (multi-channel)
    notify_tool.go                    Agent notify tool
    cli/
      cli.go                          Interactive TUI entry points
      chat.go                         Bubble Tea chat model, Update()
      chat_view.go                    View(), resize(), markdown rendering
      chat_input.go                   Input handling, slash command completion
      chat_picker.go                  Model picker key handling
      command.go                      In-chat slash commands (/compact, /model, etc.)
      model.go                        TUI model switching UI
      style.go                        Terminal styling
    telegram/
      telegram.go                     Bot setup, long polling (implements channel.Channel)
      handler.go                      Message/callback handlers
      stream.go                       Streaming (draft API + edit fallback)
      render.go                       Markdown rendering
      model.go                        Paginated model picker UI
    qq/
      qq.go                           Bot setup, WebSocket (implements channel.Channel)
      handler.go                      Message handlers, command routing
      stream.go                       Streaming via QQ Stream API
      render.go                       Message chunking
      model.go                        Text-based model selection UI
    feishu/
      feishu.go                       Bot setup, WebSocket, notification backend
      handler.go                      Message event handlers
      stream.go                       Streaming via message update (edit-in-place)
      render.go                       Response splitting
      model.go                        Text-based model list

  admin/
    server.go                         Admin HTTP API server + embedded SPA
    agents.go                         Agent CRUD endpoints
    channels.go                       Channel config endpoints
    providers.go                      Provider config endpoints
    sessions.go                       Session list/detail endpoints
    settings.go                       Global settings endpoints
    users.go                          User management endpoints
    scheduler.go                      Scheduler job endpoints
    embed.go                          Embedded frontend assets
    ui/                               SPA frontend (built assets)

  db/
    embed.go                          Embedded migrations FS
    database.go                       SQLite open, WAL, migration runner
    schemas/tables/                   Schema source of truth (Atlas reads these)
    migrations/                       Atlas-generated SQL migration files
    queries/                          sqlc query definitions
    sqlc/                             Generated query code (sqlc output)

  scheduler/
    service.go                        Scheduler service (gocron/v2)
    heartbeat.go                      Heartbeat polling (decide/execute/notify)
    persistence.go                    Job JSON persistence (load/save)
    job.go                            Job and Schedule types
    tool.go                           Agent scheduler tool (add/list/remove)

  memory/
    engine.go                         Memory engine facade
    assembler.go                      Context window assembly
    compaction.go                     Leaf + condensed compaction passes
    retrieval.go                      Message search and retrieval
    summarize.go                      LLM summarization
    types.go                          Engine interface, CompactionResult, etc.
    context.go                        Context item management
    usermemory.go                     UserMemoryStore (per-user-per-agent DB access)
    tool/                             Memory agent tools (grep/describe/expand/user_memory_update)

  skills/
    tool.go                           Agent skills tool (search/install/list/remove)
    search.go                         Skills ecosystem search via skills.sh API
    install.go                        Git clone + copy install flow (go-git)
    list.go                           List installed skills
    remove.go                         Remove installed skills

  toolspec/
    toolspec.go                       Tool definition type (zero-dependency leaf package)
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

Three LLM providers are supported:

| Provider          | API                  | Use Case                                                   |
| ----------------- | -------------------- | ---------------------------------------------------------- |
| `anthropic`       | Messages API         | Claude models                                              |
| `openai`          | Chat Completions API | GPT models                                                 |
| `openai-response` | Responses API        | OpenAI-compatible services (Perplexity, Together.ai, etc.) |

Each provider implements the `ai.ProviderAdapter` interface for streaming responses and optionally `ai.ModelLister` for model discovery. All providers support multimodal input (text + images) via the `ImageContent` type, converting to their native image format (base64 blocks for Anthropic, data URI image_url for OpenAI).

## Tools

The Go runner injects tools into LLM calls. Tools follow a common interface (defined in `internal/agent/tool/`). Tool metadata uses the `toolspec.Definition` type from the zero-dependency `internal/toolspec/` leaf package, keeping domain packages decoupled from `internal/ai/`:

```go
type Tool interface {
    Definition() toolspec.Definition
    Execute(ctx context.Context, args map[string]any) (string, error)
}
```

### Built-in Tools (always available)

| Tool       | Description                                   |
| ---------- | --------------------------------------------- |
| `read`     | Read file contents with UTF-8 safe truncation |
| `bash`     | Execute shell commands                        |
| `write`    | Create/overwrite files atomically             |
| `edit`     | Edit file sections preserving context         |
| `truncate` | Truncate large outputs to temp files          |
| `delegate` | Spawn subagent loops for bounded subtasks     |
| `webfetch` | Fetch web page contents                       |

### Delegation

The `delegate` tool enables the agent to spawn child agent loops with isolated context. This is useful for focused subtasks (research, code review, drafting) that benefit from fresh context without polluting the parent conversation.

- Each child gets a fresh message history containing only the task description
- Multiple tasks run in parallel via goroutines
- The `delegate` tool is excluded from children to prevent recursion
- Child output is truncated to ~4096 tokens to avoid bloating the parent context
- Per-task options: `model` (override), `system` (additional instructions), `tools` (whitelist), `max_turns` (default 10), `timeout_seconds` (default 120)

### Extra Tools (conditionally injected)

| Tool        | Condition                         | Description                                                   |
| ----------- | --------------------------------- | ------------------------------------------------------------- |
| `memory`    | Always                            | Unified memory tool (grep/describe/expand/user_memory_update) |
| `skills`    | Always                            | Skill management (search/install/list/remove from skills.sh)  |
| `scheduler` | `scheduler.enabled: true`         | Schedule tasks (add/list/remove jobs)                         |
| `notify`    | Gateway mode + channel configured | Send notifications via dispatcher                             |

The `user_memory_update` action within the `memory` tool is a write-only operation that replaces the entire per-user-per-agent memory content in the database. These notes are always loaded into the system prompt (in the "User Memory" section) so the agent has persistent context about user preferences and important details across sessions. This replaces the previous file-based SOUL.md/USER.md approach with DB-backed `UserMemoryStore`.

## Session Lifecycle

1. Channel resolves user and agent, producing a `ResolvedChat`
2. `ResolvedChat.Pool.Chat(ctx, sessionKey, message)` is called -- message is `string` (text) or `[]ContentBlock` (multimodal)
3. Pool finds or creates a session using the scoped key `{agentID}:{platform}:{userID}:{context}`
4. Pool acquires or creates a runner for the session, configured with the agent's Snapshot
5. Runner streams events back through a channel
6. On idle timeout, runners are reaped; sessions persist to SQLite via `memory.Engine`

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

Shared command logic (`/new`, `/compact`, `/model`, `/agent`, `/whoami`) lives in `channel.HandleCommand`, which each channel delegates to for the core logic. `/model` and `/agent` are handled per-channel since they require platform-specific UI (Telegram uses inline keyboards, QQ, Feishu, and WeChat use text lists, CLI uses a TUI picker).

## Admin API

The `internal/admin/` package provides an HTTP API and embedded SPA for managing the system. Endpoints cover CRUD operations for providers, agents, channels, users, sessions, scheduler jobs, and global settings. The admin server reads and writes through `config.Store`, giving operators a web interface for configuration that was previously done via YAML files.

## Notification Flow

```
Agent notify tool      --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
Scheduler job result   --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
```

The dispatcher is created early in setup, but backends are registered later when gateway services start. The PoolManager is used to wire notification tool injection per-agent via the `ExtraToolsFactory`. See [notification-system.md](/docs/features/notification-system) for details.
