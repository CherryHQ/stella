---
title: Channel Platform API
description: Managed channel plugin 可使用的限定宿主服务。
---

## 范围

`pkg/plugins.Platform` 属于 managed plugin host。目前它的生产消费者是 channel
类型；provider 与 sandbox 扩展不使用它。

Channel 在 `PluginInfo.RequiredCapabilities` 中声明所需能力。Host 在 seal 时
检查每项声明都有后端服务。访问未声明能力会返回 `nil`。

## 可用服务

接口提供 `Logger`、`ConfigStore`、`StateStore`、`Notifier`、`Auth`、
`RuntimeLookup` 与 `ChannelPlatform`，每个 accessor 都受对应 capability 限制。
`ChannelPlatform` 提供构建 managed channel runtime 所需的 parent context、
channel handler 与 notification registry。

## 规则

- 只声明 channel 确实需要的服务，并 nil-check 可选 accessor。
- `ConfigStore` 存 desired config，`StateStore` 存派生运行状态。
- 状态查询使用 `RuntimeLookup`，不要保留全局 runtime pointer。
- 不要把 provider 或 sandbox 依赖加入 `Platform`，它们的类型化 registry 会直接
  注入实际消费者。

Manifest 集成不能声明或获得 `Platform` capability。
