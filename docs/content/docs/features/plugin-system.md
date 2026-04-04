---
title: Plugin System
---

## Overview

Anna uses a unified subprocess plugin model. Every plugin -- both built-in and user-installed -- is a separate process that communicates with anna over a versioned stdio protocol (JSON-RPC style). There are no JavaScript plugins or in-process hooks.

Two plugin kinds:

- **Tool plugins** provide tools that the LLM agent can call (e.g., `read`, `bash`, `edit`, `write`, `webfetch`).
- **Channel plugins** provide messaging platform integrations (e.g., `telegram`, `qq`, `feishu`, `weixin`).

## Built-in Plugins

Anna ships with 9 built-in plugins, compiled into the binary:

| Kind    | Name       | Description                      |
| ------- | ---------- | -------------------------------- |
| tool    | read       | Read files                       |
| tool    | bash       | Execute shell commands           |
| tool    | edit       | Edit files (search and replace)  |
| tool    | write      | Write files                      |
| tool    | webfetch   | Fetch web pages                  |
| channel | telegram   | Telegram bot                     |
| channel | qq         | QQ bot                           |
| channel | feishu     | Feishu (Lark) bot                |
| channel | weixin     | WeChat bot (via iLink)           |

Built-in plugins use the same subprocess protocol as user-installed plugins. They can be replaced by installing a plugin with the same slot name.

## Plugin Manifest

Every plugin is defined by a `plugin.json` manifest:

```json
{
  "name": "my-tool",
  "version": "1.0.0",
  "kind": "tool",
  "protocol_version": "1",
  "description": "What the plugin does.",
  "entrypoint": "./my-tool-binary",
  "tools": [
    {
      "name": "my_tool",
      "description": "Tool description for the LLM.",
      "input_schema": {
        "type": "object",
        "properties": {
          "query": { "type": "string", "description": "Search query" }
        }
      }
    }
  ]
}
```

## Storage

Plugin state is stored in the `settings_plugins` table in the database. Each plugin has:

| Field     | Type       | Description                                         |
| --------- | ---------- | --------------------------------------------------- |
| `id`      | string     | Plugin ID (`kind/name`, e.g., `tool/read`)          |
| `kind`    | string     | `tool` or `channel`                                 |
| `name`    | string     | Plugin name                                         |
| `enabled` | bool       | Whether the plugin is active                        |
| `config`  | JSON map   | Plugin-specific configuration (tokens, keys, etc.)  |

## CLI Commands

```bash
anna plugin list               # List all plugins with status
anna plugin add <path>         # Install a plugin from a directory with plugin.json
anna plugin remove <name>      # Remove an installed plugin (alias: rm)
anna plugin enable <name>      # Enable a plugin
anna plugin disable <name>     # Disable a plugin
anna plugin config <name>      # View/set plugin configuration
```

The `add` command copies the plugin directory to `~/.anna/plugins/installed/` and registers it in the database. The `remove` command removes the entry and the installed files.

## User-Installed Plugins

User plugins are installed to `~/.anna/plugins/installed/<name>/`. Each directory must contain a `plugin.json` manifest and the entrypoint binary or script.

To install a plugin:

```bash
anna plugin add /path/to/my-plugin
```

This copies the plugin to the install directory, registers it in the database, and enables it. The plugin is loaded on the next anna startup.

## Protocol

Plugins communicate with anna over stdin/stdout using a JSON-based protocol:

1. **Host sends request** (JSON line on stdin): `{"method": "execute", "params": {"tool": "my_tool", "input": {...}}}`
2. **Plugin sends response** (JSON line on stdout): `{"result": "tool output text"}` or `{"error": "error message"}`

Plugin stderr is forwarded to anna's structured logs.

## Security Model

- Plugins run out of process -- a crash does not crash the main anna daemon.
- Subprocess plugins are supervised and restarted when appropriate.
- Tool plugins are sandboxed to allowed directories via path validation.
- Plugin stderr is captured and forwarded into anna's structured logs.
