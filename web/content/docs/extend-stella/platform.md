---
title: Platform API
description: Reference for the scoped host services available to plugin code.
---

## Why Platform Exists

`Platform` is the plugin-scoped service surface passed through capability contexts.

It exists to solve two problems:

- plugin code should only see the services it is allowed to use
- host internals should stay internal

Instead of handing plugins a global service container, Stella passes `Platform` through typed contexts such as `ToolContext`, `RuntimeContext`, and `AdminContext`.

## Declared Capabilities

`Platform` is **not** ambient. A plugin only reaches the host ports it declares in
`PluginInfo.RequiredCapabilities`. The host:

- **grants** only the declared capabilities,
- **validates** at seal time that each declared capability is backed by an injected host service (an undeclared-but-needed or unbacked capability fails closed), and
- **exposes** a plugin-scoped `Platform` whose accessor for an undeclared capability returns `nil`.

Declare what you need with the typed `pkgplugins.Capability` values, e.g. a managed channel runtime:

```go
Meta: pkgplugins.PluginInfo{
    // ...
    RequiredCapabilities: []pkgplugins.Capability{
        pkgplugins.CapabilityChannelPlatform,
        pkgplugins.CapabilityLogger,
        pkgplugins.CapabilityRuntimeLookup,
    },
},
```

Calling an accessor you did not declare returns `nil` — always nil-check services (`if svc := ctx.Platform.Notifier(); svc == nil { ... }`) rather than assuming ambient access. These declarations are static Go registration only: manifest plugins cannot declare or gain `Platform` capabilities.

## Available Services

`Platform` exposes (each gated by the matching `Capability`):

- `Logger()`
- `ConfigStore()`
- `StateStore()`
- `Notifier()`
- `Auth()`
- `RuntimeLookup()`
- `ChannelPlatform()`

Some of these are generic and safe for many plugins. Some are specialized for specific runtime types.

## Logger

Use `Logger()` for plugin-scoped structured logs.

```go
Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
    ctx.Platform.Logger().Info("building tool")
    return NewTool(), nil
}
```

The host scopes the logger to the plugin automatically.

## ConfigStore

`ConfigStore()` gives the plugin access to its own config row only.

```go
state, err := ctx.Platform.ConfigStore().Get(ctx)
if err != nil {
    return nil, err
}
```

Use this when the plugin needs to read or update its own desired config outside normal admin flows.

## StateStore

`StateStore()` gives the plugin a scoped persistence store for operational state.

This is not the same thing as config. Use it for derived state such as cursors or checkpoints.

```go
err := ctx.Platform.StateStore().Set(ctx, pkgplugins.StateScope{
    Kind: pkgplugins.StateScopeSession,
    ID:   sessionID,
}, "sync_cursor", map[string]any{
	"last_item_id": itemID,
})
```

The plugin does not pass its own plugin ID. The store is already scoped.

## Notifier

`Notifier()` gives access to user-visible notifications.

The `notify` tool is the clearest example:

```go
Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
    service := ctx.Platform.Notifier()
    if service == nil {
        return nil, nil
    }
    return &Tool{service: service}, nil
}
```

Use it when your plugin needs to send completion updates, alerts, or scheduler-driven messages.

## Auth

`Auth()` provides user and linked-identity lookup.

Use it when a plugin needs to understand the current user or route platform-specific behavior through Stella's identity model.

## RuntimeLookup

`RuntimeLookup()` resolves running managed runtimes by plugin ID and runtime name.

This is typically used by status handlers or plugins that need to inspect another runtime they own.

```go
handle, ok := build.Platform.RuntimeLookup().Lookup(PluginID, RuntimeName)
if !ok {
    return map[string]any{"state": "stopped"}, nil
}
snap, err := handle.Snapshot(ctx)
if err != nil {
    return nil, err
}
return map[string]any{"state": snap.State, "metadata": snap.Metadata}, nil
```

## ChannelPlatform

`ChannelPlatform()` is specialized for managed channel runtimes.

It gives access to:

- parent context
- channel handler
- notification registry

The Telegram plugin uses it to construct its managed runtime:

```go
channelRuntime := platform.ChannelPlatform()
parent := channelRuntime.ParentContext()
handler := channelRuntime.Handler()
notifications := channelRuntime.Notifications()
```

This service is only meaningful for channel runtime plugins.

## Contexts Determine What Else You Get

`Platform` is not the whole story. Each capability context also carries capability-specific fields.

Examples:

`ToolContext`:

- `Platform`
- `Paths`
  - `UserRoot`
  - `ToolsBinDir`
  - `StellaHome`
  - `AgentRoot`
  - `ProjectRoot`
- `Runtime`

`ProjectRoot` is the current attached project, if any. Project-aware tools should resolve relative paths from `ProjectRoot` instead of depending on runner cwd details.

`Runtime` is a capability interface for tool file and process operations. Plugin tools should use it rather than importing sandbox internals directly.

`RuntimeContext`:

- `Platform`
- `State`

`MemoryContext`:

- `Platform`
- `State`
- `DB`
- `StellaHome`
- `SummarizerFn`

That design keeps `Platform` focused while still giving each capability the extra inputs it actually needs.

## Platform Usage Guidelines

Use `Platform` when:

- the service belongs to the host
- the plugin should only access its own scoped slice of that service
- the service is needed at build or runtime time

Do not use `Platform` as a general dumping ground. If a capability needs a new host-owned service, add it deliberately and document why.

## Good Patterns

- keep all host access inside `Build`, `Run`, or runtime construction functions
- use `StateStore()` for derived operational state, not config
- use `ConfigStore()` for desired config, not runtime snapshots
- use `RuntimeLookup()` in status paths instead of storing global pointers
- keep specialized services specialized, as with `ChannelPlatform()`

## Bad Patterns

- caching global host service bags in package-level variables
- passing plugin IDs back into already scoped stores
- mixing config persistence and operational state persistence
- reading another plugin's state by bypassing the scoped API

The design works best when plugin code stays inside the boundary the host gives it.
