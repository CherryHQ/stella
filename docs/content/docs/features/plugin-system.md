---
title: Plugin System
---

## Overview

Anna uses a compiled-in plugin model. All plugins are built directly into the anna binary -- there is no subprocess protocol, no separate processes, and no third-party plugin installation. Plugins are Go packages under `plugins/` that implement standard interfaces.

Two plugin kinds:

- **Tool plugins** provide tools that the LLM agent can call (e.g., `webfetch`).
- **Channel plugins** provide messaging platform integrations (e.g., `telegram`, `qq`, `feishu`, `weixin`).

Note: Core tools (`read`, `bash`, `edit`, `write`) are always enabled and are not plugins.

## Built-in Plugins

Anna ships with 5 built-in plugins:

| Kind    | Name       | Description                      |
| ------- | ---------- | -------------------------------- |
| tool    | webfetch   | Fetch web pages                  |
| channel | telegram   | Telegram bot                     |
| channel | qq         | QQ bot                           |
| channel | feishu     | Feishu (Lark) bot                |
| channel | weixin     | WeChat bot (via iLink)           |

## Storage

Plugin state is stored in the `settings_plugins` table in the database. Each plugin has:

| Field     | Type       | Description                                         |
| --------- | ---------- | --------------------------------------------------- |
| `id`      | string     | Plugin ID (`kind/name`, e.g., `tool/webfetch`)      |
| `kind`    | string     | `tool` or `channel`                                 |
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

Channel plugins (Telegram, QQ, Feishu, WeChat) are configured via the admin panel (`anna --open`). The admin panel writes to the `settings_plugins` table and provides a UI for managing tokens, keys, and channel-specific settings.
