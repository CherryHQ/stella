---
title: 插件系统
---

> 本节面向为 Stella 贡献代码的开发者。

## 概述

Stella 采用编译内置的插件系统。插件内置于 `stellad` 二进制文件中，并在启动时通过 Go 包的 `init()` 函数完成注册。

最终公开的 API 设计有意保持精简：

- `Host` 是注册入口
- `Platform` 是能力回调内部使用的限定服务接口
- 一个插件可以在单一插件 ID 下拥有多个能力

这让一个功能可以在一个地方管理自身的元数据、配置、状态、运行时生命周期和工具暴露，而无需将宿主内部细节泄露到插件代码中。

## 插件可以拥有什么

内置插件可以注册以下能力：

- 工具（tools）
- 供应商（providers）
- 通道（channels）
- 钩子（hooks）
- 记忆提供者（memory providers）
- 托管运行时（managed runtimes）
- 配置和状态（config and status）
- 提示词库（prompt inventory）
- 系统提示词片段（system prompt sections）
- 生命周期钩子（lifecycle hooks）

当前代码库中的示例：

- `tool/notify` 是一个简单的工具插件
- `channel/telegram` 拥有配置、状态、通道注册和运行时生命周期
- `reflect` 拥有配置、状态和一个托管运行时

## 为什么这个设计很重要

宿主现在拥有一个公开的统一心智模型，而不是多个重叠的注册 API。

这给 Stella 带来：

- 插件级别的宿主服务访问权限
- 更清晰的所有权边界
- 更易于测试
- 更一致的管理和运行时编排

## 内置插件领域

Stella 在多个领域提供内置插件：

| 类型     | 示例                                     |
| -------- | ---------------------------------------- |
| tool     | `webfetch`、`notify`                     |
| channel  | `telegram`、`qq`、`feishu`、`weixin`     |
| hook     | `trace`、`rtk`                           |
| provider | `anthropic`、`openai`、`openai-response` |
| memory   | `lcm`、`simple`                          |
| runtime  | `reflect`                                |

## 按 Org 隔离

Stella 是多租户系统。每个插件运行时、manifest override、OAuth provider 配置、channel 回调都通过 `context.Context` 归属到单个 org。详见[插件按 Org 隔离](/docs/development/plugin-org-isolation)。

## 阅读插件文档

功能概述有意保持简短。要了解实际的插件 API 和开发模型，请使用专门的插件文档：

- [插件概述](/docs/extend-stella/overview)
- [创建插件](/docs/extend-stella/create-a-plugin)
- [能力](/docs/extend-stella/capabilities)
- [平台 API](/docs/extend-stella/platform)
- [示例](/docs/extend-stella/examples)
