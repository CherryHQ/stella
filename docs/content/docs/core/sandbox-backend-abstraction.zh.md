---
title: 沙箱后端抽象
---

## 状态

已实现。Docker 是唯一的沙箱后端。Anna 的本地执行边界由 `pkg/sandbox` 契约描述，runner 侧注册配置位于 `internal/sandbox`。

## 目的

沙箱抽象的目的是使 runner 代码、插件配置和工具执行不依赖于具体的后端类型。所有沙箱化执行都在 Docker 容器中运行。Docker 是必要条件；当 Docker 守护进程在会话创建时不可用时，Anna 会拒绝失败（fail closed）。

顶层模型：

- `pkg/sandbox.Policy` — 不可变的、后端无关的执行策略（文件系统根目录、工作目录、网络模式、环境变量、超时）
- `pkg/sandbox.Session` — 每次运行的执行边界和生命周期所有者；将生命周期和宿主机访问合并为单一接口
- Runner 拥有的文件 I/O — runner 使用 `os.*` 配合 `Session.ResolvePath` 读写文件；`Session` 上没有 `ReadFile`/`WriteFile` 方法

后端标识保留在 runner 和 sandbox 包内部。插件包不导入 `internal/sandbox`。

## Session 接口

`pkg/sandbox.Session` 暴露 8 个方法：

| 方法                                                       | 描述                                               |
| ---------------------------------------------------------- | -------------------------------------------------- |
| `Policy() Policy`                                          | 返回会话创建时使用的不可变策略                     |
| `Exec(ctx, command, ExecOptions) (ExecResult, error)`      | 运行命令并等待结果                                 |
| `StartProcess(ctx, ProcessRequest) (ProcessHandle, error)` | 启动带 stdio 句柄的长期运行进程                    |
| `ResolvePath(path string) (string, error)`                 | 将沙箱相对路径转换为宿主机路径，供 `os.*` 调用使用 |
| `WorkingDir() string`                                      | 返回沙箱内的逻辑工作目录                           |
| `Close() error`                                            | 关闭会话并释放资源                                 |
| `Alive() bool`                                             | 报告会话是否仍然活跃                               |
| `Done() <-chan struct{}`                                   | 会话终止时关闭的 channel                           |

文件 I/O（`read`、`write`、`edit`）由 runner 拥有：runner 调用 `ResolvePath` 获取宿主机路径，然后直接使用 `os.ReadFile`/`os.WriteFile`/`os.MkdirAll`。`Session` 不包含文件读写方法。

## Docker 要求

Docker 是唯一的沙箱后端。Docker 守护进程必须正在运行且可访问。Anna 在会话创建时连接 Docker 守护进程，如果不可用则拒绝失败：

- Docker 守护进程缺失或无法访问 → 会话创建失败，runner 不启动
- 不支持的策略 → `PolicyCompatibilityError`，runner 不启动
- 不存在静默降级路径

所有平台（Linux、macOS、Windows）使用相同的 Docker 支持会话。不存在 `auto`、`boxsh` 或 `Relaxed` 模式。

## 配置

每个代理的沙箱配置仅限于网络策略（模式和允许列表）。每个代理独立控制其沙箱是否允许出站网络访问以及哪些主机可达。

Docker 后端支持的网络模式：

| 模式        | 描述                         |
| ----------- | ---------------------------- |
| `disabled`  | 禁止所有出站网络访问（默认） |
| `allow_all` | 不受限制的出站访问           |

`whitelist` 模式已移除。Anna 在会话创建时验证已配置的模式，如果后端无法强制执行则拒绝失败。

## 当前架构

### 会话所有权

runner 为每次运行创建一个 `sandbox.Session` 并持有其生命周期所有权。当没有可用的活动沙箱会话时，runner 构建失败。

### 后端解析

Docker 后端通过 `init()` 在 `internal/sandbox` 中自注册。`pkg/sandbox.Registry.CreateSession` 按注册顺序迭代已注册的工厂，使用第一个可用且支持该策略的工厂。由于只注册了一个工厂，这总是选择 Docker 或失败。

### 执行时中介

所有必须遵守沙箱策略的本地执行路径都通过活动 runner 会话进行中介：

- 核心工具（`bash`）通过 runner 拥有的会话使用 `Session.Exec`
- `read`、`write`、`edit` 使用 `Session.ResolvePath` 然后通过 `os.*` 进行文件 I/O
- 插件工具接收 `ToolContext.Runtime`，这是活动会话上的 `pkg/plugins.ToolRuntime` 适配器
- 技能和代理预设加载在代理会话内运行时使用 `ToolRuntime`
- MCP stdio 进程派生使用 `Session.StartProcess`

### stdio-MCP 优势

Docker 后端在每次运行时完全支持 `Session.StartProcess`。这意味着 MCP stdio 传输在所有平台上均可统一工作，无需特定于平台的子进程处理。

### 非 runner 文件系统访问

某些代码路径需要在没有已注入运行时的情况下访问本地文件系统，例如活动代理运行之外的提示渲染或元数据发现。

当没有 runner 会话时，提示渲染回退到直接 `os.*` 调用。在活动 runner 外部调用时，技能和代理预设发现使用 `pkg/plugins.NewLocalToolRuntime(...)`。这些是有意为之的非 runner 路径，而不是沙箱化工具执行的回退。

### 显式例外边界

远程 MCP HTTP/SSE/StreamableHTTP 传输目前被视为独立的信任边界。

- 本地 stdio 传输通过 `Session.StartProcess` 经活动 runner 会话进行运行时中介
- 远程传输拨号目前**不**由 `ToolRuntime` 中介
- 此例外被显式跟踪为 `EX-009`，并记录为 `runtime.exception_path`

## 拒绝失败行为

Anna 优先选择显式拒绝而非静默降级：

- 会话创建时 Docker 不可用 → runner 启动失败
- 不支持的策略 → `PolicyCompatibilityError`，runner 启动失败
- 直接非中介的插件 exec → 拒绝失败
- 远程 MCP HTTP/SSE/StreamableHTTP → 显式例外，而非隐式沙箱绕过

## 验证

该抽象由以下测试覆盖：

- 会话/宿主机契约测试
- 策略兼容性测试
- 核心工具一致性测试
- Docker 后端集成测试
- 已迁移运行时路径的静态绕过回归保护

## 相关文档

- [架构](/docs/core/architecture)
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/design-spec.md`
- `.agents/sessions/2026-04-12-sandbox-interface-redesign/exceptions-register.md`
