# Pi Extension Design Notes for Anna Plugin Architecture

## Goal

Capture what Anna can borrow from `../pi-mono` so plugins behave more like pi extensions: one extension/package can register multiple capabilities at once (tool, provider, channel, hook, runtime behavior, UI/admin contributions), without forcing a single plugin kind as the primary abstraction.

## What pi gets right

### 1. Single extension abstraction, multiple capabilities

pi does not make extension authors choose one plugin type up front.

An extension receives one `ExtensionAPI` object and can register multiple things through the same surface:

- `pi.on(...)` for lifecycle/event hooks
- `pi.registerTool(...)`
- `pi.registerCommand(...)`
- `pi.registerShortcut(...)`
- `pi.registerFlag(...)`
- `pi.registerMessageRenderer(...)`
- `pi.registerProvider(...)`
- resource contributions through `resources_discover`
- state persistence through `appendEntry()`

Relevant references:

- `packages/coding-agent/docs/extensions.md`
- `packages/coding-agent/src/core/extensions/types.ts`
- `packages/coding-agent/src/core/extensions/loader.ts`
- `packages/coding-agent/src/core/extensions/runner.ts`

This is the main design lesson for Anna: **plugin kind should be metadata, not the primary interface.**

### 2. Shared runtime object with late binding

pi creates a shared `ExtensionRuntime` with stubbed actions first, then binds concrete actions later when the session/runtime is ready.

Relevant source:

- `packages/coding-agent/src/core/extensions/loader.ts#createExtensionRuntime`
- `packages/coding-agent/src/core/extensions/runner.ts#bindCore`

This gives pi two useful properties:

1. extensions can register capabilities during load
2. runtime-dependent actions can be connected later without circular construction

This is a strong model for Anna's advanced plugins, especially for MCP-like plugins that need config + runtime + tool capability in one package.

### 3. Runtime contributions are capability-oriented, not package-oriented

pi extensions can contribute different behavior categories independently:

- tools
- providers
- commands
- UI
- event handlers
- resources

This matches the desired Anna direction: a plugin should be able to register any combination of:

- tools
- providers
- channels
- hooks
- background runtimes/services
- admin config/status views

### 4. Event-driven interception is first-class

pi's `on(...)` system is broad and composable. Extensions can:

- intercept tool calls/results
- intercept provider requests
- inject system prompt changes
- manage session lifecycle
- contribute resources dynamically

Anna does not need all of pi's event surface, but the pattern is valuable: **cross-cutting behavior should be modeled as optional hooks on a shared host lifecycle, not as ad hoc special cases.**

### 5. Provider registration is dynamic and lives in the same extension API

pi's `registerProvider()` is particularly relevant. A single extension can both:

- register tools
- register or override providers

That supports the exact Anna direction V described: a plugin package should be free to register multiple capability types at once.

### 6. Extensions are loaded as packages/modules, not grouped by type directories only

pi supports extension modules as the primary unit. The fact that an extension offers tools, providers, commands, or hooks is an implementation detail.

That suggests Anna should move from:

- `plugins/tools/...`
- `plugins/hooks/...`
- `plugins/providers/...`
- `plugins/channels/...`

as the core abstraction

toward:

- `plugins/<plugin-name>/`

with optional capability registration inside each plugin package.

## What Anna should not copy directly

### 1. Arbitrary broad extension power surface

pi extensions are intentionally very powerful. They can modify prompts, intercept nearly everything, execute code, and manipulate UI/session state freely.

For Anna, that is too broad as a first platform API. Anna should stay narrower and more explicit because it has:

- daemon/runtime concerns
- persisted config/admin workflows
- long-lived background services
- multi-user/channel responsibilities

Anna should borrow pi's **shape**, not its full openness.

### 2. Everything as event interception

pi uses events extensively because it is an interactive coding agent runtime.

Anna should not force all plugin behavior through events. For server-side/runtime plugins, explicit capability interfaces are cleaner than a huge event bus.

Good fit for Anna:

- registration methods for concrete capabilities
- a smaller lifecycle hook surface for cross-cutting integration

### 3. Session-local assumptions

pi's runtime is heavily session-centric.

Anna needs process-level and service-level plugin ownership too:

- channel bots
- MCP supervisors
- background services like reflect
- admin configuration/status

So Anna needs both:

- session/agent-bound capabilities
- process/runtime-bound capabilities

## Recommended Anna Design Inspired by pi

## Core principle

**A plugin is a package that can register zero or more capabilities.**

Do not make `tool`, `provider`, `channel`, `hook`, `memory` the primary plugin taxonomy.
Those should become capability labels, not separate plugin systems.

## Proposed abstraction

Each plugin exports a single registration entrypoint that receives a host API.

```go
type Plugin interface {
    Register(host Host)
}
```

Or function-style:

```go
type PluginFunc func(host Host)
```

Then the host exposes registration methods:

```go
type Host interface {
    RegisterTool(def ToolRegistration)
    RegisterProvider(def ProviderRegistration)
    RegisterChannel(def ChannelRegistration)
    RegisterHook(def HookRegistration)
    RegisterRuntime(def RuntimeRegistration)
    RegisterAdmin(def AdminRegistration)
    RegisterPromptContributor(def PromptContribution)
}
```

This is the pi-like part: one plugin package, one host object, many optional registrations.

## Example: MCP in the new model

`plugins/mcp/plugin.go`

```go
func Register(host plugins.Host) {
    host.RegisterRuntime(...)
    host.RegisterTool(...)
    host.RegisterAdmin(...)
    host.RegisterPromptContributor(...)
}
```

No special `tool/mcp` architecture is needed internally. The plugin may expose a tool capability, but it is not defined *by* being a tool plugin.

## Example: Telegram in the new model

`plugins/telegram/plugin.go`

```go
func Register(host plugins.Host) {
    host.RegisterChannel(...)
    host.RegisterAdmin(...)
}
```

## Example: Trace plugin

`plugins/trace/plugin.go`

```go
func Register(host plugins.Host) {
    host.RegisterHook(...)
}
```

## Example: Composite plugin

A future plugin could register both:

- a provider
- a tool
- an admin page
- a runtime service

without inventing any new plugin category.

## Suggested host capability interfaces

### Tool

```go
type ToolRegistration struct {
    Name        string
    Description string
    Build       func(ctx ToolContext) (tools.Tool, error)
}
```

### Provider

```go
type ProviderRegistration struct {
    Name  string
    Build func(ctx ProviderContext) (providers.ProviderAdapter, error)
}
```

### Channel

```go
type ChannelRegistration struct {
    Name  string
    Build func(ctx ChannelContext) (channel.Channel, error)
}
```

### Hook

```go
type HookRegistration struct {
    Name  string
    Build func(ctx HookContext) (hooks.HookPlugin, error)
}
```

### Runtime / background service

```go
type RuntimeRegistration struct {
    Name   string
    Create func(ctx RuntimeContext) (Runtime, error)
}

type Runtime interface {
    Start(context.Context, PluginState) error
    Reconcile(context.Context, PluginState) error
    Stop(context.Context) error
    Status(context.Context) (any, error)
}
```

### Admin

```go
type AdminRegistration struct {
    ConfigSchema func() any
    Validate     func(map[string]any) error
    Status       func(context.Context) (any, error)
}
```

## Plugin identity model

Instead of making plugin kind part of identity, give every plugin a single stable ID:

- `mcp`
- `telegram`
- `trace`
- `anthropic`

Capability names live under that plugin.

Examples:

- plugin `mcp` registers tool `mcp` and runtime `mcp`
- plugin `telegram` registers channel `telegram`
- plugin `corp-ai` could register provider `corp-ai` and tool `corp-login`

This simplifies persistence and admin ownership.

## Persistence model

Persist plugin state by plugin ID, not by `(kind, name)`:

```json
{
  "id": "mcp",
  "enabled": true,
  "config": {...}
}
```

Capability-specific enablement can be added later only if needed, but the default should be plugin-level ownership.

That aligns with the fact that advanced plugins often own multiple related capabilities that should be configured and toggled together.

## Why this helps Anna

### 1. Fixes MCP cleanly

MCP becomes one plugin package that owns:

- config
- runtime lifecycle
- status
- tool registration
- prompt contribution
- admin integration

### 2. Allows future composite plugins naturally

A future plugin can combine:

- provider + auth flow + tool
- channel + admin + notifier integration
- runtime service + hooks + tool

without fighting the framework.

### 3. Removes registry fragmentation

Today Anna has multiple separate registry systems with inconsistent semantics.
A unified host registration model makes the plugin contract simpler.

### 4. Keeps interface design simpler

Instead of many top-level plugin kinds each with different loading rules, Anna can have:

- one plugin package abstraction
- many optional capability registration methods

This matches V's stated goal directly.

## Recommended migration path

### Phase 1

Introduce a new unified plugin host package, while keeping current registries as adapters.

### Phase 2

Allow plugin packages to register multiple capabilities via one entrypoint.

### Phase 3

Move MCP to the new package shape first.

### Phase 4

Move channels/providers/hooks incrementally.

### Phase 5

Deprecate kind-specific plugin package layout as the primary model.

## Concrete recommendation

Anna should adopt this pi-inspired rule:

> A plugin is a module that registers capabilities with a host. Tool/provider/channel/hook are capability types, not plugin types.

That is the cleanest way to support advanced built-in plugins like MCP without creating more `internal/...` escape hatches.
