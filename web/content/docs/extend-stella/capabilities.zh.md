---
title: 扩展能力
description: Stella 各类型扩展契约的参考。
---

## Provider Definition

`pkg/providers.Definition` 包含 `ID`、`Name`、`DefaultURL` 与 `Build`。`Build`
接收部署持有的 `APIKey` 和 `BaseURL`，返回 `ProviderAdapter`。Registry 会拒绝
空 metadata、nil builder 与重复 ID，并向控制面提供按 ID 排序的 provider 类型。

## 沙箱后端

后端包实现 `pkg/sandbox` 中的公开契约。创建过程还需要内部 runner path、policy、
mount source 与用户身份，因此组合根负责把后端适配成
`internal/agent/sandbox.BackendDefinition`。Agent 包只接收经过校验的 registry。

## Managed Channel

Channel 使用 `pkg/plugins.RegisterManagedChannelPlugin`。每个 managed channel
拥有自己的 `PluginInfo`、默认配置与 schema、校验与脱敏、configured 检查及
runtime factory。其公开 host capabilities 是 `AdminSpec`、`ChannelSpec` 与
`RuntimeSpec`。

Host 仍包含旧的 tool、hook、prompt 与 lifecycle capability 类型，但它们不是新
原生功能的默认开发路径。没有真实的 managed extension 边界时，原生行为应留在
所属内部模块。

## Manifest CLI 集成

Manifest entry 可以贡献 binary、bundled skill、prompt 指引、session env 与 OAuth
provider，但不能获得 host `Platform` 服务或任意 Go callback。
