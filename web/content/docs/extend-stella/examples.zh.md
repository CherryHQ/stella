---
title: 扩展示例
description: Stella 所支持扩展路径的简短示例。
---

## Provider

`plugins/providers/openai/` 导出一个 `Definition()`，包含稳定 metadata 与把
`providers.Config` 转成包内 config 的 builder。`cmd/stellad/setup_providers.go`
显式引入它，因此无需 `init()` 副作用也能审计支持的 provider 类型。

## 沙箱后端

`plugins/sandbox/docker/` 持有 Docker 专属 session 行为，不 import Stella
内部包。`cmd/stellad/setup_sandboxes.go` 把该 factory 适配进 agent backend
registry，同时提供所选镜像、内建 bundle revision、mount sources 与错误指引。

## Managed Channel

`plugins/channels/telegram/plugin.go` 是 `RegisterManagedChannelPlugin` 的参考。
它在同一个 channel ID 下持有 metadata、配置 schema 与校验、脱敏、configured
判断和 runtime 构造。

## Manifest CLI 工具

GitHub CLI 只使用 manifest：entry 声明 managed binary 与 OAuth 支持的 session
env。它没有 Go `tool/gh` 包，也没有 runtime 注册 callback。Schema 与完整示例见
[Manifest 工具集成](/docs/extend-stella/manifest-tools)。
