---
title: Plugin System
---

## Overview

Anna uses a compiled-in plugin model. All plugins are built directly into the anna binary -- there is no subprocess protocol, no separate processes, and no third-party plugin installation. Plugins are Go packages under `plugins/` that self-register via `init()` and implement standard interfaces.

Four plugin kinds:

- **Tool plugins** provide tools that the LLM agent can call (e.g., `webfetch`).
- **Channel plugins** provide messaging platform integrations (e.g., `telegram`, `qq`, `feishu`, `weixin`).
- **Hook plugins** intercept engine lifecycle events (e.g., pre/post tool calls, pre/post LLM calls).
- **Provider plugins** provide LLM API adapters (e.g., `anthropic`, `openai`, `openai-response`).

Note: Core tools (`read`, `bash`, `edit`, `write`) are always enabled and are not plugins.

## Built-in Plugins

Anna ships with 9 built-in plugins:

| Kind     | Name            | Description                                          |
| -------- | --------------- | ---------------------------------------------------- |
| tool     | webfetch        | Fetch web pages                                      |
| channel  | telegram        | Telegram bot                                         |
| channel  | qq              | QQ bot                                               |
| channel  | feishu          | Feishu (Lark) bot                                    |
| channel  | weixin          | WeChat bot (via iLink)                               |
| hook     | rtk             | Request tracking and cost logging                    |
| provider | anthropic       | Anthropic Messages API (Claude models)               |
| provider | openai          | OpenAI Chat Completions API (GPT models)             |
| provider | openai-response | OpenAI Responses API (compatible services)           |

## Plugin Architecture

All plugin kinds follow the same pattern:

1. Each plugin is a Go package under `plugins/{kind}/{name}/`
2. The package's `init()` function calls the kind-specific registry's `Register()` method
3. A blank import in `plugins/all.go` triggers registration at startup
4. The registry's `BuildEnabled()` (or `BuildAll()` for providers) instantiates active plugins at runtime

```
plugins/
├── all.go                          # Blank imports trigger init() registration
├── tools/
│   ├── registry.go                 # Tool plugin registry
│   └── webfetch/                   # Tool: web page fetcher
├── channels/
│   ├── telegram/                   # Channel: Telegram bot
│   ├── qq/                         # Channel: QQ bot
│   ├── feishu/                     # Channel: Feishu bot
│   └── weixin/                     # Channel: WeChat bot
├── hooks/
│   ├── registry.go                 # Hook plugin registry
│   └── rtk/                        # Hook: request tracking
└── providers/
    ├── registry.go                 # Provider plugin registry
    ├── anthropic/                  # Provider: Anthropic API
    ├── openai/                     # Provider: OpenAI Chat Completions
    └── openai-response/            # Provider: OpenAI Responses API
```

### Adding a New Plugin

To add a new plugin, create a package under the appropriate `plugins/{kind}/` directory with an `init()` function that registers with the kind's registry. Then add a blank import to `plugins/all.go`. No other wiring code is needed.

Example -- adding a new provider:

```go
// plugins/providers/gemini/client.go
package gemini

import (
    pluginproviders "github.com/vaayne/anna/plugins/providers"
    "github.com/vaayne/anna/internal/ai"
)

func init() {
    pluginproviders.Register("gemini", pluginproviders.ProviderMeta{
        Name:       "Google Gemini",
        DefaultURL: "https://generativelanguage.googleapis.com",
    }, func(cfg pluginproviders.ProviderConfig) ai.ProviderAdapter {
        return New(Config{APIKey: cfg.APIKey, BaseURL: cfg.BaseURL})
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

Channel plugins and provider plugins are configured via the admin panel (`anna --open`). The admin panel writes to the `settings_plugins` table and provides a UI for managing tokens, keys, and plugin-specific settings. Provider plugins appear dynamically in the providers page -- adding a new provider plugin automatically makes it available in the admin UI dropdown.
