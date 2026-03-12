# Architecture

## System Overview

anna is structured as a set of loosely coupled packages wired together in `main.go`. The core flow:

1. A **channel** (CLI, Telegram, QQ, or Feishu) receives user input
2. The **Pool** manages sessions and dispatches to a **Runner**
3. The **Go runner** calls LLM providers via `ai/`, executing tools in a loop
4. Responses stream back through the channel to the user

```
Channel (CLI/Telegram/QQ/Feishu)
    |
    v
Pool (sessions + runner lifecycle)
    |
    v
Go Runner (agent loop + tools)
    |
    v
LLM Provider (Anthropic/OpenAI/OpenAI-compatible)
```

## Package Layout

```
cmd/anna/                           Entry point, CLI commands, service wiring
config/                             Config types, YAML loading, env var overrides

ai/
  message.go                        Message, Content types
  model.go                          Model, ModelCost types
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
  pool.go                           Session pool, Chat(), runner lifecycle
  pool_options.go                   PoolOption, ChatOption, With* funcs
  pool_reaper.go                    Idle/dead runner reaping
  pool_compaction.go                Session compaction orchestration
  session.go                        Per-chat session state and history
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
    skill.go                        Skill loading from ~/.anna/workspace/skills/
    stream_proxy.go                 Stream proxy utilities
  tool/                             Built-in tools
    tool.go                         Tool interface and registry
    read.go                         Read file contents
    bash.go                         Execute shell commands
    write.go                        Create/overwrite files
    edit.go                         Edit file sections
    truncate.go                     Truncate large outputs to temp files

store/
  store.go                          Session persistence (JSONL file store)
  index.go                          Session index

channel/
  model.go                          Channel interface, model list/switch types
  command.go                        Shared Commander (handles /new, /compact, /model)
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

cron/
  service.go                        Scheduler service (gocron/v2)
  persistence.go                    Job JSON persistence (load/save)
  job.go                            Job and Schedule types
  tool.go                           Agent cron tool (add/list/remove)

skills/
  tool.go                           Agent skills tool (search/install/list/remove)
  search.go                         Skills ecosystem search via skills.sh API
  install.go                        Git clone + copy install flow (go-git)
  list.go                           List installed skills
  remove.go                         Remove installed skills
```

## Providers

Three LLM providers are supported:

| Provider | API | Use Case |
|----------|-----|----------|
| `anthropic` | Messages API | Claude models |
| `openai` | Chat Completions API | GPT models |
| `openai-response` | Responses API | OpenAI-compatible services (Perplexity, Together.ai, etc.) |

Each provider implements the `ai.ProviderAdapter` interface for streaming responses and optionally `ai.ModelLister` for model discovery. All providers support multimodal input (text + images) via the `ImageContent` type, converting to their native image format (base64 blocks for Anthropic, data URI image_url for OpenAI).

## Tools

The Go runner injects tools into LLM calls. Tools follow a common interface:

```go
type Tool interface {
    Definition() ToolDefinition
    Execute(ctx context.Context, args map[string]any) (string, error)
}
```

### Built-in Tools (always available)

| Tool | Description |
|------|-------------|
| `read` | Read file contents with UTF-8 safe truncation |
| `bash` | Execute shell commands |
| `write` | Create/overwrite files atomically |
| `edit` | Edit file sections preserving context |
| `truncate` | Truncate large outputs to temp files |

### Extra Tools (conditionally injected)

| Tool | Condition | Description |
|------|-----------|-------------|
| `memory_grep` | Always | Search messages and summaries by keyword |
| `memory_describe` | Always | Inspect a summary node's metadata and lineage |
| `memory_expand` | Always | Drill into a summary to retrieve children |
| `skills` | Always | Skill management (search/install/list/remove from skills.sh) |
| `cron` | `cron.enabled: true` | Schedule tasks (add/list/remove jobs) |
| `notify` | Gateway mode + channel configured | Send notifications via dispatcher |

## Session Lifecycle

1. Channel sends message to `Pool.Chat(ctx, sessionID, message)` — message is `string` (text) or `[]ContentBlock` (multimodal, e.g. text + images)
2. Pool finds or creates a session, loading history from disk if persisted
3. Pool acquires or creates a runner for the session
4. Runner streams events back through a channel
5. On idle timeout, runners are reaped; sessions persist to JSONL

See [session-compaction.md](session-compaction.md) for history management.

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

Shared command logic (`/new`, `/compact`, `/model`) lives in `channel.Commander`, which each channel delegates to for the core logic. `/whoami` is handled per-channel since each platform returns different ID formats. Channels handle platform-specific presentation (Telegram uses inline keyboards, QQ uses text lists, CLI uses a TUI picker).

## Notification Flow

```
Agent notify tool --> Dispatcher --> Channel (Telegram/QQ/Feishu)
Cron job result   --> Dispatcher --> Channel (Telegram/QQ/Feishu)
```

The dispatcher is created early in setup, but backends are registered later when gateway services start. See [notification-system.md](notification-system.md) for details.
