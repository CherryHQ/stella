---
title: 创建扩展
description: 添加 provider、沙箱后端、channel 或 manifest CLI 集成。
---

## 先选择类型

不要一上来就给通用 host 增加 capability。选择生命周期匹配的现有类型：

1. CLI 集成使用内建 manifest。
2. LLM adapter 导出 `providers.Definition`。
3. 隔离后端实现 `pkg/sandbox` 契约。
4. 消息 channel 使用 managed plugin host。
5. 其他能力留在所属内部包，直到确实需要独立生命周期。

## 添加 Provider

在 `plugins/providers/<name>/` 创建包，由它持有 adapter 并导出
`Definition() providers.Definition`。把 definition 加入 `cmd/stellad` 的
`setupProviderRegistry()`。测试 adapter 行为，并断言组合根公开了新类型。

Provider 包不得 import `internal/**`。凭据、配置后的 base URL、模型与启用状态
仍属于 provider 控制面。

## 添加沙箱后端

在 `plugins/sandbox/<name>/` 创建包并实现公开 session 与 factory 契约。在
`setupSandboxBackends()` 增加 `BackendDefinition` adapter，让组合根提供内部
policy 与进程级依赖。

先独立测试后端包，再测试组合根 adapter。不要让 `internal/agent/sandbox` import
具体后端。

## 添加 Channel

创建 `plugins/channels/<name>/`，通过 `RegisterManagedChannelPlugin` 注册。该包
持有配置解码、校验、脱敏、channel 构造、runtime reconcile 与针对性测试。再把
blank import 加入 channel catalog imports。

Channel 改动还要验证配置、通知、启动、quiesce 与 shutdown 行为。Provider 与
sandbox 改动不经过此路径。

## 验证边界

更新对应的组合根测试、运行包测试，并保持 `internal/boundary_test.go` 通过。
新增行为、配置或架构时，同时更新中英文文档。
