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

## 为什么这个设计很重要

宿主现在拥有一个公开的统一心智模型，而不是多个重叠的注册 API。

这给 Stella 带来：

- 插件级别的宿主服务访问权限
- 更清晰的所有权边界
- 更易于测试
- 更一致的管理和运行时编排

## 内置插件领域

Stella 在多个领域提供内置插件：

| 类型     | 示例                                            |
| -------- | ----------------------------------------------- |
| tool     | `webfetch`、`notify`                            |
| channel  | `telegram`、`discord`、`qq`、`feishu`、`weixin` |
| hook     | `trace`                                         |
| provider | `anthropic`、`openai`、`openai-response`        |
| memory   | `lcm`、`simple`                                 |

## 声明式能力

Stella 是单租户、多用户、多代理系统：一个部署服务众多用户与代理，不做按 org 的分区。

插件 `Platform` **并非**环境式（ambient）——插件只能触达它在 `PluginInfo.RequiredCapabilities` 中声明的宿主能力。宿主只授予这些能力、对任何未声明者返回 `nil`，并在密封（seal）时校验每个所声明能力都有注入的宿主服务支撑，然后才启动托管运行时。旧的"每个插件都能看到每个服务"的接口已不复存在。能力清单与访问器契约详见[平台 API](/docs/extend-stella/platform)。

## 阅读插件文档

功能概述有意保持简短。要了解实际的插件 API 和开发模型，请使用专门的插件文档：

- [插件概述](/docs/extend-stella/overview)
- [创建插件](/docs/extend-stella/create-a-plugin)
- [能力](/docs/extend-stella/capabilities)
- [平台 API](/docs/extend-stella/platform)
- [示例](/docs/extend-stella/examples)
