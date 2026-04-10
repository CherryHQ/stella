# Plugin System Redesign Plan

## Goal

Simplify Anna's plugin API so that plugins are easy to write, naming is obvious, and the architecture follows the pi-inspired principle: **a plugin is a module that registers capabilities with a host**.

## Design Principles

1. **Flat Host** — one interface, no sub-interfaces. `AddX` = declare capability, no platform services on the Host.
2. **Platform via contexts** — services (logger, state, scheduler, etc.) arrive only through typed build contexts, not at registration time.
3. **AdminSpec consolidation** — config + status + schema + validation in one struct, not three separate registrations.
4. **Capabilities derived** — `PluginInfo` carries only descriptive metadata; capabilities, `HasConfig`, `HasStatus` are derived from actual registrations.
5. **Simple plugin IDs** — `mcp` not `tool/mcp`, `telegram` not `channel/telegram`. Kind is metadata, not identity.
6. **Explicit lifecycle ordering** — `Order int` on lifecycle specs, sorted by `(Order, PluginID, Name)`.
7. **Plugin-scoped Platform** — plugins don't pass their own ID to services.

---

## API Definition

### Registration Surface

```go
// Host is the flat registration surface exposed to plugins.
// It does not expose platform services; those are provided only through
// capability-specific contexts.
type Host interface {
    SetInfo(PluginInfo)

    AddAdmin(AdminSpec)

    AddTool(ToolSpec)
    AddProvider(ProviderSpec)
    AddChannel(ChannelSpec)
    AddHook(HookSpec)
    AddMemory(MemorySpec)
    AddRuntime(RuntimeSpec)

    AddPromptInventory(PromptInventorySpec)
    AddSystemPrompt(SystemPromptSpec)

    AddBeforeRun(BeforeRunSpec)
    AddBeforeToolCall(BeforeToolCallSpec)
    AddAfterToolResult(AfterToolResultSpec)
}
```

### Plugin Identity

```go
// Plugin is the ownership unit in the unified plugin host.
type Plugin interface {
    Register(host Host)
}

// PluginFunc adapts a function to the Plugin interface.
type PluginFunc func(host Host)

func (f PluginFunc) Register(host Host) {
    if f != nil {
        f(host)
    }
}

// PluginInfo is the host discovery metadata for a plugin.
type PluginInfo struct {
    ID           string `json:"id"`
    DisplayName  string `json:"display_name,omitempty"`
    Description  string `json:"description,omitempty"`
    Managed      bool   `json:"managed,omitempty"`
    AdminVisible bool   `json:"admin_visible,omitempty"`
}

// PluginState is the canonical plugin-level desired state.
type PluginState struct {
    ID      string         `json:"id"`
    Enabled bool           `json:"enabled"`
    Config  map[string]any `json:"config,omitempty"`
}
```

### Capability Specs

```go
// ToolSpec declares a tool capability owned by a plugin.
type ToolSpec struct {
    PluginID    string
    Name        string
    Description string
    Required    bool
    Build       func(ctx ToolContext) (tools.Tool, error)
}

// ProviderSpec declares a provider capability owned by a plugin.
type ProviderSpec struct {
    PluginID string
    Name     string
    Meta     ProviderMeta
    Build    func(ctx ProviderContext) (providers.ProviderAdapter, error)
}

type ProviderMeta struct {
    Name       string
    DefaultURL string
}

// ChannelSpec declares a channel capability owned by a plugin.
type ChannelSpec struct {
    PluginID              string
    Name                  string
    SupportsNotifications bool
    Configured            func(raw map[string]any) bool
    NotificationsEnabled  func(raw map[string]any) bool
    Build                 func(ctx ChannelContext) (channel.Channel, error)
}

// HookSpec declares a hook capability owned by a plugin.
type HookSpec struct {
    PluginID string
    Name     string
    Build    func(ctx HookContext) (hooks.HookPlugin, error)
}

// MemorySpec declares a memory capability owned by a plugin.
type MemorySpec struct {
    PluginID string
    Name     string
    Build    func(ctx context.Context, build MemoryContext) (memory.Provider, error)
}

// RuntimeSpec declares a managed runtime capability owned by a plugin.
type RuntimeSpec struct {
    PluginID string
    Name     string
    Build    func(ctx RuntimeContext) (Runtime, error)
}

// AdminSpec declares plugin-owned admin behavior: config defaults, schema,
// validation, redaction, and status.
type AdminSpec struct {
    PluginID      string
    DefaultConfig func() map[string]any
    Schema        map[string]any
    Validate      func(raw map[string]any) error
    Redact        func(raw map[string]any) map[string]any
    Status        func(ctx context.Context, build AdminContext) (any, error)
}
```

### Prompt Specs

```go
// PromptInventorySpec declares structured tool inventory contribution.
type PromptInventorySpec struct {
    PluginID string
    Name     string
    GetTools func(ctx context.Context, build PromptInventoryContext) ([]PromptToolInfo, error)
}

// SystemPromptSpec declares a system prompt contribution.
type SystemPromptSpec struct {
    PluginID string
    Name     string
    Required bool
    Build    func(ctx context.Context, build SystemPromptContext) (SystemPromptSection, error)
}

// PromptToolInfo is a structured tool inventory item contributed to prompt building.
type PromptToolInfo struct {
    Name        string         `json:"name"`
    Description string         `json:"description,omitempty"`
    Metadata    map[string]any `json:"metadata,omitempty"`
}

// SystemPromptSection is a structured prompt contribution from a plugin.
type SystemPromptSection struct {
    Title   string `json:"title"`
    Content string `json:"content"`
}
```

### Lifecycle Specs

```go
// BeforeRunSpec declares a dynamic per-run lifecycle hook.
type BeforeRunSpec struct {
    PluginID string
    Name     string
    Order    int
    Required bool
    Run      func(ctx context.Context, build BeforeRunContext) (BeforeRunResult, error)
}

// BeforeToolCallSpec declares a pre-tool-call lifecycle hook.
type BeforeToolCallSpec struct {
    PluginID string
    Name     string
    Order    int
    Required bool
    Run      func(ctx context.Context, build BeforeToolCallContext) (BeforeToolCallResult, error)
}

// AfterToolResultSpec declares a post-tool-result lifecycle hook.
type AfterToolResultSpec struct {
    PluginID string
    Name     string
    Order    int
    Required bool
    Run      func(ctx context.Context, build AfterToolResultContext) (AfterToolResult, error)
}

// BeforeRunResult is the mutable output from pre-run lifecycle hooks.
type BeforeRunResult struct {
    SystemPrompt string `json:"system_prompt,omitempty"`
}

// BeforeToolCallResult is the mutable output from pre-tool-call hooks.
type BeforeToolCallResult struct {
    Arguments    map[string]any `json:"arguments,omitempty"`
    Block        bool           `json:"block,omitempty"`
    BlockMessage string         `json:"block_message,omitempty"`
}

// AfterToolResult is the mutable output from post-tool-result hooks.
type AfterToolResult struct {
    Result  *string `json:"result,omitempty"`
    IsError *bool   `json:"is_error,omitempty"`
}
```

### Runtime Contracts

```go
// Runtime is implemented by plugin-owned managed runtimes.
type Runtime interface {
    Start(ctx context.Context, desired PluginState) error
    Reconcile(ctx context.Context, desired PluginState) error
    Stop(ctx context.Context) error
    Status(ctx context.Context) (RuntimeStatus, error)
}

// RuntimeLookup resolves a running runtime by plugin and runtime name.
type RuntimeLookup interface {
    Lookup(pluginID string, runtimeName string) (RuntimeHandle, bool)
}

// RuntimeHandle exposes read-only access to a running runtime.
type RuntimeHandle interface {
    Status(ctx context.Context) (RuntimeStatus, error)
}

// RuntimeState is the host-level runtime state.
type RuntimeState string

const (
    RuntimeStateUnknown RuntimeState = "unknown"
    RuntimeStateStopped RuntimeState = "stopped"
    RuntimeStateRunning RuntimeState = "running"
    RuntimeStateError   RuntimeState = "error"
)

// RuntimeStatus is the shared runtime status envelope.
type RuntimeStatus struct {
    State     RuntimeState   `json:"state"`
    Message   string         `json:"message,omitempty"`
    UpdatedAt time.Time      `json:"updated_at,omitempty"`
    Metadata  map[string]any `json:"metadata,omitempty"`
}
```

### Platform Services

```go
// Platform is the plugin-scoped service surface available at build and runtime.
// Specialized accessors may return nil when not applicable.
type Platform interface {
    Logger() *slog.Logger
    ConfigStore() ConfigStore
    StateStore() StateStore
    Scheduler() Scheduler
    Notifier() Notifier
    Auth() Auth
    RuntimeLookup() RuntimeLookup

    ChannelPlatform() ChannelPlatform
    ReflectPlatform() ReflectPlatform
}

// ConfigStore exposes plugin config persistence for the current plugin.
type ConfigStore interface {
    Get(ctx context.Context) (PluginState, error)
    Set(ctx context.Context, config map[string]any) error
}

// StateStore exposes plugin-scoped key/value persistence.
type StateStore interface {
    Get(ctx context.Context, scope StateScope, key string) (map[string]any, bool, error)
    Set(ctx context.Context, scope StateScope, key string, value map[string]any) error
    Delete(ctx context.Context, scope StateScope, key string) error
}

const (
    StateScopeGlobal  = "global"
    StateScopeUser    = "user"
    StateScopeAgent   = "agent"
    StateScopeSession = "session"
)

type StateScope struct {
    Kind string
    ID   string
}

// Notifier exposes user-visible notification delivery.
type Notifier interface {
    Notify(ctx context.Context, n channel.Notification) error
    NotifyUser(ctx context.Context, userID int64, n channel.Notification) error
}

// Auth exposes narrow user and identity lookups.
type Auth interface {
    GetUser(ctx context.Context, userID int64) (UserInfo, error)
    ListUserIdentities(ctx context.Context, userID int64) ([]LinkedIdentity, error)
    GetIdentityByPlatform(ctx context.Context, platform, externalID string) (LinkedIdentity, error)
}

type UserInfo struct {
    ID               int64
    Username         string
    Role             string
    IsActive         bool
    DefaultAgentID   string
    NotifyIdentityID *int64
    CreatedAt        time.Time
    UpdatedAt        time.Time
}

type LinkedIdentity struct {
    ID         int64
    UserID     int64
    Platform   string
    ExternalID string
    Name       string
    LinkedAt   time.Time
}
```

### Scheduler

```go
// Scheduler exposes plugin-owned scheduled job reconciliation.
type Scheduler interface {
    ReconcileJobs(ctx context.Context, jobs []SchedulerJobSpec) error
    DeleteJobs(ctx context.Context) error
    DeleteJob(ctx context.Context, key string) error
    ListJobs(ctx context.Context) ([]SchedulerJob, error)
}

// ScheduledJobRunner is implemented by runtimes that can handle scheduled jobs.
type ScheduledJobRunner interface {
    RunScheduledJob(ctx context.Context, key string, payload map[string]any) error
}

type SchedulerSchedule struct {
    Cron  string `json:"cron,omitempty"`
    Every string `json:"every,omitempty"`
    At    string `json:"at,omitempty"`
}

type SchedulerJobSpec struct {
    Key         string            `json:"key"`
    RuntimeName string            `json:"runtime_name"`
    Name        string            `json:"name"`
    Description string            `json:"description,omitempty"`
    Schedule    SchedulerSchedule `json:"schedule"`
    Payload     map[string]any    `json:"payload,omitempty"`
    Enabled     bool              `json:"enabled,omitempty"`
}

type SchedulerJob struct {
    ID          string            `json:"id"`
    Key         string            `json:"key"`
    RuntimeName string            `json:"runtime_name"`
    Name        string            `json:"name"`
    Description string            `json:"description,omitempty"`
    Schedule    SchedulerSchedule `json:"schedule"`
    Payload     map[string]any    `json:"payload,omitempty"`
    Enabled     bool              `json:"enabled"`
    CreatedAt   time.Time         `json:"created_at"`
    UpdatedAt   time.Time         `json:"updated_at"`
    LastRunAt   *time.Time        `json:"last_run_at,omitempty"`
    LastError   string            `json:"last_error,omitempty"`
}
```

### Specialized Platform Services

```go
// ChannelRegistry exposes channel registration needed by managed channel runtimes.
type ChannelRegistry interface {
    Register(ch channel.Channel)
    Unregister(name string)
}

// ChannelPlatform exposes the narrow services needed by managed channel runtimes.
type ChannelPlatform interface {
    ParentContext() context.Context
    Handler() channel.Handler
    Notifications() ChannelRegistry
}

// ReflectPlatform exposes the narrow services needed by reflect runtimes.
type ReflectPlatform interface {
    ParentContext() context.Context
    Memory() memory.Provider
    Store() ReflectStore
    Workspace() string
    BuildProviders(api, apiKey, baseURL string) (*providers.Registry, error)
}

type ReflectStore interface {
    ListEnabledAgents(ctx context.Context) ([]ReflectAgent, error)
    Snapshot(ctx context.Context, agentID string) (*ReflectSnapshot, error)
}

type ReflectAgent struct {
    ID string
}

type ReflectProviderCreds struct {
    APIKey  string
    BaseURL string
}

type ReflectSnapshot struct {
    AgentID      string
    Provider     string
    Model        string
    ModelStrong  string
    ModelFast    string
    Workspace    string
    APIKey       string
    BaseURL      string
    SystemPrompt string
    Providers    map[string]ReflectProviderCreds
}
```

### Build Contexts

All contexts carry `Platform` for service access. No services on `Host`.

```go
type ToolContext struct {
    Platform    Platform
    State       PluginState
    WorkDir     string
    UserDataDir string
    AnnaHome    string
    Workspace   string
    ToolsBinDir string
}

type ProviderContext struct {
    Platform Platform
    State    PluginState
}

type HookContext struct {
    Platform    Platform
    State       PluginState
    ToolsBinDir string
}

type ChannelContext struct {
    Platform Platform
    State    PluginState
    Handler  channel.Handler
}

type MemoryContext struct {
    Platform     Platform
    State        PluginState
    DB           *sql.DB
    AnnaHome     string
    SummarizerFn func(context.Context, string) (string, error)
}

type RuntimeContext struct {
    Platform Platform
    State    PluginState
}

type AdminContext struct {
    Platform Platform
    State    PluginState
}

type PromptInventoryContext struct {
    Platform Platform
    State    PluginState
}

type SystemPromptContext struct {
    Platform    Platform
    State       PluginState
    AnnaHome    string
    Workspace   string
    Cwd         string
    UserID      int64
    AgentID     string
    UserDataDir string
}

type BeforeRunContext struct {
    Platform     Platform
    State        PluginState
    SessionID    string
    Channel      string
    UserID       int64
    AgentID      string
    Model        string
    MessageText  string
    SystemPrompt string
    History      []ai.Message
}

type BeforeToolCallContext struct {
    Platform   Platform
    State      PluginState
    SessionID  string
    Channel    string
    UserID     int64
    AgentID    string
    ToolName   string
    ToolCallID string
    Arguments  map[string]any
}

type AfterToolResultContext struct {
    Platform   Platform
    State      PluginState
    SessionID  string
    Channel    string
    UserID     int64
    AgentID    string
    ToolName   string
    ToolCallID string
    Arguments  map[string]any
    Result     string
    IsError    bool
    Duration   time.Duration
}
```

---

## Naming Changelog

| Current | New | Reason |
|---|---|---|
| `Host` (with sub-interfaces) | `Host` (flat) | Remove indirection |
| `RegistryHost` | *(removed)* | Folded into flat `Host` |
| `ServiceHost` | `Platform` | Clear what it is |
| `RegisterTool(ToolRegistration)` | `AddTool(ToolSpec)` | Shorter, no stutter |
| `RegisterMetadata(PluginMeta)` | `SetInfo(PluginInfo)` | Descriptive |
| `ConfigRegistration` + `StatusRegistration` + metadata fields | `AdminSpec` | Consolidated |
| `PluginMeta` | `PluginInfo` | Simpler |
| `ManagedRuntime` | `Runtime` | Drop "Managed" |
| `RuntimeSnapshot` | `RuntimeStatus` | It's status, not a snapshot |
| `ManagedRuntime.Apply()` | `Runtime.Reconcile()` | Clearer verb |
| `ManagedRuntime.Snapshot()` | `Runtime.Status()` | Matches what it returns |
| `RuntimeLookup.Get()` | `RuntimeLookup.Lookup()` | Avoids collision with generic `Get` |
| `ConfigService` | `ConfigStore` | It's a store |
| `NotificationService` | `Notifier` | Shorter |
| `PluginStateStore` | `StateStore` | Drop redundant prefix |
| `SchedulerService` | `Scheduler` | Drop "Service" |
| `AuthService` | `Auth` | Drop "Service" |
| `ChannelRuntimeServices` | `ChannelPlatform` | Consistent with `Platform` |
| `ReflectRuntimeServices` | `ReflectPlatform` | Consistent with `Platform` |
| `NotificationRegistry` | `ChannelRegistry` | Clearer scope |
| `PluginStateScopeGlobal` | `StateScopeGlobal` | Drop stutter |
| `PluginStateScope` | `StateScope` | Drop stutter |
| `BeforeToolCallResult.BlockMsg` | `BeforeToolCallResult.BlockMessage` | Full word |
| `tool/mcp` | `mcp` | Kind is metadata, not identity |
| `channel/telegram` | `telegram` | Kind is metadata, not identity |
| `provider/openai` | `openai` | Kind is metadata, not identity |
| `memory/simple` | `simple` | Kind is metadata, not identity |

---

## Plugin Author Example

### Before (current)

```go
func init() {
    pkgplugins.Register("tool/mcp", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
        host.Registry().RegisterMetadata(pkgplugins.PluginMeta{
            ID: "tool/mcp", Kind: "tool", Name: "mcp", DisplayName: "MCP",
            Description: "Connect and proxy configured MCP servers.",
            Managed: true, AdminVisible: true, HasConfig: true, HasStatus: true,
            Capabilities: []string{
                pkgplugins.CapabilityTool, pkgplugins.CapabilityPrompt,
                pkgplugins.CapabilityRuntime, pkgplugins.CapabilityConfig,
                pkgplugins.CapabilityStatus,
            },
        })
        host.Registry().RegisterConfig(pkgplugins.ConfigRegistration{
            PluginID:      "tool/mcp",
            DefaultConfig: func() map[string]any { return map[string]any{"servers": []any{}} },
            Schema:        configSchema(),
            Validate:      func(raw map[string]any) error { _, err := DecodeConfig(raw); return err },
            Redact:        redactConfig,
        })
        host.Registry().RegisterStatus(pkgplugins.StatusRegistration{
            PluginID: "tool/mcp",
            Get:      func(ctx context.Context) (any, error) { ... },
        })
        host.Registry().RegisterRuntime(pkgplugins.RuntimeRegistration{...})
        host.Registry().RegisterTool(pkgplugins.ToolRegistration{...})
        host.Registry().RegisterPromptInventory(pkgplugins.PromptInventoryRegistration{...})
    }))
}
```

### After (new)

```go
func init() {
    plugins.Register("mcp", plugins.PluginFunc(func(host plugins.Host) {
        host.SetInfo(plugins.PluginInfo{
            ID:           "mcp",
            DisplayName:  "MCP",
            Description:  "Connect and proxy configured MCP servers.",
            Managed:      true,
            AdminVisible: true,
        })
        host.AddAdmin(plugins.AdminSpec{
            PluginID:      "mcp",
            DefaultConfig: func() map[string]any { return map[string]any{"servers": []any{}} },
            Schema:        configSchema(),
            Validate:      func(raw map[string]any) error { _, err := DecodeConfig(raw); return err },
            Redact:        redactConfig,
            Status:        func(ctx context.Context, build plugins.AdminContext) (any, error) { ... },
        })
        host.AddRuntime(plugins.RuntimeSpec{
            PluginID: "mcp",
            Name:     "manager",
            Build:    func(ctx plugins.RuntimeContext) (plugins.Runtime, error) { ... },
        })
        host.AddTool(plugins.ToolSpec{
            PluginID:    "mcp",
            Name:        "mcp",
            Description: "Proxy MCP tools managed by the MCP plugin.",
            Build:       func(ctx plugins.ToolContext) (tools.Tool, error) { ... },
        })
        host.AddPromptInventory(plugins.PromptInventorySpec{
            PluginID: "mcp",
            Name:     "tools",
            GetTools: func(ctx context.Context, build plugins.PromptInventoryContext) ([]plugins.PromptToolInfo, error) { ... },
        })
    }))
}
```

---

## Migration Plan

### Phase 1: Foundation (S)

- Fix `cloneMap` to deep-copy nested maps/slices.
- Add `Order int` to lifecycle registration structs.

### Phase 2: New API Surface (M)

- Introduce new types (`Host`, `PluginInfo`, `AdminSpec`, `Platform`, `*Spec`, `Runtime`, `RuntimeStatus`) alongside current types.
- Implement flat `Host` adapter that delegates to existing `RegistryHost` internals.
- Implement `Platform` adapter that wraps existing `ServiceHost` with plugin-scoped binding.

### Phase 3: Migrate Plugins (L)

- Migrate MCP plugin first (most complex, best test of the API).
- Migrate channel plugins (telegram, feishu, qq, weixin).
- Migrate provider plugins (openai, anthropic, openai-response).
- Migrate tool plugins (bash, edit, read, write, notify, skills, agent, sandbox, webfetch).
- Migrate memory plugins (simple, lcm).
- Migrate hook plugins (rtk, trace).
- Migrate reflect plugin.

### Phase 4: Cleanup (M)

- Remove old types and `RegistryHost`/`ServiceHost` interfaces.
- Remove `Capability*` constants (capabilities are derived).
- Remove `Kind` from plugin identity.
- Flatten `plugins/` directory from `plugins/tools/mcp/` to `plugins/mcp/`.

### Phase 5: Future (optional)

- Typed config generics (`ConfigSpec[T]`).
- Error-returning `Host.AddX()` for user-authored plugins.
- Binary/RPC plugin boundary if third-party plugin distribution is needed.
