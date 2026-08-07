---
title: Plugin System
---

> This section is for developers contributing to Stella.

## Overview

Stella uses a compiled-in plugin system. Plugins are built into the `stellad` binary and registered at startup through Go package initialization.

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
- `channel/telegram` owns config, status, channel registration, and runtime lifecycle

## Why The Design Matters

The host now has one public mental model instead of multiple overlapping registration APIs.

That gives Stella:

- plugin-scoped access to host services
- cleaner ownership boundaries
- easier testing
- more coherent admin and runtime orchestration

## Built-In Plugin Areas

Stella ships built-in plugins across several areas:

| Kind     | Examples                                        |
| -------- | ----------------------------------------------- |
| tool     | `webfetch`, `notify`                            |
| channel  | `telegram`, `discord`, `qq`, `feishu`, `weixin` |
| hook     | `trace`, `rtk`                                  |
| provider | `anthropic`, `openai`, `openai-response`        |
| memory   | `lcm`, `simple`                                 |

## Declared Capabilities

Stella is single-tenant, multi-user, multi-agent: one deployment serves many users and agents, with no per-org partitioning.

The plugin `Platform` is **not** ambient — a plugin reaches only the host capabilities it declares in `PluginInfo.RequiredCapabilities`. The host grants exactly those, returns `nil` for anything undeclared, and validates at seal time that each declared capability is backed by an injected host service before a managed runtime starts. The old "every plugin sees every service" surface is gone. See [Platform API](/docs/extend-stella/platform) for the capability list and accessor contract.

## Read The Plugin Docs

The feature overview is intentionally short. For the actual plugin API and authoring model, use the dedicated plugin docs:

- [Plugin Overview](/docs/extend-stella/overview)
- [Create a Plugin](/docs/extend-stella/create-a-plugin)
- [Capabilities](/docs/extend-stella/capabilities)
- [Platform API](/docs/extend-stella/platform)
- [Examples](/docs/extend-stella/examples)
