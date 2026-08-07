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

## Provider 文件系统边界

`pkg/sandbox.Filesystem` 是持久文件操作的 provider 中立边界。调用方只能使用 `/workspace`、`/user` 或 `/tmp` 下的规范沙箱路径；该接口不会暴露宿主机路径。它支持有界流式读取、流式写入与上传，以及 stat/list、mkdir、remove 和 rename。

`local` 与受不安全开关约束的 `none` 后端直接使用根目录内受约束的文件操作实现该边界。Docker 则在沙箱容器内为每次操作启动一个 `stella-fs` helper 进程。该协议严格验证请求与响应 framing，在进程边界两端保留稳定的 `io/fs` 错误，并拒绝格式错误或超过上限的读取响应。

写操作被中断时会返回 `sandbox.ErrOutcomeUnknown`。调用方必须报告该状态且不得自动重试，因为第一次操作可能已经完成。Docker preflight 还要求镜像中的文件系统 helper revision 与正在运行的 `stellad` 二进制匹配。

该边界目前与 `Session.ResolvePath` 并存：`FilesystemSession` 暴露新实现，而现有运行时消费者仍使用旧的宿主机路径契约。后续迁移会先移动这些消费者，再删除宿主机路径方法。provider 边界不会在生产环境回退到 `ResolvePath`。

## 类型化 Home registry 与 attachment

Phase 1 将持久 Home 的类型化身份与机器路径分离。registry 为每个用户或群组 Principal Home、每 Principal 的 Agent Home，以及窄范围 system 或 system-Agent Skill 根记录不可变 Store ID 与不透明 locator。`sandbox.HomeAttachment` 是面向计算消费者的稳定契约。`internal/home.WorkspaceView` 会暂时为已迁移的当前消费者携带 local root projection，直到 Phase 2。原始 ID 相同的用户和群组仍是不同 Principal。

用户或群组运行会得到其 Principal、Agent Home attachment，以及只读的共享 Skill 根。无用户运行只得到这些只读共享 Skill 根，没有 Principal 或 Agent Home。群组 Agent Home 的 Skill materialization 不含 user 或 `user_agent` scope：它不会把群组数据变成某个用户的 `user_agent` Skill。

显式破坏性所有者删除会先 tombstone 并 fence Home，随后共享 River purge worker 清除字节。此 fencing 仅适用于单副本。Phase 3 必须加入跨副本 SessionSandbox fencing；目前尚未实现。

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
- `local`、`none` 与 Docker 共享的文件系统一致性测试
- 严格的 helper 协议与取消测试
- 使用真实镜像的 Docker 文件系统一致性测试
- Docker 二进制/镜像 helper revision preflight 测试
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

Project Skill 仍是持久 Agent/项目工作树中的普通文件。类型化 Home 文件系统是可变 `system`、`system_agent`、`user` 和 `user_agent` 内容的权威。PostgreSQL 保存 Home 身份清单、Agent Skill 策略、逻辑 Reflect 使用和 pair activity，以及迁移/审计/备份兼容性；它不保存可变 Skill 字节、当前状态或 changelog 写入。没有 PostgreSQL 回退、镜像、双读写路径或 miss 后恢复。

Docker 沙箱镜像会烤入并标记精确 revision，不会回退到宿主机 builtin。Docker provider preflight 拒绝二进制与镜像 revision 不匹配的组合，从而阻止 runner session 启动。操作员命令语法使用 `stellad system-bundle --help` 查询。开发镜像用 `mise run sandbox:docker:build` 重建；每个自定义沙箱镜像都必须从匹配的 Stella revision 重建。

生产启动时会校验严格的 Skill Home authority marker，并拒绝残留的旧 PostgreSQL 当前状态。离线迁移旧部署：进入 maintenance mode，停止所有旧 Skill writer，创建并验证 PostgreSQL 备份，运行 dry run，解决每个不支持项或冲突报告，然后执行真实迁移，再启动新服务器。dry run 和真实迁移都要求全部三个确认 flag；命令语法请运行 `stellad storage migrate-skills --help`。迁移可幂等重跑且不覆盖，校验摘要，保留规范 metadata，并将迁移的旧 PostgreSQL 文件写为 `0644`；不猜测扩展名，也不凭空设置可执行位。它将 deprecated/changelog 数据归档到隐藏的 Home migration archive，迁移逻辑 Reflect usage，绝不删除源 PostgreSQL 行或备份。完成 marker 后重跑只做校验。

## Agent Skill 策略

存储的作用域词汇为 `system`、`system_agent`、`user`、`user_agent` 和 `project`，另加上下文作用域 `builtin`。发行版 builtin 不可变；受管 Skill 可变。公开的规范 ID 是 URL-safe 的稳定资源标识符。客户端必须将其编码视为实现细节，不得解析它们或从中派生文件系统路径。

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
