---
title: Plugin System
---

> This section is for developers contributing to Stella.

## Overview

Stella uses a compiled-in plugin system. Plugins are built into the `stella` binary and registered at startup through Go package initialization.

The final public design is intentionally small:

- `Host` is the registration surface
- `Platform` is the scoped service surface used inside capability callbacks
- one plugin can own multiple capabilities under a single plugin ID

This lets a feature own its metadata, config, status, runtime lifecycle, and tool exposure in one place without leaking host internals into plugin code.

## What Plugins Can Own

Built-in plugins can register capabilities for:

- tools
- providers
- channels
- hooks
- memory providers
- managed runtimes
- config and status
- prompt inventory
- system prompt sections
- lifecycle hooks

Examples in the current tree:

- `tool/notify` is a simple tool plugin
- `tool/mcp` owns config, runtime, status, tool exposure, and prompt inventory
- `channel/telegram` owns config, status, channel registration, and runtime lifecycle
- `reflect` owns config, status, and a managed runtime

## Why The Design Matters

The host now has one public mental model instead of multiple overlapping registration APIs.

That gives Stella:

- plugin-scoped access to host services
- cleaner ownership boundaries
- easier testing
- more coherent admin and runtime orchestration

## Built-In Plugin Areas

Stella ships built-in plugins across several areas:

| Kind     | Examples                                 |
| -------- | ---------------------------------------- |
| tool     | `mcp`, `webfetch`, `notify`              |
| channel  | `telegram`, `qq`, `feishu`, `weixin`     |
| hook     | `trace`, `rtk`                           |
| provider | `anthropic`, `openai`, `openai-response` |
| memory   | `lcm`, `simple`                          |
| runtime  | `reflect`                                |

## Read The Plugin Docs

The feature overview is intentionally short. For the actual plugin API and authoring model, use the dedicated plugin docs:

- [Plugin Overview](/docs/plugins/overview)
- [Create a Plugin](/docs/plugins/create-a-plugin)
- [Capabilities](/docs/plugins/capabilities)
- [Platform API](/docs/plugins/platform)
- [Examples](/docs/plugins/examples)
