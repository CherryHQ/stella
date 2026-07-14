---
title: Architecture
---

> This section is for developers contributing to Stella.

## System Overview

stella is structured as a set of loosely coupled packages wired together at startup. The system supports multiple users and multiple agents, with routing handled per message. The core flow:

1. A **channel** (CLI, Telegram, QQ, Feishu, or WeChat) receives user input.
2. The channel **resolves the user** (upsert by external ID + platform) and **resolves the agent** (DM default, group binding, or fallback).
3. The **ServiceManager** looks up the agent's `agent.Service` by agent ID.
4. `agent.Service` resolves session intent through `session.Registry`.
5. `runtime.Runtime` executes the turn through a cached **Runner**.
6. The **Runner** calls LLM providers and executes tools in a loop.
7. Responses stream back through the channel to the user.

```
Channel (CLI / Telegram / QQ / Feishu / WeChat)
    |
    v
Resolve user  -->  Resolve agent
    |
    v
ServiceManager.GetService(agentID)  -->  agent.Service
    |                                      |
    |                                      +--> session.Registry
    |                                      |
    |                                      +--> runtime.Runtime --> Runner
    |                                                             |
    v                                                             v
Channel response stream                                      LLM Provider
```

Session keys are scoped per agent: `{agentID}:{platform}:{userID}:{context}`, ensuring that the same user talking to different agents gets independent conversation histories. See [Agent architecture](/docs/development/agent-architecture) for the session/runtime/memory design rules.

## Package Layout

```
cmd/stellad/             Entry point, server commands, service wiring
internal/
  config/              Store interface, DBStore (PostgreSQL), Snapshot, types
  ai/                  Message/Content types, Model, Provider interface, streaming events
  agent/               Service, ServiceManager, session registry, runtime, runner factory
    session/           Session lifecycle, ownership, kind/channel policy
    runtime/           Runner cache, turn execution, event persistence
    engine/            Agent loop engine (multi-turn tool execution)
    prompt/            System prompt builder and templates
  channel/             Channel interface, identity resolution, slash commands, notify
    cli/               Bubble Tea TUI
    telegram/          Telegram bot
    qq/                QQ bot
    feishu/            Feishu bot
  admin/               HTTP API + embedded React SPA
  auth/                RBAC/ABAC policy engine, sessions, sandbox
  db/                  PostgreSQL (pgx/v5), goose migrations, sqlc queries
  scheduler/           River-backed service (durable job scheduling for Web UI, CLI, and native agent tools)
  skills/              Skills tool (search/install/list/remove via skills.sh)
pkg/
  memory/              Memory Provider interface, types, Summarizer, tool auto-generation, test helpers
  tools/               Tool interface, registry, built-in tools (read, bash, write, edit, agent)
plugins/
  memory/              Memory plugin registry + implementations
    lcm/               Lossless Context Management (default) — DAG summaries, compaction, search
    simple/            Sliding-window memory — last N messages, no summaries
  tools/               Plugin tool registry + plugin tools (webfetch)
  hooks/               Plugin hook registry + plugin hooks (rtk)
  channels/            Channel plugins (telegram, qq, feishu, weixin)
  providers/           Provider plugin registry + LLM adapters (anthropic, openai, openai-response)
```

## Configuration

Configuration is stored in PostgreSQL and accessed through the `config.Store` interface. There is no YAML config file; all settings (providers, agents, channels, scheduler) are managed via the admin API or database.

- **Store** (`config.Store`) -- Interface for reading and writing providers, agents, channels, users, and chat-agent bindings. Implemented by `DBStore`.
- **DBStore** (`config.DBStore`) -- PostgreSQL-backed implementation using sqlc-generated queries.
- **Snapshot** (`config.Snapshot`) -- Read-only view of configuration for a single agent. Assembled from the Store at pool creation time. Contains resolved provider credentials, model names, workspace path, system prompt, and runner settings. Passed to the runner factory and tools that need per-agent config.

## Composition & Lifecycle

`cmd/stellad` is the single manual composition root. There is no DI framework and no generic `Lifecycle` interface — subsystems are constructed and wired explicitly, in one place, so the wiring is auditable. Startup runs in strict phases, and each phase must complete before the next:

1. **Boot config** — `serverAction` parses `config.LoadServerConfig(os.LookupEnv)` and `oidc.LoadLoginConfig(os.LookupEnv, baseURL)` once, at the startup boundary. No other package reads the environment (a test tripwire enforces this, with a small allowlist for `STELLA_HOME`/OTel/runtime passthrough). The final base URL is resolved here and threaded down, so shared services are constructed with it directly — never a `localhost` placeholder mutated later.
2. **Build** — `setup()` constructs each subsystem once. The shared credentials/email/share/recally/MCP services are built a single time (each domain owns its own query set via a `*ForPool` constructor), so the same instance backs both the agent tools and the HTTP endpoints.
3. **Bind** — genuine back-edges are closed with one-shot, pre-start binds that reject a nil/duplicate/late bind: the PoolManager's `BindVaultEnvLoader`/`BindMCPToolProvider`/`BindOAuthRegistry` (before `StartAll`), the shared River client's `BindRiverClient` on the scheduler/goal/embedding services, and `AddBuiltinTool` (duplicate-checked, sealed by `StartAll`). Ordinary dependencies are constructor-injected, not bound.
4. **Validate / Seal** — `pluginhost.Seal()` validates every static registration and capability binding, then refuses further static registration; the dynamic desired-state surface (`ApplyChannel`/`RegisterManifestPlugins`) stays open. The admin server is built from an immutable, validated `server.Deps` via `server.New(ctx, deps)` which fails fast on a missing required dependency. `server.New` reads no environment, constructs no service, and has no setters.
5. **Observability** — global OTel tracing initializes before the serving phase, so no span-emitting component (agent runs via HTTP/channel ingress) starts before the exporter is installed.
6. **Run** — only now does the composition root start ingress, and only after every backend it depends on is up. First it wires the static callbacks (`notifier.SetAuthService`, the scheduler `OnJob` handler — both mutex-guarded one-time writes) and starts River, the scheduler, the goal dispatch tick, and the embedding backfill; the scheduler handler is wired **before** River starts, since River may run a persisted job the instant it starts. Only then does ingress come up — the group-dispatch loop, the managed channel runtimes, and finally `httpSrv.Serve` (the listener is bound earlier but not served). The root owns one `errgroup`: `httpSrv.Serve` and `groupDispatcher.Run(ingressCtx)` run under it. Expected shutdown errors normalize to `nil` (`http.ErrServerClosed`, `context.Canceled`); any other component error cancels its peers and becomes the root error. Component constructors start no goroutines — background loops are entered by an explicit blocking `Run(ctx)` or a `Start` owned by the root (e.g. the trace hook's idle-session reaper).

**Immutable Server Deps.** `server.Deps` is a value struct grouped by domain (persistence, authz, runtime, shared services, optional capabilities). It carries the concrete domain services, not broad shadow stores; a reflection/AST tripwire freezes the remaining broad-persistence debt (DB pool, `config.Store`, auth stores) and forbids adding a new broad field. Optional capabilities are nil-tolerant and degrade to a single centralized 503 mapping.

**Authorization.** Agent HTTP, webhook, and channel entry points use the authoritative `internal/agent/access` policy-enforcement service. Session and workspace use cases use `internal/agent/session/access`: it loads durable owner, agent, kind, and lifecycle facts before creating a scoped registry access, then decides Agent, Session, and Workspace requests against one revision-bound `authz.Authority` evaluation. The legacy policy engine has no Agent, Session, or Workspace decision path. Authorities are minted only by trusted identity adapters (`internal/auth`, `internal/credential`, `internal/authz`) and the durable worker/group adapter in `internal/agent/access`; request body/path fields can never mint or overwrite an actor.

The execution domains follow the same shape: Workflow, Scheduler, and Goal are enforced by their own domain services (`workflow.Service`, `scheduler.Service`, `goal.Service`) and Skills by `skillaccess`, each replacing the former `Service.As(authz.Identity)` facade and scattered helpers. Every transport and tool use case calls `Begin` once and decides the domain resource against that single revision; a cross-resource agent gate is folded into the same evaluation through `agentaccess.AuthorizeWithin`. Durable workers (goal attempts, fired scheduled workflows) reconstruct the owner/executor Authority from persisted trusted state and re-decide on every action. `admin` is a policy superuser via the built-in `admin-full-access` policy rather than scattered `role == admin` checks.

The user-capability domains are all user-owned — a delegated agent turn acts with its user's access rather than an executor confinement (an agent shares a user's secrets, mail, connections, and reading library) — but they split by enforcement mechanism. **Vault is policy-backed**: `vault.Service` opens one `authz.Authorizer` evaluation per use case and decides `ResourceVault` against it, because vault entries have real `user`/`user_agent`/`system`/`system_agent` scope distinctions (`user`/`user_agent` are user-owned with an agent-read gate folded in; admin-managed `system`/`system_agent` reach only `admin-full-access`). It preserves at-rest encryption, no secret read-back, reserved-name guards, and runner invalidation. **Connections, Email, Share, and Recally are not policy-backed**: each is a coarse per-user capability with no scope or admin distinctions, so `connections.Service`, `email.Service`, `share.Service`, and `recally.Service` bind one trusted `authz.Authority` (a simple `Service.Access(authority)` constructor, not an `Authorizer` evaluation), capture the acting user, reject an invalid or no-user Authority up front, and enforce ownership through user-scoped durable queries — the account config lives in that user's vault namespace, OAuth bundles and flows are keyed by user, shares are deleted `WHERE user_id = ?`, and recally rows are uid-scoped so a foreign row is simply not found. Operations keyed only by a parent id (recally article content, feed entries) prove parent ownership with a uid-scoped load first, and Share artifacts keep os.Root workspace confinement for an agent-scoped actor. Several surfaces stay deliberately trusted or public: vault's host-side callers (MCP, OAuth, email config, channel config, sandbox env, key provisioning) use the raw service methods; the OAuth callback and token-refresh paths are keyed by the flow/user, not a live request; and the public share view is an unguessable capability URL authorized by token hash plus expiry with no session. See the [authorization guide](/docs/development/authorization) for the resource matrix and recipes.

**Static vs dynamic.** Boot-static capabilities are bound once before start and then sealed. Live reconfiguration (plugin tool/hook/provider reloads, agent sync, runner invalidation) is a distinct surface that stays available after start and applies atomically — it never re-runs the one-shot binds.

**Shutdown ordering.** The first `SIGINT`/`SIGTERM` starts a graceful drain (a second collapses to a hard stop). The `drainSequence` runs: mark `/readyz` unready and signal SSE streams → **stop every non-HTTP ingress source** (group-dispatch acceptance, channel bot pollers, and the scheduler/goal/embedding River periodics + one-time dispatch), each via an idempotent stop-once closure, so no new work or periodic fires after the drain begins → drain in-flight HTTP within `STELLA_HTTP_SHUTDOWN_TIMEOUT` (force-close on deadline) → cancel the work context, after which River drains its in-flight jobs within the soft-stop budget and the LIFO defer chain reverse-closes the subsystems. The group-dispatch loop runs on a dedicated `ingressCtx` (a child of the errgroup context) so it can be halted without cancelling the work context; outbound dependencies (pools, notifier) stay alive until that final cancel, so work accepted before the drain can still complete and deliver. The same stop-once closures back both `stopIngress` and the reverse-defer cleanup, so the crash / startup-error path tears down safely with no double-stop. A subsystem crash cancels the errgroup and tears down without a readiness drain.

## Multi-User Multi-Agent Routing

Each incoming message goes through a two-step resolution before reaching the agent loop:

1. **User resolution** (`channel.ResolveUser`) -- Upserts the sender by external platform ID, returning a `config.User` record with a stable internal user ID.
2. **Agent resolution** (`channel.ResolveAgent`) -- Determines which agent handles this message:
   - In DMs, the user's `default_agent_id` is used.
   - In group chats, a `chat_agents` binding maps `(platform, chat_id)` to an agent.
   - If neither is set, the first enabled agent is used as fallback.

The resolved user and agent are bundled into a `ResolvedChat` struct that threads through all handler and command paths. This struct holds the target `Service`, the `User`, the `AgentID`, and the `SessionKey`.

The `ServiceManager` (implemented by `PoolManager`) maintains a `map[agentID]*Service` and lazily creates services on first access. Each service is configured with its agent's `Snapshot` (model, credentials, workspace, system prompt) via the runner factory.

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

Providers live in `plugins/providers/` and self-register via `init()`. Adding a new provider requires creating a package under `plugins/providers/` -- no other wiring code is needed. See [plugin-system](/docs/development/plugin-system) for details.

## Tools

The Runner injects tools into LLM calls. Tools follow a common interface defined in `pkg/tools/`. The `tools.Definition` type is a type alias for `ai.ToolDefinition`, keeping domain packages decoupled:

```go
type Tool interface {
    Definition() tools.Definition
    Execute(ctx context.Context, args map[string]any) (string, error)
}
```

### Built-in Tools (always available)

| Tool       | Description                                       |
| ---------- | ------------------------------------------------- |
| `read`     | Read file contents with UTF-8 safe truncation     |
| `bash`     | Execute shell commands                            |
| `write`    | Create/overwrite files atomically                 |
| `edit`     | Edit file sections preserving context             |
| `delegate` | Delegate focused subtasks to isolated child loops |

### Plugin Tools (toggleable via admin)

| Tool       | Description             |
| ---------- | ----------------------- |
| `webfetch` | Fetch web page contents |

The core local-workspace tools run through a Docker sandbox backend. The `bash` tool executes via `Session.Exec`; the `read`, `write`, and `edit` tools use `Session.ResolvePath` to obtain the host path and then call `os.*` directly. Runner startup fails closed when Docker is unavailable.

### Sandbox

The sandbox system provides process, filesystem, and network isolation for agent tool execution. All core tools share the same `sandbox.Session` per runner: `bash` uses `Session.Exec`; `read`/`write`/`edit` use `Session.ResolvePath` + `os.*`. Runner startup fails closed when the sandbox backend is unavailable. See [Sandbox Backend Abstraction](/docs/development/sandbox) for the full Session interface, execution mediation, fail-closed behavior, and exception boundaries.

Sandbox tools (bash, read, write, edit) live in `internal/agent/sandbox/`; other built-in tools live with the capability they project (for example, delegate in `internal/agent/delegate`). Plugin tools (e.g. webfetch) live in `plugins/tools/` and self-register via `init()`. Adding a new plugin tool requires no changes to the wiring code beyond a blank import. See [plugin-system](/docs/development/plugin-system) for the full plugin architecture.

### Delegate Tool

The `delegate` tool enables the agent to delegate focused subtasks to isolated child loops. This is useful for research, code review, or drafting that benefit from fresh context without polluting the parent conversation.

- Each delegate gets a fresh message history containing only the task description
- Multiple tasks run in parallel via goroutines with configurable concurrency
- The `delegate` tool is excluded from children to prevent recursion
- Delegate output is truncated to ~4096 tokens to avoid bloating the parent context
- Supports presets loaded from markdown files with YAML frontmatter
- Per-task options: `preset`, `context`, `model` (override), `system` (additional instructions), `tools` (whitelist), `max_turns` (default 10), `timeout_seconds` (default 120)

### Builtin Shared Tools

| Tool        | Condition                         | Description                                                         |
| ----------- | --------------------------------- | ------------------------------------------------------------------- |
| `memory`    | Always                            | Auto-generated memory tool (actions adapt to provider capabilities) |
| `skills`    | Always                            | Skill management (search/install/list/remove from skills.sh)        |
| `scheduler` | Always                            | Schedule tasks (add/list/remove jobs)                               |
| `notify`    | Gateway mode + channel configured | Send notifications via dispatcher                                   |

The memory tool is auto-generated by `memory.BuildTool(provider)`, which inspects the provider's capabilities and produces a tool with matching actions. Ordinary chat runners narrow it with `WithSessionReadOnlyWrites()`: with the LCM provider they expose `status`, `search`, `describe`, `expand`, `get_message`, `profile_get`, `soul_get`, `profile_history`, and `constraint_list`; with the Simple provider they expose the corresponding read-only subset. Durable profile/soul/constraint writes happen through Reflect or manual UI/API/CLI paths and are injected into new session prompts.

## Session Lifecycle

1. Channel resolves user and agent, producing a `ResolvedChat`
2. `ResolvedChat.Chat(ctx, message)` is called -- message is `string` (text) or `[]ContentBlock` (multimodal)
3. `Service.Chat` resolves or creates a session through `session.Registry` using the scoped key
4. `runtime.Runtime` acquires or creates a runner for the session, configured with the agent's Snapshot
5. Runner streams events back through a channel
6. On idle timeout, runners are reaped; sessions persist to PostgreSQL via `memory.Provider`

See [session-compaction](/docs/development/session-compaction) for history management.

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

The `internal/server/` package provides an HTTP API and embedded SPA for managing the system. Endpoints cover CRUD operations for providers, agents, channels, users, sessions, scheduler jobs, and global settings. The server reads and writes through `config.Store`, giving operators a web interface for configuration that was previously done via YAML files.

## Notification Flow

```
Agent notify tool      --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
Scheduler job result   --> Dispatcher --> Channel (Telegram/QQ/Feishu/WeChat)
```

The dispatcher is created early in setup, but backends are registered later when gateway services start. The ServiceManager wires per-agent notification tool injection through the `BuiltinToolsFactory`, keeping notifications in the always-on builtin tool set while external tools remain plugin-managed.
