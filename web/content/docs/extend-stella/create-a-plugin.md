---
title: Create A Plugin
description: Step-by-step guide for adding a new built-in plugin to Stella.
---

## Start With The Plugin ID

Choose a stable plugin ID first.

Typical patterns:

- `tool/<name>`
- `channel/<name>`
- `hook/<name>`
- `provider/<name>`
- `memory/<name>`

The plugin ID is the ownership key for config, runtime state, status, and capability registration. Treat it as a long-lived identifier.

## Create The Package

Add a package under `plugins/` that matches the feature you are introducing.

Examples:

- `plugins/tools/mytool/`
- `plugins/providers/gemini/`
- `plugins/channels/slack/`

Most plugins expose a `PluginID` constant and register in `init()`.

## Register The Plugin

The minimal shape looks like this:

```go
package mytool

import (
    pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

const PluginID = "tool/mytool"

func init() {
    pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
        host.SetInfo(pkgplugins.PluginInfo{
            ID:          PluginID,
            Kind:        "tool",
            Name:        "mytool",
            DisplayName: "My Tool",
            Description: "Example tool plugin.",
            Capabilities: []string{
                pkgplugins.CapabilityTool,
            },
        })
    }))
}
```

`SetInfo` describes the plugin as a whole. It does not register a capability by itself.

## Add The Capability

For a tool plugin, add `ToolSpec`:

```go
host.AddTool(pkgplugins.ToolSpec{
    PluginID:    PluginID,
    Name:        "mytool",
    Description: "Run the mytool operation.",
    Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
        return NewTool(ctx.Platform.Logger()), nil
    },
})
```

For a managed runtime plugin, add `AdminSpec` and `RuntimeSpec`:

```go
host.AddAdmin(pkgplugins.AdminSpec{
    PluginID:      PluginID,
    DefaultConfig: DefaultConfig,
    Schema:        ConfigSchema(),
    Validate:      ValidateConfig,
    Redact:        RedactConfig,
})

host.AddRuntime(pkgplugins.RuntimeSpec{
    PluginID: PluginID,
    Name:     "main",
    Build: func(ctx pkgplugins.RuntimeContext) (pkgplugins.Runtime, error) {
        return NewManagedRuntime(ctx.Platform), nil
    },
})

host.AddAdmin(pkgplugins.AdminSpec{
    PluginID: PluginID,
    Status: func(ctx context.Context, build pkgplugins.AdminContext) (any, error) {
        handle, ok := build.Platform.RuntimeLookup().Lookup(PluginID, "main")
        if !ok {
            return map[string]any{"state": "stopped"}, nil
        }
        snap, err := handle.Snapshot(ctx)
        if err != nil {
            return nil, err
        }
        return map[string]any{"state": snap.State, "metadata": snap.Metadata}, nil
    },
})
```

## Import The Plugin

Built-in plugins are registered through blank imports. Add your package to the existing startup imports so the `init()` function runs.

Look at `cmd/stellad/plugins_imports.go` and follow the established pattern.

Without that import, the plugin package compiles but never registers itself.

## Keep Ownership Local

A plugin package should own:

- plugin metadata
- config decode/validate/redact logic
- capability registration
- runtime construction logic
- tests for registration and behavior

Do not split registration into one package and runtime/config logic into unrelated packages unless there is a clear boundary.

## Recommended Shape By Plugin Type

Tool plugin:

- `SetInfo`
- `AddTool`
- optional `AddAdmin` if the tool needs config
- optional `AddPromptInventory` if the tool should contribute prompt-visible inventory

Provider plugin:

- `SetInfo`
- `AddProvider`

Hook plugin:

- `SetInfo`
- `AddHook`

Memory plugin:

- `SetInfo`
- `AddMemory`

Managed runtime plugin:

- `SetInfo`
- `AddAdmin` for config
- `AddRuntime`
- `AddAdmin` for status

CLI-backed tool plugin:

- `SetInfo`
- `AddBinary`
- optional `AddAdmin` for OAuth or other config
- optional `AddSessionEnv` when the runner must inject session auth
- optional `AddBundledSkill` when the plugin ships a builtin system skill
- optional `AddSystemPrompt` for usage guidance

Managed channel plugin:

- usually use `RegisterManagedChannelPlugin(...)`

## A Real Tool Example

The `notify` plugin is a good minimal reference because it uses the clean surface directly:

```go
host.SetInfo(pkgplugins.PluginInfo{
    ID:          PluginID,
    Kind:        "tool",
    Name:        "notify",
    DisplayName: "Notify",
    Description: "Send notifications through Stella's configured notification routes.",
    Capabilities: []string{
        pkgplugins.CapabilityTool,
    },
})

host.AddTool(pkgplugins.ToolSpec{
    PluginID:    PluginID,
    Name:        "notify",
    Description: "Send a notification message to the user.",
    Required:    true,
    Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
        service := ctx.Platform.Notifier()
        if service == nil {
            return nil, nil
        }
        return &Tool{service: service}, nil
    },
})
```

The important part is not the feature itself. The important part is the pattern:

- metadata first
- capability registration second
- all host-owned services come from `ctx.Platform`

## A Real Managed Runtime Example

The Telegram channel plugin is a current managed-runtime reference. It uses the channel helper to register metadata, config, status, channel behavior, and the runtime under one plugin ID:

```go
pkgplugins.RegisterManagedChannelPlugin(host, pkgplugins.ManagedChannelPluginRegistration{
    PluginID:    PluginID,
    RuntimeName: RuntimeName,
    Meta: pkgplugins.PluginInfo{
        ID:   PluginID,
        Kind: "channel",
        // ...
    },
    RuntimeFactory: newRuntime,
})
```

Use direct `RuntimeSpec` registration when the runtime is not a managed channel; the host contract is the same.

## Testing A New Plugin

At minimum, add tests for:

- registration completeness
- config validation
- runtime apply or capability build behavior
- status behavior if the plugin exposes status

Good examples already exist in:

- `plugins/channels/telegram/plugin_test.go`
- `internal/pluginhost/runtime_test.go`
- `plugins/providers/openai/contract_test.go`

## Common Mistakes

- registering a capability without calling `SetInfo`
- using the wrong `PluginID` in a capability spec
- reaching for global services instead of `ctx.Platform`
- mixing plugin ID and capability name
- adding a runtime without adding status or config when operators need them
- forgetting the blank import that triggers registration

## Next Reading

- [Capabilities](/docs/extend-stella/capabilities)
- [Platform API](/docs/extend-stella/platform)
- [Examples](/docs/extend-stella/examples)
