---
title: 沙箱后端抽象
---

> 本节面向为 Stella 贡献代码的开发者。

## 状态

已实现。Docker 是推荐的沙箱后端。本地后端也可用于无 Docker 环境；Linux 保留操作系统级加固，而 macOS 当前会直接在宿主机上运行本地命令，不再附加额外沙箱。`none` 后端也可用于完全受信任的工作负载——它以当前用户权限直接在宿主机上运行代理，不提供任何隔离。Stella 的执行边界由 `pkg/sandbox` 契约描述，后端分发逻辑位于 `internal/agent/sandbox/session.go`。

## 目的

沙箱抽象的目的是使 runner 代码、插件配置和工具执行不依赖于具体的后端类型。执行总是通过 runner 选中的活动后端进行。Docker 提供最强隔离；本地后端是 Docker 不可用或不想使用时的回退方案；`none` 后端则为完全受信任的单用户本地部署跳过所有隔离。

顶层模型：

- `pkg/sandbox.Policy` — 不可变的、后端无关的执行策略（文件系统根目录、工作目录、网络模式、环境变量、超时）
- `pkg/sandbox.Session` — 每次运行的执行边界和生命周期所有者；将生命周期和宿主机访问合并为单一接口
- Runner 拥有的文件 I/O — runner 使用 `os.*` 配合 `Session.ResolvePath` 读写文件；`Session` 上没有 `ReadFile`/`WriteFile` 方法

后端标识保留在 runner 和面向 runner 的 sandbox 包内部。插件包不导入 `internal/agent/sandbox`。

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

## 后端

### Docker（推荐）

Docker 提供完整的容器级进程、文件系统和网络隔离。Docker 守护进程必须正在运行且可访问。Stella 在会话创建时连接 Docker 守护进程，如果不可用则拒绝失败：

- Docker 守护进程缺失或无法访问 → 会话创建失败，runner 不启动
- 不支持的策略 → `PolicyCompatibilityError`，runner 不启动
- 不存在静默降级路径

所有平台（Linux、macOS、Windows）均支持 Docker 后端。不存在 `auto` 或 `Relaxed` 模式。

### 本地后端（无 Docker）

本地后端直接在宿主机 OS 上运行命令。适用于 Docker 不可用的环境（无 Docker 的 CI、嵌入式部署、不希望运行守护进程的开发机器）。

**此后端不提供容器级隔离。** 它应用操作系统级加固层作为替代：

| 层级                  | 平台      | 机制                                                                                                                                                                                                                                                    |
| --------------------- | --------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 进程组终止 + 资源限制 | 所有 Unix | 对进程组发送 `SIGKILL`；通过 `prlimit(2)` 设置 `RLIMIT_FSIZE`、`RLIMIT_NOFILE`、`RLIMIT_CPU`                                                                                                                                                            |
| 文件系统 + 网络隔离   | Linux     | `bwrap`（必需）— 最小可用 Linux 根环境，`/workspace` 读写，将宿主机按用户隔离的 `/tmp/{user_id}` 挂载为沙箱内 `/tmp`，`/var/tmp` 与 `/dev/shm` 为可写 tmpfs，选定的运行时/工具目录和 DNS 解析配置只读挂载；网络模式为 `disabled` 时附加 `--unshare-net` |
| 无额外本地隔离        | macOS     | 命令直接在宿主机 OS 上运行；不强制执行文件系统和网络策略                                                                                                                                                                                                |

本地后端在 Linux 上采用**拒绝失败**策略：`bwrap`（bubblewrap）为必需项。若 bwrap 不存在或不可用（例如在未启用 `--privileged` 的 Docker 容器内），会话创建失败并返回包含操作建议的错误信息。不存在回退到仅 `unshare` 或无隔离执行的降级路径。本地沙箱进程不会继承完整宿主机环境；Stella 只注入 runner 管理的会话变量以及少量语言环境/终端/代理允许列表。macOS 当前不再附加额外沙箱工具。

#### 安装依赖

**Linux — bubblewrap（必需）：**

```bash
# Debian / Ubuntu
apt install bubblewrap

# Fedora / RHEL
dnf install bubblewrap

# Arch
pacman -S bubblewrap
```

bwrap 必须实际可用，仅安装不够。在未启用 `--privileged` 的 Docker 容器内，内核 seccomp 配置文件通常会阻止命名空间创建，即使 bwrap 已安装也无法使用——此类环境请改用 Docker 后端。

**macOS：**
无需额外依赖。当前本地后端直接在宿主机 OS 上运行命令，不应用特定于 macOS 的沙箱。

**Windows：** 不支持，请使用 Docker 后端。

#### 路径呈现

在 Linux 上，无论真实宿主机路径如何，代理始终将工作区看作 `/workspace`（bwrap 负责绑定挂载），与 Docker 绑定挂载行为一致。Docker 和 Linux 本地会话会把宿主机 `/tmp/{user_id}` 挂载为沙箱内 `/tmp`，因此沙箱内可以正常使用临时文件路径，同时仍按用户隔离。这个 `/tmp` 目录有意在同一用户的多个会话之间共享，并且不会在会话关闭时删除；只应存放临时数据，清理依赖 OS 临时目录保留策略或管理员维护。在 macOS 上，代理看到的是真实宿主机路径。

### None（宿主机直接执行）

`none` 后端以当前用户权限直接在宿主机 OS 上运行代理，不提供任何隔离——无文件系统限制、无网络限制、无进程组终止，也无资源限制（rlimits）。代理继承完整宿主机环境，并与 runner 注入的会话变量合并。

**仅在单用户本地部署中对完全受信任的代理使用。** 此后端不适用于不受信任的代理或多用户环境。

- 无外部依赖——适用于所有平台
- `ResolvePath` 将相对路径解析为工作目录下的绝对路径；绝对路径原样返回
- 网络策略始终为 `allow_all`；每个代理配置的网络模式将被忽略
- 不会因缺少工具而导致会话创建失败

## 配置

每个代理的沙箱配置仅限于网络策略（模式和允许列表）。每个代理独立控制其沙箱是否允许出站网络访问以及哪些主机可达。

Docker 后端以及 Linux 本地后端支持的网络模式：

| 模式        | 描述                         |
| ----------- | ---------------------------- |
| `disabled`  | 禁止所有出站网络访问（默认） |
| `allow_all` | 不受限制的出站访问           |

`whitelist` 模式已移除。Docker 和 Linux 本地后端会在会话创建时验证已配置的模式，如果后端无法强制执行则拒绝失败。macOS 本地后端当前会忽略网络策略并使用宿主机网络访问。

## 当前架构

### 会话所有权

runner 为每次运行创建一个 `sandbox.Session` 并持有其生命周期所有权。当没有可用的活动沙箱会话时，runner 构建失败。

### 后端解析

runner 会根据插件状态解析当前活动后端，并分派到对应的后端工厂。内置工厂当前支持 `docker`、`local` 和 `none`。

### 执行时中介

所有必须遵守沙箱策略的本地执行路径都通过活动 runner 会话进行中介：

- 核心工具（`bash`）通过 runner 拥有的会话使用 `Session.Exec`
- `read`、`write`、`edit` 使用 `Session.ResolvePath` 然后通过 `os.*` 进行文件 I/O
- 插件工具接收 `ToolContext.Runtime`，这是活动会话上的 `pkg/plugins.ToolRuntime` 适配器
- 技能和代理预设加载在代理会话内运行时使用 `ToolRuntime`
- MCP stdio 进程派生使用 `Session.StartProcess`

### stdio-MCP 优势

两个内置后端都支持 `Session.StartProcess`。Docker 为 stdio MCP 服务器提供独立的容器进程命名空间；本地后端则直接在宿主机上启动这些进程。

### 非 runner 文件系统访问

某些代码路径需要在没有已注入运行时的情况下访问本地文件系统，例如活动代理运行之外的提示渲染或元数据发现。

当没有 runner 会话时，提示渲染回退到直接 `os.*` 调用。在活动 runner 外部调用时，技能和代理预设发现使用 `pkg/plugins.NewLocalToolRuntime(...)`。这些是有意为之的非 runner 路径，而不是沙箱化工具执行的回退。

### 显式例外边界

远程 MCP HTTP/SSE/StreamableHTTP 传输目前被视为独立的信任边界。

- 本地 stdio 传输通过 `Session.StartProcess` 经活动 runner 会话进行运行时中介
- 远程传输拨号目前**不**由 `ToolRuntime` 中介
- 此例外被显式跟踪为 `EX-009`，并记录为 `runtime.exception_path`

## 拒绝失败行为

Stella 优先选择显式拒绝而非静默降级：

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

## 添加新后端

每个新沙箱后端需要在以下所有位置进行修改——遗漏任何一处都会导致运行时错误：

| 步骤 | 文件                                                                                    | 操作                                                                                              |
| ---- | --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| 1    | `internal/config/sandbox.go`                                                            | 添加 `SandboxBackend<Name> = "<name>"` 常量                                                       |
| 2    | `internal/config/plugin.go`                                                             | 将名称追加到 `builtinSandboxNames`，确保 DB 行被初始化                                            |
| 3    | `plugins/sandbox/<name>/session.go`                                                     | 实现 `sandbox.Factory` 和 `sandbox.Session`                                                       |
| 4    | `plugins/sandbox/plugin.go`                                                             | 在 `init()` 的 `backends` 切片中添加条目，注册 `AdminVisible` 插件元数据                          |
| 5    | `internal/agent/sandbox/session.go`                                                     | 在 `createSessionForBackend` 中添加 `case config.SandboxBackend<Name>:` 分支，并实现工厂函数      |
| 6    | `web/src/features/plugins/PluginsPage.tsx` 和 `web/src/features/plugins/pluginUtils.ts` | 将 `"sandbox/<name>"` 添加到 `validSandboxBackends`，并在 `sandboxMeta` 中添加包含特性/限制的条目 |
| 7    | 文档                                                                                    | 更新本文件及对应英文版                                                                            |

## 相关文档

- [架构](/docs/development/architecture)
