---
title: 插件系统
---

> 本节面向为 Stella 贡献代码的开发者。

## Plugin 表示所有权，不表示统一运行时 API

Stella 会把可信集成编译进 `stellad`，但不会让所有集成都经过同一个万能宿主。
每类集成使用符合自身生命周期的最小契约：

| 类型     | 注册方式                                              | 运行时所有者                |
| -------- | ----------------------------------------------------- | --------------------------- |
| Provider | 在 `cmd/stellad` 显式列出 `providers.Definition`      | provider registry           |
| 沙箱后端 | 在 `cmd/stellad` 显式列出 `sandbox.BackendDefinition` | agent sandbox               |
| Channel  | 编译期 catalog 注册                                   | channel plugin host         |
| CLI 工具 | 内建 manifest                                         | 沙箱 session 与 prompt 组装 |

Agent 原生工具、hook 和 memory 实现仍是内部模块。不要只为目录形式统一就把它们改成插件。

## 依赖边界

`plugins/providers/**` 与 `plugins/sandbox/**` 的生产代码可以依赖 `pkg/**` 下的
公开契约、同类插件包、标准库及第三方模块，但不得 import `internal/**`。

`cmd/stellad` 是唯一允许同时了解两侧的组合根。它选择编译内置的 definition、
注入 Stella 持有的依赖、校验重复 ID，再把不可变 registry 注入执行服务。
`internal/boundary_test.go` 中的表驱动守卫负责维持这个方向。

## Provider adapter

每个 provider 包导出一个 `Definition()`，其中包含 ID、展示信息、默认 URL 与
adapter builder。`setupProviderRegistry()` 显式列出支持的 definitions。只新增包、
不增加组合根布线，不会产生任何运行时效果。

Provider 的凭据、base URL、模型与启用状态由 provider 控制面管理。Provider
不是 plugin row，也不通过 plugin 管理界面启用或重载。

## 沙箱后端

沙箱包实现公开的 sandbox 接口。组合根把它适配成 `sandbox.BackendDefinition`，
并提供嵌入式 runtime bundle、开发镜像选择等进程级依赖。Agent sandbox 只接收
最终 registry，永远不 import 具体后端。

Runner 配置会静态选择后端。Stella 不在运行时加载第三方后端代码，因此不提供
动态 unregister 或 rollback 协议。

## Channel

Channel 仍使用现有的 managed plugin host 与包注册。每个 channel 插件自己拥有
持久化配置类型、完整 decoder、校验和脱敏逻辑。Host 只保存 channel 注册信息，
并从注册项取得可选的访客策略 decoder。访客准入始终依据当前持久化记录，缺少
decoder 或完整配置解析失败时拒绝。

账户开户是独立且受 capability 保护的 host 端口。插件只能取得宿主绑定的命名空间
与开户契约，消息 handler 不会通过类型断言获得账户写权限。飞书租户与 canonical
subject 等平台取证仍由所属插件负责。当前 channel 开发方式见
[创建插件](/docs/extend-stella/create-a-plugin)。

## Manifest CLI 集成

只贡献 binary、skill、prompt 指引或 session 环境变量的 CLI 集成使用内建
manifest，无需 Go plugin 包。见
[Manifest 工具集成](/docs/extend-stella/manifest-tools)。

## 如何选择边界

使用第一个够用的契约：

1. 普通行为留在所属的内部包。
2. 声明式 CLI 集成使用 manifest。
3. 编译内置的 provider 或沙箱后端使用对应的类型化 definition。
4. 只有 managed channel 生命周期使用 plugin host。

给通用 host 新增 capability 是升级决策，不是默认扩展方式。
