# Plugin Architecture Refactor Plan for Advanced Built-in Plugins

## Purpose

Document the target refactor for Anna's plugin system so advanced built-in plugins — starting with `tool/mcp` — can live fully under `plugins/...` without depending on ad hoc `internal/...` wiring for config, runtime lifecycle, admin integration, and status reporting.

## Discovery Summary

### What the plugin system does well today

Anna already has solid compiled-in registration for several plugin kinds:

- `plugins/tools/*` register tool factories
- `plugins/hooks/*` register hook factories
- `plugins/providers/*` register provider factories
- `plugins/memory/*` register memory factories

Plugin enablement and JSON config persistence are centralized in `settings_plugins`.

### What the plugin system does not model well yet

The current system mostly models **construction-time registration**, not **runtime ownership**.

There is no standard plugin-facing API for:

- typed config decode/validation/defaults
- config persistence hooks
- runtime start/stop/reconcile
- runtime status snapshots
- admin integration for complex plugin-specific UIs
- prompt-facing runtime contributions

This is why MCP is only partially plugin-scoped today.

## Current MCP Boundary Analysis

### Code that already lives under `plugins/...`

- `plugins/tools/mcp/tool.go`
  - registers the `mcp` tool plugin
  - exposes the `list|get|exec` tool surface
  - currently depends on `internal/mcp.DefaultManager()`

### MCP responsibilities still owned by `internal/...`

#### Config and plugin state

- `internal/mcp/config.go`
  - typed config structs
  - transport-aware validation
  - defaults
- `internal/mcp/store.go`
  - loads persisted plugin state from `config.Store`

#### Runtime and lifecycle

- `internal/mcp/manager.go`
  - shared process-wide runtime
  - discovered tool cache
  - canonical ID mapping
  - exec dispatch
- `internal/mcp/session.go`
  - transport/session creation
  - stdio/SSE/HTTP client setup
- `internal/mcp/supervisor.go`
  - server lifecycle supervision
  - restart/backoff/suppression
  - per-server runtime status

#### Admin integration

- `internal/admin/plugins.go`
  - MCP-specific config validation branch
  - MCP-specific reconcile branch
  - MCP-specific status branch
- `internal/admin/server.go`
  - MCP-specific lifecycle callbacks via `SetMCPLifecycle`
- `internal/admin/ui/pages/plugins.templ`
  - MCP-specific editor UI
- `internal/admin/ui/static/js/pages/plugins.js`
  - MCP-specific editor behavior and status polling

#### Application ownership/wiring

- `cmd/anna/commands.go`
  - creates the MCP manager
  - installs it as the process-global default
  - reconciles runtime during startup
- `cmd/anna/gateway.go`
  - wires admin lifecycle hooks for start/stop/reconcile/status

#### Prompt integration

- `internal/agent/runner/prompt.go`
  - reaches directly into `annamcp.DefaultManager()`

## Root Architectural Problem

MCP is not merely a tool factory. It is a **composite plugin** with four responsibilities:

1. `mcp` tool surface
2. background runtime manager
3. typed config contract
4. admin/status integration

The current plugin system cleanly supports only the first responsibility.

As a result, MCP is forced to rely on internal ownership points instead of owning its full behavior from within `plugins/tools/mcp`.

## Target Architecture

Introduce a **unified plugin descriptor and host API** that supports optional facets. A plugin can implement one or more facets depending on complexity.

### Core descriptor

```go
type Descriptor struct {
    ID          string
    Kind        Kind
    Name        string
    Description string

    Config  ConfigFacet
    Runtime RuntimeFacet
    Admin   AdminFacet

    Tool     ToolFacet
    Hook     HookFacet
    Provider ProviderFacet
    Channel  ChannelFacet
    Memory   MemoryFacet
}
```

MCP would implement:

- `ConfigFacet`
- `RuntimeFacet`
- `AdminFacet`
- `ToolFacet`

## Recommended Shared/Public APIs

These APIs should become shared platform APIs for advanced built-in plugins.

### 1. Config API

Plugins should be able to own validation/defaults/decode without getting raw access to internal stores.

```go
type ConfigFacet interface {
    DefaultConfig() map[string]any
    Validate(raw map[string]any) error
    Decode(raw map[string]any) (any, error)
    Redact(raw map[string]any) map[string]any
}
```

Platform-owned access service:

```go
type PluginState struct {
    ID      string
    Enabled bool
    Config  map[string]any
}

type ConfigService interface {
    Get(ctx context.Context, pluginID string) (PluginState, error)
    Set(ctx context.Context, pluginID string, raw map[string]any) error
}
```

#### Guidance

Expose `ConfigService`, not raw `internal/config.Store`.

### 2. Runtime lifecycle API

Plugins with background services should have a standard runtime surface.

```go
type RuntimeFacet interface {
    NewRuntime(host Host) (Runtime, error)
}

type Runtime interface {
    Start(ctx context.Context, state PluginState) error
    Reconcile(ctx context.Context, state PluginState) error
    Stop(ctx context.Context) error
    Status(ctx context.Context) (any, error)
}
```

Platform-owned runtime orchestration:

```go
type RuntimeManager interface {
    EnsureStarted(ctx context.Context, pluginID string) error
    Reconcile(ctx context.Context, pluginID string) error
    Stop(ctx context.Context, pluginID string) error
    Status(ctx context.Context, pluginID string) (any, error)
}
```

#### Guidance

This replaces MCP-specific hooks like `SetMCPLifecycle`.

### 3. Admin/status API

Status reporting should be generic rather than plugin-specific.

```go
type AdminFacet interface {
    StatusEnabled() bool
    ConfigView() ConfigViewSpec
}
```

Generic `/api/plugin-status/{kind}/{name}` behavior:

- resolve descriptor
- if plugin has runtime, call `Runtime.Status`
- otherwise return `{}`

For MCP, `Status()` returns the current server status snapshot.

### 4. Tool/provider/hook/channel/memory build boundaries

Retain separate build contracts, but attach them to the unified plugin descriptor.

Examples:

```go
type ToolFacet interface {
    BuildTool(host Host, bc ToolBuildContext) (tools.Tool, error)
}

type HookFacet interface {
    BuildHook(host Host, bc HookBuildContext) (hooks.HookPlugin, error)
}

type ProviderFacet interface {
    BuildProvider(host Host, cfg ProviderConfig) (providers.ProviderAdapter, error)
}

type ChannelFacet interface {
    BuildChannel(host Host, cfg any) (channel.Channel, error)
}
```

#### Guidance

- Short term: preserve existing registries and adapt them to the descriptor model.
- Medium term: move channels to the same pattern.
- Avoid a big-bang rewrite.

### 5. Plugin host API

Plugins need a narrow host surface, not broad internal access.

```go
type Host interface {
    Logger(pluginID string) *slog.Logger
    Config() ConfigService
    Runtime() RuntimeLookup
    Secrets() SecretService // optional future extension
}
```

Optional runtime lookup:

```go
type RuntimeLookup interface {
    Get(pluginID string) (RuntimeHandle, bool)
}
```

#### Guidance

This is how the MCP tool talks to the MCP runtime without importing `internal/mcp`.

## Recommended Internal-Only Boundaries

These should remain internal and not be exposed to plugins:

- `internal/config.Store`
- raw database/sql handles
- `internal/admin.Server`
- raw HTTP router internals
- `internal/auth` engine/policies
- `internal/agent.PoolManager`
- prompt template internals
- channel dispatcher/notifier internals
- full app bootstrap sequencing

## Prompt Integration Recommendation

Do not introduce a broad arbitrary prompt-mutation plugin API yet.

Instead, expose a narrow structured contributor for runtime-discovered tool inventories:

```go
type ToolInventoryContributor interface {
    PromptToolInventory(ctx context.Context) ([]PromptToolInfo, error)
}
```

For MCP:

- runtime implements `ToolInventoryContributor`
- prompt builder requests MCP inventory through the host/runtime surface
- prompt rendering remains platform-owned

This removes the current `annamcp.DefaultManager()` coupling without overexposing prompt internals.

## Recommended MCP End-State

After refactor, MCP ownership should look like this:

```text
plugins/tools/mcp/
  descriptor.go
  config.go
  runtime.go
  session.go
  supervisor.go
  tool.go
  status.go
  prompt.go
```

At that point:

- MCP config ownership lives in the plugin
- MCP runtime ownership lives in the plugin
- MCP tool uses plugin runtime lookup instead of a global singleton
- admin uses generic config/status/reconcile plumbing
- prompt integration reads structured plugin runtime inventory

## Migration Plan

### Phase A — Introduce shared plugin host/facet APIs

Goal: create the platform capability model first, without changing MCP behavior.

Deliverables:

- new shared `pkg/plugins` descriptor/facet interfaces
- internal plugin host implementation
- internal runtime manager for plugin runtimes
- adapter layer for existing tool/hook/provider registries

Behavior should remain unchanged in this phase.

### Phase B — Move MCP config ownership to the plugin descriptor

Goal: MCP owns typed config/defaults/validation.

Changes:

- register MCP with `ConfigFacet`
- move MCP config validation under `plugins/tools/mcp`
- make admin config save path generic through `ConfigFacet`
- remove MCP-specific validation branch from `internal/admin/plugins.go`

### Phase C — Move MCP runtime ownership behind `RuntimeFacet`

Goal: remove MCP-specific lifecycle plumbing.

Changes:

- MCP exposes `RuntimeFacet`
- startup generically starts enabled plugin runtimes
- enable/disable/config-change paths call generic runtime reconcile/stop
- generic plugin status route reads runtime status

Expected cleanup:

- delete `SetMCPLifecycle`
- delete MCP-specific admin lifecycle callback fields
- delete MCP-specific gateway wiring branches

### Phase D — Rewire MCP tool to the host runtime lookup

Goal: remove `internal/mcp.DefaultManager()`.

Changes:

- MCP tool receives runtime access from `Host`
- prompt inventory also reads through runtime lookup / contributor interface
- tool behavior stays the same

### Phase E — Physically move MCP runtime implementation into `plugins/tools/mcp`

Goal: finish ownership transfer.

Changes:

- move manager/session/supervisor/config/status code under the plugin package
- keep temporary compatibility shims only if needed for rollout safety
- update tests to target the plugin package

### Phase F — Generalize the same architecture to other advanced plugins

Potential adopters:

- `reflect`
- channels
- future background-service plugins

## Risks and Mitigations

### Import cycles

**Risk:** high if plugins import internal host/runtime code and internals import plugins.

**Mitigation:**

- put plugin-facing interfaces in `pkg/plugins`
- keep host implementation in a separate internal package
- plugins import only `pkg/...` APIs

### Overexposing internals

**Risk:** plugins become privileged internal code.

**Mitigation:**

Only expose:

- config access
- runtime lifecycle/status
- logger
- narrow runtime lookup

Do not expose raw DB/admin/auth/pool internals.

### Security / trust boundaries

**Risk:** compiled plugins are not sandboxed, and MCP is especially privileged.

**Mitigation:**

- keep plugin config mutation admin-only
- add secret-redaction support for plugin config responses
- document clearly that built-in plugins run with process privileges

### Runtime ordering issues

**Risk:** tools may be built before runtimes are started.

**Mitigation:**

- host owns runtime registry
- startup order should be: load plugin state → create runtimes → start enabled runtimes → build pools
- tools should tolerate unavailable runtimes gracefully

### Testability regressions

**Risk:** refactor could hide behavior in hard-to-mock boot code.

**Mitigation:**

Test at multiple levels:

- config facet tests
- runtime lifecycle/status tests
- tool/runtime integration tests with fake sessions
- generic admin/plugin-host tests for validate/persist/reconcile flow

## Recommended Implementation Order

1. Add `pkg/plugins` unified descriptor + facets
2. Add internal plugin host/runtime manager
3. Convert admin config/status flow to generic descriptor-driven logic
4. Convert MCP to descriptor-backed config + runtime
5. Remove `SetMCPLifecycle` and global `DefaultManager` usage
6. Move MCP runtime code physically into `plugins/tools/mcp`
7. Document MCP as the reference advanced-plugin pattern

## Bottom Line

The plugin system should remain compiled-in, but it needs to grow from **factory registries** into a real **plugin runtime platform**.

MCP has demonstrated the missing platform capabilities clearly:

- typed plugin-owned config
- generic runtime lifecycle management
- generic status reporting
- narrow host APIs instead of internal reach-through

The proposed descriptor + facet + host model solves those gaps while keeping Anna's current compiled-in architecture and avoiding overexposure of internal systems.
