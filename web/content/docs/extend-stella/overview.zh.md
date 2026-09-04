---
title: 扩展概览
description: 选择与集成类型匹配的最小扩展契约。
---

## 没有万能 Plugin Runtime

Stella 会把可信 Go 集成编译进 `stellad`。"Plugin" 描述集成的所有权，不表示所有
集成都要实现同一套 API。

| 需求                                     | 扩展方式                                    |
| ---------------------------------------- | ------------------------------------------- |
| LLM API adapter                          | `providers.Definition` 与 provider registry |
| 进程或文件系统隔离                       | 公开 sandbox 接口与注入式 backend registry  |
| 消息入口与通知                           | managed channel plugin host                 |
| CLI binary、skill、prompt 或 session env | 内建 manifest                               |

Agent 原生工具、hook 和 memory 实现留在所属的内部包。Stella 不会在运行时加载
第三方 Go binary 或 subprocess plugin。

## 组合与依赖

`cmd/stellad` 是组合根。它显式列出 provider 与 sandbox definitions，提供 Stella
持有的依赖、校验 ID，并注入最终 registry。

`plugins/providers/**` 与 `plugins/sandbox/**` 的生产包不得 import `internal/**`。
Channel 包使用公开的 `pkg/plugins` 契约与现有编译期 catalog。依赖守卫位于
`internal/boundary_test.go`。

## 继续阅读

- [创建扩展](/docs/extend-stella/create-a-plugin)
- [能力参考](/docs/extend-stella/capabilities)
- [Manifest 工具集成](/docs/extend-stella/manifest-tools)
- [Platform API](/docs/extend-stella/platform)，仅适用于 managed channel
