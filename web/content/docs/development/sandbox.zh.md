---
title: 沙箱后端抽象
---

> 本节面向为 Stella 贡献代码的开发者。选择和配置沙箱后端请参阅[沙箱指南](/docs/guides/sandbox)。

## 核心模型

沙箱抽象的目的是使 runner 代码、插件配置和工具执行不依赖于具体的后端类型。执行总是通过 runner 选中的活动后端进行。

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

## 本地运行 Docker 后端

`mise run dev:docker` 一条命令拉起整套栈，对齐生产的 `docker-compose.yml`：`stellad` 跑在**容器内**，docker 沙箱后端走 **volume 模式**（`STELLA_SANDBOX_BACKEND=docker`、`STELLA_DOCKER_SANDBOX_MODE=volume`、`STELLA_HOME_VOLUME=stella-data`），外加一个 `otel-lgtm` 边车。它会构建本地镜像（`docker:build` → `stella:latest`、`sandbox:docker:build` → `stella-sandbox:dev`）、按需新建命名卷，并确保 `~/.stella-dev/.env` 里有 dev vault key。它跑的是和 prod 同一份 `docker-compose.yml`，只是导出 `STELLA_IMAGE=stella:latest`，从而用本地构建而非发布镜像。

容器内 Go 服务器在 `localhost:25688` 提供其烤进镜像的内嵌 SPA（见 `web/embed.go`），Grafana 在 `localhost:13413`。

用 `docker compose down` 停掉整套栈。

sandbox 镜像通过 `stellad mise reconcile-builtins`（与宿主相同的 `resources/tools.yaml` reconcile）把 mise 工具链烤在 `/opt/stella`，因此 docker 与 Linux `local` 后端呈现完全一致的 mise 路径。

## builtin Skill bundle 与投影

`resources.Registry` 是发行版自带 builtin 的唯一权威。它产出不可变、内容寻址的 bundle，供原生 `local` 和 `none` 执行安装到 `$STELLA_HOME/bundles/<revision>`。隔离执行将这一精确 bundle 以只读方式投影到 `/opt/stella/skills/builtin`；`/opt` 是执行坐标而非另一份权威，bundle 中辅助可执行文件的模式必须在投影中保留。

Project Skill 仍是持久 Agent/项目工作树中的普通文件。PostgreSQL 仍是可变 `system`、`system_agent`、`user` 和 `user_agent` 记录的权威；它们的执行 materialization 是派生缓存。Home 文件系统权威切换已规划但尚未启用。

Docker 沙箱镜像会烤入并标记精确 revision，不会回退到宿主机 builtin。Docker provider preflight 拒绝二进制与镜像 revision 不匹配的组合，从而阻止 runner session 启动。操作员命令语法使用 `stellad system-bundle --help` 查询。开发镜像用 `mise run sandbox:docker:build` 重建；每个自定义沙箱镜像都必须从匹配的 Stella revision 重建。

升级前，操作员必须使用旧的可工作二进制，将遗留 `$STELLA_HOME/.agents/skills` 下的每个自定义 Skill 根导入为全局（`system`）Skill：旧版入口为 **设置 → 技能**，新版入口为 **管理控制台 → 部署资源 → 全局技能**。其他残留路径必须先备份、验证后删除。启动会报告每个阻塞路径并退出，不会修改任何内容。当前 manifest 路径即使内容或模式不同也只是惰性数据；其他每个 Skill 根或残留路径都会阻塞启动。

## Agent Skill 策略

存储的作用域词汇为 `system`、`system_agent`、`user`、`user_agent` 和 `project`，另加上下文作用域 `builtin`。发行版的 `builtin:<name>` 不可变。管理员安装的 `system:<name>` 与绑定 Agent 的 `system_agent:<name>` 是独立的可变身份。

解析会先选择唯一的胜出项，再应用策略：`project > user_agent > user > system_agent > system > builtin`。禁用该胜出项不会暴露同名的低优先级 Skill。策略默认启用、按 Agent 共享，且与编辑内容的授权、`disable_model_invocation` 彼此独立。已接纳的 turn 保留其快照，下一次 turn 才会看到成功提交。旧版非空数组是诊断信息但表示全部启用；悬空的禁用引用不影响执行，需显式清理。

## 添加新后端

每个新沙箱后端需要在以下所有位置进行修改——遗漏任何一处都会导致运行时错误：

| 步骤 | 文件                                | 操作                                                                                         |
| ---- | ----------------------------------- | -------------------------------------------------------------------------------------------- |
| 1    | `internal/config/sandbox.go`        | 添加 `SandboxBackend<Name> = "<name>"` 常量                                                  |
| 2    | `internal/config/sandbox_env.go`    | 在 `ActiveSandboxBackend` 的 `STELLA_SANDBOX_BACKEND` switch 中接受该名称                    |
| 3    | `plugins/sandbox/<name>/session.go` | 实现 `sandbox.Factory` 和 `sandbox.Session`                                                  |
| 4    | `internal/agent/sandbox/session.go` | 在 `createSessionForBackend` 中添加 `case config.SandboxBackend<Name>:` 分支，并实现工厂函数 |
| 5    | 文档                                | 更新[沙箱指南](/docs/guides/sandbox)和本文件                                                 |

## 相关文档

- [沙箱指南](/docs/guides/sandbox) — 选择和配置后端
- [架构](/docs/development/architecture)
