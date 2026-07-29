---
title: Plugin Overview
description: Concepts, architecture, and lifecycle for Stella's built-in plugin system.
---

## What A Plugin Is

In Stella, a plugin is the ownership unit for a feature area.

A plugin:

- has one canonical plugin ID such as `tool/webfetch` or `channel/telegram`
- owns its own metadata, config schema, validation, and status behavior
- can register one or more capabilities against the plugin host
- is compiled into the `stellad` binary at build time

Stella does not load third-party binaries or subprocess plugins. Built-in plugins register themselves during process startup through Go `init()` functions.

## The Public Design

The plugin API has two public surfaces:

- `Host`: registration only
- `Platform`: scoped access to host-owned services at build or runtime time

That split is intentional.

`Host` is used during registration:

```go
func init() {
    pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
        host.SetInfo(...)
        host.AddTool(...)
        host.AddAdmin(...)
    }))
}
```

`Platform` is used inside capability callbacks:

```go
Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
    log := ctx.Platform.Logger()
    store := ctx.Platform.StateStore()
    _ = log
    _ = store
    return newTool(), nil
}
```

Plugins do not receive a global service bag. They only receive the scoped `Platform` through typed capability contexts.

## Plugin IDs And Capability IDs

A plugin ID identifies the plugin as a whole.

Examples:

- `tool/notify`
- `tool/webfetch`
- `channel/telegram`
- `hook/rtk`
- `provider/openai`

Capabilities are registered under that plugin ID, but each capability may also have its own capability-local name.

Examples:

- plugin `tool/notify` owns tool capability `notify`
- plugin `channel/telegram` owns config, channel registration, and a managed runtime

This matters because one plugin can own more than one capability.

## Capability Model

Stella's built-in plugin system supports these capability types:

- `AdminSpec`: config defaults, schema, validation, redaction, and status
- `ToolSpec`: agent-callable tools
- `ProviderSpec`: model provider adapters
- `ChannelSpec`: messaging integrations
- `HookSpec`: hook plugins
- `MemorySpec`: memory providers
- `RuntimeSpec`: long-lived managed runtimes
- `PromptInventorySpec`: prompt-visible tool inventory
- `SystemPromptSpec`: prompt sections
- `BeforeRunSpec`: pre-run lifecycle hooks
- `BeforeToolCallSpec`: pre-tool lifecycle hooks
- `AfterToolResultSpec`: post-tool lifecycle hooks

Some plugins register only one capability. Others register several.

Examples:

- `tool/notify` is a simple tool plugin
- `channel/telegram` owns config, status, channel registration, and runtime lifecycle

## CLI-Backed Tool Plugins

Some `tool/*` plugins do not expose an Stella JSON tool at all. Instead, they own
CLI integration that affects bash sessions and prompt guidance.

Examples now include:

- `tool/mise`: embedded binary + prompt guidance
- `tool/tap-web`: binary + bundled skill + prompt guidance
- `tool/gh`: manifest binary + out-of-box OAuth session env
- `tool/lark-cli`: pinned binary + bundled skill + prompt guidance

The important boundary is ownership, not whether the plugin exposes `ToolSpec`.
If a feature owns a CLI binary, injected env vars, bundled skill, or prompt
guidance, that state should live in the same plugin package.

## Host Ownership Model

The process-wide plugin host lives in `internal/pluginhost/`.

Plugin-facing contracts live in `pkg/plugins/`.

This split is the core design rule:

- code in `pkg/plugins/` defines what plugins are allowed to see
- code in `internal/pluginhost/` can do richer host-internal orchestration

The host loads plugins from the process catalog, stores plugin metadata, builds capabilities when needed, and reconciles managed runtimes from desired plugin state.

## Registration Flow

At startup:

1. blank imports load built-in plugin packages
2. each plugin package calls `pkgplugins.Register(...)` in `init()`
3. the host loads the catalog
4. each plugin receives a `Host` and registers its capabilities
5. the host validates the registration graph

At runtime:

1. the host reads desired plugin state from config storage
2. enabled capabilities are built on demand
3. managed runtimes receive `Apply(ctx, desired PluginState)`
4. admin and discovery pages ask the host for config schema, status, and metadata

## Managed Runtime Model

Managed runtimes use declarative desired-state semantics:

```go
type Runtime interface {
    Apply(ctx context.Context, desired PluginState) error
    Start(ctx context.Context, desired PluginState) error
    Reconcile(ctx context.Context, desired PluginState) error
    Stop(ctx context.Context) error
    Snapshot(ctx context.Context) (RuntimeStatus, error)
    Status(ctx context.Context) (RuntimeStatus, error)
}
```

In practice, new runtimes should treat `Apply` as the main entrypoint. The host uses plugin config and enabled state to reconcile the runtime into the requested shape.

## Built-In Plugin Layout

Built-in plugins live under `plugins/<kind>/<name>/`.

Common examples:

- `plugins/tools/webfetch/`
- `plugins/channels/telegram/`
- `plugins/providers/openai/`

This keeps plugin ownership obvious: the package that registers a plugin also owns its config logic, runtime wiring, and capability-specific behavior.

## When To Add A Plugin

Add a plugin when a feature needs one or more of these:

- toggleable config and enable/disable behavior
- admin-visible schema and status
- tool, provider, channel, hook, or memory registration
- a long-lived managed runtime
- prompt or lifecycle integration owned by a specific feature

Do not add a plugin for generic internal helpers. Plugins are feature boundaries, not just packages.

## Next Reading

- [Create a Plugin](/docs/extend-stella/create-a-plugin)
- [Capabilities](/docs/extend-stella/capabilities)
- [Platform API](/docs/extend-stella/platform)
- [Examples](/docs/extend-stella/examples)
