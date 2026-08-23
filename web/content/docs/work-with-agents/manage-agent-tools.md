---
title: Manage an agent's tools
description: Enable or disable the builtin and plugin tools an agent can use.
---

# Manage an agent's tools

You can choose which non-core tools an agent can use from the Web UI.

## Enable or disable a tool

1. Open **Settings** -> **Agents**.
2. Select the agent.
3. Open the **Tools** tab.
4. Toggle builtin or plugin tools on or off.

The change applies to new agent runs immediately. If a tool is disabled, the agent will not see it in its runtime tool list.

## Core tools

Core sandbox tool definitions cannot be disabled:

- `bash`, always available for shell commands and textual file operations
- `view_image`, always available for returning image pixels to the parent model
- `vllm`, available only when the deployment has configured a vision model

They are part of the sandbox boundary. Keeping them fixed avoids half-configured agents that cannot inspect or update their workspace, or cannot pass image pixels into the active model turn.

## MCP and plugin tools

MCP servers are managed from **Settings** -> **Plugins** -> **MCP servers**. The agent Tools tab shows visible MCP servers, but does not duplicate their management controls.

Plugin tools appear in the Tools tab after the plugin is enabled. Disable a plugin from the plugin settings page if you want to remove all of its capabilities.
