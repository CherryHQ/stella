---
title: Examples
description: Worked plugin examples for tools, runtimes, and managed channels.
---

## Example 1: Minimal Tool Plugin

This is the smallest useful plugin shape.

```go
package greet

import (
    "context"

    pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
    "github.com/CherryHQ/stella/pkg/tools"
)

const PluginID = "tool/greet"

func init() {
    pkgplugins.Register(PluginID, pkgplugins.PluginFunc(func(host pkgplugins.Host) {
        host.SetInfo(pkgplugins.PluginInfo{
            ID:          PluginID,
            Kind:        "tool",
            Name:        "greet",
            DisplayName: "Greet",
            Description: "A minimal example tool plugin.",
            Capabilities: []string{
                pkgplugins.CapabilityTool,
            },
        })

        host.AddTool(pkgplugins.ToolSpec{
            PluginID:    PluginID,
            Name:        "greet",
            Description: "Return a greeting.",
            Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
                return greetTool{}, nil
            },
        })
    }))
}

type greetTool struct{}

func (greetTool) Definition() tools.Definition {
    return tools.Definition{
        Name:        "greet",
        Description: "Return a greeting.",
        InputSchema: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "name": map[string]any{"type": "string"},
            },
        },
    }
}

func (greetTool) Execute(ctx context.Context, args map[string]any) (string, error) {
    name, _ := args["name"].(string)
    if name == "" {
        name = "world"
    }
    return "hello, " + name, nil
}
```

Use this shape when the plugin only needs one tool and no runtime or admin behavior.

## Example 2: Tool Plugin Using Platform

This example uses a scoped host service.

```go
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

This is the right pattern when the tool depends on host-owned services such as notifications or auth.

## Example 3: Managed Runtime Plugin

This is the generic shape for a host-managed background service.

```go
host.SetInfo(pkgplugins.PluginInfo{
    ID:           PluginID,
    Kind:         "runtime",
    Name:         "worker",
    DisplayName:  "Worker",
    Description:  "Background worker example.",
    Managed:      true,
    AdminVisible: true,
    HasConfig:    true,
    HasStatus:    true,
    Capabilities: []string{
        pkgplugins.CapabilityRuntime,
        pkgplugins.CapabilityConfig,
        pkgplugins.CapabilityStatus,
    },
})

host.AddAdmin(pkgplugins.AdminSpec{
    PluginID:      PluginID,
    DefaultConfig: DefaultConfig,
    Schema:        ConfigSchema(),
    Validate:      ValidateConfig,
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
        return map[string]any{
            "state":    snap.State,
            "message":  snap.Message,
            "metadata": snap.Metadata,
        }, nil
    },
})
```

This is the default pattern for background services that need config, reconcile from desired state, and report status to the admin surface.

## Example 4: Managed Channel Plugin

Managed channels have a helper because the pattern is repetitive.

```go
pkgplugins.RegisterManagedChannelPlugin(host, pkgplugins.ManagedChannelPluginRegistration{
    PluginID:    PluginID,
    RuntimeName: RuntimeName,
    Meta: pkgplugins.PluginInfo{
        ID:                    PluginID,
        Kind:                  "channel",
        Name:                  "telegram",
        DisplayName:           "Telegram",
        Description:           "Telegram bot integration.",
        AdminVisible:          true,
        SupportsNotifications: true,
        Capabilities: []string{
            pkgplugins.CapabilityRuntime,
            pkgplugins.CapabilityConfig,
            pkgplugins.CapabilityStatus,
        },
    },
    DefaultConfig: func() map[string]any { return map[string]any{} },
    Schema:        configSchema(),
    Validate:      func(raw map[string]any) error { _, err := DecodeConfig(raw); return err },
    Redact:        RedactConfig,
    Configured:    isConfigured,
    NotificationsEnabled: func(raw map[string]any) bool {
        cfg, err := DecodeConfig(raw)
        return err == nil && cfg.EnableNotify
    },
    RuntimeFactory: func(platform pkgplugins.Platform) (pkgplugins.Runtime, error) {
        channelRuntime := platform.ChannelPlatform()
        return NewManagedRuntime(RuntimeDeps{
            Parent:        channelRuntime.ParentContext(),
            Handler:       channelRuntime.Handler(),
            Notifications: channelRuntime.Notifications(),
        }), nil
    },
})
```

Use this helper for host-managed messaging integrations.

## Example 5: Dynamic Prompt Inventory

The MCP plugin contributes dynamic prompt-visible tools:

```go
host.AddPromptInventory(pkgplugins.PromptInventorySpec{
    PluginID: PluginID,
    Name:     "tools",
    GetTools: func(ctx context.Context, build pkgplugins.PromptInventoryContext) ([]pkgplugins.PromptToolInfo, error) {
        rt, ok := LookupRuntime(build.Platform.RuntimeLookup())
        if !ok {
            return nil, nil
        }

        items := make([]pkgplugins.PromptToolInfo, 0, len(rt.Manager().ValidTools()))
        for _, tool := range rt.Manager().ValidTools() {
            items = append(items, pkgplugins.PromptToolInfo{
                Name:        tool.ID,
                Description: tool.Description,
                Metadata: map[string]any{
                    "server_name": tool.ServerName,
                },
            })
        }
        return items, nil
    },
})
```

This is useful when the plugin exposes dynamic capability inventory that the model should know about.

## Example 6: Manifest-Only CLI Tool

Use a manifest entry when the integration only needs a managed binary and session
environment injection. For example, GitHub CLI is manifest-only: there is no Go
`tool/gh` package, no admin config, and no system-prompt contribution.

```yaml
plugins:
  - id: tool/gh
    kind: tool
    name: gh
    display_name: GitHub CLI
    description: GitHub CLI integration with OAuth-backed session auth.
    enabled: true
    binaries:
      - name: gh
        repo: cli/cli
        bin_path: bin
    session_env:
      - env_var: GH_TOKEN
        source: github_token
```

Use a Go plugin only when the integration needs Go-owned behavior such as config
validation, runtime management, bundled skill syncing, or prompt/lifecycle hooks.

## Picking The Right Example

Start with:

- minimal tool plugin if you just need a tool
- tool plus platform usage if the tool needs host services
- managed runtime plugin if the feature owns a background service
- managed channel plugin if the feature is a messaging integration
- prompt inventory if the plugin exposes dynamic tool lists

If a plugin starts simple and grows later, extend it by adding more capabilities under the same plugin ID instead of splitting it prematurely.
