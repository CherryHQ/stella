---
title: Plugin System
---

## Overview

Anna uses a compiled-in plugin model. All plugins are built directly into the anna binary -- there is no subprocess protocol, no separate processes, and no third-party plugin installation.

The runtime now uses a **unified plugin host**:

- a **plugin** is the ownership unit for config, lifecycle, runtime services, and capability registration
- a plugin may register multiple **capabilities**: tool, provider, channel, hook, memory, runtime, config, status, and prompt inventory
- plugin identity and capability identity stay separate
- runtime plugins use declarative `Apply(ctx, desired PluginState)` semantics

Plugin-facing contracts live in `pkg/plugins/`. The host implementation lives in `internal/pluginhost/`.

Current built-in plugin capabilities cover:

- **Tool plugins** for agent-callable tools (for example `mcp`, `webfetch`)
- **Channel plugins** for messaging integrations (`telegram`, `qq`, `feishu`, `weixin`)
- **Hook plugins** for engine interception (`trace`, `rtk`)
- **Provider plugins** for LLM adapters (`anthropic`, `openai`, `openai-response`)
- **Memory plugins** for conversation storage (`lcm`, `simple`)
- **Runtime/status plugins** for background services such as MCP and reflect

Note: Core tools (`read`, `bash`, `edit`, `write`) are always enabled and are not plugins.

## Built-in Plugins

Anna ships with built-in plugins across tools, channels, hooks, providers, memory, and standalone runtimes:

| Kind      | Name            | Description                                           |
| --------- | --------------- | ----------------------------------------------------- |
| tool      | mcp             | Connect to configured MCP servers and proxy MCP tools |
| tool      | webfetch        | Fetch web pages                                       |
| channel   | telegram        | Telegram bot                                          |
| channel   | qq              | QQ bot                                                |
| channel   | feishu          | Feishu (Lark) bot                                     |
| channel   | weixin          | WeChat bot (via iLink)                                |
| hook      | trace           | Structured logging + optional OpenTelemetry tracing   |
| hook      | rtk             | Request tracking and cost logging                     |
| provider  | anthropic       | Anthropic Messages API (Claude models)                |
| provider  | openai          | OpenAI Chat Completions API (GPT models)              |
| provider  | openai-response | OpenAI Responses API (compatible services)            |
| memory    | lcm             | Lossless Context Management                           |
| memory    | simple          | Sliding-window memory                                 |
| runtime   | reflect         | Background conversation review                        |

See the [Plugins](/docs/plugins) section for detailed documentation on individual plugins.

## Plugin Architecture

Anna keeps the existing package layout, but plugin ownership is now unified under the plugin host.

Today the architecture is split like this:

1. Each built-in plugin package still lives under `plugins/{kind}/{name}/`
2. Plugin-facing host contracts are defined in `pkg/plugins/`
3. The process-wide plugin host lives in `internal/pluginhost/`
4. Existing kind-specific registries still exist as compatibility adapters for tools, hooks, providers, and memory
5. MCP, reflect, and all built-in messaging channels are host-backed: MCP owns config validation, runtime lifecycle, status, tool exposure, and prompt inventory; reflect owns config validation, runtime lifecycle, and status through the same host; Telegram, QQ, Feishu, and Weixin all reconcile channel config, runtime lifecycle, and status through the host while preserving their existing plugin rows and admin UX

`cmd/anna/plugins_imports.go` provides the blank imports that trigger built-in plugin registration at startup.

### Adding a New Plugin

New plugins should register through `pkg/plugins.Register(...)` and declare the capabilities they own. Legacy kind-specific registry registration still exists for compatibility, but new advanced plugins should prefer the unified host.

Example -- adding a new provider capability through the legacy registry:

```go
// plugins/providers/gemini/client.go
package gemini

import (
    pluginproviders "github.com/vaayne/anna/plugins/providers"
    "github.com/vaayne/anna/internal/ai"
)

func init() {
    pluginproviders.Register("gemini", pluginproviders.Registration{
        Meta: pluginproviders.ProviderMeta{
            Name:       "Google Gemini",
            DefaultURL: "https://generativelanguage.googleapis.com",
        },
        Factory: func(cfg pluginproviders.ProviderConfig) (providers.ProviderAdapter, error) {
            return New(Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL}), nil
        },
    })
}
```

## Storage

Plugin state is stored in the `settings_plugins` table in the database. Each plugin has:

| Field     | Type       | Description                                         |
| --------- | ---------- | --------------------------------------------------- |
| `id`      | string     | Plugin ID (`kind/name`, e.g., `tool/webfetch`)      |
| `kind`    | string     | `tool`, `channel`, `hook`, or `provider`            |
| `name`    | string     | Plugin name                                         |
| `enabled` | bool       | Whether the plugin is active                        |
| `config`  | JSON map   | Plugin-specific configuration (tokens, keys, etc.)  |

## CLI Commands

```bash
anna plugin list               # List all plugins with status
anna plugin enable <id>        # Enable a plugin
anna plugin disable <id>       # Disable a plugin
anna plugin config <id>        # View plugin configuration
anna plugin config <id> k=v    # Set plugin configuration key-value pairs
```

## Admin Panel

Channel plugins, provider plugins, memory plugins, standalone runtimes, and the MCP plugin are configured via the admin panel (`anna --open`). The admin panel writes to the `settings_plugins` table while routing host-backed config, status, and runtime apply operations through the unified plugin host.

The MCP plugin stores its server definitions as JSON in `settings_plugins.config`, with an admin form editor for multiple servers/transports, transport-specific fields, structured args/env/header editors, and live runtime status badges for discovered/suppressed servers.

The reflect plugin keeps its existing standalone `settings_plugins` row (`id="reflect"`) and reconciles its background review loop through the unified host without any schema changes.

The Telegram, QQ, Feishu, and Weixin channels keep using their existing `settings_plugins` rows (`id="channel/..."`) and the same `/channels` admin UI, but their config validation, runtime lifecycle, and status are now all host-backed. Saving or toggling channel config re-applies the corresponding managed runtime through the plugin host while preserving existing plugin IDs, admin UX, and channel-specific behavior.
